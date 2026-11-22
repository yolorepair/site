package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"time"
)

// intakeClient posts requests to benchhud-intake. The token it holds can
// write a request and read nothing.
type intakeClient struct {
	url      string // full URL of POST /api/requests
	token    string
	http     *http.Client
	attempts int
	backoff  time.Duration
}

func newIntakeClient(baseURL, token string) *intakeClient {
	return &intakeClient{
		url:      baseURL + "/api/requests",
		token:    token,
		http:     &http.Client{Timeout: 5 * time.Second},
		attempts: 2,
		backoff:  300 * time.Millisecond,
	}
}

// send posts body to intake and returns the status it answered with. A
// connection failure or a gateway error is tried again with the same
// Idempotency-Key, so a request that did land the first time isn't stored
// twice. err is set only when no attempt got an answer.
func (c *intakeClient) send(ctx context.Context, body []byte, key string) (status int, err error) {
	for attempt := 1; ; attempt++ {
		status, err = c.post(ctx, body, key)
		retryable := err != nil || status == http.StatusBadGateway ||
			status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
		if !retryable || attempt >= c.attempts {
			return status, err
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-time.After(c.backoff):
		}
	}
}

func (c *intakeClient) post(ctx context.Context, body []byte, key string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// The answer's body is an opaque id or an error list. Neither is used.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

// newKey makes the Idempotency-Key for one form submission.
func newKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
