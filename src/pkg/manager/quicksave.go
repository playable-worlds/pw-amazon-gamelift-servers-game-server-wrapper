package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type QuickSave struct {
	ZoneId string `json:"zone_id"`
}

// QuickSaveAuth provides authorization headers for quicksave requests.
type QuickSaveAuth interface {
	AuthorizationHeader(ctx context.Context) (string, error)
}

const (
	defaultQuickSaveWait  = 60 * time.Second
	defaultQuickSavePath  = "/quicksave"
)

func buildQuickSaveURL(port int, path string, query string) string {
	if path == "" {
		path = defaultQuickSavePath
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	u := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("localhost:%d", port),
		Path:   path,
	}
	if query != "" {
		u.RawQuery = strings.TrimPrefix(query, "?")
	}

	return u.String()
}

func parseQuickSaveWait(wait string) (time.Duration, bool) {
	if wait == "" {
		return defaultQuickSaveWait, true
	}

	d, err := time.ParseDuration(wait)
	if err != nil {
		return 0, false
	}

	return d, false
}

func quicksave(ctx context.Context, zoneid string, port int, path string, query string, auth QuickSaveAuth, apiKey string) error {
	// Create a timeout context for quicksave to ensure it doesn't hang indefinitely
	quicksaveCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	url := buildQuickSaveURL(port, path, query)

	payload := QuickSave{ZoneId: zoneid}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(quicksaveCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != nil {
		authHeader, err := auth.AuthorizationHeader(quicksaveCtx)
		if err != nil {
			return fmt.Errorf("failed to acquire auth token: %w", err)
		}
		req.Header.Set("Authorization", authHeader)
	} else if apiKey != "" {
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
