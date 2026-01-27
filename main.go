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
	DefaultAPIURL        = "https://api.karabast.net/api/ongoing-games"
	DefaultBloomFilePath = "./data/bloom_filter.dat"
	DefaultStatsFilePath = "./data/stats.json"
	DefaultExportDir     = "./data"
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
}

type APIResponse struct {
	OngoingGames []Game `json:"ongoingGames"`
}

type LeaderBaseCombination struct {
	Leader string `json:"leader"`
	Base   string `json:"base"`
}

type PlayRateStats struct {
	mu    sync.RWMutex
	Stats map[LeaderBaseCombination]*CombinationStats `json:"-"`
}

type CombinationStats struct {
	DailyCounts map[string]int `json:"dailyCounts"`
}

// playRateStatsJSON is a JSON-friendly version of PlayRateStats
type playRateStatsJSON struct {
	Stats map[string]*CombinationStats `json:"stats"`
}

// MarshalJSON implements custom JSON marshaling for PlayRateStats
func (p *PlayRateStats) MarshalJSON() ([]byte, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Convert map with struct keys to map with string keys
	statsMap := make(map[string]*CombinationStats)
	for combo, stats := range p.Stats {
		key := fmt.Sprintf("%s/%s", combo.Leader, combo.Base)
		statsMap[key] = stats
	}

	return json.Marshal(playRateStatsJSON{
		Stats: statsMap,
	})
}

// UnmarshalJSON implements custom JSON unmarshaling for PlayRateStats
func (p *PlayRateStats) UnmarshalJSON(data []byte) error {
	var jsonData playRateStatsJSON
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

type Config struct {
	APIURL        string
	BloomFilePath string
	StatsFilePath string
	ExportDir     string
	HTTPTimeout   time.Duration
	Mode          string
	DedupGames    bool
}

type Scraper struct {
	config      Config
	client      *http.Client
	bloomFilter *StableBloomFilter
	stats       *PlayRateStats
}

func NewConfig() Config {
	return Config{
		APIURL:        getEnv("API_URL", DefaultAPIURL),
		BloomFilePath: getEnv("BLOOM_FILE_PATH", DefaultBloomFilePath),
		StatsFilePath: getEnv("STATS_FILE_PATH", DefaultStatsFilePath),
		ExportDir:     getEnv("EXPORT_DIR", DefaultExportDir),
		HTTPTimeout:   30 * time.Second,
		Mode:          getEnv("MODE", "scrape"),
		DedupGames:    getEnv("DEDUP_GAMES", "true") == "true",
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func NewScraper(config Config) (*Scraper, error) {
	bloomFilter, err := NewStableBloomFilter(config.BloomFilePath, 1000000, 0.01)
	if err != nil {
		return nil, fmt.Errorf("failed to create bloom filter: %w", err)
	}

	stats, err := LoadStats(config.StatsFilePath)
	if err != nil {
		log.Printf("No existing stats found, starting fresh: %v", err)
		stats = NewPlayRateStats()
	}

	return &Scraper{
		config: config,
		client: &http.Client{
			Timeout: config.HTTPTimeout,
		},
		bloomFilter: bloomFilter,
		stats:       stats,
	}, nil
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
	for _, game := range apiResp.OngoingGames {
		if s.config.DedupGames && s.bloomFilter.TestAndAdd(game.ID) {
			continue // seen game before
		}
		errs := s.stats.AddGame(game)
		if errs != nil {
			if uw, ok := err.(interface{ Unwrap() []error }); ok {
				for _, err := range uw.Unwrap() {
					errMap[err.Error()] = struct{}{}
				}
			}
		}
		newGames++
	}

	for err := range errMap {
		// Log parsing errors as github warnings
		log.Printf("::warning::%v", err)
	}

	log.Printf("Scrape complete: %d total games, %d new games", len(apiResp.OngoingGames), newGames)

	// Save state
	if err := s.bloomFilter.Save(); err != nil {
		log.Printf("Warning: failed to save bloom filter: %v", err)
	}

	if err := s.stats.Save(s.config.StatsFilePath); err != nil {
		log.Printf("Warning: failed to save stats: %v", err)
	}

	return nil
}

func (s *Scraper) exportSummary(data interface{}, filename string) error {
	if err := os.MkdirAll(s.config.ExportDir, 0755); err != nil {
		return fmt.Errorf("failed to create export directory: %w", err)
	}

	path := fmt.Sprintf("%s/%s", s.config.ExportDir, filename)
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create export file: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		return fmt.Errorf("failed to encode summary: %w", err)
	}

	log.Printf("Exported summary to %s", path)

	return nil
}

func NewPlayRateStats() *PlayRateStats {
	return &PlayRateStats{
		Stats: make(map[LeaderBaseCombination]*CombinationStats),
	}
}

func (p *PlayRateStats) AddGame(game Game) error {
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

		stats.DailyCounts[dayKey]++
	}

	return nil
}

func (p *PlayRateStats) Save(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(p)
}

func LoadStats(path string) (*PlayRateStats, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var stats PlayRateStats
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
	default:
		log.Fatalf("Unknown mode: %s. Use 'scrape'", config.Mode)
	}
}
