/*
 * Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

package services

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/internal/config"
	"github.com/stretchr/testify/assert"
)

func Test_containerQuickSaveTargets_Reverse_Start_Order(t *testing.T) {
	cfg := &config.Config{BuildDetail: config.BuildDetail{
		Launcher: config.LauncherDocker,
		Containers: []config.Container{
			{Name: "casim", Image: "casim", QuickSave: &config.ContainerQuickSave{Port: 50042, ApiKey: "key", Wait: "10s"}},
			{Name: "sidecar", Image: "sidecar"},
			{Name: "gameserver", Image: "gameserver", QuickSave: &config.ContainerQuickSave{
				Port: 4500, Path: "/save", Query: "scope=zone", Method: "get", UseInterServerAuth: true, Wait: "60s",
			}},
		},
	}}

	targets := containerQuickSaveTargets(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	assert.Len(t, targets, 2)
	assert.Equal(t, "gameserver", targets[0].Name)
	assert.Equal(t, 4500, targets[0].Port)
	assert.Equal(t, "GET", targets[0].Method)
	assert.NotNil(t, targets[0].Auth)
	assert.Equal(t, 60*time.Second, targets[0].Wait)

	assert.Equal(t, "casim", targets[1].Name)
	assert.Equal(t, "key", targets[1].ApiKey)
	assert.Nil(t, targets[1].Auth)
	assert.Equal(t, "POST", targets[1].Method)
	assert.Equal(t, 10*time.Second, targets[1].Wait)
}

func Test_containerQuickSaveTargets_None_Configured(t *testing.T) {
	cfg := &config.Config{BuildDetail: config.BuildDetail{
		Launcher:   config.LauncherDocker,
		Containers: []config.Container{{Name: "casim", Image: "casim"}},
	}}

	assert.Empty(t, containerQuickSaveTargets(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))))
}
