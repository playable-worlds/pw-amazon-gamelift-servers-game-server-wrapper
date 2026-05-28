/*
 * Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

package events

import (
	"context"
	"time"

	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/config"
)

// HostingStart represents the initialization configuration for a game server instance.
type HostingStart struct {
	CliArgs                   []config.CliArg
	EnvVars                   []config.EnvVar
	ContainerPort             int
	DNSName                   string
	FleetId                   string
	GamePort                  int
	GameProperties            string
	GameSessionData           string
	GameSessionId             string
	GameSessionName           string
	IpAddress                 string
	LogDirectory              string
	MatchmakerData            string
	MaximumPlayerSessionCount int
	Provider                  config.Provider
	AwsCredentials            *AwsCredentials
	// CredentialsFetcher is called by the local credential server to obtain refreshed fleet-role credentials.
	// Not serialized — wired at runtime by the GameLift hosting layer.
	CredentialsFetcher func(ctx context.Context) (accessKeyId, secretAccessKey, sessionToken string, expiration time.Time, err error) `json:"-"`
}

// AwsCredentials represents temporary AWS credentials provided by GameLift fleet role.
type AwsCredentials struct {
	AccessKeyId     string
	SecretAccessKey string
	SessionToken    string
}
