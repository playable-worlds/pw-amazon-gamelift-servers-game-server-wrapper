/*
 * Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

package process

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/errors"
)

// Result represents the outcome of a process execution.
type Result struct {
	ReturnCode int
	Signal     os.Signal
}

// State represents the current state of a process.
type State struct {
	Exited bool
	Pid    int
}

// Args contains the arguments and I/O configuration for process execution.
type Args struct {
	CliArgs []string
	Stdout  io.Writer
	Stderr  io.Writer
}

// Process defines the interface for managing the process lifecycle.
type Process interface {
	Init(ctx context.Context) error
	Run(ctx context.Context, args *Args, pidChan chan<- int) (*Result, error)
	State() *State
}

type process struct {
	cfg     *Config
	exePath string
	cmd     *exec.Cmd
	logger  *slog.Logger
}

func (process *process) State() *State {
	state := &State{}

	if process.cmd == nil {
		return state
	}

	if process.cmd.ProcessState != nil {
		state.Exited = process.cmd.ProcessState.Exited()
	}

	if process.cmd.Process != nil {
		state.Pid = process.cmd.Process.Pid
	}

	return state
}

func (process *process) Init(ctx context.Context) error {
	if process.cfg == nil {
		return errors.New("Process configuration is nil")
	}

	if !filepath.IsAbs(process.cfg.ExeName) {
		process.exePath = filepath.Join(process.cfg.WorkingDirectory, process.cfg.ExeName)
	} else {
		process.exePath = process.cfg.ExeName
	}

	fi, err := os.Stat(process.exePath)
	if err != nil {
		return errors.Wrapf(err, "Failed to access executable '%s'", process.exePath)
	}

	return ensureExecutable(fi, process.exePath)
}

func (process *process) Run(ctx context.Context, args *Args, pidChan chan<- int) (*Result, error) {
	res := &Result{
		ReturnCode: -1,
	}

	process.logger.DebugContext(ctx, "Preparing command", "path", process.exePath, "workingDir", process.cfg.WorkingDirectory)

	process.cmd = exec.CommandContext(ctx, process.exePath, args.CliArgs...)
	process.cmd.Dir = process.cfg.WorkingDirectory

	stdoutPipe, err := process.cmd.StdoutPipe()
	if err != nil {
		return res, errors.Wrap(err, "Failed to create stdout pipe")
	}
	stderrPipe, err := process.cmd.StderrPipe()
	if err != nil {
		return res, errors.Wrap(err, "Failed to create stderr pipe")
	}

	if process.cfg.EnvVars != nil {
		// Preserve parent environment and overlay configured variables
		base := os.Environ()
		envMap := make(map[string]string, len(base)+len(process.cfg.EnvVars))
		for _, kv := range base {
			for i := 0; i < len(kv); i++ {
				if kv[i] == '=' {
					envMap[kv[:i]] = kv[i+1:]
					break
				}
			}
		}
		for k, v := range process.cfg.EnvVars {
			envMap[k] = v
		}
		env := make([]string, 0, len(envMap))
		for k, v := range envMap {
			env = append(env, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}
		process.cmd.Env = env

		awsVars := []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION"}
		awsEnvPresent := make(map[string]bool, len(awsVars))
		for _, k := range awsVars {
			_, awsEnvPresent[k] = envMap[k]
		}
		process.logger.InfoContext(ctx, "AWS env vars in child process environment", "present", awsEnvPresent)
	}

	process.logger.InfoContext(ctx, "Starting process", "path", process.exePath, "args", args)
	if process.cfg.DelayStart != "" {
		process.logger.InfoContext(ctx, "DelayStart requested", "delay", process.cfg.DelayStart)
		d, err := time.ParseDuration(process.cfg.DelayStart)
		if err != nil {
			process.logger.WarnContext(ctx, "Unable to parse duration, defaulting to 10s")
			time.Sleep(time.Duration(10) * time.Second)
		} else {
			time.Sleep(d)
		}
	}
	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(args.Stdout, stdoutPipe)
	}()
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(args.Stderr, stderrPipe)
	}()

	err = process.cmd.Start()
	if err != nil {
		return res, err
	}

	process.logger.InfoContext(ctx, "Process started", "pid", process.cmd.Process.Pid)
	if pidChan != nil {
		go func() {
			pidChan <- process.cmd.Process.Pid
		}()
	}

	// Wait only for the direct child process. cmd.Wait() also blocks until
	// stdout/stderr pipes are closed, which can hang forever when a grandchild
	// inherits those FDs and outlives the direct child.
	state, err := process.cmd.Process.Wait()
	process.cmd.ProcessState = state

	_ = stdoutPipe.Close()
	_ = stderrPipe.Close()
	go copyWG.Wait()

	var ee *exec.ExitError
	if errors.As(err, &ee) {
		ws := ee.Sys().(syscall.WaitStatus)
		res.Signal = ws.Signal()
		// it's been killed by either the context or by an external pid termination command
		if ws.Signal() == syscall.SIGKILL {
			process.logger.DebugContext(ctx, "Process terminated by signal",
				"signal", "SIGKILL")
			err = nil
		}
	}

	if state != nil {
		res.ReturnCode = state.ExitCode()
	}
	process.logger.InfoContext(ctx, "Process finished", "pid", process.cmd.Process.Pid, "exitCode", res.ReturnCode, "err", err)

	return res, err
}

// Config contains the configuration for a process.
type Config struct {
	ExeName          string
	WorkingDirectory string
	EnvVars          map[string]string
	DelayStart       string
}

// New creates a new Process instance with the provided configuration and logger.
//
// Parameters:
//   - cfg: Process configuration
//   - logger: Logger for process operations
//
// Returns:
//   - Process: Configured process instance
func New(cfg *Config, logger *slog.Logger) Process {
	process := &process{
		cfg:    cfg,
		logger: logger,
	}

	return process
}
