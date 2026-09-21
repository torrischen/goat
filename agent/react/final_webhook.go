package react

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/util/logging"

	"github.com/go-resty/resty/v2"
)

func (a *Agent) buildFinalAnswerWebhookPayload(
	signature common.RunSignature,
	args *common.AgentDoArgs,
	finalAnswer string,
) *common.FinalAnswerWebhookPayload {
	return &common.FinalAnswerWebhookPayload{
		Event:       "final_answer",
		Agent:       "react",
		ContextUID:  signature.ContextUID,
		RunUID:      signature.RunUID,
		UserInput:   args.UserInput.Text,
		FinalAnswer: finalAnswer,
		GeneratedAt: time.Now().UTC(),
	}
}

func (a *Agent) sendFinalAnswerWebhook(
	ctx context.Context,
	cfg *common.FinalAnswerWebhookConfig,
	payload *common.FinalAnswerWebhookPayload,
) {
	if cfg == nil {
		return
	}

	url := strings.TrimSpace(cfg.URL)
	if url == "" {
		return
	}

	reqCtx := ctx
	cancel := func() {}
	if cfg.Timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
	}
	defer cancel()

	client := resty.NewWithClient(http.DefaultClient)
	if cfg.Timeout > 0 {
		client.SetTimeout(cfg.Timeout)
	}

	request := client.R().
		SetContext(reqCtx).
		SetBody(payload)
	for k, v := range cfg.Headers {
		request.SetHeader(k, v)
	}
	if _, ok := cfg.Headers["Content-Type"]; !ok {
		request.SetHeader("Content-Type", "application/json")
	}

	resp, err := request.Post(url)
	if err != nil {
		logging.Errorf("react final answer webhook send error: %v", err)
		return
	}

	if resp.StatusCode() < http.StatusOK || resp.StatusCode() >= http.StatusMultipleChoices {
		respBody := strings.TrimSpace(string(resp.Body()))
		if len(respBody) > 4096 {
			respBody = respBody[:4096]
		}
		logging.Errorf("react final answer webhook failed with status %d: %s", resp.StatusCode(), respBody)
	}
}
