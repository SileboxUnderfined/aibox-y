package providers

import (
	"context"
	"errors"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type claude struct {
	cfg Config
}

func newAnthropic(cfg Config) *claude { return &claude{cfg: cfg} }

func (c *claude) Complete(ctx context.Context, system string, messages []Message) (string, error) {
	key, err := apiKey(c.cfg)
	if err != nil {
		return "", err
	}
	opts := []option.RequestOption{option.WithAPIKey(key)}
	if c.cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(c.cfg.BaseURL))
	}
	client := anthropic.NewClient(opts...)
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.cfg.Model),
		MaxTokens: int64(c.cfg.MaxTokens),
	}
	if system != "" {
		params.System = []anthropic.TextBlockParam{{Text: system}}
	}
	if c.cfg.Effort != "" {
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(c.cfg.Effort)}
	}
	for _, m := range messages {
		block := anthropic.NewTextBlock(m.Content)
		if m.Role == "assistant" {
			params.Messages = append(params.Messages, anthropic.NewAssistantMessage(block))
		} else {
			params.Messages = append(params.Messages, anthropic.NewUserMessage(block))
		}
	}
	resp, err := client.Messages.New(ctx, params)
	if err != nil {
		return "", err
	}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return "", errors.New("model declined the request")
	}
	var parts []string
	for _, block := range resp.Content {
		if text, ok := block.AsAny().(anthropic.TextBlock); ok {
			parts = append(parts, text.Text)
		}
	}
	answer := strings.TrimSpace(strings.Join(parts, ""))
	if answer == "" {
		return "", errors.New("provider returned no answer")
	}
	return answer, nil
}
