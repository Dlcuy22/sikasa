package sikasa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// AIProvider represents the resolved configuration of an AI completion provider.
type AIProvider struct {
	Name     string
	APIKey   string
	Endpoint string
	Model    string
	TokenCap int
}

// AIClient manages communication with OpenAI-compatible APIs.
type AIClient struct {
	providers    map[string]*AIProvider
	defaultProv  string
	systemPrompt string
}

// ChatMessage represents a single message in the conversation context.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionRequest is the payload for OpenAI-compatible chat completion.
type ChatCompletionRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

// ChatCompletionChoice is one selection choice returned by the API.
type ChatCompletionChoice struct {
	Message ChatMessage `json:"message"`
}

// ChatCompletionResponse maps the expected JSON layout returned by the API.
type ChatCompletionResponse struct {
	Choices []ChatCompletionChoice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// NewAIClient initializes the AI client resolving key details from yaml config or environment variables.
func NewAIClient(cfg *Config) *AIClient {
	client := &AIClient{
		providers:    make(map[string]*AIProvider),
		defaultProv:  cfg.AI.DefaultProvider,
		systemPrompt: cfg.AI.SystemPrompt,
	}

	// 1. Load providers from YAML config
	for _, p := range cfg.AI.Providers {
		nameLower := strings.ToLower(p.Name)
		provider := &AIProvider{
			Name:     p.Name,
			APIKey:   p.APIKey,
			Endpoint: p.Endpoint,
			Model:    p.Model,
			TokenCap: p.TokenCap,
		}

		if provider.TokenCap == 0 {
			provider.TokenCap = cfg.AI.TokenCap
		}

		// Fallback to SIKASA_AI_<PROVIDER>_API if APIKey is empty in YAML
		if provider.APIKey == "" {
			envKey := fmt.Sprintf("SIKASA_AI_%s_API", strings.ToUpper(p.Name))
			provider.APIKey = os.Getenv(envKey)
		}

		client.providers[nameLower] = provider
	}

	// 2. Discover providers dynamically from environment variables
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "SIKASA_AI_") {
			parts := strings.SplitN(env, "=", 2)
			key := parts[0]
			val := parts[1]

			if strings.HasSuffix(key, "_API") {
				provName := strings.TrimPrefix(key, "SIKASA_AI_")
				provName = strings.TrimSuffix(provName, "_API")
				nameLower := strings.ToLower(provName)

				p, exists := client.providers[nameLower]
				if !exists {
					p = &AIProvider{
						Name: nameLower,
					}
					client.providers[nameLower] = p
				}

				if p.APIKey == "" {
					p.APIKey = val
				}

				// Resolve optional endpoints/models from environment variables
				endpointEnv := fmt.Sprintf("SIKASA_AI_%s_ENDPOINT", provName)
				if ep := os.Getenv(endpointEnv); ep != "" {
					p.Endpoint = ep
				} else if p.Endpoint == "" {
					if nameLower == "openai" {
						p.Endpoint = "https://api.openai.com/v1"
					}
				}

				modelEnv := fmt.Sprintf("SIKASA_AI_%s_MODEL", provName)
				if m := os.Getenv(modelEnv); m != "" {
					p.Model = m
				} else if p.Model == "" {
					if nameLower == "openai" {
						p.Model = "gpt-4o-mini"
					}
				}

				tokenCapEnv := fmt.Sprintf("SIKASA_AI_%s_TOKEN_CAP", provName)
				if tc := os.Getenv(tokenCapEnv); tc != "" {
					if v, err := strconv.Atoi(tc); err == nil {
						p.TokenCap = v
					}
				} else if p.TokenCap == 0 {
					p.TokenCap = cfg.AI.TokenCap
				}
			}
		}
	}

	// Log configured providers at startup for verification
	var registered []string
	for name, p := range client.providers {
		if p.APIKey != "" {
			registered = append(registered, fmt.Sprintf("%s (model: %s, token_cap: %d)", name, p.Model, p.TokenCap))
		}
	}
	slog.Info("AI Client initialized", "providers", registered, "default", client.defaultProv)

	return client
}

/*
Chat executes an OpenAI-compatible v1 ChatCompletion request to the specified provider.

	params:
	      ctx:      request cancellation context
	      provider: target provider name (uses default if empty)
	      prompt:   the prompt to complete
	returns:
	      string: completion reply message content
	      error:  if request execution fails or returns an API error
*/
func (c *AIClient) Chat(ctx context.Context, provider string, prompt string) (string, error) {
	if provider == "" {
		provider = c.defaultProv
	}
	provider = strings.ToLower(provider)

	p, ok := c.providers[provider]
	if !ok || p.APIKey == "" {
		return "", fmt.Errorf("AI provider %q not configured or missing API key", provider)
	}

	// Build messages
	var messages []ChatMessage

	// Built-in system prompt extensions for Discord formatting and style
	discordPrompt := "Do NOT use markdown features unsupported by Discord: do NOT use horizontal rules (---) and do NOT use markdown tables (use simple bullet points or lists instead). Do NOT collapse or reduce spacing too much; use paragraphs with clear spacing (double newlines) to structure your response for readability. Use emojis very sparingly and respond in a concise, direct manner. do not use - sequence for listing stuff, prefer to explain in paragraphs than short lists."

	sysPrompt := c.systemPrompt
	if sysPrompt == "" {
		sysPrompt = discordPrompt
	} else {
		sysPrompt = sysPrompt + "\n\n" + discordPrompt
	}

	messages = append(messages, ChatMessage{
		Role:    "system",
		Content: sysPrompt,
	})
	messages = append(messages, ChatMessage{
		Role:    "user",
		Content: prompt,
	})

	reqBody := ChatCompletionRequest{
		Model:     p.Model,
		Messages:  messages,
		MaxTokens: p.TokenCap,
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := p.Endpoint
	if !strings.HasSuffix(url, "/chat/completions") {
		url = strings.TrimSuffix(url, "/") + "/chat/completions"
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return "", fmt.Errorf("create HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("execute HTTP request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var compResp ChatCompletionResponse
		if err := json.Unmarshal(bodyBytes, &compResp); err == nil && compResp.Error != nil {
			return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, compResp.Error.Message)
		}
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var compResp ChatCompletionResponse
	if err := json.Unmarshal(bodyBytes, &compResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if len(compResp.Choices) == 0 {
		return "", fmt.Errorf("empty choices in API response")
	}

	return cleanAIResponse(compResp.Choices[0].Message.Content), nil
}

// HasProvider returns true if the specified provider is registered and has an API key configured.
func (c *AIClient) HasProvider(name string) bool {
	p, ok := c.providers[strings.ToLower(name)]
	return ok && p.APIKey != ""
}

// cleanAIResponse strips unsupported Discord formatting (like horizontal rules ---)
// and collapses blank lines after headers to prevent excessively large margins in Discord.
func cleanAIResponse(text string) string {
	// 1. Remove horizontal rules (---)
	lines := strings.Split(text, "\n")
	var cleanedLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "---") && strings.Trim(trimmed, "-") == "" {
			continue // skip horizontal rules
		}
		cleanedLines = append(cleanedLines, line)
	}
	text = strings.Join(cleanedLines, "\n")

	// 2. Collapse 3 or more consecutive newlines into 2
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}

	// 3. Remove blank lines immediately following headers (# Title)
	lines = strings.Split(text, "\n")
	cleanedLines = nil
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		cleanedLines = append(cleanedLines, line)

		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			// Skip any following empty lines
			for i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" {
				i++
			}
		}
	}
	text = strings.Join(cleanedLines, "\n")

	return strings.TrimSpace(text)
}
