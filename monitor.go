package main

import (
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

// Monitor manages the URL checking service
type Monitor struct {
	userAgentManager         *UserAgentManager
	config                   Config
	state                    *ServiceState           // Persistent state across restarts
	lastAlertTime            map[AlertKey]time.Time
	emailsSentThisHour       []time.Time
	emailsSentPerURLToday    map[string][]time.Time // Track emails per URL per day (in-memory, synced with state)
	errorEmailsSentPerURLToday map[string][]time.Time // Track error emails per URL per day (in-memory, synced with state)
	foundURLs                map[string]bool
	unreachableURLs          map[string]bool         // Track URLs that are down
	lastURLDownTime          map[string]time.Time    // When URL went down
	recentEvents             *CircularBuffer
	asyncLogger              *AsyncLogger
	workerPool               *WorkerPool
	dnsCache                 *DNSCache               // DNS resolution cache
	httpRateLimiter          *HTTPRateLimiter
	sessionManager           *SessionManager
	templates                *template.Template
	statsStartTime           time.Time
	lastCheckTime            time.Time               // When the last check cycle completed (legacy, for compatibility)
	perURLCheckTime          map[string]time.Time    // Last check time per URL
	stopChan                 chan struct{}           // Signal to stop all goroutines
	mu                       sync.RWMutex
	emailMu                  sync.Mutex
}

// NewMonitor creates a new monitor instance
func NewMonitor(config Config) *Monitor {
	// Load persistent state
	state := LoadState(config.StateFilePath)
	
	// Create DNS cache
	dnsCacheTTL := time.Duration(config.DNSCacheTTLMinutes) * time.Minute
	dnsCache := NewDNSCache(dnsCacheTTL)

	// Create User-Agent manager
	userAgentManager := NewUserAgentManager()
	
	// Fetch recent User-Agents if rotation is enabled (non-blocking, falls back on failure)
	if config.UserAgentRotation {
		go func() {
			if err := userAgentManager.FetchUserAgents(config); err != nil {
				log.Printf("⚠️  Using fallback User-Agent due to fetch failure")
			}
		}()
	} else {
		log.Printf("ℹ️  User-Agent rotation disabled, using static User-Agent")
	}

	m := &Monitor{
		userAgentManager:         userAgentManager,
		config:                     config,
		state:                      state,
		lastAlertTime:              make(map[AlertKey]time.Time),
		emailsSentThisHour:         make([]time.Time, 0),
		emailsSentPerURLToday:      state.EmailsSentPerURLToday,      // Initialize from persisted state
		errorEmailsSentPerURLToday: state.ErrorEmailsSentPerURLToday, // Initialize from persisted state
		foundURLs:                  make(map[string]bool),
		unreachableURLs:            make(map[string]bool),
		lastURLDownTime:            make(map[string]time.Time),
		recentEvents:               NewCircularBuffer(config.RecentEventsBufferSize),
		workerPool:                 NewWorkerPool(config.MaxConcurrentChecks),
		dnsCache:                   dnsCache,
		statsStartTime:             time.Now(),
		perURLCheckTime:            make(map[string]time.Time),
		stopChan:                   make(chan struct{}),
	}

	// Initialize async logger
	m.asyncLogger = NewAsyncLogger(
		config.HTTPLogLines,
		time.Duration(config.LogBufferFlushSeconds)*time.Second,
	)

	// Initialize HTTP rate limiter if HTTP is enabled
	if config.HTTPEnabled {
		m.httpRateLimiter = &HTTPRateLimiter{
			requests: make(map[string][]time.Time),
			limit:    config.HTTPRateLimitPerMinute,
			window:   time.Minute,
		}
	}

	// Initialize session manager if auth is enabled
	if config.AuthEnabled {
		m.sessionManager = NewSessionManager(&config)
	}

	return m
}

// Start starts the monitoring service with independent goroutines per URL
func (m *Monitor) Start() {
	m.addLog("🎯 Nestanak-Info Service Started")
	log.Printf("🔍 Monitoring %d URLs with independent check goroutines", len(m.config.URLConfigs))
	log.Printf("📧 Sending alerts to %d recipients", len(m.config.Recipients))
	log.Printf("🚫 Email limit: %d per URL per day", m.config.MaxEmailsPerURLPerDay)
	log.Printf("🌐 DNS cache TTL: %d minutes", m.config.DNSCacheTTLMinutes)
	log.Printf("⏱️  Check interval: %d seconds per URL", m.config.CheckIntervalSeconds)
	
	// Log state statistics
	if m.state != nil {
		stats := m.state.GetStats()
		log.Printf("💾 State loaded: %d seen matches, %d URLs tracked", 
			stats["seen_matches_count"], stats["urls_tracked"])
	}

	// Initialize templates if HTTP is enabled
	if m.config.HTTPEnabled {
		m.templates = initTemplates()
		m.startHTTPServer()
	}

	// Start independent goroutine for each URL with staggered timing
	var wg sync.WaitGroup
	for i, urlConfig := range m.config.URLConfigs {
		wg.Add(1)
		go m.monitorURL(urlConfig, i, &wg)
	}

	// Start state persistence ticker (every 5 minutes)
	stateTicker := time.NewTicker(5 * time.Minute)
	defer stateTicker.Stop()

	// Start DNS cache cleanup ticker (every 10 minutes)
	cleanupTicker := time.NewTicker(10 * time.Minute)
	defer cleanupTicker.Stop()

	// Background maintenance tasks
	go func() {
		for {
			select {
			case <-stateTicker.C:
				m.saveState()
			case <-cleanupTicker.C:
				m.dnsCache.CleanupExpired()
			case <-m.stopChan:
				return
			}
		}
	}()

	// Wait for all URL monitors to finish (they run forever until stopChan is closed)
	wg.Wait()
}

// Shutdown gracefully shuts down the monitor
func (m *Monitor) Shutdown() {
	log.Printf("🛑 Initiating shutdown...")
	
	// Close stopChan to signal all goroutines to stop
	close(m.stopChan)
	
	// Give goroutines a moment to finish
	time.Sleep(2 * time.Second)
	
	// Save state one last time
	m.saveState()
	log.Printf("💾 Final state saved")
	
	// Stop async logger
	if m.asyncLogger != nil {
		m.asyncLogger.Stop()
	}
	
	// Stop worker pool
	if m.workerPool != nil {
		m.workerPool.Stop()
	}
	
	log.Printf("✅ Monitor shutdown complete")
}

// saveState persists current state to disk
func (m *Monitor) saveState() {
	if m.state == nil || m.config.StateFilePath == "" {
		return
	}

	// Sync in-memory state with persistent state
	m.emailMu.Lock()
	m.state.EmailsSentPerURLToday = m.emailsSentPerURLToday
	m.state.ErrorEmailsSentPerURLToday = m.errorEmailsSentPerURLToday
	m.emailMu.Unlock()

	// Save to file
	if err := m.state.SaveState(m.config.StateFilePath); err != nil {
		log.Printf("⚠️  Failed to save state: %v", err)
	} else {
		log.Printf("💾 State saved to %s", m.config.StateFilePath)
	}
}

// monitorURL runs an independent check loop for a single URL
func (m *Monitor) monitorURL(urlConfig URLConfig, index int, wg *sync.WaitGroup) {
	defer wg.Done()

	// Calculate staggered start delay to distribute checks
	// Spread URLs evenly across the check interval
	intervalDuration := time.Duration(m.config.CheckIntervalSeconds) * time.Second
	staggerDelay := (intervalDuration / time.Duration(len(m.config.URLConfigs))) * time.Duration(index)
	
	displayName := urlConfig.Name
	if displayName == "" {
		displayName = urlConfig.URL
	}
	
	log.Printf("🔄 URL monitor starting for '%s' (stagger: %v)", displayName, staggerDelay)
	
	// Initial staggered delay
	select {
	case <-time.After(staggerDelay):
	case <-m.stopChan:
		return
	}

	// Run first check immediately after stagger
	m.checkSingleURL(urlConfig)

	// Set up ticker for periodic checks
	ticker := time.NewTicker(intervalDuration)
	defer ticker.Stop()

	// Check loop
	for {
		select {
		case <-ticker.C:
			m.checkSingleURL(urlConfig)
		case <-m.stopChan:
			log.Printf("🛑 Stopping monitor for '%s'", displayName)
			return
		}
	}
}

// checkSingleURL checks a single URL and handles the result
func (m *Monitor) checkSingleURL(urlConfig URLConfig) {
	result := m.checkURL(urlConfig)
	m.handleCheckResult(result)
	
	// Update per-URL check time
	m.mu.Lock()
	m.perURLCheckTime[urlConfig.URL] = time.Now()
	m.lastCheckTime = time.Now() // Also update legacy field for compatibility
	m.mu.Unlock()
}

// checkURL checks a single URL for search terms
func (m *Monitor) checkURL(urlConfig URLConfig) URLCheckResult {
	result := URLCheckResult{
		URL:         urlConfig.URL,
		Name:        urlConfig.Name,
		SearchTerms: urlConfig.SearchTerms,
		CheckedAt:   time.Now(),
	}

	// Parse URL to extract hostname for DNS caching
	parsedURL, err := url.Parse(urlConfig.URL)
	if err == nil && parsedURL.Hostname() != "" {
		// Resolve DNS with caching
		_, ipChanged, dnsErr := m.dnsCache.Resolve(parsedURL.Hostname())
		if dnsErr != nil {
			log.Printf("⚠️  DNS resolution warning for %s: %v (will try HTTP anyway)", parsedURL.Hostname(), dnsErr)
		}
		if ipChanged {
			displayName := urlConfig.Name
			if displayName == "" {
				displayName = urlConfig.URL
			}
			m.addLog(fmt.Sprintf("🔄 DNS IP changed for %s", displayName))
		}
	}

	client := &http.Client{
		Timeout: time.Duration(m.config.ConnectTimeout) * time.Second,
	}

	// Create request with rotating User-Agent header
	req, err := http.NewRequest("GET", urlConfig.URL, nil)
	if err != nil {
		result.Error = err
		return result
	}
	currentUserAgent := m.userAgentManager.GetNext()
	req.Header.Set("User-Agent", currentUserAgent)

	startTime := time.Now()
	resp, err := client.Do(req)
	result.ResponseTime = time.Since(startTime)

	if err != nil {
		result.Error = err
		// Error will be handled in handleCheckResult
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		result.Error = fmt.Errorf("HTTP %d", resp.StatusCode)
		return result
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		result.Error = err
		return result
	}

	bodyStr := string(body)

	// Check if content contains all search terms for this URL
	if containsAllSearchTerms(bodyStr, urlConfig.SearchTerms) {
		result.Found = true
		result.FoundTerms = urlConfig.SearchTerms
		
		// Extract detailed information based on URL type
		if strings.Contains(urlConfig.URL, "bvk.rs") {
			// Water outage extraction (different format)
			result.Date = extractDateWater(bodyStr, urlConfig.SearchTerms)
			result.Time = extractTimeWater(bodyStr, urlConfig.URL)
			result.Address = extractAddressWater(bodyStr, urlConfig.SearchTerms, urlConfig.URL)
		} else {
			// Power outage extraction (original format)
			result.Date = extractDate(bodyStr)
			result.Time = extractTime(bodyStr, urlConfig.SearchTerms)
			result.Address = extractAddress(bodyStr, urlConfig.SearchTerms)
		}
	}

	return result
}

// handleCheckResult handles the result of a URL check
func (m *Monitor) handleCheckResult(result URLCheckResult) {
	if result.Error != nil {
		log.Printf("⚠️  Error checking %s: %v", result.URL, result.Error)
		m.addLog(fmt.Sprintf("Error checking %s: %v", result.URL, result.Error))
		
		// Track connection failure
		m.handleConnectionFailure(result)
		return
	}
	
	// URL is reachable - check if it was previously down
	m.handleConnectionRecovery(result)

	m.mu.Lock()
	wasFound := m.foundURLs[result.URL]
	m.foundURLs[result.URL] = result.Found
	m.mu.Unlock()

	if result.Found {
		// Generate hash from match content (URL + Date + Time + Address)
		matchHash := GenerateMatchHash(result.URL, result.Date, result.Time, result.Address)
		
		// Check if we've already notified about this exact match
		maxAge := 7 * 24 * time.Hour // Don't send duplicate emails for 7 days
		alreadySeen := m.state != nil && m.state.IsMatchSeen(matchHash, maxAge)
		
		if !wasFound {
			// Terms found for the first time
			log.Printf("🚨 FOUND: Terms found on %s: %v", result.URL, result.FoundTerms)
			log.Printf("   📅 Date: %s, Time: %s, Address: %s", result.Date, result.Time, result.Address)
			m.addLog(fmt.Sprintf("FOUND: Terms found on %s: %v", result.URL, result.FoundTerms))

			// Record event
			event := EventRecord{
				Timestamp:   time.Now(),
				EventType:   "found",
				URL:         result.URL,
				SearchTerms: result.FoundTerms,
				Message:     fmt.Sprintf("Search terms found: %s", strings.Join(result.FoundTerms, ", ")),
			}
			m.recentEvents.Add(event)

			// Send alert if allowed and not already seen
			if alreadySeen {
				log.Printf("ℹ️  Skipping duplicate email - already notified about this incident (hash: %s...)", matchHash[:8])
				m.addLog("Skipping duplicate email - already notified about this incident")
			} else if m.canSendAlert(result.URL, "found") {
				if err := m.sendEmail(result); err != nil {
					log.Printf("⚠️  Failed to send email alert: %v", err)
					m.addLog(fmt.Sprintf("Failed to send email alert: %v", err))
				} else {
					m.recordAlert(result.URL, "found")
					// Record this match in persistent state
					if m.state != nil {
						m.state.RecordMatch(matchHash, result.URL, result.Date, result.Time, result.Address)
						// Save state immediately after sending email (don't wait for 5min ticker)
						go m.saveState()
					}
				}
			}
		} else {
			log.Printf("✓ Still found on %s: %v", result.URL, result.FoundTerms)
			
			// Even if still found, don't send another email if it's the same incident
			if alreadySeen {
				log.Printf("   (Same incident as before - hash: %s...)", matchHash[:8])
			}
		}
	} else if !result.Found && wasFound {
		// Terms no longer found
		log.Printf("✓ Terms no longer found on %s", result.URL)
		m.addLog(fmt.Sprintf("Terms no longer found on %s", result.URL))
		
		// Save state since status changed
		go m.saveState()

		// Record recovery event
		event := EventRecord{
			Timestamp: time.Now(),
			EventType: "not_found",
			URL:       result.URL,
			Message:   "Search terms no longer found",
		}
		m.recentEvents.Add(event)
	} else {
		log.Printf("✓ No terms found on %s", result.URL)
	}
}

// canSendAlert checks if an alert can be sent based on cooldown and rate limiting
func (m *Monitor) canSendAlert(url string, alertType string) bool {
	m.mu.RLock()
	key := AlertKey{URL: url, AlertType: alertType}
	lastAlert, exists := m.lastAlertTime[key]
	m.mu.RUnlock()

	// Check cooldown
	if exists {
		cooldownDuration := time.Duration(m.config.AlertCooldownMinutes) * time.Minute
		if time.Since(lastAlert) < cooldownDuration {
			log.Printf("⏱️  Alert cooldown active for %s (%s)", url, alertType)
			return false
		}
	}

	// Check global rate limit (per hour)
	m.emailMu.Lock()
	defer m.emailMu.Unlock()

	now := time.Now()
	oneHourAgo := now.Add(-time.Hour)
	validEmails := make([]time.Time, 0)
	for _, t := range m.emailsSentThisHour {
		if t.After(oneHourAgo) {
			validEmails = append(validEmails, t)
		}
	}
	m.emailsSentThisHour = validEmails

	if len(m.emailsSentThisHour) >= m.config.EmailRateLimitPerHour {
		log.Printf("⚠️  Global email rate limit reached (%d/hour)", m.config.EmailRateLimitPerHour)
		return false
	}

	// Check per-URL daily limit
	oneDayAgo := now.Add(-24 * time.Hour)
	urlEmails, exists := m.emailsSentPerURLToday[url]
	if exists {
		validURLEmails := make([]time.Time, 0)
		for _, t := range urlEmails {
			if t.After(oneDayAgo) {
				validURLEmails = append(validURLEmails, t)
			}
		}
		m.emailsSentPerURLToday[url] = validURLEmails

		if len(validURLEmails) >= m.config.MaxEmailsPerURLPerDay {
			log.Printf("⚠️  Daily email limit reached for URL %s (%d/%d)", url, len(validURLEmails), m.config.MaxEmailsPerURLPerDay)
			return false
		}
	}

	return true
}

// recordAlert records that an alert was sent
func (m *Monitor) recordAlert(url string, alertType string) {
	now := time.Now()

	m.mu.Lock()
	key := AlertKey{URL: url, AlertType: alertType}
	m.lastAlertTime[key] = now
	m.mu.Unlock()

	m.emailMu.Lock()
	m.emailsSentThisHour = append(m.emailsSentThisHour, now)
	
	// Track per-URL emails
	if m.emailsSentPerURLToday[url] == nil {
		m.emailsSentPerURLToday[url] = make([]time.Time, 0)
	}
	m.emailsSentPerURLToday[url] = append(m.emailsSentPerURLToday[url], now)
	m.emailMu.Unlock()
}

// addLog adds a log entry
func (m *Monitor) addLog(message string) {
	if m.asyncLogger != nil {
		m.asyncLogger.Add(LogEntry{
			Timestamp: time.Now(),
			Message:   message,
		})
	}
}

// getRecentLogs returns recent log entries
func (m *Monitor) getRecentLogs() []LogEntry {
	if m.asyncLogger != nil {
		return m.asyncLogger.GetLogs()
	}
	return []LogEntry{}
}

// getRecentMatches returns recent matches for display
func (m *Monitor) getRecentMatches() []IncidentInfo {
	matches := make([]IncidentInfo, 0)
	
	if m.recentEvents == nil {
		return matches
	}

	events := m.recentEvents.GetAll()
	cutoff := time.Now().Add(-time.Duration(m.config.RecentMatchesHours) * time.Hour)

	for _, item := range events {
		event, ok := item.(EventRecord)
		if !ok {
			continue
		}

		if event.Timestamp.Before(cutoff) {
			continue
		}

		match := IncidentInfo{
			URL:         event.URL,
			Timestamp:   m.formatLocalTime(event.Timestamp),
			EventType:   event.EventType,
			Description: event.Message,
			IsResolved:  event.EventType == "not_found",
		}

		matches = append(matches, match)
	}

	return matches
}

// containsAllSearchTerms checks if content contains all search terms
// Supports both Cyrillic and Latin script variants automatically
func containsAllSearchTerms(content string, terms []string) bool {
	if len(terms) == 0 {
		return false
	}
	
	// Convert content to lowercase for case-insensitive matching
	contentLower := strings.ToLower(content)
	
	// Helper function to check if any variant of a term is in content
	containsAnyVariant := func(content string, term string) bool {
		variants := getSearchVariants(term)
		for _, variant := range variants {
			if strings.Contains(content, strings.ToLower(variant)) {
				return true
			}
		}
		return false
	}
	
	// Special logic for exactly 2 search terms:
	// - Term 1 (index 0) = broader/general term (e.g., "Земун" = municipality)
	// - Term 2 (index 1) = specific term (e.g., "Батајница" = settlement)
	// Logic:
	//   - If only term 1 found → Ignore (too broad, could be anywhere in municipality)
	//   - If term 1 + term 2 → Match (specific area mentioned)
	//   - If only term 2 → Match (specific area mentioned)
	if len(terms) == 2 {
		broadTerm := terms[0]    // First term is the broader one
		specificTerm := terms[1] // Second term is the specific one
		
		hasBroad := containsAnyVariant(contentLower, broadTerm)
		hasSpecific := containsAnyVariant(contentLower, specificTerm)
		
		// Only broad term found (no specific) → Ignore
		if hasBroad && !hasSpecific {
			return false
		}
		
		// Specific term found (with or without broad term) → Match
		if hasSpecific {
			return true
		}
		
		// Neither found
		return false
	}
	
	// For 1 term or 3+ terms: all must be present (standard AND logic)
	for _, term := range terms {
		if !containsAnyVariant(contentLower, term) {
			return false
		}
	}
	return true
}

// extractDate extracts the date from HTML content
func extractDate(htmlContent string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return ""
	}

	var date string
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if strings.Contains(text, "Планирана искључења за датум:") {
				date = strings.TrimPrefix(text, "Планирана искључења за датум:")
				date = strings.TrimSpace(date)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)
	return date
}

// extractTime extracts the time information from HTML table
func extractTime(htmlContent string, searchTerms []string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return ""
	}

	// Helper function to check if row should be extracted based on search term logic
	shouldExtractRow := func(rowText string) bool {
		rowLower := strings.ToLower(rowText)
		
		// For 2 search terms: use special broad/specific logic
		if len(searchTerms) == 2 {
			specificTerm := searchTerms[1] // e.g., "Батајница" (specific term)
			
			// Check if row contains the specific term (with Cyrillic/Latin variants)
			specificVariants := getSearchVariants(specificTerm)
			hasSpecific := false
			for _, variant := range specificVariants {
				if strings.Contains(rowLower, strings.ToLower(variant)) {
					hasSpecific = true
					break
				}
			}
			
			// Only extract if specific term is present
			return hasSpecific
		}
		
		// For 1 or 3+ terms: row must contain ALL terms
		for _, term := range searchTerms {
			variants := getSearchVariants(term)
			hasTerm := false
			for _, variant := range variants {
				if strings.Contains(rowLower, strings.ToLower(variant)) {
					hasTerm = true
					break
				}
			}
			if !hasTerm {
				return false
			}
		}
		return true
	}

	// Parse table structure: find rows where search terms appear, extract time from same row
	var result string
	var findTable func(*html.Node)
	findTable = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "table" {
			// Found a table, now parse rows
			var parseRow func(*html.Node)
			parseRow = func(row *html.Node) {
				if row.Type == html.ElementNode && row.Data == "tr" {
					// Extract all cells from this row
					var cells []string
					var extractCells func(*html.Node)
					extractCells = func(cell *html.Node) {
						if cell.Type == html.ElementNode && (cell.Data == "td" || cell.Data == "th") {
							// Get text content from this cell
							cellText := getTextContent(cell)
							cells = append(cells, cellText)
						}
						for c := cell.FirstChild; c != nil; c = c.NextSibling {
							extractCells(c)
						}
					}
					for c := row.FirstChild; c != nil; c = c.NextSibling {
						extractCells(c)
					}
					
					// Check if row should be extracted (uses smart term matching)
					if len(cells) >= 3 {
						// Get full row text for matching
						rowText := strings.Join(cells, " ")
						
						if shouldExtractRow(rowText) {
							// Extract time from the appropriate column (usually column index 1)
							// Try each cell until we find one with time format
							for _, cell := range cells {
								if isTimeFormat(cell) {
									result = strings.TrimSpace(cell)
									return
								}
							}
						}
					}
				}
				for c := row.FirstChild; c != nil; c = c.NextSibling {
					parseRow(c)
				}
			}
			parseRow(n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findTable(c)
		}
	}
	findTable(doc)
	return result
}

// extractAddress extracts the address information from HTML table
func extractAddress(htmlContent string, searchTerms []string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return ""
	}

	// Helper function to check if row should be extracted based on search term logic
	shouldExtractRow := func(rowText string) bool {
		rowLower := strings.ToLower(rowText)
		
		// For 2 search terms: use special broad/specific logic
		if len(searchTerms) == 2 {
			specificTerm := searchTerms[1] // e.g., "Батајница" (specific term)
			
			// Check if row contains the specific term (with Cyrillic/Latin variants)
			specificVariants := getSearchVariants(specificTerm)
			hasSpecific := false
			for _, variant := range specificVariants {
				if strings.Contains(rowLower, strings.ToLower(variant)) {
					hasSpecific = true
					break
				}
			}
			
			// Only extract if specific term is present
			return hasSpecific
		}
		
		// For 1 or 3+ terms: row must contain ALL terms
		for _, term := range searchTerms {
			variants := getSearchVariants(term)
			hasTerm := false
			for _, variant := range variants {
				if strings.Contains(rowLower, strings.ToLower(variant)) {
					hasTerm = true
					break
				}
			}
			if !hasTerm {
				return false
			}
		}
		return true
	}

	// Parse table structure: find rows where search terms appear, extract address from same row
	var result string
	var findTable func(*html.Node)
	findTable = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "table" {
			// Found a table, now parse rows
			var parseRow func(*html.Node)
			parseRow = func(row *html.Node) {
				if row.Type == html.ElementNode && row.Data == "tr" {
					// Extract all cells from this row
					var cells []string
					var extractCells func(*html.Node)
					extractCells = func(cell *html.Node) {
						if cell.Type == html.ElementNode && (cell.Data == "td" || cell.Data == "th") {
							// Get text content from this cell
							cellText := getTextContent(cell)
							cells = append(cells, cellText)
						}
						for c := cell.FirstChild; c != nil; c = c.NextSibling {
							extractCells(c)
						}
					}
					for c := row.FirstChild; c != nil; c = c.NextSibling {
						extractCells(c)
					}
					
					// Check if row should be extracted (uses smart term matching)
					if len(cells) >= 3 {
						// Get full row text for matching
						rowText := strings.Join(cells, " ")
						
						if shouldExtractRow(rowText) {
							// Return the THIRD column (index 2) which contains the addresses
							addressCell := cells[2] // Third column = Улице (addresses)
							result = strings.TrimSpace(addressCell)
							return
						}
					}
				}
				for c := row.FirstChild; c != nil; c = c.NextSibling {
					parseRow(c)
				}
			}
			parseRow(n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findTable(c)
		}
	}
	findTable(doc)
	return result
}

// getTextContent extracts all text content from a node and its children
func getTextContent(n *html.Node) string {
	var result strings.Builder
	var extract func(*html.Node)
	extract = func(node *html.Node) {
		if node.Type == html.TextNode {
			result.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			extract(c)
		}
	}
	extract(n)
	return strings.TrimSpace(result.String())
}

// extractTextNodes extracts all text nodes from HTML
func extractTextNodes(n *html.Node) []string {
	var texts []string
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				texts = append(texts, text)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(n)
	return texts
}

// containsSearchTerm checks if text contains a search term
func containsSearchTerm(text string, term string) bool {
	return strings.Contains(text, term)
}

// isTimeFormat checks if text matches time format like "08:00-16:00" or "08:00 - 16:00"
func isTimeFormat(text string) bool {
	text = strings.TrimSpace(text)
	// Match patterns like "09:30 - 14:00" or "09:30-14:00" or "08:00–16:00"
	// Must have digits:digits format, not just any colon (to avoid matching street addresses like "УЛИЦА: 2-14А")
	timePattern := regexp.MustCompile(`\d{1,2}:\d{2}\s*[-–]\s*\d{1,2}:\d{2}`)
	return timePattern.MatchString(text)
}

// ========== Water-specific extraction functions (BVK) ==========

// extractDateWater extracts date from BVK water pages
func extractDateWater(htmlContent string, searchTerms []string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return ""
	}

	textNodes := extractTextNodes(doc)
	
	// Look for date patterns near the search terms
	// Format: "31.10/01.11.2025. године" or "31.10.2025."
	for i, text := range textNodes {
		// Check if this line contains our search terms
		hasSearchTerm := false
		for _, term := range searchTerms {
			if strings.Contains(text, term) {
				hasSearchTerm = true
				break
			}
		}
		
		if hasSearchTerm {
			// Look backwards and forwards for date pattern
			for j := i - 3; j <= i+3 && j < len(textNodes); j++ {
				if j < 0 {
					continue
				}
				// Look for patterns like "31.10/01.11.2025. године" or "31.10.2025."
				if strings.Contains(textNodes[j], "године") || strings.Contains(textNodes[j], ".2025") || strings.Contains(textNodes[j], ".2026") {
					return strings.TrimSpace(textNodes[j])
				}
			}
		}
	}
	return ""
}

// extractTimeWater extracts time from BVK water pages
func extractTimeWater(htmlContent string, url string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return ""
	}

	textNodes := extractTextNodes(doc)
	
	// For planned work (planirani-radovi): look for "у времену од XX.XX до XX.XX сати"
	if strings.Contains(url, "planirani-radovi") {
		for _, text := range textNodes {
			if strings.Contains(text, "времену од") && strings.Contains(text, "сати") {
				return strings.TrimSpace(text)
			}
		}
	}
	
	// For malfunctions (kvarovi): look for "До XX:XX" pattern at the top
	if strings.Contains(url, "kvarovi") {
		for _, text := range textNodes {
			if strings.Contains(text, "До") && strings.Contains(text, ":") {
				return strings.TrimSpace(text)
			}
		}
	}
	
	return ""
}

// extractAddressWater extracts address/location from BVK water pages
func extractAddressWater(htmlContent string, searchTerms []string, url string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return ""
	}

	textNodes := extractTextNodes(doc)
	addresses := make([]string, 0)
	
	// For planned work: extract settlement names
	if strings.Contains(url, "planirani-radovi") {
		for _, text := range textNodes {
			// Look for lines with our search terms
			for _, term := range searchTerms {
				if strings.Contains(strings.ToLower(text), strings.ToLower(term)) {
					// Extract the whole line as it contains settlement info
					// Example: "у naseljима Батајница и Бусије"
					cleaned := strings.TrimSpace(text)
					if len(cleaned) > 0 {
						addresses = append(addresses, cleaned)
					}
				}
			}
		}
	}
	
	// For malfunctions: only extract from "Без воде су потрошачи" section
	if strings.Contains(url, "kvarovi") {
		inWaterOutageSection := false
		
		for i, text := range textNodes {
			// Detect start of relevant section
			if strings.Contains(text, "Без воде су потрошачи") {
				inWaterOutageSection = true
				continue
			}
			
			// Detect end of relevant section (cistern trucks section)
			if strings.Contains(text, "Распоред аутоцистерни") || strings.Contains(text, "аутоцистерни") {
				inWaterOutageSection = false
				break
			}
			
			// Only process if we're in the correct section
			if inWaterOutageSection {
				// For 2 search terms: use smart logic
				if len(searchTerms) == 2 {
					broadTerm := searchTerms[0]    // e.g., "Земун" (municipality)
					specificTerm := searchTerms[1] // e.g., "Батајница" (settlement)
					
					// Look for broad term followed by ":" (e.g., "Земун:")
					if strings.Contains(strings.ToLower(text), strings.ToLower(broadTerm)+":") {
						// Check next few lines for specific term mention
						hasSpecificNearby := false
						for j := i; j < i+5 && j < len(textNodes); j++ {
							if strings.Contains(strings.ToLower(textNodes[j]), strings.ToLower(specificTerm)) {
								hasSpecificNearby = true
								break
							}
						}
						
						// Include if specific term is nearby or in the line itself
						if hasSpecificNearby || strings.Contains(strings.ToLower(text), strings.ToLower(specificTerm)) {
							cleaned := strings.TrimSpace(text)
							cleaned = strings.ReplaceAll(cleaned, "&#8211;", "–")
							
							// Filter addresses to only include those containing the specific term
							// Split by comma and keep only addresses with the specific term
							if strings.Contains(cleaned, ",") {
								// Extract the municipality prefix (e.g., "Земун:")
								parts := strings.SplitN(cleaned, ":", 2)
								if len(parts) == 2 {
									prefix := strings.TrimSpace(parts[0]) + ":"
									addressList := parts[1]
									
									// Split addresses by comma
									addressParts := strings.Split(addressList, ",")
									filteredAddresses := make([]string, 0)
									
									for _, addr := range addressParts {
										addr = strings.TrimSpace(addr)
										// Keep addresses that contain the specific term
										if strings.Contains(strings.ToLower(addr), strings.ToLower(specificTerm)) {
											filteredAddresses = append(filteredAddresses, addr)
										}
									}
									
									// Only add if we found relevant addresses
									if len(filteredAddresses) > 0 {
										result := prefix + " " + strings.Join(filteredAddresses, ", ")
										addresses = append(addresses, result)
									}
								}
							} else {
								// No commas, just add the whole line if it contains specific term
								if len(cleaned) > 0 && strings.Contains(strings.ToLower(cleaned), strings.ToLower(specificTerm)) {
									addresses = append(addresses, cleaned)
								}
							}
						}
					} else if strings.Contains(strings.ToLower(text), strings.ToLower(specificTerm)) {
						// Also look for direct specific term mentions (not already processed above)
						cleaned := strings.TrimSpace(text)
						cleaned = strings.ReplaceAll(cleaned, "&#8211;", "–")
						
						// If this line has commas, it might be a multi-address line, so filter it
						if strings.Contains(cleaned, ",") {
							// Split addresses by comma
							addressParts := strings.Split(cleaned, ",")
							filteredAddresses := make([]string, 0)
							
							for _, addr := range addressParts {
								addr = strings.TrimSpace(addr)
								// Keep addresses that contain the specific term
								if strings.Contains(strings.ToLower(addr), strings.ToLower(specificTerm)) {
									filteredAddresses = append(filteredAddresses, addr)
								}
							}
							
							// Only add if we found relevant addresses and not already added
							if len(filteredAddresses) > 0 {
								result := strings.Join(filteredAddresses, ", ")
								if !strings.Contains(strings.Join(addresses, " "), result) {
									addresses = append(addresses, result)
								}
							}
						} else {
							// No commas, just add the whole line if it contains specific term
							if len(cleaned) > 0 && !strings.Contains(strings.Join(addresses, " "), cleaned) && strings.Contains(strings.ToLower(cleaned), strings.ToLower(specificTerm)) {
								addresses = append(addresses, cleaned)
							}
						}
					}
				} else {
					// For 1 or 3+ search terms: include lines containing any term
					for _, term := range searchTerms {
						if strings.Contains(strings.ToLower(text), strings.ToLower(term)) {
							cleaned := strings.TrimSpace(text)
							cleaned = strings.ReplaceAll(cleaned, "&#8211;", "–")
							if len(cleaned) > 0 && !strings.Contains(strings.Join(addresses, " "), cleaned) {
								addresses = append(addresses, cleaned)
							}
							break
						}
					}
				}
			}
		}
	}
	
	// Return combined addresses
	if len(addresses) > 0 {
		return strings.Join(addresses, "; ")
	}
	return ""
}

// handleConnectionFailure handles a URL that is unreachable
func (m *Monitor) handleConnectionFailure(result URLCheckResult) {
	m.mu.Lock()
	wasUnreachable := m.unreachableURLs[result.URL]
	m.unreachableURLs[result.URL] = true
	
	// Record when it went down (if this is the first failure)
	if !wasUnreachable {
		m.lastURLDownTime[result.URL] = time.Now()
	}
	m.mu.Unlock()
	
	// If this is the first failure, send error email (with rate limiting)
	if !wasUnreachable {
		if m.canSendErrorEmail(result.URL) {
			m.sendErrorEmail(result.URL, result.Name, result.Error)
			m.recordErrorEmail(result.URL)
			// Save state immediately after sending error email
			go m.saveState()
		}
	}
}

// handleConnectionRecovery handles a URL that recovered from being unreachable
func (m *Monitor) handleConnectionRecovery(result URLCheckResult) {
	m.mu.Lock()
	wasUnreachable := m.unreachableURLs[result.URL]
	downTime := m.lastURLDownTime[result.URL]
	
	// Clear unreachable status
	if wasUnreachable {
		delete(m.unreachableURLs, result.URL)
		delete(m.lastURLDownTime, result.URL)
	}
	m.mu.Unlock()
	
	// Send recovery email if it was previously unreachable
	if wasUnreachable {
		duration := time.Since(downTime)
		log.Printf("✅ URL recovered: %s (was down for %s)", result.URL, formatDuration(duration))
		m.addLog(fmt.Sprintf("URL recovered: %s (was down for %s)", result.URL, formatDuration(duration)))
		
		if m.canSendErrorEmail(result.URL) {
			m.sendRecoveryEmail(result.URL, result.Name, duration)
			m.recordErrorEmail(result.URL)
			// Save state immediately after sending recovery email
			go m.saveState()
		}
	}
}

// canSendErrorEmail checks if an error email can be sent for this URL
func (m *Monitor) canSendErrorEmail(url string) bool {
	if m.config.ErrorRecipient == "" {
		return false
	}
	
	m.emailMu.Lock()
	defer m.emailMu.Unlock()
	
	now := time.Now()
	oneDayAgo := now.Add(-24 * time.Hour)
	
	// Check per-URL daily limit for error emails
	urlErrorEmails, exists := m.errorEmailsSentPerURLToday[url]
	if exists {
		validErrorEmails := make([]time.Time, 0)
		for _, t := range urlErrorEmails {
			if t.After(oneDayAgo) {
				validErrorEmails = append(validErrorEmails, t)
			}
		}
		m.errorEmailsSentPerURLToday[url] = validErrorEmails
		
		// Allow up to 3 error emails per URL per day
		if len(validErrorEmails) >= 3 {
			log.Printf("⚠️  Daily error email limit reached for URL %s (%d/3)", url, len(validErrorEmails))
			return false
		}
	}
	
	return true
}

// recordErrorEmail records that an error email was sent
func (m *Monitor) recordErrorEmail(url string) {
	m.emailMu.Lock()
	defer m.emailMu.Unlock()
	
	now := time.Now()
	if m.errorEmailsSentPerURLToday[url] == nil {
		m.errorEmailsSentPerURLToday[url] = make([]time.Time, 0)
	}
	m.errorEmailsSentPerURLToday[url] = append(m.errorEmailsSentPerURLToday[url], now)
}

// sendErrorEmail sends an error notification email
func (m *Monitor) sendErrorEmail(url, name string, err error) {
	displayName := name
	if displayName == "" {
		displayName = url
	}
	
	subject := fmt.Sprintf("🔴 Nestanak-Info - Connection Error: %s", displayName)
	body := fmt.Sprintf(`Connection Error Detected

URL Name: %s
URL: %s

Error Details:
%v

Timestamp: %s

This URL is currently unreachable. You will receive a recovery notification when the connection is restored.`, displayName, url, err, m.formatLocalTime(time.Now()))

	if sendErr := sendBrevoEmail(m.config, m.config.ErrorRecipient, subject, body); sendErr != nil {
		log.Printf("Failed to send error email to %s: %v", m.config.ErrorRecipient, sendErr)
	} else {
		log.Printf("📧 Error notification sent to %s for %s", m.config.ErrorRecipient, displayName)
		m.recordEmailNotification(url, name, []string{m.config.ErrorRecipient}, "error", subject)
	}
}

// sendRecoveryEmail sends a recovery notification email
func (m *Monitor) sendRecoveryEmail(url, name string, downtime time.Duration) {
	displayName := name
	if displayName == "" {
		displayName = url
	}
	
	subject := fmt.Sprintf("🟢 Nestanak-Info - Connection Restored: %s", displayName)
	body := fmt.Sprintf(`Connection Restored

URL Name: %s
URL: %s

Downtime Duration: %s
Restored At: %s

The URL is now reachable again and monitoring has resumed.`, displayName, url, formatDuration(downtime), m.formatLocalTime(time.Now()))

	if sendErr := sendBrevoEmail(m.config, m.config.ErrorRecipient, subject, body); sendErr != nil {
		log.Printf("Failed to send recovery email to %s: %v", m.config.ErrorRecipient, sendErr)
	} else {
		log.Printf("📧 Recovery notification sent to %s for %s", m.config.ErrorRecipient, displayName)
		m.recordEmailNotification(url, name, []string{m.config.ErrorRecipient}, "recovery", subject)
	}
}

// recordEmailNotification records an email notification for display in web UI
func (m *Monitor) recordEmailNotification(url, name string, recipients []string, emailType, subject string) {
	if m.state == nil {
		return // State not initialized, skip recording
	}

	m.state.mu.Lock()
	defer m.state.mu.Unlock()

	notification := EmailNotification{
		Timestamp:  time.Now(),
		Recipients: recipients,
		URL:        url,
		URLName:    name,
		Type:       emailType,
		Subject:    subject,
	}

	m.state.RecentEmailNotifications = append(m.state.RecentEmailNotifications, notification)

	// Keep only last 100 notifications
	if len(m.state.RecentEmailNotifications) > 100 {
		m.state.RecentEmailNotifications = m.state.RecentEmailNotifications[len(m.state.RecentEmailNotifications)-100:]
	}
}

// getRecentEmailNotifications returns recent email notifications for display
func (m *Monitor) getRecentEmailNotifications(limit int) []EmailNotification {
	if m.state == nil {
		return []EmailNotification{} // State not initialized, return empty
	}

	m.state.mu.RLock()
	defer m.state.mu.RUnlock()

	if len(m.state.RecentEmailNotifications) == 0 {
		return []EmailNotification{}
	}

	// Return last N notifications (most recent first)
	notifications := make([]EmailNotification, 0)
	start := len(m.state.RecentEmailNotifications) - limit
	if start < 0 {
		start = 0
	}

	for i := len(m.state.RecentEmailNotifications) - 1; i >= start; i-- {
		notifications = append(notifications, m.state.RecentEmailNotifications[i])
	}

	return notifications
}

