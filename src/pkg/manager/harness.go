/*
 * Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

package manager

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/game"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/observability"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/types/events"
	"github.com/google/uuid"
	"github.com/pkg/errors"
)

// Harness defines the interface for game server lifecycle management.
type Harness interface {
	Init(ctx context.Context, args *game.InitArgs) (*game.InitMeta, error)
	Run(ctx context.Context) error
	HostingStart(ctx context.Context, h *events.HostingStart, end <-chan error) error
	HostingTerminate(ctx context.Context, h *events.HostingTerminate) error
	HealthCheck(ctx context.Context) events.GameStatus
	Close(ctx context.Context) error
}

type harness struct {
	hostingTerminate  chan *events.HostingTerminate
	hostingStart      chan *events.HostingStart
	logger            *slog.Logger
	game              game.Server
	spanner           observability.Spanner
	hostingStartEvent *events.HostingStart
	quickSaveEnabled   bool
	quickSaveAuth      QuickSaveAuth
	quickSaveApiKey    string
	quickSavePort      int
	quickSavePath      string
	quickSaveQuery     string
	quickSaveMethod    string
	quickSaveWait      time.Duration
	quickSaveAttempted bool
}

// HostingStart handles the server hosting start event.
// It prepares the server for hosting game sessions.
//
// Parameters:
//   - ctx: The context for the hosting operation
//   - h: Hosting start event details
//   - end: Channel that signals hosting termination
//
// Returns:
//   - error: An error if hosting start fails
func (harness *harness) HostingStart(_ context.Context, h *events.HostingStart, end <-chan error) error {
	harness.hostingStart <- h
	if end != nil {
		return <-end
	}

	return nil
}

// HostingTerminate handles the server hosting termination event.
// It performs cleanup and shutdown procedures for the hosting session.
//
// Parameters:
//   - ctx: The context for the termination operation
//   - h: Hosting termination event details
//
// Returns:
//   - error: An error if termination process fails
func (harness *harness) HostingTerminate(_ context.Context, h *events.HostingTerminate) error {
	harness.hostingTerminate <- h
	return nil
}

// HealthCheck performs a health check of the game server.
// It returns the current status of the game server.
//
// Parameters:
//   - ctx: The context for the health check operation
//
// Returns:
//   - GameStatus: Current status of the game server
func (harness *harness) HealthCheck(ctx context.Context) events.GameStatus {
	return harness.game.HealthCheck(ctx)
}

// Init initializes the game server with the provided initialization arguments.
// It performs necessary setup and returns initialization metadata or an error if initialization fails.
//
// Parameters:
//   - ctx: The context for the initialization operation
//   - args: Initialization arguments containing server configuration
//
// Returns:
//   - *InitMeta: Metadata about the initialized server
//   - error: An error if initialization fails
func (harness *harness) Init(ctx context.Context, args *game.InitArgs) (*game.InitMeta, error) {
	return harness.game.Init(ctx, args)
}

// Run starts the main game server loop.
// It manages the core game server operations until completion or error.
//
// Parameters:
//   - ctx: The context for the running operation
//
// Returns:
//   - error: An error if the server encounters issues while running
func (harness *harness) Run(ctx context.Context) error {
	harness.logger.DebugContext(ctx, "Game server harness starting")

	ctx, cancel := context.WithCancel(ctx)

	defer cancel()

	hostingStartErrorChannel, hostingTerminateErrorChannel := make(chan error), make(chan error)

	go func() {
		var hostingStartEvent *events.HostingStart
		select {
		case hostingStartEvent = <-harness.hostingStart:
			break
		case <-ctx.Done():
			return
		}

		harness.logger.DebugContext(ctx, "Received hosting start event", "event", hostingStartEvent)

		harness.hostingStartEvent = hostingStartEvent

		meta := map[string]string{
			"fleet-id":        hostingStartEvent.FleetId,
			"provider":        string(hostingStartEvent.Provider),
			"game-session-id": hostingStartEvent.GameSessionId,
		}

		// game session id is a full arn, eg:
		// arn:aws:gamelift:eu-west-1::gamesession/fleet-8a8e55eb-6607-4eb3-9031-eed48907d5a4/dev-jim/6aa3a161-f2fb-4b53-bfd9-1f31c3b20cd2
		sessionIdParts := strings.Split(hostingStartEvent.GameSessionId, "/")
		gsessStr := sessionIdParts[len(sessionIdParts)-1]

		gsessUUID, err := uuid.Parse(gsessStr)
		if err != nil {
			gameSession := ""
			if strings.HasPrefix(gsessStr, "gsess-") {
				gameSession = strings.Replace(gsessStr, "gsess-", "", 1)
			}
			// check if allstr starts with gss, if so remove gss and just use the rest of the string
			gsessUUID, err = uuid.Parse(gameSession)
			if err != nil {
				gsessUUID = uuid.New()
				harness.logger.WarnContext(ctx, "game session is not a uuid, generating a new one", "GameSessionId", hostingStartEvent.GameSessionId, "gsessUUID", gsessUUID.String())
			}
		}

		ctx, span, err := harness.spanner.NewSpanWithTraceId(ctx, "game-run", gsessUUID, meta)
		if err != nil {
			hostingStartErrorChannel <- errors.Wrap(err, "failed to generate span")
		}

		defer span.End()

		err = harness.game.Run(ctx, &game.StartArgs{
			HostingStart: hostingStartEvent,
		})

		span.End()

		hostingStartErrorChannel <- err
	}()

	go func() {
		var hostingTerminateEvent *events.HostingTerminate
		select {
		case hostingTerminateEvent = <-harness.hostingTerminate:
			break
		case <-ctx.Done():
			return
		}

		harness.logger.DebugContext(ctx, "Received hosting terminate event", "event", hostingTerminateEvent)

		// Perform quicksave BEFORE stopping the game to ensure it completes
		harness.performQuickSave(ctx)

		hostingTerminateErrorChannel <- harness.game.Stop(ctx)
	}()

	select {
	case reason := <-ctx.Done():
		harness.logger.DebugContext(ctx, "Game server harness context done", "reason", reason)
		return nil
	case err := <-hostingStartErrorChannel:
		harness.logger.DebugContext(ctx, "Hosting start result")
		return err
	case err := <-hostingTerminateErrorChannel:
		harness.logger.DebugContext(ctx, "Hosting terminate result")
		return err
	}
}

// performQuickSave executes the quicksave operation if enabled and not already attempted.
// This method ensures quicksave completes before the game server is stopped.
func (harness *harness) performQuickSave(ctx context.Context) {
	if harness.quickSaveEnabled && harness.hostingStartEvent != nil && !harness.quickSaveAttempted {
		port := harness.quickSavePort
		if port == 0 {
			port = harness.hostingStartEvent.GamePort
		}
		harness.logger.DebugContext(ctx, "attempting to quicksave", "saveEnabled", harness.quickSaveEnabled, "sessionName", harness.hostingStartEvent.GameSessionName, "port", port)
		harness.quickSaveAttempted = true
		// Use context.Background() to ensure quicksave isn't cancelled by context cancellation
		if err := quicksave(context.Background(), harness.hostingStartEvent.GameSessionName, port, harness.quickSavePath, harness.quickSaveQuery, harness.quickSaveMethod, harness.quickSaveAuth, harness.quickSaveApiKey); err != nil {
			harness.logger.WarnContext(ctx, "Failed to perform quicksave", "error", err)
		} else {
			harness.logger.DebugContext(ctx, "Quicksave completed successfully")
		}

		if harness.quickSaveWait > 0 {
			harness.logger.DebugContext(ctx, "waiting after quicksave before shutdown", "wait", harness.quickSaveWait)
			time.Sleep(harness.quickSaveWait)
		}
	}
}

// Close performs cleanup and releases resources used by the harness.
// It should be called when shutting down the server.
//
// Parameters:
//   - ctx: The context for the close operation
//
// Returns:
//   - error: An error if cleanup fails
func (harness *harness) Close(ctx context.Context) error {
	// Perform quicksave BEFORE stopping the game to ensure it completes
	harness.performQuickSave(ctx)

	err := harness.game.Stop(ctx)

	if harness.hostingTerminate != nil {
		close(harness.hostingTerminate)
	}

	if harness.hostingStart != nil {
		close(harness.hostingStart)
	}

	return err
}

// NewHarness creates and returns a new instance of the harness.
// It initializes all necessary components for game server management.
//
// Parameters:
//   - game: The game server instance to be managed
//   - logger: Logger instance for recording operations and events
//   - spanner: Observability component for monitoring and tracing
//   - quickSaveEnabled: Whether quicksave functionality is enabled
//   - quickSaveAuth: Optional inter-server auth provider for quicksave requests
//   - quickSaveApiKey: API key for quicksave requests when inter-server auth is not used
//   - quickSavePort: HTTP port for quicksave requests (defaults to the game session port when 0)
//   - quickSavePath: HTTP path for quicksave requests (defaults to /quicksave)
//   - quickSaveQuery: Optional query string for quicksave requests (without leading ?)
//   - quickSaveMethod: HTTP method for quicksave requests (GET or POST, defaults to POST)
//   - quickSaveWait: Duration string to wait after quicksave before allowing shutdown (e.g. "30s")
//
// Returns:
//   - *harness: A new harness instance configured with the provided components
func NewHarness(game game.Server, logger *slog.Logger, spanner observability.Spanner, quickSaveEnabled bool, quickSaveAuth QuickSaveAuth, quickSaveApiKey string, quickSavePort int, quickSavePath string, quickSaveQuery string, quickSaveMethod string, quickSaveWait string) *harness {
	wait, usedDefault := parseQuickSaveWait(quickSaveWait)
	if usedDefault && quickSaveEnabled {
		logger.Info("No quick-save-wait configured, using default", "quickSaveWait", wait)
	}
	if quickSaveWait != "" && wait == 0 {
		logger.Warn("Invalid quick-save-wait duration, shutdown will not be delayed after quicksave", "quickSaveWait", quickSaveWait)
	}

	b := &harness{
		hostingTerminate: make(chan *events.HostingTerminate),
		hostingStart:     make(chan *events.HostingStart),
		logger:           logger,
		game:             game,
		spanner:          spanner,
		quickSaveEnabled: quickSaveEnabled,
		quickSaveAuth:    quickSaveAuth,
		quickSaveApiKey:  quickSaveApiKey,
		quickSavePort:    quickSavePort,
		quickSavePath:    quickSavePath,
		quickSaveQuery:   quickSaveQuery,
		quickSaveMethod:  normalizeQuickSaveMethod(quickSaveMethod),
		quickSaveWait:    wait,
	}

	return b
}
