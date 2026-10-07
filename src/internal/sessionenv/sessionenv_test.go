/*
 * Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

package sessionenv

import (
	"testing"

	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/internal/credserver"
	pkgConfig "github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/config"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/game"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/types/events"
	"github.com/stretchr/testify/assert"
)

func startArgs() *game.StartArgs {
	return &game.StartArgs{HostingStart: &events.HostingStart{
		GameSessionName: "zone-1",
		GameProperties:  `{"zoneName":"Acorn","WORLD_ID":"from-props"}`,
		AwsCredentials:  &events.AwsCredentials{AccessKeyId: "AKIA", SecretAccessKey: "secret", SessionToken: "token"},
	}}
}

func Test_Build_All_Sources(t *testing.T) {
	credSrv := &credserver.Server{Addr: "http://127.0.0.1:1234", Token: "tok"}

	env, err := Build(startArgs(), []pkgConfig.EnvVar{
		{Name: "WORLD_ID", Value: "{{.GameSessionName}}"},
		{Name: "ZONE", Value: `{{.GameProperty "zoneName"}}`},
	}, Options{InjectAwsCredentials: true, CredentialServer: credSrv})

	assert.NoError(t, err)
	assert.Equal(t, map[string]string{
		"zoneName":              "Acorn",
		"WORLD_ID":              "zone-1", // configured env vars win over game properties
		"ZONE":                  "Acorn",
		"AWS_ACCESS_KEY_ID":     "AKIA",
		"AWS_SECRET_ACCESS_KEY": "secret",
		"AWS_SESSION_TOKEN":     "token",
		"PW_CRED_SERVER_URL":    "http://127.0.0.1:1234",
		"PW_CRED_SERVER_TOKEN":  "tok",
	}, env)
}

func Test_Build_Without_Options(t *testing.T) {
	env, err := Build(startArgs(), nil, Options{})

	assert.NoError(t, err)
	assert.NotContains(t, env, "AWS_ACCESS_KEY_ID")
	assert.NotContains(t, env, "PW_CRED_SERVER_URL")
	assert.Equal(t, "Acorn", env["zoneName"])
}

func Test_Build_Invalid_Game_Properties(t *testing.T) {
	args := startArgs()
	args.GameProperties = "not json"

	_, err := Build(args, nil, Options{})

	assert.ErrorContains(t, err, "failed to unmarshall process args")
}

func Test_Build_Nil_Start_Args(t *testing.T) {
	env, err := Build(nil, []pkgConfig.EnvVar{{Name: "A", Value: "b"}}, Options{})

	assert.NoError(t, err)
	assert.Empty(t, env)
}
