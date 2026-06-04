package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const defaultBaseURL = "https://api.resend.com"

type Email struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	HTML    string `json:"html"`
}

type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

type ClientOption func(*Client)

func WithBaseURL(url string) ClientOption {
	return func(c *Client) { c.baseURL = url }
}

func NewClient(apiKey string, opts ...ClientOption) *Client {
	c := &Client{
		apiKey:  apiKey,
		baseURL: defaultBaseURL,
		http:    &http.Client{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type SendError struct {
	StatusCode int
	Message    string
	Retryable  bool
}

func (e *SendError) Error() string {
	return fmt.Sprintf("email send failed (HTTP %d): %s", e.StatusCode, e.Message)
}

func IsRetryable(err error) bool {
	if se, ok := err.(*SendError); ok {
		return se.Retryable
	}
	return true // network errors are retryable
}

func (c *Client) SendEmail(ctx context.Context, e Email) error {
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal email: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/emails", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err // network error — retryable
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	msg := string(respBody)

	retryable := resp.StatusCode >= 500 || resp.StatusCode == 429
	return &SendError{
		StatusCode: resp.StatusCode,
		Message:    msg,
		Retryable:  retryable,
	}
}
