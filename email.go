package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/sendinblue/APIv3-go-library/v2/lib"
)

// formatAddresses formats address string for email display
// Input: "Насеље БАТАЈНИЦА: БРАНКА ЖИВКОВИЋА: 16-30,41-61, ШАНГАЈСКА: 38-54Х,49-81,; Насеље БАТАЈНИЦА: ..."
// Output: "Насеље БАТАЈНИЦА:\nБРАНКА ЖИВКОВИЋА: 16-30,41-61\nШАНГАЈСКА: 38-54Х,49-81\n..."
func formatAddresses(addressStr string) string {
	if addressStr == "" {
		return ""
	}

	// Extract settlement name (e.g., "БАТАЈНИЦА" from "Насеље БАТАЈНИЦА:")
	settlementPattern := regexp.MustCompile(`Насеље\s+([^:]+):`)
	matches := settlementPattern.FindStringSubmatch(addressStr)
	settlementName := "БАТАЈНИЦА" // Default fallback
	if len(matches) >= 2 {
		settlementName = strings.TrimSpace(matches[1])
	}

	// Split by semicolon to get individual address entries
	entries := strings.Split(addressStr, ";")
	var formattedLines []string

	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		// Remove "Насеље БАТАЈНИЦА:" prefix from each entry
		entry = regexp.MustCompile(`Насеље\s+[^:]+:\s*`).ReplaceAllString(entry, "")
		entry = strings.TrimSpace(entry)

		// Clean up trailing commas and spaces
		entry = strings.TrimRight(entry, ", ")
		entry = strings.TrimSpace(entry)

		if entry == "" {
			continue
		}

		// Split by ":" - like AWK with separator ":"
		// Format: "STREET1: numbers STREET2: numbers STREET3: numbers"
		parts := strings.Split(entry, ":")
		
		if len(parts) < 2 {
			// No colons found, use entry as-is
			entry = strings.TrimRight(entry, ", ")
			if entry != "" {
				formattedLines = append(formattedLines, entry)
			}
			continue
		}

		// Process: parts[0] = street1, parts[1] = numbers1 + street2, parts[2] = numbers2 + street3, etc.
		currentStreetName := strings.TrimSpace(parts[0])
		
		for i := 1; i < len(parts); i++ {
			part := strings.TrimSpace(parts[i])
			if part == "" {
				continue
			}

			// Extract numbers and next street name from this part
			words := strings.Fields(part)
			var numbers []string
			nextStreetName := ""
			
			for j, word := range words {
				if len(word) > 0 {
					firstRune := []rune(word)[0]
					isCapital := unicode.IsUpper(firstRune)
					
					if isCapital {
						// This is the start of the next street name
						// Collect all consecutive capital words (street name might be multi-word)
						streetWords := []string{word}
						for k := j + 1; k < len(words); k++ {
							nextWord := words[k]
							if len(nextWord) > 0 {
								nextFirstRune := []rune(nextWord)[0]
								if unicode.IsUpper(nextFirstRune) {
									streetWords = append(streetWords, nextWord)
								} else {
									break
								}
							}
						}
						nextStreetName = strings.Join(streetWords, " ")
						// Numbers are all words before this street name
						numbers = words[:j]
						break
					}
				}
			}
			
			// If no next street found, all words are numbers
			if nextStreetName == "" {
				numbers = words
			}
			
			// Format numbers
			numbersStr := strings.Join(numbers, " ")
			numbersStr = strings.TrimRight(numbersStr, ", ")
			
			// Add formatted line
			if currentStreetName != "" && numbersStr != "" {
				formattedLines = append(formattedLines, fmt.Sprintf("%s: %s", currentStreetName, numbersStr))
			}
			
			// Move to next street
			currentStreetName = nextStreetName
		}
	}

	if len(formattedLines) == 0 {
		return ""
	}

	// Format as: "Насеље БАТАЈНИЦА:\nline1\nline2\n..."
	result := fmt.Sprintf("Насеље %s:", settlementName)
	for _, line := range formattedLines {
		result += "\n" + line
	}

	return result
}

// sendEmail sends a notification email with extracted information
func (m *Monitor) sendEmail(result URLCheckResult) error {
	var subject, body string
	
	// Determine if this is water or power outage
	isWater := strings.Contains(result.URL, "bvk.rs")
	isPlanned := strings.Contains(result.URL, "planirani") || strings.Contains(result.URL, "planirana")
	isMalfunction := strings.Contains(result.URL, "kvarovi")
	
	// Build subject and body based on type
	if isWater && isPlanned {
		// Water planned work
		subject = fmt.Sprintf("💧 Planirana iskljucenja vode - %s", result.Date)
		if result.Date == "" {
			subject = "💧 Planirana iskljucenja vode u Batajnici"
		}
		formattedAddress := formatAddresses(result.Address)
		body = fmt.Sprintf(`Planirana iskljucenja vode u Batajnici:

%s

Vreme: %s

Lokacije - %s`, result.Date, result.Time, formattedAddress)
	} else if isWater && isMalfunction {
		// Water malfunctions
		subject = "💧 KVAR - Nema vode u Batajnici"
		formattedAddress := formatAddresses(result.Address)
		body = fmt.Sprintf(`Trenutno nema vode na sledecim lokacijama:

%s

Procenjeno vreme popravke: %s

Za vise informacija: https://www.bvk.rs/kvarovi-na-mrezi/`, formattedAddress, result.Time)
	} else {
		// Power outage (original)
		subject = fmt.Sprintf("⚡ Nece biti struje u Batajnici - %s", result.Date)
		if result.Date == "" {
			subject = "⚡ Planirano iskljucenje struje u Batajnici"
		}
		
		// Format addresses nicely
		formattedAddress := formatAddresses(result.Address)
		
		body = fmt.Sprintf(`Nece biti struje u Batajnici:

%s

Vreme: %s h

Na adresama - %s`, result.Date, result.Time, formattedAddress)
	}

	// Send to all recipients with delay between sends
	sentTo := make([]string, 0)
	for i, recipient := range m.config.Recipients {
		if err := sendBrevoEmail(m.config, recipient, subject, body); err != nil {
			log.Printf("Failed to send email to %s: %v", recipient, err)
		} else {
			log.Printf("📧 Email sent to %s", recipient)
			sentTo = append(sentTo, recipient)
		}
		
		// Add delay between emails (except after the last one)
		if i < len(m.config.Recipients)-1 {
			time.Sleep(1 * time.Second)
		}
	}

	// Record notification if any emails were sent
	if len(sentTo) > 0 {
		m.recordEmailNotification(result.URL, result.Name, sentTo, "match", subject)
	}

	return nil
}

// sendBrevoEmail sends an email using Brevo API
func sendBrevoEmail(config Config, to, subject, body string) error {
	// Create Brevo client
	cfg := lib.NewConfiguration()
	cfg.AddDefaultHeader("api-key", config.BrevoAPIKey)

	client := lib.NewAPIClient(cfg)
	ctx := context.Background()

	// Create email request
	sender := lib.SendSmtpEmailSender{
		Email: config.SenderEmail,
		Name:  config.SenderName,
	}

	recipient := lib.SendSmtpEmailTo{
		Email: to,
	}

	email := lib.SendSmtpEmail{
		Sender:      &sender,
		To:          []lib.SendSmtpEmailTo{recipient},
		Subject:     subject,
		TextContent: body,
	}

	// Send email
	_, resp, err := client.TransactionalEmailsApi.SendTransacEmail(ctx, email)
	if err != nil {
		return fmt.Errorf("failed to send email: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("brevo API error: %d - %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

