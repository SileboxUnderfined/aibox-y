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
	search := searchFrom(ctx)
	switch {
	case search.Mode == SearchTool:
		params.Tools = []anthropic.ToolUnionParam{{OfTool: &anthropic.ToolParam{
			Name:        searchToolName,
			Description: anthropic.String(searchToolDescription),
			InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{"query": map[string]string{"type": "string", "description": "Поисковый запрос"}}, Required: []string{"query"}},
		}}}
	case search.Mode == SearchNative && NativeSearch(c.cfg) == "anthropic":
		params.Tools = []anthropic.ToolUnionParam{{OfWebSearchTool20250305: &anthropic.WebSearchTool20250305Param{
			MaxUses:      anthropic.Int(maxSearchRounds),
			UserLocation: anthropic.UserLocationParam{Country: anthropic.String("RU")},
		}}}
	}
	for round := 0; ; round++ {
		if round >= maxSearchRounds && search.Mode == SearchTool {
			params.ToolChoice = anthropic.ToolChoiceUnionParam{OfNone: &anthropic.ToolChoiceNoneParam{}}
		}
		resp, err := client.Messages.New(ctx, params)
		if err != nil {
			return "", err
		}
		if resp.StopReason == anthropic.StopReasonRefusal {
			return "", errors.New("model declined the request")
		}
		continueTurn := resp.StopReason == anthropic.StopReasonPauseTurn
		var results []anthropic.ContentBlockParamUnion
		if resp.StopReason == anthropic.StopReasonToolUse && search.Mode == SearchTool {
			for _, block := range resp.Content {
				if use, ok := block.AsAny().(anthropic.ToolUseBlock); ok {
					result := "Неизвестный инструмент."
					if use.Name == searchToolName {
						result = runSearch(ctx, search.Searcher, string(use.Input))
					}
					results = append(results, anthropic.NewToolResultBlock(use.ID, result, false))
				}
			}
		}
		if (continueTurn || len(results) > 0) && round < 2*maxSearchRounds {
			params.Messages = append(params.Messages, resp.ToParam())
			if len(results) > 0 {
				params.Messages = append(params.Messages, anthropic.NewUserMessage(results...))
			}
			continue
		}
		return claudeAnswer(resp, search.Mode != "")
	}
}

// claudeAnswer is the text the model wrote after its last search; what it
// said before searching ("сейчас поищу") is not part of the answer.
func claudeAnswer(resp *anthropic.Message, searched bool) (string, error) {
	var parts []string
	for _, block := range resp.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			parts = append(parts, b.Text)
		case anthropic.ServerToolUseBlock, anthropic.WebSearchToolResultBlock, anthropic.ToolUseBlock:
			parts = nil
		}
	}
	answer := strings.TrimSpace(strings.Join(parts, ""))
	if searched {
		answer = speakable(answer)
	}
	if answer == "" {
		return "", errors.New("provider returned no answer")
	}
	return answer, nil
}
