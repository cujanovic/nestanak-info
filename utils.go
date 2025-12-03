package main

import (
	"fmt"
	"sync"
	"time"
)

// NewAsyncLogger creates a new async logger
func NewAsyncLogger(maxLines int, flushInterval time.Duration) *AsyncLogger {
	al := &AsyncLogger{
		logChan:     make(chan LogEntry, 1000),
		buffer:      make([]LogEntry, 0, maxLines),
		maxLines:    maxLines,
		flushTicker: time.NewTicker(flushInterval),
		stopChan:    make(chan struct{}),
	}

	go al.run()
	return al
}

// run processes log entries
func (al *AsyncLogger) run() {
	for {
		select {
		case entry := <-al.logChan:
			al.mu.Lock()
			al.buffer = append(al.buffer, entry)
			if len(al.buffer) > al.maxLines {
				al.buffer = al.buffer[len(al.buffer)-al.maxLines:]
			}
			al.mu.Unlock()

		case <-al.flushTicker.C:
			// Periodic flush (for future file logging if needed)

		case <-al.stopChan:
			return
		}
	}
}

// Add adds a log entry
func (al *AsyncLogger) Add(entry LogEntry) {
	select {
	case al.logChan <- entry:
	default:
		// Channel full, drop entry
	}
}

// GetLogs returns all log entries
func (al *AsyncLogger) GetLogs() []LogEntry {
	al.mu.RLock()
	defer al.mu.RUnlock()
	
	result := make([]LogEntry, len(al.buffer))
	copy(result, al.buffer)
	return result
}

// Stop stops the async logger
func (al *AsyncLogger) Stop() {
	close(al.stopChan)
	al.flushTicker.Stop()
}

// NewCircularBuffer creates a new circular buffer
func NewCircularBuffer(capacity int) *CircularBuffer {
	return &CircularBuffer{
		items:    make([]interface{}, capacity),
		capacity: capacity,
	}
}

// Add adds an item to the circular buffer
func (cb *CircularBuffer) Add(item interface{}) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.count < cb.capacity {
		cb.items[cb.tail] = item
		cb.tail = (cb.tail + 1) % cb.capacity
		cb.count++
	} else {
		cb.items[cb.head] = item
		cb.head = (cb.head + 1) % cb.capacity
		cb.tail = (cb.tail + 1) % cb.capacity
	}
}

// GetAll returns all items in the circular buffer
func (cb *CircularBuffer) GetAll() []interface{} {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	result := make([]interface{}, 0, cb.count)
	
	if cb.count == 0 {
		return result
	}

	if cb.count < cb.capacity {
		// Buffer not full yet
		for i := 0; i < cb.count; i++ {
			result = append(result, cb.items[i])
		}
	} else {
		// Buffer is full, read from head to tail
		for i := 0; i < cb.capacity; i++ {
			idx := (cb.head + i) % cb.capacity
			result = append(result, cb.items[idx])
		}
	}

	return result
}

// formatDuration formats a duration into a human-readable string
func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm %ds", days, hours, minutes, seconds)
	} else if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	} else if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

// cyrillicToLatin converts Serbian Cyrillic text to Latin
func cyrillicToLatin(text string) string {
	result := text
	
	// Process two-letter Cyrillic letters first (they map to two Latin letters)
	twoLetterReplacements := [][]string{
		{"Љ", "Lj"}, {"љ", "lj"},
		{"Њ", "Nj"}, {"њ", "nj"},
		{"Џ", "Dž"}, {"џ", "dž"},
	}
	for _, pair := range twoLetterReplacements {
		result = replaceAll(result, pair[0], pair[1])
	}
	
	// Then single Cyrillic letters
	singleLetterReplacements := [][]string{
		{"А", "A"}, {"а", "a"},
		{"Б", "B"}, {"б", "b"},
		{"В", "V"}, {"в", "v"},
		{"Г", "G"}, {"г", "g"},
		{"Д", "D"}, {"д", "d"},
		{"Ђ", "Đ"}, {"ђ", "đ"},
		{"Е", "E"}, {"е", "e"},
		{"Ж", "Ž"}, {"ж", "ž"},
		{"З", "Z"}, {"з", "z"},
		{"И", "I"}, {"и", "i"},
		{"Ј", "J"}, {"ј", "j"},
		{"К", "K"}, {"к", "k"},
		{"Л", "L"}, {"л", "l"},
		{"М", "M"}, {"м", "m"},
		{"Н", "N"}, {"н", "n"},
		{"О", "O"}, {"о", "o"},
		{"П", "P"}, {"п", "p"},
		{"Р", "R"}, {"р", "r"},
		{"С", "S"}, {"с", "s"},
		{"Т", "T"}, {"т", "t"},
		{"Ћ", "Ć"}, {"ћ", "ć"},
		{"У", "U"}, {"у", "u"},
		{"Ф", "F"}, {"ф", "f"},
		{"Х", "H"}, {"х", "h"},
		{"Ц", "C"}, {"ц", "c"},
		{"Ч", "Č"}, {"ч", "č"},
		{"Ш", "Š"}, {"ш", "š"},
	}
	
	for _, pair := range singleLetterReplacements {
		result = replaceAll(result, pair[0], pair[1])
	}
	
	return result
}

// latinToCyrillic converts Serbian Latin text to Cyrillic
func latinToCyrillic(text string) string {
	result := text
	
	// Process two-letter replacements first (most specific to least specific)
	twoLetterReplacements := [][]string{
		{"Lj", "Љ"}, {"lj", "љ"}, {"LJ", "Љ"},
		{"Nj", "Њ"}, {"nj", "њ"}, {"NJ", "Њ"},
		{"Dž", "Џ"}, {"dž", "џ"}, {"DŽ", "Џ"},
	}
	for _, pair := range twoLetterReplacements {
		result = replaceAll(result, pair[0], pair[1])
	}
	
	// Then single-letter replacements (order matters - special chars before basic ones)
	singleLetterReplacements := [][]string{
		// Special Latin characters first
		{"Đ", "Ђ"}, {"đ", "ђ"},
		{"Ž", "Ж"}, {"ž", "ж"},
		{"Ć", "Ћ"}, {"ć", "ћ"},
		{"Č", "Ч"}, {"č", "ч"},
		{"Š", "Ш"}, {"š", "ш"},
		// Basic letters
		{"A", "А"}, {"a", "а"},
		{"B", "Б"}, {"b", "б"},
		{"V", "В"}, {"v", "в"},
		{"G", "Г"}, {"g", "г"},
		{"D", "Д"}, {"d", "д"},
		{"E", "Е"}, {"e", "е"},
		{"Z", "З"}, {"z", "з"},
		{"I", "И"}, {"i", "и"},
		{"J", "Ј"}, {"j", "ј"},
		{"K", "К"}, {"k", "к"},
		{"L", "Л"}, {"l", "л"},
		{"M", "М"}, {"m", "м"},
		{"N", "Н"}, {"n", "н"},
		{"O", "О"}, {"o", "о"},
		{"P", "П"}, {"p", "п"},
		{"R", "Р"}, {"r", "р"},
		{"S", "С"}, {"s", "с"},
		{"T", "Т"}, {"t", "т"},
		{"U", "У"}, {"u", "у"},
		{"F", "Ф"}, {"f", "ф"},
		{"H", "Х"}, {"h", "х"},
		{"C", "Ц"}, {"c", "ц"},
	}
	
	for _, pair := range singleLetterReplacements {
		result = replaceAll(result, pair[0], pair[1])
	}
	
	return result
}

// replaceAll is a helper function for string replacement
func replaceAll(s, old, new string) string {
	result := ""
	for len(s) > 0 {
		if len(s) >= len(old) && s[:len(old)] == old {
			result += new
			s = s[len(old):]
		} else {
			result += s[:1]
			s = s[1:]
		}
	}
	return result
}

// getSearchVariants returns all variants (original + transliterated) of a search term
func getSearchVariants(term string) []string {
	variants := []string{term}
	
	// Convert to Latin if Cyrillic
	latinVersion := cyrillicToLatin(term)
	if latinVersion != term {
		variants = append(variants, latinVersion)
	}
	
	// Convert to Cyrillic if Latin
	cyrillicVersion := latinToCyrillic(term)
	if cyrillicVersion != term && cyrillicVersion != latinVersion {
		variants = append(variants, cyrillicVersion)
	}
	
	return variants
}

// CachedStats methods (for future use if needed)

// Set sets the cached data
func (cs *CachedStats) Set(data interface{}) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.data = data
	cs.timestamp = time.Now()
}

// Get gets the cached data
func (cs *CachedStats) Get() (interface{}, time.Time) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.data, cs.timestamp
}

// IsExpired checks if the cache is expired
func (cs *CachedStats) IsExpired(maxAge time.Duration) bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return time.Since(cs.timestamp) > maxAge
}

// CachedStatsManager for managing multiple cached stats
type CachedStatsManager struct {
	stats map[string]*CachedStats
	mu    sync.RWMutex
}

// NewCachedStatsManager creates a new cached stats manager
func NewCachedStatsManager() *CachedStatsManager {
	return &CachedStatsManager{
		stats: make(map[string]*CachedStats),
	}
}

// GetOrCreate gets or creates a cached stat
func (csm *CachedStatsManager) GetOrCreate(key string) *CachedStats {
	csm.mu.Lock()
	defer csm.mu.Unlock()

	if cs, exists := csm.stats[key]; exists {
		return cs
	}

	cs := &CachedStats{}
	csm.stats[key] = cs
	return cs
}

