package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// NewServiceState creates a new empty state
func NewServiceState() *ServiceState {
	return &ServiceState{
		EmailsSentPerURLToday:      make(map[string][]time.Time),
		ErrorEmailsSentPerURLToday: make(map[string][]time.Time),
		LastAlertTimes:             make(map[string]time.Time),
		SeenMatches:                make(map[string]*MatchRecord),
		RecentEmailNotifications:   make([]EmailNotification, 0, 100),
		LastSaved:                  time.Now(),
	}
}

// LoadState loads state from file, returns empty state if file doesn't exist or is corrupted
func LoadState(filePath string) *ServiceState {
	state := NewServiceState()

	// If no file path configured, return empty state
	if filePath == "" {
		log.Println("⚠️  No state file path configured, starting with fresh state")
		return state
	}

	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		log.Printf("ℹ️  State file not found at %s, starting with fresh state", filePath)
		return state
	}

	// Read file
	data, err := os.ReadFile(filePath)
	if err != nil {
		log.Printf("⚠️  Failed to read state file: %v, starting with fresh state", err)
		return state
	}

	// Parse JSON
	if err := json.Unmarshal(data, state); err != nil {
		log.Printf("⚠️  Failed to parse state file (possibly corrupted): %v, starting with fresh state", err)
		// Backup corrupted file
		backupPath := filePath + ".corrupted." + time.Now().Format("20060102-150405")
		if copyErr := os.Rename(filePath, backupPath); copyErr == nil {
			log.Printf("ℹ️  Corrupted state file backed up to: %s", backupPath)
		}
		return NewServiceState()
	}

	// Cleanup old data
	state.CleanupOldData()

	log.Printf("✅ State loaded from %s (%d seen matches, %d URLs tracked)",
		filePath, len(state.SeenMatches), len(state.EmailsSentPerURLToday))

	return state
}

// SaveState saves state to file
func (s *ServiceState) SaveState(filePath string) error {
	if filePath == "" {
		return fmt.Errorf("no state file path configured")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Cleanup old data before saving
	s.cleanupOldDataUnsafe()

	// Update last saved timestamp
	s.LastSaved = time.Now()

	// Marshal to JSON with indentation for readability
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Write to temporary file first (atomic write)
	tempFile := filePath + ".tmp"
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	// Rename to final location (atomic operation)
	if err := os.Rename(tempFile, filePath); err != nil {
		os.Remove(tempFile) // Clean up temp file
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	return nil
}

// CleanupOldData removes expired data (thread-safe)
func (s *ServiceState) CleanupOldData() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupOldDataUnsafe()
}

// cleanupOldDataUnsafe removes expired data (must be called with lock held)
func (s *ServiceState) cleanupOldDataUnsafe() {
	now := time.Now()
	oneDayAgo := now.Add(-24 * time.Hour)
	sevenDaysAgo := now.Add(-7 * 24 * time.Hour)

	// Clean up email timestamps older than 24 hours
	for url, times := range s.EmailsSentPerURLToday {
		validTimes := make([]time.Time, 0)
		for _, t := range times {
			if t.After(oneDayAgo) {
				validTimes = append(validTimes, t)
			}
		}
		if len(validTimes) > 0 {
			s.EmailsSentPerURLToday[url] = validTimes
		} else {
			delete(s.EmailsSentPerURLToday, url)
		}
	}

	// Clean up error email timestamps older than 24 hours
	for url, times := range s.ErrorEmailsSentPerURLToday {
		validTimes := make([]time.Time, 0)
		for _, t := range times {
			if t.After(oneDayAgo) {
				validTimes = append(validTimes, t)
			}
		}
		if len(validTimes) > 0 {
			s.ErrorEmailsSentPerURLToday[url] = validTimes
		} else {
			delete(s.ErrorEmailsSentPerURLToday, url)
		}
	}

	// Clean up last alert times older than 24 hours
	for key, t := range s.LastAlertTimes {
		if t.Before(oneDayAgo) {
			delete(s.LastAlertTimes, key)
		}
	}

	// Clean up seen matches older than 7 days
	for hash, record := range s.SeenMatches {
		if record.LastNotified.Before(sevenDaysAgo) {
			delete(s.SeenMatches, hash)
		}
	}
}

// GenerateMatchHash creates a unique hash for an incident
func GenerateMatchHash(url, date, timeStr, address string) string {
	// Normalize inputs to prevent minor variations from creating different hashes
	normalized := fmt.Sprintf("%s|%s|%s|%s", url, date, timeStr, address)
	hash := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("%x", hash)
}

// IsMatchSeen checks if we've already notified about this specific match
func (s *ServiceState) IsMatchSeen(hash string, maxAge time.Duration) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, exists := s.SeenMatches[hash]
	if !exists {
		return false
	}

	// Check if the match is still within the notification window
	if time.Since(record.LastNotified) > maxAge {
		return false
	}

	return true
}

// RecordMatch records that we've seen and notified about this match
func (s *ServiceState) RecordMatch(hash, url, date, timeStr, address string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	
	if record, exists := s.SeenMatches[hash]; exists {
		// Update existing record
		record.LastNotified = now
		record.Count++
	} else {
		// Create new record
		s.SeenMatches[hash] = &MatchRecord{
			FirstSeen:    now,
			LastNotified: now,
			Count:        1,
			Date:         date,
			Time:         timeStr,
			Address:      address,
			URL:          url,
		}
	}
}

// GetEmailsSentToday returns the count of emails sent today for a URL
func (s *ServiceState) GetEmailsSentToday(url string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	times, exists := s.EmailsSentPerURLToday[url]
	if !exists {
		return 0
	}

	// Count only emails from last 24 hours
	oneDayAgo := time.Now().Add(-24 * time.Hour)
	count := 0
	for _, t := range times {
		if t.After(oneDayAgo) {
			count++
		}
	}

	return count
}

// RecordEmailSent records that an email was sent for a URL
func (s *ServiceState) RecordEmailSent(url string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if s.EmailsSentPerURLToday[url] == nil {
		s.EmailsSentPerURLToday[url] = make([]time.Time, 0)
	}
	s.EmailsSentPerURLToday[url] = append(s.EmailsSentPerURLToday[url], now)
}

// GetErrorEmailsSentToday returns the count of error emails sent today for a URL
func (s *ServiceState) GetErrorEmailsSentToday(url string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	times, exists := s.ErrorEmailsSentPerURLToday[url]
	if !exists {
		return 0
	}

	// Count only emails from last 24 hours
	oneDayAgo := time.Now().Add(-24 * time.Hour)
	count := 0
	for _, t := range times {
		if t.After(oneDayAgo) {
			count++
		}
	}

	return count
}

// RecordErrorEmailSent records that an error email was sent for a URL
func (s *ServiceState) RecordErrorEmailSent(url string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if s.ErrorEmailsSentPerURLToday[url] == nil {
		s.ErrorEmailsSentPerURLToday[url] = make([]time.Time, 0)
	}
	s.ErrorEmailsSentPerURLToday[url] = append(s.ErrorEmailsSentPerURLToday[url], now)
}

// GetLastAlertTime returns the last alert time for a specific alert key
func (s *ServiceState) GetLastAlertTime(url, alertType string) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := fmt.Sprintf("%s|%s", url, alertType)
	t, exists := s.LastAlertTimes[key]
	return t, exists
}

// RecordAlertTime records the time an alert was sent
func (s *ServiceState) RecordAlertTime(url, alertType string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := fmt.Sprintf("%s|%s", url, alertType)
	s.LastAlertTimes[key] = time.Now()
}

// GetStats returns statistics about the current state
func (s *ServiceState) GetStats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	totalEmailsSent := 0
	for _, times := range s.EmailsSentPerURLToday {
		totalEmailsSent += len(times)
	}

	totalErrorEmailsSent := 0
	for _, times := range s.ErrorEmailsSentPerURLToday {
		totalErrorEmailsSent += len(times)
	}

	return map[string]interface{}{
		"seen_matches_count":       len(s.SeenMatches),
		"urls_tracked":             len(s.EmailsSentPerURLToday),
		"total_emails_sent_24h":    totalEmailsSent,
		"total_error_emails_24h":   totalErrorEmailsSent,
		"last_saved":               s.LastSaved.Format("2006-01-02 15:04:05"),
	}
}

