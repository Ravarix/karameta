package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	DefaultAPIURL        = "https://api.karabast.net/api/ongoing-games"
	DefaultBloomFilePath = "./bloom_filter.dat"
	DefaultStatsFilePath = "./stats.json"
	DefaultExportDir     = "./exports"
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
	mu      sync.RWMutex
	Stats   map[LeaderBaseCombination]*CombinationStats `json:"-"`
	Since   time.Time                                   `json:"since"`
	LastRun time.Time                                   `json:"lastRun"`
}

type CombinationStats struct {
	Count        int            `json:"count"`
	FirstSeen    time.Time      `json:"firstSeen"`
	LastSeen     time.Time      `json:"lastSeen"`
	DailyCounts  map[string]int `json:"dailyCounts"`
	WeeklyCounts map[string]int `json:"weeklyCounts"`
}

// playRateStatsJSON is a JSON-friendly version of PlayRateStats
type playRateStatsJSON struct {
	Stats   map[string]*CombinationStats `json:"stats"`
	Since   time.Time                    `json:"since"`
	LastRun time.Time                    `json:"lastRun"`
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
		Stats:   statsMap,
		Since:   p.Since,
		LastRun: p.LastRun,
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

	p.Since = jsonData.Since
	p.LastRun = jsonData.LastRun

	return nil
}

func (b Base) Normalize() string {
	if normalized, ok := baseMap[b.ID]; ok {
		return normalized
	}
	return b.ID
}

func (l Leader) Normalize() string {
	if normalized, ok := leaderMap[l.ID]; ok {
		return normalized
	}
	return l.ID
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

type DailySummary struct {
	Date         string                        `json:"date"`
	TotalGames   int                           `json:"totalGames"`
	Combinations map[LeaderBaseCombination]int `json:"combinations"`
	TopCombos    []TopCombination              `json:"topCombinations"`
}

type WeeklySummary struct {
	WeekStart    string                        `json:"weekStart"`
	WeekEnd      string                        `json:"weekEnd"`
	TotalGames   int                           `json:"totalGames"`
	Combinations map[LeaderBaseCombination]int `json:"combinations"`
	TopCombos    []TopCombination              `json:"topCombinations"`
}

type TopCombination struct {
	Combination LeaderBaseCombination `json:"combination"`
	Count       int                   `json:"count"`
	Percentage  float64               `json:"percentage"`
}

type Config struct {
	APIURL        string
	BloomFilePath string
	StatsFilePath string
	ExportDir     string
	HTTPTimeout   time.Duration
	Mode          string // "scrape", "export-daily", or "export-weekly"
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
	for _, game := range apiResp.OngoingGames {
		if !s.bloomFilter.TestAndAdd(game.ID) {
			// New game, not seen before
			s.stats.AddGame(game)
			newGames++
		}
	}

	log.Printf("Scrape complete: %d total games, %d new games", len(apiResp.OngoingGames), newGames)

	// Save state
	if err := s.bloomFilter.Save(); err != nil {
		log.Printf("Warning: failed to save bloom filter: %v", err)
	}

	if err := s.stats.Save(s.config.StatsFilePath); err != nil {
		log.Printf("Warning: failed to save stats: %v", err)
	}

	s.stats.UpdateLastRun()

	return nil
}

func (s *Scraper) ExportDailySummary(date time.Time) error {
	summary := s.stats.GetDailySummary(date)
	return s.exportSummary(summary, fmt.Sprintf("daily_%s.json", date.Format("2006-01-02")))
}

func (s *Scraper) ExportWeeklySummary(weekStart time.Time) error {
	summary := s.stats.GetWeeklySummary(weekStart)
	return s.exportSummary(summary, fmt.Sprintf("weekly_%s.json", weekStart.Format("2006-01-02")))
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
		Since: time.Now(),
	}
}

func (p *PlayRateStats) AddGame(game Game) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	dayKey := now.Format("2006-01-02")
	weekKey := getWeekStart(now).Format("2006-01-02")

	// Track both player combinations, extracting IDs from nested objects
	combinations := []LeaderBaseCombination{
		{Leader: game.Player1Leader.Normalize(), Base: game.Player1Base.Normalize()},
		{Leader: game.Player2Leader.Normalize(), Base: game.Player2Base.Normalize()},
	}

	for _, combo := range combinations {
		stats, exists := p.Stats[combo]
		if !exists {
			stats = &CombinationStats{
				FirstSeen:    now,
				DailyCounts:  make(map[string]int),
				WeeklyCounts: make(map[string]int),
			}
			p.Stats[combo] = stats
		}

		stats.Count++
		stats.LastSeen = now
		stats.DailyCounts[dayKey]++
		stats.WeeklyCounts[weekKey]++
	}
}

func (p *PlayRateStats) GetDailySummary(date time.Time) DailySummary {
	p.mu.RLock()
	defer p.mu.RUnlock()

	dayKey := date.Format("2006-01-02")
	combinations := make(map[LeaderBaseCombination]int)
	totalGames := 0

	for combo, stats := range p.Stats {
		if count, ok := stats.DailyCounts[dayKey]; ok {
			combinations[combo] = count
			totalGames += count
		}
	}

	return DailySummary{
		Date:         dayKey,
		TotalGames:   totalGames,
		Combinations: combinations,
		TopCombos:    getTopCombinations(combinations, totalGames, 10),
	}
}

func (p *PlayRateStats) GetWeeklySummary(weekStart time.Time) WeeklySummary {
	p.mu.RLock()
	defer p.mu.RUnlock()

	weekKey := getWeekStart(weekStart).Format("2006-01-02")
	weekEnd := weekStart.AddDate(0, 0, 6)
	combinations := make(map[LeaderBaseCombination]int)
	totalGames := 0

	for combo, stats := range p.Stats {
		if count, ok := stats.WeeklyCounts[weekKey]; ok {
			combinations[combo] = count
			totalGames += count
		}
	}

	return WeeklySummary{
		WeekStart:    weekKey,
		WeekEnd:      weekEnd.Format("2006-01-02"),
		TotalGames:   totalGames,
		Combinations: combinations,
		TopCombos:    getTopCombinations(combinations, totalGames, 10),
	}
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

func (p *PlayRateStats) UpdateLastRun() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.LastRun = time.Now()
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

func getTopCombinations(combinations map[LeaderBaseCombination]int, totalGames int, limit int) []TopCombination {
	var topCombos []TopCombination

	for combo, count := range combinations {
		percentage := 0.0
		if totalGames > 0 {
			percentage = float64(count) / float64(totalGames) * 100
		}
		topCombos = append(topCombos, TopCombination{
			Combination: combo,
			Count:       count,
			Percentage:  percentage,
		})
	}

	// Simple bubble sort for top combinations
	for i := 0; i < len(topCombos); i++ {
		for j := i + 1; j < len(topCombos); j++ {
			if topCombos[j].Count > topCombos[i].Count {
				topCombos[i], topCombos[j] = topCombos[j], topCombos[i]
			}
		}
	}

	if len(topCombos) > limit {
		topCombos = topCombos[:limit]
	}

	return topCombos
}

func getWeekStart(t time.Time) time.Time {
	// Start week on Monday
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	return t.AddDate(0, 0, -weekday+1).Truncate(24 * time.Hour)
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

	case "export-daily":
		log.Println("Mode: Daily Export")
		yesterday := time.Now().AddDate(0, 0, -1)
		if err := scraper.ExportDailySummary(yesterday); err != nil {
			log.Fatalf("Daily export failed: %v", err)
		}
		log.Printf("Daily export completed for %s", yesterday.Format("2006-01-02"))

	case "export-weekly":
		log.Println("Mode: Weekly Export")
		lastWeek := time.Now().AddDate(0, 0, -7)
		weekStart := getWeekStart(lastWeek)
		if err := scraper.ExportWeeklySummary(weekStart); err != nil {
			log.Fatalf("Weekly export failed: %v", err)
		}
		log.Printf("Weekly export completed for week starting %s", weekStart.Format("2006-01-02"))

	default:
		log.Fatalf("Unknown mode: %s. Use 'scrape', 'export-daily', or 'export-weekly'", config.Mode)
	}
}
