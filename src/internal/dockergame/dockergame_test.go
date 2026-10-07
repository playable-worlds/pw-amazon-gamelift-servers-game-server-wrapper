/*
 * Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

package dockergame

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/internal/config"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/internal/mocks"
	pkgConfig "github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/config"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/game"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/logging"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/types/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type sessionLoggerFactory struct {
	logger *slog.Logger
}

func (f *sessionLoggerFactory) New(ctx context.Context, name string, logDirectory string) (*logging.BufferedLogger, error) {
	return logging.NewBufferedLogger(ctx, f.logger, name, logDirectory)
}

type fixture struct {
	game     *DockerGame
	state    string
	logDir   string
	logs     *syncBuffer
	runErr   chan error
	startArg *game.StartArgs
}

func newFixture(t *testing.T, containers ...config.Container) *fixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake docker CLI is a shell script")
	}

	testdata, err := filepath.Abs("testdata")
	require.NoError(t, err)
	t.Setenv("PATH", testdata+string(os.PathListSeparator)+os.Getenv("PATH"))

	state := t.TempDir()
	t.Setenv("FAKE_DOCKER_STATE", state)

	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := config.Config{
		Ports:       config.Ports{GamePort: 7777},
		BuildDetail: config.BuildDetail{Launcher: config.LauncherDocker, Containers: containers},
	}
	dockerGame, err := New(cfg, logger, &sessionLoggerFactory{logger: logger}, &mocks.SpannerMock{})
	require.NoError(t, err)

	logDir := t.TempDir()
	return &fixture{
		game:   dockerGame,
		state:  state,
		logDir: logDir,
		logs:   logs,
		runErr: make(chan error, 1),
		startArg: &game.StartArgs{HostingStart: &events.HostingStart{
			GameSessionName: "zone-1234",
			GamePort:        7777,
			LogDirectory:    logDir,
			GameProperties:  `{"zoneName":"Acorn"}`,
			AwsCredentials:  &events.AwsCredentials{AccessKeyId: "AKIA", SecretAccessKey: "secret", SessionToken: "token"},
		}},
	}
}

func (f *fixture) run(t *testing.T) {
	t.Helper()
	_, err := f.game.Init(context.Background(), &game.InitArgs{})
	require.NoError(t, err)
	go func() {
		f.runErr <- f.game.Run(context.Background(), f.startArg)
	}()
}

func (f *fixture) calls() []string {
	b, _ := os.ReadFile(filepath.Join(f.state, "calls.log"))
	var calls []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		// "run <name> ...", "stop <name> ...", "rm <name>"
		calls = append(calls, fields[0]+" "+fields[1])
	}
	return calls
}

func (f *fixture) waitForCall(t *testing.T, call string) {
	t.Helper()
	assert.Eventually(t, func() bool {
		for _, c := range f.calls() {
			if c == call {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "waiting for %q, calls: %v", call, f.calls())
}

func (f *fixture) waitRun(t *testing.T) error {
	t.Helper()
	select {
	case err := <-f.runErr:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("Run did not return, calls: %v", f.calls())
		return nil
	}
}

func casim(image string) config.Container {
	return config.Container{
		Name:                "casim",
		Image:               image,
		UseCredentialServer: true,
		EnvVars:             []pkgConfig.EnvVar{{Name: "S3_ZONE_ID", Value: "{{.GameSessionName}}"}},
		DockerRunArgs:       []string{"--entrypoint", "/usr/local/bin/casim"},
		StopTimeout:         "5s",
	}
}

func gameserver(image string) config.Container {
	return config.Container{
		Name:                 "gameserver",
		Image:                image,
		InjectAwsCredentials: true,
		EnvVars: []pkgConfig.EnvVar{
			{Name: "CASIM_URL", Value: "http://{{.GameSessionName}}.worlds.example.com:50042"},
			{Name: "zone_name", Value: `{{.GameProperty "zoneName"}}`},
		},
		Args: []pkgConfig.CliArg{
			{Name: "-nographics", Position: 2},
			{Name: "-batchmode", Position: 1},
			{Name: "-logFile", Value: "-", Position: 3},
		},
	}
}

func Test_Run_StartsInOrder_And_Stop_Stops_In_Reverse_Order(t *testing.T) {
	f := newFixture(t, casim("img-sleep"), gameserver("img-sleep"))
	f.run(t)
	f.waitForCall(t, "run gameserver")
	assert.Equal(t, events.GameStatusRunning, f.game.HealthCheck(context.Background()))

	// env files are private while the session runs
	for _, name := range []string{"casim", "gameserver"} {
		fi, err := os.Stat(filepath.Join(f.game.envDir, name+".env"))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), fi.Mode().Perm())
	}

	require.NoError(t, f.game.Stop(context.Background()))
	require.NoError(t, f.waitRun(t))

	assert.Equal(t, []string{"rm casim", "run casim", "rm gameserver", "run gameserver", "stop gameserver", "stop casim"}, f.calls())
	assert.Equal(t, events.GameStatusFinished, f.game.HealthCheck(context.Background()))

	_, err := os.Stat(f.game.envDir)
	assert.True(t, os.IsNotExist(err), "env dir should be removed on stop")

	stdout, err := os.ReadFile(filepath.Join(f.logDir, "gameserver-stdout.log"))
	require.NoError(t, err)
	assert.Contains(t, string(stdout), "hello from gameserver")
}

func Test_Run_Passes_Docker_Run_Args(t *testing.T) {
	f := newFixture(t, casim("img-sleep"), gameserver("img-sleep"))
	f.run(t)
	f.waitForCall(t, "run gameserver")
	require.NoError(t, f.game.Stop(context.Background()))
	require.NoError(t, f.waitRun(t))

	b, err := os.ReadFile(filepath.Join(f.state, "calls.log"))
	require.NoError(t, err)
	calls := string(b)
	// the fake CLI logs the arguments after the "run" subcommand
	assert.Contains(t, calls, "run casim --rm --name casim --network host --env-file ")
	assert.Contains(t, calls, "--label com.playableworlds.gamelift.session=zone-1234 --entrypoint /usr/local/bin/casim img-sleep\n")
	assert.Contains(t, calls, "img-sleep -batchmode -nographics -logFile -\n")
	assert.Contains(t, calls, "stop gameserver -t 30 gameserver")
	assert.Contains(t, calls, "stop casim -t 5 casim")
}

func Test_Run_Writes_Env_Files(t *testing.T) {
	f := newFixture(t, casim("img-sleep"), gameserver("img-sleep"))
	f.run(t)
	f.waitForCall(t, "run gameserver")
	require.NoError(t, f.game.Stop(context.Background()))
	require.NoError(t, f.waitRun(t))

	gs, err := os.ReadFile(filepath.Join(f.state, "gameserver.env"))
	require.NoError(t, err)
	assert.Contains(t, string(gs), "CASIM_URL=http://zone-1234.worlds.example.com:50042\n")
	assert.Contains(t, string(gs), "ZONE_NAME=Acorn\n")
	assert.Contains(t, string(gs), "ZONENAME=Acorn\n")
	assert.Contains(t, string(gs), "AWS_ACCESS_KEY_ID=AKIA\n")
	assert.NotContains(t, string(gs), "PW_CRED_SERVER")

	cs, err := os.ReadFile(filepath.Join(f.state, "casim.env"))
	require.NoError(t, err)
	assert.Contains(t, string(cs), "S3_ZONE_ID=zone-1234\n")
	assert.NotContains(t, string(cs), "AWS_ACCESS_KEY_ID")
}

func Test_Run_Container_Exit_Ends_Session_And_Stops_Others(t *testing.T) {
	f := newFixture(t, casim("img-sleep"), gameserver("img-exit3"))
	f.run(t)

	err := f.waitRun(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "container gameserver failed")
	assert.Equal(t, events.GameStatusErrored, f.game.HealthCheck(context.Background()))
	assert.Contains(t, f.calls(), "stop casim")
	assert.NotContains(t, f.calls(), "stop gameserver")

	require.NoError(t, f.game.Stop(context.Background()))
}

func Test_Run_Clean_Exit_Finishes_Session(t *testing.T) {
	f := newFixture(t, casim("img-sleep"), gameserver("img-exit0"))
	f.run(t)

	require.NoError(t, f.waitRun(t))
	assert.Equal(t, events.GameStatusFinished, f.game.HealthCheck(context.Background()))
	assert.Contains(t, f.calls(), "stop casim")
}

func Test_Run_Ready_Check_Gates_Next_Container(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := casim("img-sleep")
	c.ReadyCheck = &config.ReadyCheck{Url: server.URL + "/ping", Interval: "20ms", Timeout: "5s"}
	f := newFixture(t, c, gameserver("img-sleep"))
	f.run(t)

	f.waitForCall(t, "run gameserver")
	assert.GreaterOrEqual(t, hits.Load(), int32(3), "gameserver must not start before casim is ready")

	require.NoError(t, f.game.Stop(context.Background()))
	require.NoError(t, f.waitRun(t))
}

func Test_Run_Ready_Check_Timeout_Fails_Session(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	c := casim("img-sleep")
	c.ReadyCheck = &config.ReadyCheck{Url: server.URL, Interval: "20ms", Timeout: "200ms"}
	f := newFixture(t, c, gameserver("img-sleep"))
	f.run(t)

	err := f.waitRun(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "casim did not become ready")
	assert.NotContains(t, f.calls(), "run gameserver")
	assert.Contains(t, f.calls(), "stop casim")
	assert.Equal(t, events.GameStatusErrored, f.game.HealthCheck(context.Background()))

	require.NoError(t, f.game.Stop(context.Background()))
}

func Test_Run_Context_Cancel_Stops_Containers(t *testing.T) {
	f := newFixture(t, casim("img-sleep"), gameserver("img-sleep"))
	_, err := f.game.Init(context.Background(), &game.InitArgs{})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { f.runErr <- f.game.Run(ctx, f.startArg) }()
	f.waitForCall(t, "run gameserver")
	cancel()

	require.NoError(t, f.waitRun(t))
	calls := f.calls()
	assert.Equal(t, []string{"stop gameserver", "stop casim"}, calls[len(calls)-2:])
	require.NoError(t, f.game.Stop(context.Background()))
}

func Test_Init_Fails_When_Image_Missing(t *testing.T) {
	f := newFixture(t, casim("img-sleep"), gameserver("img-missing"))

	_, err := f.game.Init(context.Background(), &game.InitArgs{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `docker image "img-missing" for container "gameserver" is not available`)
}

func Test_Stop_Before_Run(t *testing.T) {
	f := newFixture(t, casim("img-sleep"))

	require.NoError(t, f.game.Stop(context.Background()))
	err := f.game.Run(context.Background(), f.startArg)

	require.Error(t, err)
	assert.Empty(t, f.calls())
}

func Test_writeEnvFile_Rejects_Newlines(t *testing.T) {
	dockerGame := &DockerGame{envDir: t.TempDir()}

	_, err := dockerGame.writeEnvFile("casim", map[string]string{"KEY": "a\nb"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "KEY contains a newline")
}
