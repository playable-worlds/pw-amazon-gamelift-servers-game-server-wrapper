package manager

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

type stubQuickSaveAuth struct {
	header string
	err    error
}

func (s stubQuickSaveAuth) AuthorizationHeader(ctx context.Context) (string, error) {
	return s.header, s.err
}

func Test_quicksave_Uses_Inter_Server_Auth(t *testing.T) {
	var gotAuth string
	var gotAPIKey string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, portStr, err := net.SplitHostPort(server.Listener.Addr().String())
	assert.Nil(t, err)
	port, err := strconv.Atoi(portStr)
	assert.Nil(t, err)

	err = quicksave(context.Background(), "zone-1", port, "", "", http.MethodPost, stubQuickSaveAuth{header: "Bearer test-token"}, "ignored-api-key")
	assert.Nil(t, err)
	assert.Equal(t, "Bearer test-token", gotAuth)
	assert.Empty(t, gotAPIKey)
}

func Test_quicksave_Uses_Api_Key_When_Auth_Not_Configured(t *testing.T) {
	var gotAuth string
	var gotAPIKey string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, portStr, err := net.SplitHostPort(server.Listener.Addr().String())
	assert.Nil(t, err)
	port, err := strconv.Atoi(portStr)
	assert.Nil(t, err)

	err = quicksave(context.Background(), "zone-1", port, "", "", http.MethodPost, nil, "secret-key")
	assert.Nil(t, err)
	assert.Empty(t, gotAuth)
	assert.Equal(t, "secret-key", gotAPIKey)
}

func Test_quicksave_Uses_Get_Method(t *testing.T) {
	var gotMethod string
	var gotQuery string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, portStr, err := net.SplitHostPort(server.Listener.Addr().String())
	assert.Nil(t, err)
	port, err := strconv.Atoi(portStr)
	assert.Nil(t, err)

	err = quicksave(context.Background(), "zone-1", port, "", "force=true", http.MethodGet, nil, "")
	assert.Nil(t, err)
	assert.Equal(t, http.MethodGet, gotMethod)
	assert.Equal(t, "force=true&zone_id=zone-1", gotQuery)
}

func Test_normalizeQuickSaveMethod(t *testing.T) {
	assert.Equal(t, http.MethodPost, normalizeQuickSaveMethod(""))
	assert.Equal(t, http.MethodPost, normalizeQuickSaveMethod("POST"))
	assert.Equal(t, http.MethodGet, normalizeQuickSaveMethod("GET"))
	assert.Equal(t, http.MethodGet, normalizeQuickSaveMethod("get"))
	assert.Equal(t, http.MethodPost, normalizeQuickSaveMethod("PUT"))
}

func Test_buildQuickSaveURL(t *testing.T) {
	tests := []struct {
		name     string
		port     int
		path     string
		query    string
		expected string
	}{
		{
			name:     "defaults",
			port:     8080,
			path:     "",
			query:    "",
			expected: "http://localhost:8080/quicksave",
		},
		{
			name:     "custom path",
			port:     50042,
			path:     "/api/save",
			query:    "",
			expected: "http://localhost:50042/api/save",
		},
		{
			name:     "path without leading slash",
			port:     50042,
			path:     "save",
			query:    "",
			expected: "http://localhost:50042/save",
		},
		{
			name:     "with query string",
			port:     8080,
			path:     "/quicksave",
			query:    "force=true",
			expected: "http://localhost:8080/quicksave?force=true",
		},
		{
			name:     "query string with leading question mark",
			port:     8080,
			path:     "/quicksave",
			query:    "?force=true",
			expected: "http://localhost:8080/quicksave?force=true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, buildQuickSaveURL(tt.port, tt.path, tt.query))
		})
	}
}
