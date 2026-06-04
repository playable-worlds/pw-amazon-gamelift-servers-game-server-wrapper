/*
 * Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

package otelcol

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_UpdateTags_NewConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	service := NewForTesting(configPath, "", map[string]string{"session_name": "{{.GameSessionName}}"}, logger)

	ctx := context.Background()
	hostingStart := &struct {
		GameSessionName string
	}{
		GameSessionName: "test-session-123",
	}

	err := service.UpdateTags(ctx, hostingStart)
	require.NoError(t, err)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	configContent := string(data)
	assert.Contains(t, configContent, "attributes/gamesession:")
	assert.Contains(t, configContent, "key: session_name")
	assert.Contains(t, configContent, "value: test-session-123")
	assert.Contains(t, configContent, "action: upsert")
}

func TestService_UpdateTags_ExistingConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	initialConfig := `processors:
  attributes/deploymenttype:
    actions:
      - key: deployment_type
        value: EC2
        action: upsert
service:
  pipelines:
    metrics:
      receivers: [statsd]
      processors:
        - attributes/deploymenttype
      exporters: [awsemf]
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	service := NewForTesting(configPath, "", map[string]string{"session_name": "{{.GameSessionName}}"}, logger)

	ctx := context.Background()
	hostingStart := &struct {
		GameSessionName string
	}{
		GameSessionName: "test-session-456",
	}

	err = service.UpdateTags(ctx, hostingStart)
	require.NoError(t, err)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	configContent := string(data)
	assert.Contains(t, configContent, "session_name")
	assert.Contains(t, configContent, "test-session-456")
	assert.Contains(t, configContent, "deployment_type")
	assert.Contains(t, configContent, "attributes/gamesession")
	assert.Contains(t, configContent, "attributes/deploymenttype")
	assert.Contains(t, configContent, "- attributes/gamesession")
}

func TestService_UpdateTags_ReplacesOldTags(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	initialConfig := `processors:
  attributes/gamesession:
    actions:
      - key: session_name
        value: old-session
        action: upsert
      - key: fleet_id
        value: old-fleet
        action: upsert
      - key: region
        value: us-west-2
        action: upsert
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	tagTemplates := map[string]string{
		"session_name": "{{.GameSessionName}}",
		"fleet_id":     "{{.FleetId}}",
	}
	service := NewForTesting(configPath, "", tagTemplates, logger)

	ctx := context.Background()
	hostingStart := &struct {
		GameSessionName string
		FleetId         string
	}{
		GameSessionName: "new-session",
		FleetId:         "new-fleet",
	}

	err = service.UpdateTags(ctx, hostingStart)
	require.NoError(t, err)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	configContent := string(data)
	assert.NotContains(t, configContent, "old-session")
	assert.NotContains(t, configContent, "old-fleet")
	assert.Contains(t, configContent, "new-session")
	assert.Contains(t, configContent, "new-fleet")
	assert.Contains(t, configContent, "region")
	assert.Contains(t, configContent, "us-west-2")
}

func TestService_UpdateTags_DuplicateTag(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	service := NewForTesting(configPath, "", map[string]string{"session_name": "{{.GameSessionName}}"}, logger)

	ctx := context.Background()
	hostingStart := &struct {
		GameSessionName string
	}{
		GameSessionName: "test-session-789",
	}

	require.NoError(t, service.UpdateTags(ctx, hostingStart))
	require.NoError(t, service.UpdateTags(ctx, hostingStart))

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "value: test-session-789" {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestService_TestMode_LogsCommand(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	var logOutput bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logOutput, &slog.HandlerOptions{Level: slog.LevelInfo}))
	service := NewForTesting(configPath, "", map[string]string{"session_name": "{{.GameSessionName}}"}, logger)

	ctx := context.Background()
	hostingStart := &struct {
		GameSessionName string
	}{
		GameSessionName: "test-session",
	}

	err := service.UpdateTags(ctx, hostingStart)
	require.NoError(t, err)

	logContent := logOutput.String()
	assert.Contains(t, logContent, "Test mode: Would execute reload command")
	assert.Contains(t, logContent, "command=\"sudo systemctl restart otelcol-contrib\"")
}

func TestService_UpdateTags_CustomProcessor(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	service := NewForTesting(configPath, "attributes/session", map[string]string{"session_id": "{{.GameSessionId}}"}, logger)

	ctx := context.Background()
	hostingStart := &struct {
		GameSessionId string
	}{
		GameSessionId: "gsess-123",
	}

	err := service.UpdateTags(ctx, hostingStart)
	require.NoError(t, err)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	configContent := string(data)
	assert.Contains(t, configContent, "attributes/session:")
	assert.Contains(t, configContent, "key: session_id")
	assert.Contains(t, configContent, "value: gsess-123")
}
