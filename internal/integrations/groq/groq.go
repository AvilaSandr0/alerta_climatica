package groq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// Message representa un mensaje en formato compatible OpenAI/Groq
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type choiceMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type choice struct {
	Message choiceMessage `json:"message"`
}

type chatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Choices []choice `json:"choices"`
}

// Chat realiza una petición a la API de Groq/OpenAI-compatible y retorna
// el texto de la primera elección.
func Chat(ctx context.Context, apiKey, model string, messages []Message) (string, error) {
	if apiKey == "" {
		return "", errors.New("missing groq api key")
	}
	if model == "" {
		model = "openai/gpt-oss-120b"
	}

	reqBody := chatRequest{Model: model, Messages: messages}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.groq.com/openai/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("groq api error: " + resp.Status + " - " + string(body))
	}

	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return "", err
	}
	if len(cr.Choices) == 0 {
		return "", errors.New("no choices returned from groq")
	}
	return cr.Choices[0].Message.Content, nil
}
