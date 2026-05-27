package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type QuickSave struct {
	ZoneId string `json:"zone_id"`
}

func quicksave(ctx context.Context, zoneid string, port int, apiKey string) error {
	url := fmt.Sprintf("http://localhost:%d/quicksave", port)

	payload := QuickSave{ZoneId: zoneid}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}
