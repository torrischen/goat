package openai

import (
	"fmt"

	"github.com/openai/openai-go/v3/responses"
)

// streamError builds an error from a stream "error" event's code and message.
func streamError(code, msg string) error {
	if msg == "" {
		msg = "openai responses stream error"
	}
	if code != "" {
		return fmt.Errorf("openai responses stream error (%s): %s", code, msg)
	}
	return fmt.Errorf("openai responses stream error: %s", msg)
}

// responseFailedError builds an error from a failed terminal response.
func responseFailedError(resp responses.Response) error {
	code := string(resp.Error.Code)
	msg := resp.Error.Message
	if msg == "" {
		msg = "openai responses request failed"
	}
	if code != "" {
		return fmt.Errorf("openai responses failed (%s): %s", code, msg)
	}
	return fmt.Errorf("openai responses failed: %s", msg)
}
