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

