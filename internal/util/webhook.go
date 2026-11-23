package util

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/local/govoid/internal/config"
)

// EmbedField es un campo de embed de Discord.
type EmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type webhookEmbed struct {
	Title     string       `json:"title"`
	Color     int          `json:"color"`
	Fields    []EmbedField `json:"fields,omitempty"`
	Timestamp string       `json:"timestamp"`
}

type webhookPayload struct {
	Username string         `json:"username,omitempty"`
	Embeds   []webhookEmbed `json:"embeds"`
}

// PostWebhook envía un embed al webhook configurado. Devuelve error si el
// POST falla o Discord responde con un estado no exitoso.
func PostWebhook(title string, color int, fields []EmbedField) error {
	payload := webhookPayload{
		Username: config.AppName,
		Embeds: []webhookEmbed{{
			Title:     title,
			Color:     color,
			Fields:    fields,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("webhook: marshal: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, config.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: post: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			_ = cerr
		}
	}()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: estado HTTP %d", resp.StatusCode)
	}
	return nil
}
