package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ListModels returns the model ids the provider offers to this key.
func ListModels(ctx context.Context, cfg Config) ([]string, error) {
	key, err := apiKey(cfg)
	if err != nil {
		return nil, err
	}
	var ids []string
	if cfg.Type == "anthropic" {
		ids, err = anthropicModels(ctx, cfg, key)
		if err != nil {
			return nil, err
		}
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.BaseURL, "/")+"/models", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
		}
		var out struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, errors.New("unexpected model list")
		}
		for _, m := range out.Data {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
