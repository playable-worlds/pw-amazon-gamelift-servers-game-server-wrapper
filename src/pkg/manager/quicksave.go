package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	defaultQuickSaveWait   = 60 * time.Second
	defaultQuickSavePath   = "/quicksave"
	defaultQuickSaveMethod = http.MethodPost
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

func normalizeQuickSaveMethod(method string) string {
	switch strings.ToUpper(method) {
	case http.MethodGet:
		return http.MethodGet
	default:
		return defaultQuickSaveMethod
	}
}

func mergeQuickSaveQuery(query string, zoneID string) string {
	values, err := url.ParseQuery(strings.TrimPrefix(query, "?"))
	if err != nil {
		values = url.Values{}
	}
	values.Set("zone_id", zoneID)
	return values.Encode()
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

func quicksave(ctx context.Context, zoneid string, port int, path string, query string, method string, auth QuickSaveAuth, apiKey string) error {
	// Create a timeout context for quicksave to ensure it doesn't hang indefinitely
	quicksaveCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	method = normalizeQuickSaveMethod(method)
	requestQuery := query
	if method == http.MethodGet {
		requestQuery = mergeQuickSaveQuery(query, zoneid)
	}

	url := buildQuickSaveURL(port, path, requestQuery)

	var body io.Reader
	if method == http.MethodPost {
		payload := QuickSave{ZoneId: zoneid}
		bodyBytes, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to marshal payload: %w", err)
		}
		body = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(quicksaveCtx, method, url, body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
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
