package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	DefaultAPIURL               = "https://api.karabast.net/api/ongoing-games"
	DefaultOngoingGamesFilePath = "./data/ongoing_games.json"
	DefaultExportDir            = "./data"
)

type Leader struct {
	ID string `json:"id"`
}

type Base struct {
	ID string `json:"id"`
}

type Game struct {
	ID            string `json:"id"`
	Player1Leader Leader `json:"player1Leader"`
	Player1Base   Base   `json:"player1Base"`
	Player2Leader Leader `json:"player2Leader"`
	Player2Base   Base   `json:"player2Base"`
	Format        string `json:"format"`
}

type APIResponse struct {
	OngoingGames []Game `json:"ongoingGames"`
}

type LeaderBaseCombination struct {
	Leader string `json:"leader"`
	Base   string `json:"base"`
}

type GameStats struct {
	mu    sync.RWMutex
	Stats map[LeaderBaseCombination]*CombinationStats `json:"-"`
}

type CombinationStats struct {
	DailyCounts map[string]int `json:"dailyCounts"`
}

type gameStatsJSON struct {
	Stats map[string]*CombinationStats `json:"stats"`
}

type TrackedGame struct {
	Game      Game      `json:"game"`
	StartTime time.Time `json:"startTime"`
}

type OngoingGames struct {
	Timestamp time.Time               `json:"timestamp"`
	Games     map[string]*TrackedGame `json:"games"`
}

// MarshalJSON implements custom JSON marshaling for GameStats
func (p *GameStats) MarshalJSON() ([]byte, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Convert map with struct keys to map with string keys
	statsMap := make(map[string]*CombinationStats)
	for combo, stats := range p.Stats {
		key := fmt.Sprintf("%s/%s", combo.Leader, combo.Base)
		statsMap[key] = stats
	}

	return json.Marshal(gameStatsJSON{
		Stats: statsMap,
	})
}

// UnmarshalJSON implements custom JSON unmarshaling for GameStats
func (p *GameStats) UnmarshalJSON(data []byte) error {
	var jsonData gameStatsJSON
	if err := json.Unmarshal(data, &jsonData); err != nil {
		return err
	}

	// Convert map with string keys back to map with struct keys
	p.Stats = make(map[LeaderBaseCombination]*CombinationStats)
	for key, stats := range jsonData.Stats {
		// Parse "leader/base" format
		parts := splitLeaderBase(key)
		if len(parts) == 2 {
			combo := LeaderBaseCombination{
				Leader: parts[0],
				Base:   parts[1],
			}

			p.Stats[combo] = stats
		}
	}

	return nil
}

func (b Base) Info() (BaseInfo, error) {
	if info, ok := baseMap[b.ID]; ok {
		return info, nil
	}
	return BaseInfo{}, fmt.Errorf("unknown base ID: %s", b.ID)
}

func (l Leader) Info() (LeaderInfo, error) {
	if info, ok := leaderMap[l.ID]; ok {
		return info, nil
	}

	return LeaderInfo{}, fmt.Errorf("unknown leader ID: %s", l.ID)
}

// splitLeaderBase splits a "leader/base" string into parts
func splitLeaderBase(s string) []string {
	// Find the first occurrence of "/"
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}

func getNormalizedTime() time.Time {
	pacific := time.FixedZone("PST", -8*60*60)
	return time.Now().UTC().In(pacific)
}

// isLAWCard checks if a leader or base ID starts with "LAW"
func isLAWCard(id string) bool {
	return len(id) >= 3 && id[:3] == "LAW"
}

// getStatsFilePath returns the format-specific stats file path
func getStatsFilePath(exportDir, format string) string {
	return fmt.Sprintf("%s/stats_%s.json", exportDir, format)
}

// getPlaytimeStatsFilePath returns the format-specific playtime stats file path
func getPlaytimeStatsFilePath(exportDir, format string) string {
	return fmt.Sprintf("%s/playtime_stats_%s.json", exportDir, format)
}

type Config struct {
	APIURL               string
	BloomFilePath        string
	OngoingGamesFilePath string
	ExportDir            string
	HTTPTimeout          time.Duration
	Mode                 string
	DedupGames           bool
}

type Scraper struct {
	config        Config
	client        *http.Client
	stats         map[string]*GameStats // keyed by format
	playtimeStats map[string]*GameStats // keyed by format
	ongoingGames  *OngoingGames
}

func NewConfig() Config {
	return Config{
		APIURL:               getEnv("API_URL", DefaultAPIURL),
		OngoingGamesFilePath: getEnv("ONGOING_GAMES_FILE_PATH", DefaultOngoingGamesFilePath),
		ExportDir:            getEnv("EXPORT_DIR", DefaultExportDir),
		HTTPTimeout:          30 * time.Second,
		Mode:                 getEnv("MODE", "scrape"),
		DedupGames:           getEnv("DEDUP_GAMES", "true") == "true",
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func NewScraper(config Config) (*Scraper, error) {
	formats := []string{"premier", "open", "nextSetPreview"}
	
	// Initialize stats and playtime maps
	statsMap := make(map[string]*GameStats)
	playtimeMap := make(map[string]*GameStats)
	
	// Load format-specific files or create new ones
	for _, format := range formats {
		// Load stats
		statsPath := getStatsFilePath(config.ExportDir, format)
		stats, err := LoadStats(statsPath)
		if err != nil {
			log.Printf("No existing stats for format %s, starting fresh", format)
			stats = NewGameStats()
		}
		statsMap[format] = stats
		
		// Load playtime stats
		playtimePath := getPlaytimeStatsFilePath(config.ExportDir, format)
		playtimeStats, err := LoadStats(playtimePath)
		if err != nil {
			log.Printf("No existing playtime stats for format %s, starting fresh", format)
			playtimeStats = NewGameStats()
		}
		playtimeMap[format] = playtimeStats
	}
	
	scraper := &Scraper{
		config: config,
		client: &http.Client{
			Timeout: config.HTTPTimeout,
		},
		stats:         statsMap,
		playtimeStats: playtimeMap,
	}
	
	if err := scraper.LoadOngoingGames(); err != nil {
		log.Printf("Warning: failed to load ongoing games, starting fresh: %v", err)
		scraper.ongoingGames = &OngoingGames{
			Games: make(map[string]*TrackedGame),
		}
	}
	
	return scraper, nil
}

func (s *Scraper) Scrape(ctx context.Context) error {
	log.Println("Starting scrape...")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.config.APIURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	newGames := 0
	errMap := make(map[string]struct{})
	currentGames := make(map[string]struct{})
	for _, game := range apiResp.OngoingGames {
		currentGames[game.ID] = struct{}{}
		if s.config.DedupGames {
			if _, ok := s.ongoingGames.Games[game.ID]; ok {
				continue // seen game before
			}
		}
		errs := s.AddGame(game)
		if errs != nil {
			if uw, ok := errs.(interface{ Unwrap() []error }); ok {
				for _, err := range uw.Unwrap() {
					errMap[err.Error()] = struct{}{}
				}
			}
		}
		newGames++
	}

	// Identify finished games
	now := getNormalizedTime()
	finishedCount := 0
	for id, tg := range s.ongoingGames.Games {
		if _, ok := currentGames[id]; !ok {
			// Game ended
			duration := s.ongoingGames.Timestamp.Sub(tg.StartTime)
			if duration >= time.Minute {
				if err := s.AddPlaytime(tg.Game, int(duration.Minutes())); err != nil {
					if uw, ok := err.(interface{ Unwrap() []error }); ok {
						for _, err := range uw.Unwrap() {
							errMap[err.Error()] = struct{}{}
						}
					}
				}
			}
			delete(s.ongoingGames.Games, id)
			finishedCount++
		}
	}

	// Update ongoing games
	s.ongoingGames.Timestamp = now
	for _, game := range apiResp.OngoingGames {
		if _, exists := s.ongoingGames.Games[game.ID]; !exists {
			s.ongoingGames.Games[game.ID] = &TrackedGame{
				Game:      game,
				StartTime: now,
			}
		}
	}

	for err := range errMap {
		// Log parsing errors as github warnings
		log.Printf("::warning::%v", err)
	}

	log.Printf("Scrape complete: %d total games, %d new games, %d finished games", len(apiResp.OngoingGames), newGames, finishedCount)

	return nil
}

func (s *Scraper) Save() {
	formats := []string{"premier", "open", "nextSetPreview"}
	
	for _, format := range formats {
		// Save stats for this format
		if stats, ok := s.stats[format]; ok {
			filePath := getStatsFilePath(s.config.ExportDir, format)
			if err := stats.Save(filePath); err != nil {
				log.Printf("Warning: failed to save stats for format %s: %v", format, err)
			}
		}
		
		// Save playtime stats for this format
		if playtimeStats, ok := s.playtimeStats[format]; ok {
			filePath := getPlaytimeStatsFilePath(s.config.ExportDir, format)
			if err := playtimeStats.Save(filePath); err != nil {
				log.Printf("Warning: failed to save playtime stats for format %s: %v", format, err)
			}
		}
	}

	if err := s.SaveOngoingGames(); err != nil {
		log.Printf("Warning: failed to save ongoing games: %v", err)
	}
}

func (s *Scraper) LoadOngoingGames() error {
	file, err := os.Open(s.config.OngoingGamesFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			s.ongoingGames = &OngoingGames{
				Games: make(map[string]*TrackedGame),
			}
			return nil
		}
		return err
	}
	defer file.Close()

	s.ongoingGames = &OngoingGames{
		Games: make(map[string]*TrackedGame),
	}
	return json.NewDecoder(file).Decode(&s.ongoingGames)
}

func (s *Scraper) SaveOngoingGames() error {
	file, err := os.Create(s.config.OngoingGamesFilePath)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(s.ongoingGames)
}

func NewGameStats() *GameStats {
	return &GameStats{
		Stats: make(map[LeaderBaseCombination]*CombinationStats),
	}
}

func (s *Scraper) AddGame(game Game) error {
	// Get format, default to "premier" if empty
	format := game.Format
	if format == "" {
		format = "premier"
	}
	
	// Get the stats for this format
	stats, ok := s.stats[format]
	if !ok {
		return fmt.Errorf("unknown format: %s", format)
	}
	
	return stats.accumulate(game, 1)
}

func (s *Scraper) AddPlaytime(game Game, minutes int) error {
	// Get format, default to "premier" if empty
	format := game.Format
	if format == "" {
		format = "premier"
	}
	
	// Get the playtime stats for this format
	playtimeStats, ok := s.playtimeStats[format]
	if !ok {
		return fmt.Errorf("unknown format: %s", format)
	}
	
	return playtimeStats.accumulate(game, minutes)
}

func (p *GameStats) accumulate(game Game, count int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := getNormalizedTime()
	dayKey := now.Format("2006-01-02")

	// Track both player combinations, extracting IDs from nested objects
	p1Leader, err1 := game.Player1Leader.Info()
	p2Leader, err2 := game.Player2Leader.Info()
	p1Base, err3 := game.Player1Base.Info()
	p2Base, err4 := game.Player2Base.Info()
	if errs := errors.Join(err1, err2, err3, err4); errs != nil {
		return errs
	}

	for _, combo := range []struct {
		LeaderInfo
		BaseInfo
	}{
		{p1Leader, p1Base},
		{p2Leader, p2Base},
	} {
		comboKey := LeaderBaseCombination{Leader: combo.LeaderInfo.ID, Base: combo.BaseInfo.DataKey}
		stats, exists := p.Stats[comboKey]

		if !exists {
			stats = &CombinationStats{
				DailyCounts: make(map[string]int),
			}
			p.Stats[comboKey] = stats
		}

		stats.DailyCounts[dayKey] += count
	}

	return nil
}

func (p *GameStats) Save(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(p)
}

func LoadStats(path string) (*GameStats, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var stats GameStats
	if err := json.NewDecoder(file).Decode(&stats); err != nil {
		return nil, err
	}

	return &stats, nil
}

func main() {
	log.Println("Starting Game Scraper...")

	config := NewConfig()
	scraper, err := NewScraper(config)
	if err != nil {
		log.Fatalf("Failed to initialize scraper: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()

	switch config.Mode {
	case "scrape":
		log.Println("Mode: Scraping")
		if err := scraper.Scrape(ctx); err != nil {
			log.Fatalf("Scrape failed: %v", err)
		}
		log.Println("Scrape completed successfully")
		scraper.Save()
		log.Println("Scrape saved successfully")
	default:
		log.Fatalf("Unknown mode: %s. Use 'scrape'", config.Mode)
	}
}
