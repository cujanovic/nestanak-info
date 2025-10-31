package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// URLConfig represents a URL to monitor with its search terms
type URLConfig struct {
	URL         string   `json:"url"`
	SearchTerms []string `json:"search_terms"`
	Name        string   `json:"name"` // Optional friendly name for the URL
}

// Config represents the configuration structure
type Config struct {
	CheckIntervalSeconds    int         `json:"check_interval_seconds"`
	AlertCooldownMinutes    int         `json:"alert_cooldown_minutes"`
	EmailRateLimitPerHour   int         `json:"email_rate_limit_per_hour"`
	MaxEmailsPerURLPerDay   int         `json:"max_emails_per_url_per_day"`
	MaxConcurrentChecks     int         `json:"max_concurrent_checks"`
	ConnectTimeout          int         `json:"connect_timeout"`
	TimeOffsetHours         int         `json:"time_offset_hours"`
	DNSCacheTTLMinutes      int         `json:"dns_cache_ttl_minutes"`
	UserAgentRotation       bool        `json:"user_agent_rotation_enabled"`
	UserAgentPoolSize       int         `json:"user_agent_pool_size"`
	HTTPEnabled             bool        `json:"http_enabled"`
	HTTPListen              string      `json:"http_listen"`
	HTTPLogLines            int         `json:"http_log_lines"`
	HTTPRateLimitPerMinute  int         `json:"http_rate_limit_per_minute"`
	LogBufferFlushSeconds   int         `json:"log_buffer_flush_seconds"`
	RecentMatchesHours      int         `json:"recent_matches_hours"`
	RecentEventsBufferSize  int         `json:"recent_events_buffer_size"`
	AuthEnabled             bool        `json:"auth_enabled"`
	PasswordHash            string      `json:"password_hash"`
	Argon2Memory            uint32      `json:"argon2_memory"`
	Argon2Time              uint32      `json:"argon2_time"`
	Argon2Threads           uint8       `json:"argon2_threads"`
	SessionTimeoutMinutes   int         `json:"session_timeout_minutes"`
	MaxLoginAttempts        int         `json:"max_login_attempts"`
	LockoutDurationMinutes  int         `json:"lockout_duration_minutes"`
	URLConfigs              []URLConfig `json:"url_configs"`
	Recipients              []string    `json:"recipients"`
	ErrorRecipient          string      `json:"error_recipient"`
	BrevoAPIKey             string      `json:"brevo_api_key"`
	SenderEmail             string      `json:"sender_email"`
	SenderName              string      `json:"sender_name"`
	StateFilePath           string      `json:"state_file_path"` // Path to persist state across restarts
}

// loadConfig loads configuration from a JSON file
func loadConfig(filename string) (Config, error) {
	var config Config

	data, err := os.ReadFile(filename)
	if err != nil {
		return config, fmt.Errorf("failed to read config file: %v", err)
	}

	if err := json.Unmarshal(data, &config); err != nil {
		return config, fmt.Errorf("failed to parse config file: %v", err)
	}

	return config, nil
}

// ValidateConfig validates the configuration
func ValidateConfig(config Config) error {
	errors := make([]string, 0)

	// Validate basic settings
	if config.CheckIntervalSeconds <= 0 {
		errors = append(errors, "check_interval_seconds must be greater than 0")
	}
	if config.CheckIntervalSeconds < 5 {
		errors = append(errors, "check_interval_seconds should be at least 5 seconds for reliability")
	}
	if config.AlertCooldownMinutes < 0 {
		errors = append(errors, "alert_cooldown_minutes cannot be negative")
	}
	if config.EmailRateLimitPerHour < 1 {
		errors = append(errors, "email_rate_limit_per_hour must be at least 1")
	}
	if config.EmailRateLimitPerHour > 300 {
		errors = append(errors, "email_rate_limit_per_hour exceeds Brevo free tier limit (300/day)")
	}
	if config.MaxEmailsPerURLPerDay < 1 {
		errors = append(errors, "max_emails_per_url_per_day must be at least 1")
	}
	if config.MaxEmailsPerURLPerDay > 10 {
		errors = append(errors, "max_emails_per_url_per_day should not exceed 10 to avoid spam")
	}
	if config.MaxConcurrentChecks < 1 {
		errors = append(errors, "max_concurrent_checks must be at least 1")
	}
	if config.MaxConcurrentChecks > 50 {
		errors = append(errors, "max_concurrent_checks should not exceed 50 for stability")
	}
	if config.ConnectTimeout < 1 || config.ConnectTimeout > 60 {
		errors = append(errors, "connect_timeout must be between 1 and 60 seconds")
	}
	if config.TimeOffsetHours < -12 || config.TimeOffsetHours > 14 {
		errors = append(errors, "time_offset_hours must be between -12 and +14")
	}
	if config.DNSCacheTTLMinutes < 1 || config.DNSCacheTTLMinutes > 1440 {
		errors = append(errors, "dns_cache_ttl_minutes must be between 1 and 1440 (24 hours)")
	}
	if config.UserAgentPoolSize < 1 || config.UserAgentPoolSize > 100 {
		errors = append(errors, "user_agent_pool_size must be between 1 and 100")
	}

	// Validate email config
	if config.BrevoAPIKey == "" || config.BrevoAPIKey == "YOUR_BREVO_API_KEY_HERE" {
		errors = append(errors, "brevo_api_key must be configured with a valid Brevo API key")
	}
	if config.SenderEmail == "" {
		errors = append(errors, "sender_email cannot be empty")
	}
	if !strings.Contains(config.SenderEmail, "@") {
		errors = append(errors, "sender_email must be a valid email address")
	}
	if len(config.Recipients) == 0 {
		errors = append(errors, "at least one recipient email must be configured")
	}
	for i, recipient := range config.Recipients {
		if !strings.Contains(recipient, "@") {
			errors = append(errors, fmt.Sprintf("recipients[%d] must be a valid email address", i))
		}
	}
	if config.ErrorRecipient != "" && !strings.Contains(config.ErrorRecipient, "@") {
		errors = append(errors, "error_recipient must be a valid email address")
	}

	// Validate authentication settings
	if config.AuthEnabled {
		if config.PasswordHash == "" {
			errors = append(errors, "password_hash cannot be empty when auth_enabled is true")
		} else if !strings.HasPrefix(config.PasswordHash, "$argon2id$") {
			errors = append(errors, "password_hash must be a valid Argon2id hash")
		}
		if config.Argon2Memory < 8192 {
			errors = append(errors, "argon2_memory must be at least 8192 KB (8 MB)")
		}
		if config.Argon2Memory > 1048576 {
			errors = append(errors, "argon2_memory should not exceed 1048576 KB (1 GB)")
		}
		if config.Argon2Time < 1 || config.Argon2Time > 10 {
			errors = append(errors, "argon2_time must be between 1 and 10")
		}
		if config.Argon2Threads < 1 || config.Argon2Threads > 16 {
			errors = append(errors, "argon2_threads must be between 1 and 16")
		}
		if config.SessionTimeoutMinutes < 5 {
			errors = append(errors, "session_timeout_minutes must be at least 5")
		}
		if config.SessionTimeoutMinutes > 10080 {
			errors = append(errors, "session_timeout_minutes should not exceed 10080 (1 week)")
		}
		if config.MaxLoginAttempts < 3 {
			errors = append(errors, "max_login_attempts must be at least 3")
		}
		if config.LockoutDurationMinutes < 1 {
			errors = append(errors, "lockout_duration_minutes must be at least 1")
		}
	}

	// Validate URL configs
	if len(config.URLConfigs) == 0 {
		errors = append(errors, "at least one URL configuration must be provided")
	}
	
	urlMap := make(map[string]bool)
	for i, urlConfig := range config.URLConfigs {
		if urlConfig.URL == "" {
			errors = append(errors, fmt.Sprintf("url_configs[%d].url cannot be empty", i))
		}
		if !strings.HasPrefix(urlConfig.URL, "http://") && !strings.HasPrefix(urlConfig.URL, "https://") {
			errors = append(errors, fmt.Sprintf("url_configs[%d].url must start with http:// or https://", i))
		}
		
		// Check for duplicate URLs
		if urlMap[urlConfig.URL] {
			errors = append(errors, fmt.Sprintf("duplicate URL found: %s", urlConfig.URL))
		}
		urlMap[urlConfig.URL] = true
		
		// Validate search terms for this URL
		if len(urlConfig.SearchTerms) == 0 {
			errors = append(errors, fmt.Sprintf("url_configs[%d] must have at least one search term", i))
		}
		for j, term := range urlConfig.SearchTerms {
			if term == "" {
				errors = append(errors, fmt.Sprintf("url_configs[%d].search_terms[%j] cannot be empty", i, j))
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("configuration validation failed:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
}

