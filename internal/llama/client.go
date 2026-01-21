package llama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Comments in this file are intentionally in English.

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
		HTTP: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

type ModelsResponse struct {
	Data []struct {
		ID     string `json:"id"`
		Status struct {
			Value    string `json:"value"`     // loaded/loading/unloaded/...
			Failed   bool   `json:"failed"`    // best-effort
			ExitCode int    `json:"exit_code"` // best-effort
		} `json:"status"`
	} `json:"data"`
}

func (c *Client) GetModels(ctx context.Context) (*ModelsResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("models status=%d", res.StatusCode)
	}
	var out ModelsResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

type SlotsResponse struct {
	Slots []struct {
		IsProcessing bool `json:"is_processing"`
	} `json:"slots"`
}

func (c *Client) GetSlotsInflight(ctx context.Context) (uint32, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/slots", nil)
	if err != nil {
		return 0, err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()

	// If /slots is disabled, llama.cpp may return an error or non-2xx.
	if res.StatusCode/100 != 2 {
		return 0, nil
	}

	var out SlotsResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return 0, err
	}

	var inflight uint32
	for _, s := range out.Slots {
		if s.IsProcessing {
			inflight++
		}
	}
	return inflight, nil
}
