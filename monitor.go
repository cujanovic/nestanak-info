package main

import (
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// Monitor manages the URL checking service
type Monitor struct {
	config                   Config
	lastAlertTime            map[AlertKey]time.Time
	emailsSentThisHour       []time.Time
	emailsSentPerURLToday    map[string][]time.Time // Track emails per URL per day
	errorEmailsSentPerURLToday map[string][]time.Time // Track error emails per URL per day
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
	mu                       sync.RWMutex
	emailMu                  sync.Mutex
}

// NewMonitor creates a new monitor instance
func NewMonitor(config Config) *Monitor {
	// Create DNS cache
	dnsCacheTTL := time.Duration(config.DNSCacheTTLMinutes) * time.Minute
	dnsCache := NewDNSCache(dnsCacheTTL)

	m := &Monitor{
		config:                     config,
		lastAlertTime:              make(map[AlertKey]time.Time),
		emailsSentThisHour:         make([]time.Time, 0),
		emailsSentPerURLToday:      make(map[string][]time.Time),
		errorEmailsSentPerURLToday: make(map[string][]time.Time),
		foundURLs:                  make(map[string]bool),
		unreachableURLs:            make(map[string]bool),
		lastURLDownTime:            make(map[string]time.Time),
		recentEvents:               NewCircularBuffer(config.RecentEventsBufferSize),
		workerPool:                 NewWorkerPool(config.MaxConcurrentChecks),
		dnsCache:                   dnsCache,
		statsStartTime:             time.Now(),
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

// Start starts the monitoring service
func (m *Monitor) Start() {
	m.addLog("🎯 Nestanak-Info Service Started")
	log.Printf("🔍 Monitoring %d URLs", len(m.config.URLConfigs))
	log.Printf("📧 Sending alerts to %d recipients", len(m.config.Recipients))
	log.Printf("🚫 Email limit: %d per URL per day", m.config.MaxEmailsPerURLPerDay)
	log.Printf("🌐 DNS cache TTL: %d minutes", m.config.DNSCacheTTLMinutes)

	// Initialize templates if HTTP is enabled
	if m.config.HTTPEnabled {
		m.templates = initTemplates()
		m.startHTTPServer()
	}

	// Start the check loop
	ticker := time.NewTicker(time.Duration(m.config.CheckIntervalSeconds) * time.Second)
	defer ticker.Stop()

	// Run initial check
	m.checkAllURLs()

	// Start DNS cache cleanup ticker (every 10 minutes)
	cleanupTicker := time.NewTicker(10 * time.Minute)
	defer cleanupTicker.Stop()

	// Run periodic checks
	for {
		select {
		case <-ticker.C:
			m.checkAllURLs()
		case <-cleanupTicker.C:
			m.dnsCache.CleanupExpired()
		}
	}
}

// checkAllURLs checks all configured URLs for search terms
func (m *Monitor) checkAllURLs() {
	log.Printf("🔍 Starting URL check cycle...")
	m.addLog("Starting URL check cycle")

	for _, urlConfig := range m.config.URLConfigs {
		urlConfig := urlConfig // capture for goroutine
		m.workerPool.Submit(func() {
			result := m.checkURL(urlConfig)
			m.handleCheckResult(result)
		})
	}
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

	// Create request with User-Agent header
	req, err := http.NewRequest("GET", urlConfig.URL, nil)
	if err != nil {
		result.Error = err
		return result
	}
	req.Header.Set("User-Agent", userAgent)

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

	if result.Found && !wasFound {
		// Terms found for the first time
		log.Printf("🚨 FOUND: Terms found on %s: %v", result.URL, result.FoundTerms)
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

		// Send alert if allowed
		if m.canSendAlert(result.URL, "found") {
			if err := m.sendEmail(result); err != nil {
				log.Printf("⚠️  Failed to send email alert: %v", err)
				m.addLog(fmt.Sprintf("Failed to send email alert: %v", err))
			} else {
				m.recordAlert(result.URL, "found")
			}
		}
	} else if result.Found {
		log.Printf("✓ Still found on %s: %v", result.URL, result.FoundTerms)
	} else if !result.Found && wasFound {
		// Terms no longer found
		log.Printf("✓ Terms no longer found on %s", result.URL)
		m.addLog(fmt.Sprintf("Terms no longer found on %s", result.URL))

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
func containsAllSearchTerms(content string, terms []string) bool {
	// Special logic for Zemun/Batajnica search:
	// - If only "Земун" is found → Ignore (too broad)
	// - If "Земун" + "Батајница" → Match (valid)
	// - If only "Батајница" → Match (valid)
	
	hasZemun := strings.Contains(content, "Земун")
	hasBatajnica := strings.Contains(content, "Батајница")
	
	// If we have both Zemun and Batajnica in search terms
	if len(terms) == 2 {
		for _, term := range terms {
			if strings.Contains(term, "Земун") {
				hasZemun = strings.Contains(content, term)
			}
			if strings.Contains(term, "Батајница") || strings.Contains(term, "БАТАЈНИЦА") {
				hasBatajnica = strings.Contains(content, term)
			}
		}
		
		// Only Zemun found (no Batajnica) → Ignore
		if hasZemun && !hasBatajnica {
			return false
		}
		
		// Batajnica found (with or without Zemun) → Match
		if hasBatajnica {
			return true
		}
		
		// Neither found
		return false
	}
	
	// Fallback: original logic for other search term combinations
	for _, term := range terms {
		if !strings.Contains(content, term) {
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

// extractTime extracts the time information from HTML content
func extractTime(htmlContent string, searchTerms []string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return ""
	}

	textNodes := extractTextNodes(doc)

	// Find the index of the search term
	for i, text := range textNodes {
		matchAll := true
		for _, term := range searchTerms {
			if !strings.Contains(text, term) {
				matchAll = false
				break
			}
		}

		if matchAll || containsSearchTerm(text, searchTerms[0]) {
			// Look backwards for time information (typically in format XX:XX-XX:XX)
			for j := i - 1; j >= 0 && j >= i-20; j-- {
				if isTimeFormat(textNodes[j]) {
					return strings.TrimSpace(textNodes[j])
				}
			}
		}
	}
	return ""
}

// extractAddress extracts the address information from HTML content
func extractAddress(htmlContent string, searchTerms []string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return ""
	}

	textNodes := extractTextNodes(doc)

	for _, text := range textNodes {
		// Check if this node contains the specific settlement pattern
		for _, term := range searchTerms {
			if strings.Contains(text, term) && strings.Contains(text, ":") {
				// Extract everything after the colon
				parts := strings.SplitN(text, ":", 2)
				if len(parts) == 2 {
					return strings.TrimSpace(parts[1])
				}
			}
		}
	}
	return ""
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

// isTimeFormat checks if text matches time format like "08:00-16:00"
func isTimeFormat(text string) bool {
	text = strings.TrimSpace(text)
	return strings.Contains(text, ":") && (strings.Contains(text, "-") || strings.Contains(text, "–"))
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
				if strings.Contains(text, term) {
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
				// Look for "Земун:" with Batajnica validation
				if strings.Contains(text, "Земун:") {
					// Check next few lines for Batajnica mention or just include Zemun entry
					hasBatajnicaNearby := false
					for j := i; j < i+5 && j < len(textNodes); j++ {
						if strings.Contains(textNodes[j], "Батајница") || strings.Contains(textNodes[j], "Батајнички") {
							hasBatajnicaNearby = true
							break
						}
					}
					
					// Include if Batajnica is nearby or if line itself has relevant terms
					if hasBatajnicaNearby || strings.Contains(text, "Батајница") {
						cleaned := strings.TrimSpace(text)
						cleaned = strings.ReplaceAll(cleaned, "&#8211;", "–")
						if len(cleaned) > 0 {
							addresses = append(addresses, cleaned)
						}
					}
				}
				
				// Also look for direct Batajnica mentions
				if strings.Contains(text, "Батајница") || strings.Contains(text, "Батајнички") {
					cleaned := strings.TrimSpace(text)
					cleaned = strings.ReplaceAll(cleaned, "&#8211;", "–")
					if len(cleaned) > 0 && !strings.Contains(strings.Join(addresses, " "), cleaned) {
						addresses = append(addresses, cleaned)
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
	}
}

