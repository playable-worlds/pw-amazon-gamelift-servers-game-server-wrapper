/*
 * Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func validConfig(buildDetail BuildDetail) Config {
	return Config{
		Ports:       Ports{GamePort: 7777},
		BuildDetail: buildDetail,
	}
}

func Test_Validate_Launcher(t *testing.T) {
	tests := []struct {
		name        string
		buildDetail BuildDetail
		wantErr     string
	}{
		{name: "default launcher", buildDetail: BuildDetail{}},
		{name: "process launcher", buildDetail: BuildDetail{Launcher: LauncherProcess}},
		{name: "unknown launcher", buildDetail: BuildDetail{Launcher: "podman"}, wantErr: `launcher must be "process" or "docker"`},
		{name: "docker without containers", buildDetail: BuildDetail{Launcher: LauncherDocker}, wantErr: "containers must not be empty"},
		{
			name: "docker valid",
			buildDetail: BuildDetail{Launcher: LauncherDocker, Containers: []Container{
				{Name: "casim", Image: "casim-rust:v1", StopTimeout: "55s", ReadyCheck: &ReadyCheck{Url: "http://localhost:50042/ping", Timeout: "2m"}},
				{Name: "gameserver", Image: "gameserver:1"},
			}},
		},
		{
			name:        "docker bad name",
			buildDetail: BuildDetail{Launcher: LauncherDocker, Containers: []Container{{Name: "-casim", Image: "casim"}}},
			wantErr:     "is not a valid container name",
		},
		{
			name: "docker duplicate name",
			buildDetail: BuildDetail{Launcher: LauncherDocker, Containers: []Container{
				{Name: "casim", Image: "a"}, {Name: "casim", Image: "b"},
			}},
			wantErr: "is duplicated",
		},
		{
			name:        "docker missing image",
			buildDetail: BuildDetail{Launcher: LauncherDocker, Containers: []Container{{Name: "casim"}}},
			wantErr:     "image must not be empty",
		},
		{
			name:        "docker bad stop timeout",
			buildDetail: BuildDetail{Launcher: LauncherDocker, Containers: []Container{{Name: "casim", Image: "a", StopTimeout: "soon"}}},
			wantErr:     "stop-timeout",
		},
		{
			name:        "docker ready check without url",
			buildDetail: BuildDetail{Launcher: LauncherDocker, Containers: []Container{{Name: "casim", Image: "a", ReadyCheck: &ReadyCheck{}}}},
			wantErr:     "ready-check.url must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validConfig(tt.buildDetail).Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func Test_AdaptConfigWrapperToConfig_Docker_Launcher(t *testing.T) {
	wrapper := ConfigWrapper{
		Ports: Ports{GamePort: 7777},
		GameServerDetails: GameServerDetails{
			Launcher:   LauncherDocker,
			Containers: []Container{{Name: "casim", Image: "casim-rust:v1"}},
		},
	}
	var cfg Config

	err := AdaptConfigWrapperToConfig(&wrapper, &cfg)

	assert.NoError(t, err)
	assert.True(t, cfg.BuildDetail.IsDocker())
	assert.Empty(t, cfg.BuildDetail.RelativeExePath)
	assert.Equal(t, wrapper.GameServerDetails.Containers, cfg.BuildDetail.Containers)
	assert.NoError(t, cfg.Validate())
}

func Test_AdaptConfigWrapperToConfig_Process_Launcher_Is_Default(t *testing.T) {
	wrapper := ConfigWrapper{
		Ports: Ports{GamePort: 7777},
		GameServerDetails: GameServerDetails{
			WorkingDir:         "/local/game",
			ExecutableFilePath: "/local/game/gameserver/sr.x86_64",
		},
	}
	var cfg Config

	err := AdaptConfigWrapperToConfig(&wrapper, &cfg)

	assert.NoError(t, err)
	assert.False(t, cfg.BuildDetail.IsDocker())
	assert.Equal(t, "./gameserver/sr.x86_64", cfg.BuildDetail.RelativeExePath)
}

func Test_Example_Docker_Launcher_Config_Loads(t *testing.T) {
	v := viper.New()
	v.SetConfigFile("../../template/example-docker-launcher-config.yaml")
	assert.NoError(t, v.ReadInConfig())

	var wrapper ConfigWrapper
	assert.NoError(t, v.Unmarshal(&wrapper))
	var cfg Config
	assert.NoError(t, AdaptConfigWrapperToConfig(&wrapper, &cfg))
	assert.NoError(t, cfg.Validate())

	containers := cfg.BuildDetail.Containers
	assert.Len(t, containers, 2)
	assert.Equal(t, "casim", containers[0].Name)
	assert.Equal(t, []string{"--entrypoint", "/usr/local/bin/casim"}, containers[0].DockerRunArgs)
	assert.True(t, containers[0].UseCredentialServer)
	assert.Equal(t, "http://localhost:50042/ping", containers[0].ReadyCheck.Url)
	assert.Equal(t, 50042, containers[0].QuickSave.Port)
	assert.Equal(t, "{{.GameSessionName}}", containers[0].EnvVars[0].Value)

	assert.Equal(t, "gameserver", containers[1].Name)
	assert.True(t, containers[1].InjectAwsCredentials)
	assert.Equal(t, "-logFile", containers[1].Args[1].Name)
	assert.Equal(t, "-", containers[1].Args[1].Value)
	assert.True(t, containers[1].QuickSave.UseInterServerAuth)
	assert.Nil(t, containers[1].ReadyCheck)
}
