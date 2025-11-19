package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

// UserAgentManager handles rotating User-Agent strings
type UserAgentManager struct {
	agents        []string
	mu            sync.RWMutex
	fallbackAgent string
}

// Default fallback User-Agent (current hardcoded one)
const defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// Source for User-Agent strings
// Using microlinkhq/top-user-agents - maintained list from 300M+ monthly requests
// Repository: https://github.com/microlinkhq/top-user-agents
// Data is updated regularly with the top 100 most used User-Agents
const userAgentSourceURL = "https://raw.githubusercontent.com/microlinkhq/top-user-agents/master/src/index.json"

// NewUserAgentManager creates a new User-Agent manager with fallback
func NewUserAgentManager() *UserAgentManager {
	// Note: Go 1.20+ automatically seeds the global random generator
	// No need to call rand.Seed() anymore
	
	return &UserAgentManager{
		agents:        []string{defaultUserAgent},
		fallbackAgent: defaultUserAgent,
	}
}

// FetchUserAgents tries to fetch recent User-Agent strings from online sources
func (uam *UserAgentManager) FetchUserAgents(config Config) error {
	log.Printf("📡 Fetching recent User-Agent strings...")

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	var fetchedAgents []string
	
	log.Printf("   Fetching from: %s", userAgentSourceURL)
	
	resp, err := client.Get(userAgentSourceURL)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to fetch User-Agent strings from GitHub: %v", err)
		log.Printf("⚠️  %s", errMsg)
		// Send notification email
		uam.sendFetchFailureEmail(config, errMsg)
		return fmt.Errorf(errMsg)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errMsg := fmt.Sprintf("Failed to fetch User-Agent strings: HTTP %d", resp.StatusCode)
		log.Printf("⚠️  %s", errMsg)
		uam.sendFetchFailureEmail(config, errMsg)
		return fmt.Errorf(errMsg)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to read User-Agent response: %v", err)
		log.Printf("⚠️  %s", errMsg)
		uam.sendFetchFailureEmail(config, errMsg)
		return fmt.Errorf(errMsg)
	}

	// Parse JSON array of User-Agent strings
	var jsonAgents []string
	if err := json.Unmarshal(body, &jsonAgents); err != nil {
		errMsg := fmt.Sprintf("Failed to parse User-Agent JSON: %v", err)
		log.Printf("⚠️  %s", errMsg)
		uam.sendFetchFailureEmail(config, errMsg)
		return fmt.Errorf(errMsg)
	}

	fetchedAgents = jsonAgents
	
	if len(fetchedAgents) == 0 {
		errMsg := "No User-Agent strings found in response"
		log.Printf("⚠️  %s", errMsg)
		uam.sendFetchFailureEmail(config, errMsg)
		return fmt.Errorf(errMsg)
	}

	log.Printf("   ✅ Fetched %d User-Agent strings from microlinkhq/top-user-agents", len(fetchedAgents))

	// Select N diverse ones (prefer recent Chrome, Firefox, Safari)
	poolSize := config.UserAgentPoolSize
	if poolSize < 1 {
		poolSize = 6 // Default
	}
	selectedAgents := uam.selectUserAgents(fetchedAgents, poolSize)
	
	uam.mu.Lock()
	uam.agents = selectedAgents
	uam.mu.Unlock()

	log.Printf("✅ User-Agent pool ready with %d agents", len(selectedAgents))
	
	return nil
}

// selectUserAgents picks N User-Agent strings (prefer recent Chrome, Firefox, Safari)
func (uam *UserAgentManager) selectUserAgents(agents []string, count int) []string {
	if len(agents) <= count {
		return agents
	}

	// Prioritize modern browsers
	var chrome, firefox, safari, others []string
	
	for _, agent := range agents {
		agentLower := strings.ToLower(agent)
		if strings.Contains(agentLower, "chrome") && !strings.Contains(agentLower, "edg") {
			chrome = append(chrome, agent)
		} else if strings.Contains(agentLower, "firefox") {
			firefox = append(firefox, agent)
		} else if strings.Contains(agentLower, "safari") && !strings.Contains(agentLower, "chrome") {
			safari = append(safari, agent)
		} else {
			others = append(others, agent)
		}
	}

	// Pick diverse set: 3 Chrome, 2 Firefox, 1 Safari
	selected := make([]string, 0, count)
	
	// Add Chrome (most popular)
	for i := 0; i < 3 && i < len(chrome); i++ {
		selected = append(selected, chrome[i])
	}
	
	// Add Firefox
	for i := 0; i < 2 && i < len(firefox); i++ {
		selected = append(selected, firefox[i])
	}
	
	// Add Safari
	if len(safari) > 0 {
		selected = append(selected, safari[0])
	}
	
	// Fill remaining with others if needed
	for len(selected) < count && len(others) > 0 {
		selected = append(selected, others[0])
		others = others[1:]
	}
	
	// Shuffle to avoid predictable pattern
	// Go 1.20+ automatically seeds the global random generator
	rand.Shuffle(len(selected), func(i, j int) {
		selected[i], selected[j] = selected[j], selected[i]
	})
	
	return selected
}

// GetNext returns a random User-Agent from the top half (most popular)
func (uam *UserAgentManager) GetNext() string {
	uam.mu.RLock()
	defer uam.mu.RUnlock()
	
	if len(uam.agents) == 0 {
		return uam.fallbackAgent
	}
	
	// Use top half of agents (most popular browsers)
	// This provides good realism since top agents are most common in real traffic
	topHalfSize := len(uam.agents) / 2
	if topHalfSize < 1 {
		topHalfSize = len(uam.agents) // If only 1-2 agents, use all
	}
	
	// Randomly select from top half
	randomIndex := rand.Intn(topHalfSize)
	return uam.agents[randomIndex]
}

// sendFetchFailureEmail notifies admin about User-Agent fetch failure
func (uam *UserAgentManager) sendFetchFailureEmail(config Config, errorMsg string) {
	if config.ErrorRecipient == "" {
		return
	}

	subject := "⚠️ Nestanak-Info - User-Agent Fetch Failed"
	body := fmt.Sprintf(`User-Agent Fetch Failure

The service failed to fetch recent User-Agent strings from GitHub and is using the hardcoded fallback.

Error Details:
%s

Source:
%s
(microlinkhq/top-user-agents - Top 100 HTTP User-Agents from 300M+ monthly requests)

Fallback User-Agent:
%s

Impact:
- Service continues operating normally
- Using static User-Agent (may be less effective at avoiding detection)

Timestamp: %s

The service will retry fetching on next restart.

Repository: https://github.com/microlinkhq/top-user-agents`, 
		errorMsg,
		userAgentSourceURL,
		defaultUserAgent,
		time.Now().Format("2006-01-02 15:04:05"))

	if err := sendBrevoEmail(config, config.ErrorRecipient, subject, body); err != nil {
		log.Printf("Failed to send User-Agent fetch failure email: %v", err)
	} else {
		log.Printf("📧 User-Agent fetch failure notification sent to %s", config.ErrorRecipient)
	}
}

