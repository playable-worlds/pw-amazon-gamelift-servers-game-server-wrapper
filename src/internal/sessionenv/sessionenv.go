/*
 * Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

// Package sessionenv builds the environment variables handed to a game session's
// process or containers, shared by the process and docker launchers.
package sessionenv

import (
	"fmt"

	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/internal/credserver"
	pkgConfig "github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/config"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/game"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/sessiontemplate"
)

// Options controls which optional variables are added.
type Options struct {
	// InjectAwsCredentials adds AWS_* from the session's fleet-role credentials, when present.
	InjectAwsCredentials bool
	// CredentialServer, when non-nil, adds PW_CRED_SERVER_URL and PW_CRED_SERVER_TOKEN.
	CredentialServer *credserver.Server
}

// Build returns the session environment: game properties, then AWS credentials and
// credential server details (per opts), then envVars rendered with the session template.
// Later entries win on name clashes.
func Build(startArgs *game.StartArgs, envVars []pkgConfig.EnvVar, opts Options) (map[string]string, error) {
	env := map[string]string{}
	if startArgs == nil {
		return env, nil
	}

	if startArgs.HostingStart != nil && startArgs.GameProperties != "" {
		if err := startArgs.EnsureGamePropertiesMap(); err != nil {
			return nil, fmt.Errorf("failed to unmarshall process args: %w", err)
		}

		for k, v := range startArgs.GamePropertiesMap {
			env[k] = v
		}
	}

	if opts.InjectAwsCredentials && startArgs.HostingStart != nil && startArgs.AwsCredentials != nil {
		env["AWS_ACCESS_KEY_ID"] = startArgs.AwsCredentials.AccessKeyId
		env["AWS_SECRET_ACCESS_KEY"] = startArgs.AwsCredentials.SecretAccessKey
		env["AWS_SESSION_TOKEN"] = startArgs.AwsCredentials.SessionToken
	}

	if opts.CredentialServer != nil {
		env["PW_CRED_SERVER_URL"] = opts.CredentialServer.Addr
		env["PW_CRED_SERVER_TOKEN"] = opts.CredentialServer.Token
	}

	for _, envVar := range envVars {
		value, err := sessiontemplate.Execute(envVar.Name, envVar.Value, startArgs)
		if err != nil {
			return nil, err
		}

		env[envVar.Name] = value
	}

	return env, nil
}
