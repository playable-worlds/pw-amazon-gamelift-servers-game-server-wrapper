/*
 * Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

package services

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/runner"

	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/internal/config"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/datadog"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/helpers"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/otelcol"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/logging"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/manager"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/observability"
	"github.com/pkg/errors"
	"interserverauth"
)

// Services defines the components required for running the game server wrapper
type Services struct {
	Logger  *slog.Logger
	Runner  *runner.Runner
	Spanner observability.Spanner
	Datadog *datadog.Service
	Otelcol *otelcol.Service
}

// Default initializes a new Services instance with all required components.
// It sets up the game server environment, hosting service, and management components.
func Default(ctx context.Context, cfg *config.Config, logger *slog.Logger, obs *observability.Observability, gameLogger logging.Game) (*Services, error) {
	logger.DebugContext(ctx, "Initializing game server wrapper services")

	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "Service initialization failed: invalid configuration")
	}

	hosting, err := getHosting(ctx, cfg, logger, obs.Spanner)
	if err != nil {
		return nil, errors.Wrapf(err, "Service initialization failed: failed to get hosting")
	}

	game, err := getGame(ctx, cfg, logger, gameLogger, obs.Spanner)
	if err != nil {
		return nil, errors.Wrapf(err, "Service initialization failed: failed to get game")
	}

	// Initialize datadog service if enabled
	var datadogService *datadog.Service
	if cfg.Datadog.Enabled {
		logger.DebugContext(ctx, "Initializing datadog service")
		datadogService = datadog.New(cfg.Datadog.ConfigPath, cfg.Datadog.Tags, logger)
	} else {
		logger.DebugContext(ctx, "Datadog service disabled")
	}

	var otelcolService *otelcol.Service
	if cfg.Otelcol.Enabled {
		logger.DebugContext(ctx, "Initializing otelcol-contrib service")
		otelcolService = otelcol.New(cfg.Otelcol.ConfigPath, cfg.Otelcol.ProcessorName, cfg.Otelcol.Tags, logger)
	} else {
		logger.DebugContext(ctx, "otelcol-contrib service disabled")
	}

	logger.DebugContext(ctx, "Creating game manager instance")
	var quickSaveAuth manager.QuickSaveAuth
	if cfg.Hosting.QuickSaveUseInterServerAuth {
		quickSaveAuth = &quickSaveInterServerAuth{
			auth: interserverauth.New(
				cfg.Orchestration.AuthHeaderPrefix,
				cfg.Orchestration.ClientId,
				cfg.Orchestration.ClientSecret,
				cfg.Orchestration.GetTokenUrl,
				helpers.NewHttpRequestHandler(http.DefaultClient, logger),
				logger,
			),
		}
	}

	managerInstance := manager.New(&manager.Config{
		QuickSave:                   cfg.Hosting.QuickSave,
		QuickSaveUseInterServerAuth: cfg.Hosting.QuickSaveUseInterServerAuth,
		QuickSaveApiKey:             cfg.Hosting.QuickSaveApiKey,
		QuickSavePort:               cfg.Hosting.QuickSavePort,
		QuickSavePath:               cfg.Hosting.QuickSavePath,
		QuickSaveQuery:              cfg.Hosting.QuickSaveQuery,
		QuickSaveMethod:             cfg.Hosting.QuickSaveMethod,
		QuickSaveWait:               cfg.Hosting.QuickSaveWait,
	}, game, hosting, logger, obs.Spanner, manager.NewHarness(game, logger, obs.Spanner, cfg.Hosting.QuickSave, quickSaveAuth, cfg.Hosting.QuickSaveApiKey, cfg.Hosting.QuickSavePort, cfg.Hosting.QuickSavePath, cfg.Hosting.QuickSaveQuery, cfg.Hosting.QuickSaveMethod, cfg.Hosting.QuickSaveWait), datadogService, otelcolService)

	logger.DebugContext(ctx, "Creating game runner instance")
	runnerInstance := runner.New("runner", managerInstance, logger, obs.Spanner)

	s := &Services{
		Logger:  logger,
		Runner:  runnerInstance,
		Spanner: obs.Spanner,
		Datadog: datadogService,
		Otelcol: otelcolService,
	}

	logger.DebugContext(ctx, "Game server wrapper services initialized successfully")

	return s, nil
}
