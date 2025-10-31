package main

import (
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// HTTPRateLimiter methods

// Allow checks if a request from the given IP is allowed
func (rl *HTTPRateLimiter) Allow(ip string) bool {
	if rl == nil {
		return true
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	requests := rl.requests[ip]
	validRequests := make([]time.Time, 0, len(requests))
	for _, t := range requests {
		if t.After(cutoff) {
			validRequests = append(validRequests, t)
		}
	}

	if len(validRequests) >= rl.limit {
		rl.requests[ip] = validRequests
		return false
	}

	validRequests = append(validRequests, now)
	rl.requests[ip] = validRequests
	return true
}

// Cleanup removes old IP entries
func (rl *HTTPRateLimiter) Cleanup() {
	if rl == nil {
		return
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window * 2)

	for ip, requests := range rl.requests {
		allOld := true
		for _, t := range requests {
			if t.After(cutoff) {
				allOld = false
				break
			}
		}
		if allOld {
			delete(rl.requests, ip)
		}
	}
}

// initTemplates initializes HTML templates from disk
func initTemplates() *template.Template {
	tmpl, err := template.ParseGlob("templates/*.html")
	if err != nil {
		log.Fatalf("❌ Failed to load templates: %v", err)
	}
	return tmpl
}

// getClientIP extracts the client IP address from the request
func getClientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		ips := strings.Split(forwarded, ",")
		return strings.TrimSpace(ips[0])
	}

	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// securityHeadersMiddleware adds security headers to responses
func securityHeadersMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Content Security Policy - allow scripts, styles, and images from same origin
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"font-src 'self'; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'; "+
				"require-trusted-types-for 'script'")

		// Additional security headers
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		next(w, r)
	}
}

// rateLimitMiddleware wraps a handler with rate limiting
func (m *Monitor) rateLimitMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if m.httpRateLimiter != nil {
			ip := getClientIP(r)
			if !m.httpRateLimiter.Allow(ip) {
				http.Error(w, "Rate limit exceeded. Please try again later.", http.StatusTooManyRequests)
				log.Printf("⚠️  Rate limit exceeded for IP: %s", ip)
				return
			}
		}
		next(w, r)
	}
}

// handleRoot handles the root endpoint
func (m *Monitor) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	uptime := time.Since(m.statsStartTime)
	
	// Get last check time and calculate next check
	m.mu.RLock()
	lastCheck := m.lastCheckTime
	m.mu.RUnlock()
	
	var lastCheckStr, nextCheckStr string
	if !lastCheck.IsZero() {
		lastCheckStr = m.formatLocalTime(lastCheck)
		nextCheck := lastCheck.Add(time.Duration(m.config.CheckIntervalSeconds) * time.Second)
		nextCheckStr = m.formatLocalTime(nextCheck)
	} else {
		lastCheckStr = "In progress..."
		nextCheckStr = "After current check"
	}

	// Build URL status list
	type URLInfo struct {
		URL           string
		Name          string
		IsFound       bool
		IsUnreachable bool
		ShortURL      string
		SearchTerms   []string
		LastCheck     string
		NextCheck     string
	}

	m.mu.RLock()
	urlList := make([]URLInfo, len(m.config.URLConfigs))
	for i, urlConfig := range m.config.URLConfigs {
		shortURL := urlConfig.URL
		if len(urlConfig.URL) > 60 {
			shortURL = urlConfig.URL[:57] + "..."
		}
		name := urlConfig.Name
		if name == "" {
			name = shortURL
		}
		
		// Get per-URL check time
		var lastCheckStr, nextCheckStr string
		if lastCheck, exists := m.perURLCheckTime[urlConfig.URL]; exists {
			lastCheckStr = m.formatLocalTime(lastCheck)
			nextCheck := lastCheck.Add(time.Duration(m.config.CheckIntervalSeconds) * time.Second)
			nextCheckStr = m.formatLocalTime(nextCheck)
		} else {
			lastCheckStr = "Pending..."
			nextCheckStr = "Soon..."
		}
		
		urlList[i] = URLInfo{
			URL:           urlConfig.URL,
			Name:          name,
			IsFound:       m.foundURLs[urlConfig.URL],
			IsUnreachable: m.unreachableURLs[urlConfig.URL],
			ShortURL:      shortURL,
			SearchTerms:   urlConfig.SearchTerms,
			LastCheck:     lastCheckStr,
			NextCheck:     nextCheckStr,
		}
	}
	m.mu.RUnlock()

	// Get recent matches
	matches := m.getRecentMatches()
	
	// Get recent email notifications (last 20)
	emailNotifications := m.getRecentEmailNotifications(20)

	data := struct {
		URLCount           int
		Uptime             string
		Interval           int
		Timestamp          string
		LastCheck          string
		NextCheck          string
		URLs               []URLInfo
		RecentMatches      []IncidentInfo
		EmailNotifications []EmailNotification
		MatchesHours       int
		MaxEmailsPerDay    int
	}{
		URLCount:           len(m.config.URLConfigs),
		Uptime:             formatDuration(uptime),
		Interval:           m.config.CheckIntervalSeconds,
		Timestamp:          m.formatLocalTime(time.Now()),
		LastCheck:          lastCheckStr,
		NextCheck:          nextCheckStr,
		URLs:               urlList,
		RecentMatches:      matches,
		EmailNotifications: emailNotifications,
		MatchesHours:       m.config.RecentMatchesHours,
		MaxEmailsPerDay:    m.config.MaxEmailsPerURLPerDay,
	}

	if err := m.templates.ExecuteTemplate(w, "root.html", data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
		log.Printf("⚠️  Template error: %v", err)
	}
}

// handleStatus handles the status endpoint
func (m *Monitor) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "OK\n")
}

// startHTTPServer starts the HTTP server
func (m *Monitor) startHTTPServer() {
	if !m.config.HTTPEnabled {
		return
	}

	// Public routes (no auth required, with security headers)
	http.HandleFunc("/status", securityHeadersMiddleware(m.handleStatus))
	http.HandleFunc("/login", securityHeadersMiddleware(m.handleLogin))
	http.HandleFunc("/logout", securityHeadersMiddleware(m.handleLogout))

	// Protected routes (require auth if enabled, with security headers)
	http.HandleFunc("/", securityHeadersMiddleware(m.rateLimitMiddleware(m.AuthMiddleware(m.handleRoot))))

	if m.httpRateLimiter != nil {
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				m.httpRateLimiter.Cleanup()
			}
		}()
	}

	go func() {
		log.Printf("🌐 Starting HTTP server on %s", m.config.HTTPListen)
		m.addLog(fmt.Sprintf("Starting HTTP server on %s", m.config.HTTPListen))

		if err := http.ListenAndServe(m.config.HTTPListen, nil); err != nil {
			// Fatal error - exit so systemd can restart the service (network may not be ready)
			log.Fatalf("❌ Failed to start HTTP server on %s: %v", m.config.HTTPListen, err)
		}
	}()
}

