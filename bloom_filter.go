package main

import (
	"encoding/gob"
	"fmt"
	"os"
	"sync"

	"github.com/bits-and-blooms/bloom/v3"
)

// StableBloomFilter is a persistent bloom filter that maintains state across restarts
type StableBloomFilter struct {
	mu       sync.RWMutex
	filter   *bloom.BloomFilter
	filePath string
}

// NewStableBloomFilter creates a new stable bloom filter or loads existing one
func NewStableBloomFilter(filePath string, expectedItems uint, falsePositiveRate float64) (*StableBloomFilter, error) {
	sbf := &StableBloomFilter{
		filePath: filePath,
	}

	// Try to load existing filter
	if err := sbf.Load(); err != nil {
		// If loading fails, create a new filter
		sbf.filter = bloom.NewWithEstimates(expectedItems, falsePositiveRate)
	}

	return sbf, nil
}

// Test checks if an item might exist in the filter
func (sbf *StableBloomFilter) Test(item string) bool {
	sbf.mu.RLock()
	defer sbf.mu.RUnlock()
	return sbf.filter.TestString(item)
}

// Add adds an item to the filter
func (sbf *StableBloomFilter) Add(item string) {
	sbf.mu.Lock()
	defer sbf.mu.Unlock()
	sbf.filter.AddString(item)
}

// TestAndAdd tests if an item exists and adds it if it doesn't
// Returns true if the item was already in the filter (probably seen before)
// Returns false if the item was not in the filter (definitely new)
func (sbf *StableBloomFilter) TestAndAdd(item string) bool {
	sbf.mu.Lock()
	defer sbf.mu.Unlock()
	return sbf.filter.TestAndAddString(item)
}

// Save persists the bloom filter to disk
func (sbf *StableBloomFilter) Save() error {
	sbf.mu.RLock()
	defer sbf.mu.RUnlock()

	file, err := os.Create(sbf.filePath)
	if err != nil {
		return fmt.Errorf("failed to create bloom filter file: %w", err)
	}
	defer file.Close()

	encoder := gob.NewEncoder(file)
	if err := encoder.Encode(sbf.filter); err != nil {
		return fmt.Errorf("failed to encode bloom filter: %w", err)
	}

	return nil
}

// Load loads the bloom filter from disk
func (sbf *StableBloomFilter) Load() error {
	sbf.mu.Lock()
	defer sbf.mu.Unlock()

	file, err := os.Open(sbf.filePath)
	if err != nil {
		return fmt.Errorf("failed to open bloom filter file: %w", err)
	}
	defer file.Close()

	var filter bloom.BloomFilter
	decoder := gob.NewDecoder(file)
	if err := decoder.Decode(&filter); err != nil {
		return fmt.Errorf("failed to decode bloom filter: %w", err)
	}

	sbf.filter = &filter
	return nil
}

// ApproximateCount returns the approximate number of items in the filter
func (sbf *StableBloomFilter) ApproximateCount() uint32 {
	sbf.mu.RLock()
	defer sbf.mu.RUnlock()
	return sbf.filter.ApproximatedSize()
}
