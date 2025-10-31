package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/sendinblue/APIv3-go-library/v2/lib"
)

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
		body = fmt.Sprintf(`Planirana iskljucenja vode u Batajnici:

%s

Vreme: %s

Lokacije: %s`, result.Date, result.Time, result.Address)
	} else if isWater && isMalfunction {
		// Water malfunctions
		subject = "💧 KVAR - Nema vode u Batajnici"
		body = fmt.Sprintf(`Trenutno nema vode na sledecim lokacijama:

%s

Procenjeno vreme popravke: %s

Za vise informacija: https://www.bvk.rs/kvarovi-na-mrezi/`, result.Address, result.Time)
	} else {
		// Power outage (original)
		subject = fmt.Sprintf("⚡ Nece biti struje u Batajnici - %s", result.Date)
		if result.Date == "" {
			subject = "⚡ Planirano iskljucenje struje u Batajnici"
		}
		body = fmt.Sprintf(`Nece biti struje u Batajnici:

%s

Vreme: %s h

Na adresama: %s`, result.Date, result.Time, result.Address)
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

