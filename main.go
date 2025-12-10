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
	mu      sync.RWMutex
	Stats   map[LeaderBaseCombination]*CombinationStats `json:"-"`
	Since   time.Time                                   `json:"since"`
	LastRun time.Time                                   `json:"lastRun"`
}

type CombinationStats struct {
	Count       int            `json:"count"`
	DailyCounts map[string]int `json:"dailyCounts"`
	Aspects     []string       `json:"aspects"`
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

			// Ensure aspects array is not nil
			if stats.Aspects == nil {
				stats.Aspects = []string{}
			}

			p.Stats[combo] = stats
		}
	}

	p.Since = jsonData.Since
	p.LastRun = jsonData.LastRun

	return nil
}

func (b Base) Info() BaseInfo {
	if info, ok := baseMap[b.ID]; ok {
		return info
	}
	return BaseInfo{b.ID, "Unknown"}
}

func (l Leader) Info() LeaderInfo {
	if info, ok := leaderMap[l.ID]; ok {
		return info
	}
	log.Printf("Warning: unknown leader ID: %s", l.ID)
	return LeaderInfo{l.ID, []string{"Unknown"}}
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
	Date               string                        `json:"date"`
	TotalGames         int                           `json:"totalGames"`
	Combinations       map[LeaderBaseCombination]int `json:"-"`
	TopCombos          []TopCombination              `json:"topCombinations"`
	AspectDistribution map[string]int                `json:"aspectDistribution"`
}

type TopCombination struct {
	Combination LeaderBaseCombination `json:"combination"`
	Count       int                   `json:"count"`
	Percentage  float64               `json:"percentage"`
}

// MarshalJSON implements custom JSON marshaling for DailySummary
func (d DailySummary) MarshalJSON() ([]byte, error) {
	// Convert map with struct keys to map with string keys
	combinations := make(map[string]int)
	for combo, count := range d.Combinations {
		key := fmt.Sprintf("%s/%s", combo.Leader, combo.Base)
		combinations[key] = count
	}

	type Alias DailySummary
	return json.Marshal(&struct {
		Combinations map[string]int `json:"combinations"`
		*Alias
	}{
		Combinations: combinations,
		Alias:        (*Alias)(&d),
	})
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
	} else {
		// Backfill aspects for any existing data that doesn't have them
		stats.BackfillAspects()
		log.Println("Backfilled aspects for existing combination stats")
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

	// Automatically update index after any export (except index.json itself)
	if filename != "index.json" {
		if err := s.updateIndex(); err != nil {
			log.Printf("Warning: failed to update index: %v", err)
			// Don't fail the export if index update fails
		}
	}

	return nil
}

func (s *Scraper) updateIndex() error {
	files, err := os.ReadDir(s.config.ExportDir)
	if err != nil {
		return fmt.Errorf("failed to read export directory: %w", err)
	}

	dailyDates := []string{}

	for _, file := range files {
		if file.IsDir() {
			continue
		}
		name := file.Name()

		// Match daily_YYYY-MM-DD.json
		if len(name) > 6 && name[:6] == "daily_" && name[len(name)-5:] == ".json" {
			date := name[6 : len(name)-5]
			dailyDates = append(dailyDates, date)
		}
	}

	// Sort dates in descending order (newest first)
	sortDatesDescending(dailyDates)

	index := map[string]interface{}{
		"daily": dailyDates,
	}

	return s.exportSummary(index, "index.json")
}

func sortDatesDescending(dates []string) {
	// Simple bubble sort in descending order
	for i := 0; i < len(dates); i++ {
		for j := i + 1; j < len(dates); j++ {
			if dates[i] < dates[j] {
				dates[i], dates[j] = dates[j], dates[i]
			}
		}
	}
}

func NewPlayRateStats() *PlayRateStats {
	return &PlayRateStats{
		Stats: make(map[LeaderBaseCombination]*CombinationStats),
		Since: time.Now(),
	}
}

// BackfillAspects updates any combination stats that don't have aspects populated
func (p *PlayRateStats) BackfillAspects() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for combo, stats := range p.Stats {
		if stats.Aspects == nil || len(stats.Aspects) == 0 {
			// Reconstruct aspects from combo
			aspects := make(map[string]struct{})

			// Try to get leader info
			for _, leaderInfo := range leaderMap {
				if leaderInfo.Name == combo.Leader {
					for _, aspect := range leaderInfo.Aspects {
						if aspect != "" && aspect != "Unknown" {
							aspects[aspect] = struct{}{}
						}
					}
					break
				}
			}

			// Try to get base info
			for _, baseInfo := range baseMap {
				if baseInfo.Name == combo.Base {
					if baseInfo.Aspect != "" && baseInfo.Aspect != "Unknown" {
						aspects[baseInfo.Aspect] = struct{}{}
					}
					break
				}
			}

			// Convert to array
			aspectArray := make([]string, 0, len(aspects))
			for aspect := range aspects {
				aspectArray = append(aspectArray, aspect)
			}
			stats.Aspects = aspectArray
		}
	}
}

func (p *PlayRateStats) AddGame(game Game) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	dayKey := now.Format("2006-01-02")

	// Track both player combinations, extracting IDs from nested objects
	combinations := []struct {
		leader LeaderInfo
		base   BaseInfo
	}{
		{leader: game.Player1Leader.Info(), base: game.Player1Base.Info()},
		{leader: game.Player2Leader.Info(), base: game.Player2Base.Info()},
	}

	for _, combo := range combinations {
		comboKey := LeaderBaseCombination{Leader: combo.leader.Name, Base: combo.base.Name}
		stats, exists := p.Stats[comboKey]

		// Build aspects set, filtering out "Unknown" and empty
		aspects := make(map[string]struct{})
		for _, aspect := range combo.leader.Aspects {
			if aspect != "" && aspect != "Unknown" {
				aspects[aspect] = struct{}{}
			}
		}
		if combo.base.Aspect != "" && combo.base.Aspect != "Unknown" {
			aspects[combo.base.Aspect] = struct{}{}
		}

		// Convert to sorted array
		aspectArray := make([]string, 0, len(aspects))
		for aspect := range aspects {
			aspectArray = append(aspectArray, aspect)
		}

		if !exists {
			stats = &CombinationStats{
				DailyCounts: make(map[string]int),
				Aspects:     aspectArray,
			}
			p.Stats[comboKey] = stats
		} else if stats.Aspects == nil || len(stats.Aspects) == 0 {
			// Backfill aspects for existing stats that don't have them
			stats.Aspects = aspectArray
		}

		stats.Count++
		stats.DailyCounts[dayKey]++
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
		Date:               dayKey,
		TotalGames:         totalGames,
		Combinations:       combinations,
		TopCombos:          getTopCombinations(combinations, totalGames, 10),
		AspectDistribution: calculateAspectDistribution(p.Stats, combinations),
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

func calculateAspectDistribution(stats map[LeaderBaseCombination]*CombinationStats, dayCounts map[LeaderBaseCombination]int) map[string]int {
	aspectCounts := make(map[string]int)

	for combo, count := range dayCounts {
		if comboStats, ok := stats[combo]; ok && comboStats.Aspects != nil {
			for _, aspect := range comboStats.Aspects {
				aspectCounts[aspect] += count
			}
		}
	}

	return aspectCounts
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

	case "export-current":
		log.Println("Mode: Current Day Export")
		today := time.Now()
		if err := scraper.ExportDailySummary(today); err != nil {
			log.Fatalf("Current export failed: %v", err)
		}
		log.Printf("Current day export completed for %s (index updated)", today.Format("2006-01-02"))
	default:
		log.Fatalf("Unknown mode: %s. Use 'scrape', 'export-current', or 'export-daily'", config.Mode)
	}
}
