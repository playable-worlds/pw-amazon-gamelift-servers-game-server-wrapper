/*
 * Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

package events

import (
	"context"
	"encoding/json"
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
	GamePropertiesMap         map[string]string `json:"-"`
	GameSessionData           string
	GameSessionId             string
	GameSessionName           string
	IpAddress                 string
	LogDirectory              string
	MatchmakerData            string
	MaximumPlayerSessionCount int
	Provider       config.Provider
	AwsCredentials *AwsCredentials
	// InjectAwsCredentials instructs the child-process launcher to set AWS_* env vars from AwsCredentials.
	// Controlled by inject-fleet-role-credentials; not serialized.
	InjectAwsCredentials bool `json:"-"`
	// CredentialsFetcher is called by the local credential server to obtain refreshed fleet-role credentials.
	// Not serialized — wired at runtime by the GameLift hosting layer.
	CredentialsFetcher func(ctx context.Context) (accessKeyId, secretAccessKey, sessionToken string, expiration time.Time, err error) `json:"-"`
}

// EnsureGamePropertiesMap parses GameProperties into GamePropertiesMap when needed.
func (h *HostingStart) EnsureGamePropertiesMap() error {
	if h == nil || h.GamePropertiesMap != nil {
		return nil
	}

	h.GamePropertiesMap = make(map[string]string)
	if h.GameProperties == "" {
		return nil
	}

	if err := json.Unmarshal([]byte(h.GameProperties), &h.GamePropertiesMap); err != nil {
		return err
	}

	return nil
}

// GameProperty returns a single game session property value for use in templates.
func (h *HostingStart) GameProperty(key string) string {
	if h == nil {
		return ""
	}

	if err := h.EnsureGamePropertiesMap(); err != nil {
		return ""
	}
	if h.GamePropertiesMap == nil {
		return ""
	}

	return h.GamePropertiesMap[key]
}

// AwsCredentials represents temporary AWS credentials provided by GameLift fleet role.
type AwsCredentials struct {
	AccessKeyId     string
	SecretAccessKey string
	SessionToken    string
}
