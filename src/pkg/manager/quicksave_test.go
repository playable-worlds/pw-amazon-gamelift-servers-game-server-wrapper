package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
