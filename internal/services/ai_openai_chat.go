// 遵循project_guide.md
package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"balanciz/internal/models"
)

const aiChatHTTPTimeout = 45 * time.Second

// OpenAICompatibleChatCompletion calls an OpenAI-compatible /v1/chat/completions endpoint.
// baseURL should be like https://api.openai.com/v1 (trailing slash optional).
func OpenAICompatibleChatCompletion(row models.AIConnectionSettings, userMessage, systemMessage string) (string, error) {
	if strings.TrimSpace(row.APIKey) == "" {
		return "", fmt.Errorf("no API key configured")
	}
	base := strings.TrimSpace(row.APIBaseURL)
	if base == "" {
		return "", fmt.Errorf("no API base URL configured")
	}
	base = strings.TrimRight(base, "/")
	url := base + "/chat/completions"

	model := strings.TrimSpace(row.ModelName)
	if model == "" {
		model = "gpt-4o-mini"
	}

	body := map[string]any{
		"model":         model,
		"temperature":   0.2,
		"max_tokens":    800,
		"messages": []map[string]string{
			{"role": "system", "content": systemMessage},
			{"role": "user", "content": userMessage},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(row.APIKey))

	client := &http.Client{Timeout: aiChatHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("AI HTTP %d: %s", resp.StatusCode, truncateForLog(string(respBody), 500))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("parse AI response: %w", err)
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("empty AI completion")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
