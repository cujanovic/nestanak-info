package main

import (
	"sync"
	"time"
)

// EventRecord tracks a single event occurrence
type EventRecord struct {
	Timestamp   time.Time
	EventType   string // "found", "not_found"
	URL         string
	SearchTerms []string
	Message     string
}

// URLCheckResult represents the result of checking a URL
type URLCheckResult struct {
	URL          string
	Name         string   // Friendly name
	Found        bool
	FoundTerms   []string
	SearchTerms  []string // The search terms used
	Date         string   // Extracted date
	Time         string   // Extracted time
	Address      string   // Extracted address
	Error        error
	CheckedAt    time.Time
	ResponseTime time.Duration
}

// AlertKey uniquely identifies an alert type for a URL
type AlertKey struct {
	URL       string
	AlertType string
}

// LogEntry represents a single log entry
type LogEntry struct {
	Timestamp time.Time
	Message   string
}

// AsyncLogger handles async logging with channels
type AsyncLogger struct {
	logChan     chan LogEntry
	buffer      []LogEntry
	maxLines    int
	mu          sync.RWMutex
	flushTicker *time.Ticker
	stopChan    chan struct{}
}

// CachedStats holds pre-calculated statistics for HTTP serving
type CachedStats struct {
	data      interface{}
	timestamp time.Time
	mu        sync.RWMutex
}

// WorkerPool manages concurrent URL check operations
type WorkerPool struct {
	workers  int
	taskChan chan func()
	wg       sync.WaitGroup
	stopChan chan struct{}
}

// CircularBuffer for recent incidents
type CircularBuffer struct {
	items    []interface{}
	head     int
	tail     int
	count    int
	capacity int
	mu       sync.RWMutex
}

// HTTPRateLimiter tracks HTTP requests per IP
type HTTPRateLimiter struct {
	requests map[string][]time.Time
	mu       sync.Mutex
	limit    int
	window   time.Duration
}

// IncidentInfo represents an incident for display
type IncidentInfo struct {
	URL         string
	Timestamp   string
	EventType   string
	Description string
	IsResolved  bool
	Duration    string
}

// DNSCacheEntry holds cached DNS resolution with expiry
type DNSCacheEntry struct {
	ResolvedIP  string    // The resolved IP address
	OriginalDNS string    // The original DNS name
	CachedAt    time.Time // When this was cached
	ExpiresAt   time.Time // When this cache expires
	mu          sync.RWMutex
}

// DNSCache manages DNS resolution caching
type DNSCache struct {
	entries map[string]*DNSCacheEntry // key: hostname
	mu      sync.RWMutex
	ttl     time.Duration // How long to cache DNS entries
}

