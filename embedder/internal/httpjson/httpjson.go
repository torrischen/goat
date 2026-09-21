package httpjson

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/bytedance/sonic"
)

const maxErrorBody = 64 << 10

// Post sends a JSON request and decodes its JSON response.
func Post(ctx context.Context, client *http.Client, url string, headers map[string]string, input, output any) error {
	body, err := sonic.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshal embedding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		if value != "" {
			req.Header.Set(key, value)
		}
	}

	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send embedding request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return fmt.Errorf("embedding API returned %s: %s", resp.Status, bytes.TrimSpace(message))
	}
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read embedding response: %w", err)
	}
	if err := sonic.Unmarshal(body, output); err != nil {
		return fmt.Errorf("decode embedding response: %w", err)
	}
	return nil
}
