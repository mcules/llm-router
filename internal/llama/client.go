package llama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
		HTTP: &http.Client{
			Timeout: 10 * time.Second,
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

	// If /slots is disabled, llama.cpp may return non-2xx. Treat as 0 inflight.
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

type unloadReq struct {
	Model string `json:"model"`
}

func (c *Client) UnloadModel(ctx context.Context, modelID string) error {
	body, _ := json.Marshal(unloadReq{Model: modelID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/models/unload", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode/100 != 2 {
		return fmt.Errorf("unload status=%d", res.StatusCode)
	}
	return nil
}
