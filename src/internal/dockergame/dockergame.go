/*
 * Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

// Package dockergame implements game.Server by running one or more docker containers
// per game session (launcher: docker), e.g. the Unity gameserver next to CASim.
package dockergame

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/internal/config"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/internal/credserver"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/internal/sessionenv"
	pkgConfig "github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/config"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/game"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/helpers"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/logging"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/observability"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/process"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/route53manager"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/sessiontemplate"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/types/events"
	"github.com/pkg/errors"
)

const (
	defaultNetwork            = "host"
	defaultStopTimeout        = 30 * time.Second
	defaultReadyCheckTimeout  = 120 * time.Second
	defaultReadyCheckInterval = 2 * time.Second
	// stopGrace is how long to wait for `docker run` to return after `docker stop` timed out.
	stopGrace = 15 * time.Second
	// sessionLabel marks containers started by the wrapper.
	sessionLabel = "com.playableworlds.gamelift.session"
)

// SessionLoggerFactory defines the interface for creating session-specific loggers.
type SessionLoggerFactory interface {
	New(ctx context.Context, name string, logDirectory string) (*logging.BufferedLogger, error)
}

// DockerGame runs the configured containers for a game session using the docker CLI.
type DockerGame struct {
	cfg                  config.Config
	logger               *slog.Logger
	sessionLoggerFactory SessionLoggerFactory
	spanner              observability.Spanner
	docker               string
	httpClient           *http.Client

	mu         sync.Mutex
	status     events.GameStatus
	containers []*container
	envDir     string
	stopping   bool
	stopCh     chan struct{}
	runDone    chan struct{}
	stopOnce   sync.Once
	closeOnce  sync.Once
}

type container struct {
	spec           config.Container
	stopTimeout    time.Duration
	proc           process.Process
	stdout, stderr *logging.BufferedLogger
	done           chan struct{}
	err            error
	result         *process.Result
}

// New creates a DockerGame. The docker binary is resolved from PATH.
func New(cfg config.Config, logger *slog.Logger, sessionLoggerFactory SessionLoggerFactory, spanner observability.Spanner) (*DockerGame, error) {
	if logger == nil {
		return nil, errors.New("docker game initialization failed: logger not provided")
	}
	if sessionLoggerFactory == nil {
		return nil, errors.New("docker game initialization failed: session logger factory not provided")
	}
	if spanner == nil {
		return nil, errors.New("docker game initialization failed: spanner not provided")
	}
	if len(cfg.BuildDetail.Containers) == 0 {
		return nil, errors.New("docker game initialization failed: no containers configured")
	}

	docker, err := exec.LookPath("docker")
	if err != nil {
		return nil, errors.Wrap(err, "docker game initialization failed: docker binary not found")
	}
	docker, err = filepath.Abs(docker)
	if err != nil {
		return nil, errors.Wrap(err, "docker game initialization failed: resolving docker path")
	}

	return &DockerGame{
		cfg:                  cfg,
		logger:               logger,
		sessionLoggerFactory: sessionLoggerFactory,
		spanner:              spanner,
		docker:               docker,
		httpClient:           &http.Client{Timeout: 5 * time.Second},
		stopCh:               make(chan struct{}),
	}, nil
}

// Init checks that every configured image is present locally, so a broken build fails
// before the process reports ready to GameLift rather than at session start.
func (dockerGame *DockerGame) Init(ctx context.Context, args *game.InitArgs) (*game.InitMeta, error) {
	dockerGame.logger.DebugContext(ctx, "Starting docker game initialization", "args", args)

	for _, spec := range dockerGame.cfg.BuildDetail.Containers {
		out, err := exec.CommandContext(ctx, dockerGame.docker, "image", "inspect", "--format", "{{.Id}}", spec.Image).CombinedOutput()
		if err != nil {
			return nil, errors.Wrapf(err, "docker image %q for container %q is not available: %s", spec.Image, spec.Name, strings.TrimSpace(string(out)))
		}
		dockerGame.logger.InfoContext(ctx, "Docker image available", "container", spec.Name, "image", spec.Image, "id", strings.TrimSpace(string(out)))
	}

	dockerGame.setStatus(events.GameStatusWaiting)
	dockerGame.logger.InfoContext(ctx, "Docker game initialized", "containers", len(dockerGame.cfg.BuildDetail.Containers))

	return &game.InitMeta{}, nil
}

// HealthCheck returns the current status of the game session.
func (dockerGame *DockerGame) HealthCheck(_ context.Context) events.GameStatus {
	dockerGame.mu.Lock()
	defer dockerGame.mu.Unlock()

	return dockerGame.status
}

// Run starts the containers in order and blocks until one of them exits, the context is
// cancelled or Stop is called. All containers are stopped before Run returns.
func (dockerGame *DockerGame) Run(ctx context.Context, startArgs *game.StartArgs) error {
	ctx, span, _ := dockerGame.spanner.NewSpan(ctx, "run", nil)
	defer span.End()

	if startArgs == nil || startArgs.HostingStart == nil {
		return errors.New("docker game run failed: no start arguments")
	}

	dockerGame.mu.Lock()
	if dockerGame.stopping {
		dockerGame.mu.Unlock()
		return errors.New("docker game run failed: game is stopping")
	}
	dockerGame.runDone = make(chan struct{})
	dockerGame.mu.Unlock()
	defer close(dockerGame.runDone)

	if dockerGame.cfg.Route53.DoMapping {
		requestHandler := helpers.NewHttpRequestHandler(http.DefaultClient, dockerGame.logger)
		err := route53manager.SetupRoute53Mappings(ctx, dockerGame.logger, startArgs.GameSessionName, &dockerGame.cfg, startArgs.AwsCredentials, requestHandler)
		if err != nil {
			dockerGame.logger.ErrorContext(ctx, "Could not set up Route 53 mapping", "err", err)
		}
	}

	if dockerGame.cfg.Ports.GamePort == 0 {
		return errors.New("game server initialization failed: invalid game port: 0")
	}

	var credSrv *credserver.Server
	if startArgs.CredentialsFetcher != nil {
		var err error
		credSrv, err = credserver.New(dockerGame.logger, startArgs.CredentialsFetcher)
		if err != nil {
			return fmt.Errorf("failed to create credential server: %w", err)
		}
		if err := credSrv.Start(ctx); err != nil {
			return fmt.Errorf("failed to start credential server: %w", err)
		}
		defer credSrv.Stop(context.WithoutCancel(ctx))
	}

	envDir, err := os.MkdirTemp("", "gamelift-wrapper-env-")
	if err != nil {
		return fmt.Errorf("failed to create env file directory: %w", err)
	}
	dockerGame.mu.Lock()
	dockerGame.envDir = envDir
	dockerGame.mu.Unlock()

	// The docker CLI processes must outlive ctx: cancelling ctx only kills the CLI client,
	// which would orphan the containers. They are stopped explicitly with `docker stop`.
	procCtx, procCancel := context.WithCancel(context.WithoutCancel(ctx))
	defer procCancel()

	dockerGame.setStatus(events.GameStatusRunning)
	exited := make(chan *container, len(dockerGame.cfg.BuildDetail.Containers))

	startErr := dockerGame.startAll(ctx, procCtx, startArgs, credSrv, exited)

	var first *container
	if startErr == nil {
		dockerGame.logger.InfoContext(ctx, "All containers started, waiting for the session to end")
		select {
		case first = <-exited:
			dockerGame.logger.InfoContext(ctx, "Container exited, ending game session", "container", first.spec.Name, "result", first.result, "error", first.err)
		case <-ctx.Done():
			dockerGame.logger.InfoContext(ctx, "Run context done, stopping containers", "reason", ctx.Err())
		case <-dockerGame.stopCh:
			dockerGame.logger.InfoContext(ctx, "Stop requested, stopping containers")
		}
	}

	dockerGame.stopContainers(context.WithoutCancel(ctx))
	dockerGame.waitContainers(ctx)

	dockerGame.mu.Lock()
	stopping := dockerGame.stopping
	dockerGame.mu.Unlock()

	switch {
	case startErr != nil:
		dockerGame.setStatus(events.GameStatusErrored)
		return startErr
	case first != nil && !stopping && first.err != nil:
		dockerGame.setStatus(events.GameStatusErrored)
		return fmt.Errorf("container %s failed: %w", first.spec.Name, first.err)
	default:
		dockerGame.setStatus(events.GameStatusFinished)
		return nil
	}
}

func (dockerGame *DockerGame) startAll(ctx context.Context, procCtx context.Context, startArgs *game.StartArgs, credSrv *credserver.Server, exited chan<- *container) error {
	for _, spec := range dockerGame.cfg.BuildDetail.Containers {
		select {
		case <-dockerGame.stopCh:
			return errors.New("stop requested while starting containers")
		case <-ctx.Done():
			return errors.Wrap(ctx.Err(), "context done while starting containers")
		default:
		}

		c, err := dockerGame.startContainer(ctx, procCtx, spec, startArgs, credSrv, exited)
		if err != nil {
			return errors.Wrapf(err, "failed to start container %s", spec.Name)
		}

		if spec.ReadyCheck != nil {
			if err := dockerGame.waitReady(ctx, c); err != nil {
				return errors.Wrapf(err, "container %s did not become ready", spec.Name)
			}
		}
	}

	return nil
}

func (dockerGame *DockerGame) startContainer(ctx context.Context, procCtx context.Context, spec config.Container, startArgs *game.StartArgs, credSrv *credserver.Server, exited chan<- *container) (*container, error) {
	opts := sessionenv.Options{InjectAwsCredentials: spec.InjectAwsCredentials}
	if spec.UseCredentialServer {
		if credSrv == nil {
			dockerGame.logger.WarnContext(ctx, "use-credential-server is set but the credential server is not running (is local-credential-server enabled?)", "container", spec.Name)
		}
		opts.CredentialServer = credSrv
	}
	if spec.InjectAwsCredentials && startArgs.AwsCredentials == nil {
		dockerGame.logger.WarnContext(ctx, "inject-aws-credentials is set but no fleet-role credentials are available (is use-fleet-role-credentials enabled?)", "container", spec.Name)
	}

	env, err := sessionenv.Build(startArgs, spec.EnvVars, opts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build environment")
	}

	envFile, err := dockerGame.writeEnvFile(spec.Name, env)
	if err != nil {
		return nil, err
	}

	containerArgs, err := renderArgs(spec.Args, startArgs)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render container args")
	}

	// Remove a leftover container with the same name, e.g. from a crashed wrapper.
	if out, err := exec.CommandContext(ctx, dockerGame.docker, "rm", "-f", spec.Name).CombinedOutput(); err != nil {
		dockerGame.logger.DebugContext(ctx, "docker rm of previous container failed", "container", spec.Name, "output", strings.TrimSpace(string(out)), "error", err)
	}

	stdout, err := dockerGame.sessionLoggerFactory.New(ctx, spec.Name+"-stdout.log", startArgs.LogDirectory)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create stdout log")
	}
	stderr, err := dockerGame.sessionLoggerFactory.New(ctx, spec.Name+"-stderr.log", startArgs.LogDirectory)
	if err != nil {
		_ = stdout.Close()
		return nil, errors.Wrap(err, "failed to create stderr log")
	}

	stopTimeout := defaultStopTimeout
	if spec.StopTimeout != "" {
		if d, err := time.ParseDuration(spec.StopTimeout); err == nil {
			stopTimeout = d
		}
	}

	c := &container{
		spec:        spec,
		stopTimeout: stopTimeout,
		stdout:      stdout,
		stderr:      stderr,
		done:        make(chan struct{}),
		proc: process.New(&process.Config{
			ExeName: dockerGame.docker,
		}, dockerGame.logger),
	}
	if err := c.proc.Init(ctx); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, errors.Wrap(err, "failed to initialize docker process")
	}

	runArgs := dockerRunArgs(spec, envFile, startArgs.GameSessionName, containerArgs)

	dockerGame.mu.Lock()
	dockerGame.containers = append(dockerGame.containers, c)
	dockerGame.mu.Unlock()

	dockerGame.logger.InfoContext(ctx, "Starting container", "container", spec.Name, "image", spec.Image, "envVars", len(env))
	go func() {
		defer close(c.done)
		c.result, c.err = c.proc.Run(procCtx, &process.Args{
			CliArgs: runArgs,
			Stdout:  io.MultiWriter(os.Stdout, stdout),
			Stderr:  io.MultiWriter(os.Stderr, stderr),
		}, nil)
		dockerGame.logger.InfoContext(ctx, "Container finished", "container", spec.Name, "result", c.result, "error", c.err)
		exited <- c
	}()

	return c, nil
}

// dockerRunArgs builds the `docker run` arguments for a container.
func dockerRunArgs(spec config.Container, envFile string, sessionName string, containerArgs []string) []string {
	network := spec.Network
	if network == "" {
		network = defaultNetwork
	}

	args := []string{
		"run",
		"--rm",
		"--name", spec.Name,
		"--network", network,
		"--env-file", envFile,
		"--label", sessionLabel + "=" + sessionName,
	}
	args = append(args, spec.DockerRunArgs...)
	args = append(args, spec.Image)
	args = append(args, containerArgs...)

	return args
}

// renderArgs orders args by position and renders their values with the session template.
func renderArgs(cliArgs []pkgConfig.CliArg, startArgs *game.StartArgs) ([]string, error) {
	sorted := slices.Clone(cliArgs)
	slices.SortStableFunc(sorted, func(a, b pkgConfig.CliArg) int {
		return cmp.Compare(a.Position, b.Position)
	})

	rendered := make([]string, 0, len(sorted)*2)
	for _, arg := range sorted {
		if arg.Name != "" {
			rendered = append(rendered, arg.Name)
		}
		if arg.Value != "" {
			value, err := sessiontemplate.Execute(arg.Name, arg.Value, startArgs)
			if err != nil {
				return nil, err
			}
			rendered = append(rendered, value)
		}
	}

	return rendered, nil
}

// writeEnvFile writes env in docker --env-file format to a 0600 file. Values are taken
// literally by docker, so only newlines need rejecting.
func (dockerGame *DockerGame) writeEnvFile(name string, env map[string]string) (string, error) {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	var b strings.Builder
	for _, k := range keys {
		v := env[k]
		if strings.ContainsAny(k, "=\n\r") || k == "" {
			return "", errors.Errorf("invalid environment variable name %q", k)
		}
		if strings.ContainsAny(v, "\n\r") {
			return "", errors.Errorf("environment variable %s contains a newline, which docker env files do not support", k)
		}
		// Match the process launcher, which upper-cases variable names.
		fmt.Fprintf(&b, "%s=%s\n", strings.ToUpper(k), v)
	}

	path := filepath.Join(dockerGame.envDir, name+".env")
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		return "", errors.Wrapf(err, "failed to write env file for container %s", name)
	}

	return path, nil
}

func (dockerGame *DockerGame) waitReady(ctx context.Context, c *container) error {
	timeout := defaultReadyCheckTimeout
	if c.spec.ReadyCheck.Timeout != "" {
		if d, err := time.ParseDuration(c.spec.ReadyCheck.Timeout); err == nil {
			timeout = d
		}
	}
	interval := defaultReadyCheckInterval
	if c.spec.ReadyCheck.Interval != "" {
		if d, err := time.ParseDuration(c.spec.ReadyCheck.Interval); err == nil && d > 0 {
			interval = d
		}
	}

	dockerGame.logger.InfoContext(ctx, "Waiting for container to become ready", "container", c.spec.Name, "url", c.spec.ReadyCheck.Url, "timeout", timeout)

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.spec.ReadyCheck.Url, nil)
		if err != nil {
			return errors.Wrap(err, "invalid ready-check url")
		}
		resp, err := dockerGame.httpClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				dockerGame.logger.InfoContext(ctx, "Container is ready", "container", c.spec.Name)
				return nil
			}
			err = errors.Errorf("status %d", resp.StatusCode)
		}
		dockerGame.logger.DebugContext(ctx, "Container not ready yet", "container", c.spec.Name, "error", err)

		select {
		case <-ticker.C:
		case <-c.done:
			return errors.New("container exited before becoming ready")
		case <-deadline.C:
			return errors.Errorf("timed out after %s", timeout)
		case <-dockerGame.stopCh:
			return errors.New("stop requested")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// stopContainers runs `docker stop` on every started container in reverse start order.
// It is safe to call more than once; only the first call does anything.
func (dockerGame *DockerGame) stopContainers(ctx context.Context) {
	dockerGame.stopOnce.Do(func() {
		dockerGame.mu.Lock()
		containers := slices.Clone(dockerGame.containers)
		dockerGame.mu.Unlock()

		for i := len(containers) - 1; i >= 0; i-- {
			c := containers[i]
			select {
			case <-c.done:
				dockerGame.logger.DebugContext(ctx, "Container already exited", "container", c.spec.Name)
				continue
			default:
			}

			seconds := strconv.Itoa(int(c.stopTimeout.Round(time.Second) / time.Second))
			stopCtx, cancel := context.WithTimeout(ctx, c.stopTimeout+stopGrace)
			dockerGame.logger.InfoContext(ctx, "Stopping container", "container", c.spec.Name, "timeout", seconds)
			out, err := exec.CommandContext(stopCtx, dockerGame.docker, "stop", "-t", seconds, c.spec.Name).CombinedOutput()
			cancel()
			if err != nil {
				dockerGame.logger.WarnContext(ctx, "docker stop failed", "container", c.spec.Name, "output", strings.TrimSpace(string(out)), "error", err)
			}
		}
	})
}

// waitContainers waits for every `docker run` client to return after stopContainers.
func (dockerGame *DockerGame) waitContainers(ctx context.Context) {
	dockerGame.mu.Lock()
	containers := slices.Clone(dockerGame.containers)
	dockerGame.mu.Unlock()

	for _, c := range containers {
		select {
		case <-c.done:
		case <-time.After(stopGrace):
			dockerGame.logger.WarnContext(ctx, "docker run did not return after stop", "container", c.spec.Name)
		}
	}
}

// Stop gracefully stops all containers (reverse start order) and cleans up.
func (dockerGame *DockerGame) Stop(ctx context.Context) error {
	dockerGame.logger.InfoContext(ctx, "Initiating docker game shutdown")

	dockerGame.mu.Lock()
	if !dockerGame.stopping {
		dockerGame.stopping = true
		close(dockerGame.stopCh)
	}
	runDone := dockerGame.runDone
	dockerGame.mu.Unlock()

	dockerGame.stopContainers(context.WithoutCancel(ctx))

	if runDone != nil {
		select {
		case <-runDone:
		case <-time.After(stopGrace):
			dockerGame.logger.WarnContext(ctx, "Run did not return after stopping containers")
		}
	}

	dockerGame.closeOnce.Do(func() {
		dockerGame.mu.Lock()
		containers := slices.Clone(dockerGame.containers)
		envDir := dockerGame.envDir
		dockerGame.mu.Unlock()

		for _, c := range containers {
			for _, l := range []*logging.BufferedLogger{c.stdout, c.stderr} {
				if err := l.Close(); err != nil {
					dockerGame.logger.ErrorContext(ctx, "failed to close container log file", "container", c.spec.Name, "err", err)
				}
			}
		}

		if envDir != "" {
			if err := os.RemoveAll(envDir); err != nil {
				dockerGame.logger.ErrorContext(ctx, "failed to remove env file directory", "dir", envDir, "err", err)
			}
		}
	})

	dockerGame.logger.InfoContext(ctx, "Docker game shutdown completed")

	return nil
}

func (dockerGame *DockerGame) setStatus(status events.GameStatus) {
	dockerGame.mu.Lock()
	defer dockerGame.mu.Unlock()

	dockerGame.status = status
}
