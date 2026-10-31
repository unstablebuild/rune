// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package extensionv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/auth"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"google.golang.org/grpc"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/workspace/processctx"
)

type recordingExecutor struct {
	mu   sync.Mutex
	ctx  context.Context
	cmd  workspaceapi.Cmd
	pids []workspaceapi.Pid
}

var _ schemeapi.Executor = (*recordingExecutor)(nil)

func (r *recordingExecutor) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctx = ctx
	r.cmd = cmd
	pid := workspaceapi.Pid(len(r.pids) + 1)
	r.pids = append(r.pids, pid)
	return pid, nil
}

func (r *recordingExecutor) snapshotCmd() workspaceapi.Cmd {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cmd
}

// waitCmd blocks until StartCommand has recorded a command with a usable
// Stdout, then returns it. Tests that drive the protocol from a goroutine
// must not assume the goroutine has reached StartCommand by some fixed
// deadline; under load the spawned startExtension may not be scheduled in
// time, leaving r.cmd zero-valued and r.cmd.Stdout nil.
func (r *recordingExecutor) waitCmd(t *testing.T) workspaceapi.Cmd {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if cmd := r.snapshotCmd(); cmd.Stdout != nil {
			return cmd
		}
		if time.Now().After(deadline) {
			t.Fatal("StartCommand was not called before deadline")
		}
		time.Sleep(time.Millisecond)
	}
}

// protocolDrivingExecutor is a recordingExecutor that, upon StartCommand,
// writes a valid extension metadata document to the command's stdout so the
// workspace runner protocol handshake completes immediately. Tests that
// exercise startExtension without manually driving the protocol use this to
// avoid blocking on the readiness channel.
type protocolDrivingExecutor struct {
	recordingExecutor
	extensionID string
}

var _ schemeapi.Executor = (*protocolDrivingExecutor)(nil)

func (p *protocolDrivingExecutor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	pid, err := p.recordingExecutor.StartCommand(ctx, cmd)
	if err != nil {
		return pid, err
	}
	meta := extensionapi.Metadata{
		DeveloperID:    "dev-id",
		DeveloperEmail: "dev@example.com",
		DeveloperKey:   "dev-key",
		ExtensionID:    p.extensionID,
		ExtensionName:  "Test Extension",
		Permissions:    extensionapi.AllPermissions(),
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return pid, err
	}
	if cmd.Stdout != nil {
		go func() {
			_, _ = cmd.Stdout.Write(encoded)
		}()
	}
	return pid, nil
}

func (*protocolDrivingExecutor) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (r *recordingExecutor) Signal(workspaceapi.Pid, syscall.Signal) error {
	return nil
}

func (*recordingExecutor) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (r *recordingExecutor) Close() error {
	return nil
}

type startErrorExecutor struct {
	err error
}

func (s startErrorExecutor) StartCommand(context.Context, workspaceapi.Cmd) (workspaceapi.Pid, error) {
	return 0, s.err
}

func (s startErrorExecutor) Signal(workspaceapi.Pid, syscall.Signal) error {
	return nil
}

func (startErrorExecutor) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (s startErrorExecutor) Close() error {
	return nil
}

// exitingExecutor starts processes that exit with err before StartCommand
// returns, which it only does once wait does.
type exitingExecutor struct {
	recordingExecutor
	err  error
	wait func()
}

func (e *exitingExecutor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	pid, err := e.recordingExecutor.StartCommand(ctx, cmd)
	if err != nil {
		return pid, err
	}
	cmd.Watcher.WatchProcess() <- e.err
	e.wait()
	return pid, nil
}

func (*exitingExecutor) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func extensionMetadata(t *testing.T, id string) []byte {
	t.Helper()
	meta, err := json.Marshal(extensionapi.Metadata{
		DeveloperID: "dev-id", DeveloperEmail: "dev@example.com", DeveloperKey: "dev-key",
		ExtensionID: id, ExtensionName: "Ext",
		Permissions: extensionapi.AllPermissions(),
	})
	require.NoError(t, err)
	return meta
}

// stressExecutor runs fake extension processes that, like real ones, are
// killed when their context is cancelled and take a while to die, and that
// at random exit at once, fail their handshake, or complete it and then
// crash or live until killed. A process names its pid in what it writes to
// stderr and exits with.
type stressExecutor struct {
	// meta is the valid metadata of each extension id.
	meta  map[string][]byte
	exits sync.WaitGroup

	mu   sync.Mutex
	pids workspaceapi.Pid
	// alive holds the extension id of each live process.
	alive map[workspaceapi.Pid]string
}

var _ schemeapi.Executor = (*stressExecutor)(nil)

func newStressExecutor(meta map[string][]byte) *stressExecutor {
	return &stressExecutor{meta: meta, alive: make(map[workspaceapi.Pid]string)}
}

func (e *stressExecutor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	id, _ := processctx.ExtensionIDFromContext(ctx)
	e.mu.Lock()
	e.pids++
	pid := e.pids
	e.alive[pid] = id
	e.mu.Unlock()

	e.exits.Add(1)
	go debug.CapturePanicReport(func() {
		defer e.exits.Done()
		err := fmt.Errorf("process %d killed", pid)
		var crash <-chan time.Time
		switch rand.IntN(4) {
		case 0:
			err = fmt.Errorf("process %d failed", pid)
		case 1:
			_, _ = cmd.Stdout.Write([]byte(`{"developer_id":"dev-id"}`))
			<-ctx.Done()
		case 2:
			crash = time.After(rand.N(2 * time.Millisecond))
			fallthrough
		default:
			_, _ = cmd.Stdout.Write(e.meta[id])
			select {
			case <-ctx.Done():
			case <-crash:
				err = fmt.Errorf("process %d crashed", pid)
			}
		}
		time.Sleep(rand.N(time.Millisecond))
		_, _ = fmt.Fprintf(cmd.Stderr, "process %d exiting\n", pid)
		e.mu.Lock()
		delete(e.alive, pid)
		e.mu.Unlock()
		cmd.Watcher.WatchProcess() <- err
	})
	return pid, nil
}

func (e *stressExecutor) alivePids() map[workspaceapi.Pid]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return maps.Clone(e.alive)
}

func (e *stressExecutor) started() workspaceapi.Pid {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pids
}

func (e *stressExecutor) Signal(workspaceapi.Pid, syscall.Signal) error {
	return nil
}

func (*stressExecutor) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (e *stressExecutor) Close() error {
	return nil
}

func TestWorkspaceRunnerStartCommandPreservesCallerEnv(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)

	uri, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)

	exec := &recordingExecutor{}
	runner := newWorkspaceRunner(
		exec,
		exec, // separate extExecutor not exercised here
		nil,  // grantor is not used by StartCommand
		nopTrustVerifier{},
		uri,
		"/tmp/ext.sock",
		"/tmp/ext-data",
		"/tmp/ext-install",
		[]byte("cert"),
		keys,
	)

	_, err = runner.StartCommand(context.Background(), workspaceapi.Cmd{
		Path: "/bin/zsh",
		Args: []string{"--login", "-i"},
		Env:  []string{"ZDOTDIR=/Applications/Rune.app/Contents/Resources/zdot", "FOO=bar"},
	})
	require.NoError(t, err)

	assert.Contains(t, exec.cmd.Env, "ZDOTDIR=/Applications/Rune.app/Contents/Resources/zdot")
	assert.Contains(t, exec.cmd.Env, "FOO=bar")
	assert.Contains(t, exec.cmd.Env, "RUNE_SOCKET=/tmp/ext.sock")
	assert.Contains(t, exec.cmd.Env, "RUNE_DATADIR=/tmp/ext-install",
		"ad-hoc commands run on the workspace host and see its data dir")
	assert.NotContains(t, exec.cmd.Env, "RUNE_DATADIR=/tmp/ext-data")
	assert.Contains(t, exec.cmd.Env, "RUNE_INSTALLDIR=/tmp/ext-install",
		"install dir is carried separately from the local data dir")
	// cmd.Dir is left untouched: the host-side fileScheme defaults
	// it to its own resolved workspace path when empty, and
	// pre-resolving here mishandled SSH URIs containing "~". See
	// TestWorkspaceRunnerStartCommandDoesNotDoubleResolveDir for
	// the regression that made us drop the override.
	assert.Empty(t, exec.cmd.Dir,
		"workspaceRunner must not synthesize cmd.Dir; that's the "+
			"host-side fileScheme's job")
	assert.Equal(t, "/bin/zsh", exec.cmd.Path)
	assert.Equal(t, []string{"--login", "-i"}, exec.cmd.Args)
}

func TestWorkspaceRunnerStartCommandPID(t *testing.T) {
	t.Parallel()
	keys, err := auth.GenerateKeys()
	require.NoError(t, err)

	for _, tt := range []struct {
		name     string
		uri      string
		insecure bool
		want     string
	}{
		{name: "local", uri: "file:///tmp", want: fmt.Sprint(os.Getpid())},
		{name: "local insecure", uri: "file:///tmp", insecure: true, want: fmt.Sprint(os.Getpid())},
		{name: "remote", uri: "ssh://user@host/tmp"},
		{name: "remote insecure", uri: "ssh://user@host/tmp", insecure: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			uri, err := workspaceapi.ParseURI(tt.uri)
			require.NoError(t, err)
			exec := &recordingExecutor{}
			runner := newWorkspaceRunner(exec, exec, nil, nopTrustVerifier{}, uri,
				"/tmp/ext.sock", "/tmp/ext-data", "/tmp/ext-install", []byte("cert"), keys)
			defer runner.Close()
			runner.cfg.insecureAuth = tt.insecure
			_, err = runner.StartCommand(context.Background(), workspaceapi.Cmd{
				Path: "/bin/ext",
				Env:  []string{"RUNE_PID=123"},
			})
			require.NoError(t, err)
			var pid string
			for _, env := range exec.snapshotCmd().Env {
				if value, ok := strings.CutPrefix(env, "RUNE_PID="); ok {
					pid = value
				}
			}
			assert.Equal(t, tt.want, pid, "generated environment must override the caller's PID")
		})
	}
}

func TestWorkspaceRunnerStartCommandDoesNotDoubleResolveDir(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)

	uri, err := workspaceapi.ParseURI("ssh://test@10.0.0.9/~/src/blue")
	require.NoError(t, err)

	exec := &recordingExecutor{}
	runner := newWorkspaceRunner(
		exec, exec, nil, nopTrustVerifier{}, uri,
		"/tmp/ext.sock", "/tmp/ext-data", "/tmp/ext-install", []byte("cert"), keys,
	)

	_, err = runner.StartCommand(context.Background(), workspaceapi.Cmd{
		// vte sends empty Path: the host-side fileScheme owns
		// resolution. We don't care what Path the executor sees
		// here; we care that cmd.Dir does not get a doubled,
		// non-existent path.
	})
	require.NoError(t, err)

	got := exec.snapshotCmd().Dir
	// The bug: ExpandPathWithURI returns "/~/src/blue/src/blue"
	// because uri.Path() is used both as HomeDir and cwdFn. Either
	// leave Dir empty (and let the host fileScheme default it) or
	// pass a non-doubled path; "/~/src/blue/src/blue" is never OK.
	assert.NotContains(t, got, "src/blue/src/blue",
		"workspaceRunner must not double-expand the workspace path "+
			"when computing cmd.Dir; the host-side fileScheme already "+
			"knows the resolved workspace path. Got Dir=%q", got)
}

func TestWorkspaceRunnerStartCommandMarksTokenPlugin(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	verifyKeys, err := keys.Verify(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, verifyKeys)

	uri, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)

	runner := newWorkspaceRunner(
		&recordingExecutor{},
		&recordingExecutor{}, // separate extExecutor not exercised here
		nil,                  // grantor is not used by StartCommand
		nopTrustVerifier{},
		uri,
		"/tmp/ext.sock",
		"/tmp/ext-data",
		"/tmp/ext-install",
		[]byte("cert"),
		keys,
	)

	env, err := runner.commandEnvs(context.Background(), "/bin/zsh",
		[]string{"--login", "-i"}, "/tmp/ext-install")
	require.NoError(t, err)

	var token string
	for _, e := range env {
		if value, ok := strings.CutPrefix(e, runner.cfg.authTokenEnv+"="); ok {
			token = value
			break
		}
	}
	require.NotEmpty(t, token)

	claims, err := auth.VerifyToken[ideauthorizer.Extension](verifyKeys[0], token)
	require.NoError(t, err)
	assert.True(t, claims.Extra.Plugin)
	assert.Equal(t, "/bin/zsh", claims.Extra.Path)
	assert.Equal(t, []string{"--login", "-i"}, claims.Extra.Args)
	assert.Equal(t, extensionapi.AllPermissions(), claims.Extra.Permissions)
}

func TestWorkspaceRunnerRunCarriesExtensionID(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)

	uri, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)

	exec := &recordingExecutor{}
	runner := newWorkspaceRunner(
		exec,
		exec, // separate extExecutor not exercised here
		nil,  // grantor is not used before process execution in this test
		nopTrustVerifier{},
		uri,
		"/tmp/ext.sock",
		"/tmp/ext-data",
		"/tmp/ext-install",
		[]byte("cert"),
		keys,
	)
	require.NoError(t, runner.Run("test-extension", "/bin/ext", config.NopConfig()))

	extensionID, ok := processctx.ExtensionIDFromContext(exec.ctx)
	require.True(t, ok)
	assert.Equal(t, "test-extension", extensionID)
	assert.Contains(t, exec.cmd.Env, "RUNE_DATADIR=/tmp/ext-data",
		"extensions run next to the IDE and see its data dir")
	assert.Contains(t, exec.cmd.Env, fmt.Sprintf("RUNE_PID=%d", os.Getpid()))

	states := runner.listExtensions()
	require.Len(t, states, 1)
	assert.Equal(t, "test-extension", states[0].ID)
	assert.Equal(t, "/bin/ext", states[0].CmdAndArgs)
	assert.Equal(t, workspaceapi.Pid(1), states[0].Pid)
	assert.True(t, states[0].Running)
	assert.Equal(t, 1, states[0].StartCount)
}

func TestWorkspaceRunnerRunSSHWorkspaceUsesExtExecutor(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)

	uri, err := workspaceapi.ParseURI("ssh://user@host/home/user/project")
	require.NoError(t, err)

	dataDir := t.TempDir()
	cmdExec := &recordingExecutor{}
	extExec := &recordingExecutor{}
	runner := newWorkspaceRunner(
		cmdExec, // workspace executor (would route to remote)
		extExec, // local executor for extension binaries
		nil,
		nopTrustVerifier{},
		uri,
		"/tmp/ext.sock",
		dataDir,
		"",
		[]byte("cert"),
		keys,
	)
	require.NoError(t, runner.Run("ext-id", "/bin/ext", config.NopConfig()))

	assert.NotZero(t, len(extExec.pids),
		"Run() must dispatch extension binaries to the *local* "+
			"extExecutor, not the workspace's command executor")
	assert.Zero(t, len(cmdExec.pids),
		"Run() must NOT touch the workspace's command executor; that "+
			"path is reserved for vte/term StartCommand calls that "+
			"need workspace-bound stdio (pty fds, remote files)")
	assert.Equal(t, dataDir, extExec.snapshotCmd().Dir,
		"makeCommand sets dataDir for non-file workspaces so "+
			"extensions can chdir into a path that exists on the IDE host")
	assert.Contains(t, extExec.snapshotCmd().Env, "RUNE_DATADIR="+dataDir)
	assert.Contains(t, extExec.snapshotCmd().Env, fmt.Sprintf("RUNE_PID=%d", os.Getpid()))
}

func TestWorkspaceRunnerRunSourceEntrypoint(t *testing.T) {
	t.Parallel()

	usr, err := user.Current()
	require.NoError(t, err)

	// uv and cargo discover the project via a pyproject.toml or
	// Cargo.toml above the entrypoint, so the python and rust cases
	// need real project trees.
	pyProj := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(pyProj, "src"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(pyProj, "pyproject.toml"), []byte("[project]\n"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(pyProj, "uv.lock"), []byte("version = 1\n"), 0o644))
	pyMain := filepath.Join(pyProj, "src", "main.py")
	require.NoError(t, os.WriteFile(pyMain, []byte("print()\n"), 0o644))
	pyProj, err = filepath.EvalSymlinks(pyProj)
	require.NoError(t, err)
	pyMain, err = filepath.EvalSymlinks(pyMain)
	require.NoError(t, err)
	pyDevProj := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(pyDevProj, "pyproject.toml"), []byte("[project]\n"), 0o644))
	pyDevMain := filepath.Join(pyDevProj, "main.py")
	require.NoError(t, os.WriteFile(pyDevMain, []byte("print()\n"), 0o644))
	pyDevProj, err = filepath.EvalSymlinks(pyDevProj)
	require.NoError(t, err)
	pyDevMain, err = filepath.EvalSymlinks(pyDevMain)
	require.NoError(t, err)
	orphanPy := filepath.Join(t.TempDir(), "main.py")
	require.NoError(t, os.WriteFile(orphanPy, []byte("print()\n"), 0o644))

	rustProj := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(rustProj, "src"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(rustProj, "Cargo.toml"), []byte("[package]\n"), 0o644))
	rustMain := filepath.Join(rustProj, "src", "main.rs")
	require.NoError(t, os.WriteFile(rustMain, []byte("fn main() {}\n"), 0o644))
	orphanRs := filepath.Join(t.TempDir(), "main.rs")

	// A Go package-directory entrypoint points at the main package dir;
	// the runner recognizes it as Go when the directory has a go.mod.
	goPkgDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(goPkgDir, "go.mod"), []byte("module ext\n"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(goPkgDir, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o644))
	plainDir := t.TempDir()

	tests := []struct {
		name       string
		cmdAndArgs string
		bin        string // toolchain binary provisioned under <dataDir>/bin
		wantErr    string
		wantPath   string
		wantArgs   []string
	}{
		{
			name:       "python entrypoint runs via uv project",
			cmdAndArgs: pyMain + " --flag",
			bin:        "uv",
			wantArgs: []string{
				"run", "--locked", "--project", pyProj, pyMain, "--flag",
			},
		},
		{
			name:       "python project without lock remains mutable",
			cmdAndArgs: pyDevMain,
			bin:        "uv",
			wantArgs:   []string{"run", "--project", pyDevProj, pyDevMain},
		},
		{
			name:       "python entrypoint without pyproject errors",
			cmdAndArgs: orphanPy,
			bin:        "uv",
			wantErr:    "no pyproject.toml",
		},
		{
			name:       "python package missing",
			cmdAndArgs: pyMain,
			wantErr:    "pkg install python",
		},
		{
			name:       "go main package with args",
			cmdAndArgs: "/opt/ext/main.go --flag",
			bin:        "go",
			wantArgs:   []string{"-C", "/opt/ext", "run", ".", "--flag"},
		},
		{
			name:       "go home-relative entrypoint expands",
			cmdAndArgs: "~/ext/main.go --flag",
			bin:        "go",
			wantArgs: []string{
				"-C", filepath.Join(usr.HomeDir, "ext"), "run", ".", "--flag",
			},
		},
		{
			name:       "go entrypoint must be a main.go",
			cmdAndArgs: "/opt/ext/tool.go",
			bin:        "go",
			wantErr:    "must be a main.go",
		},
		{
			name:       "go package missing",
			cmdAndArgs: "/opt/ext/main.go",
			wantErr:    "pkg install go",
		},
		{
			name:       "go package directory runs whole package",
			cmdAndArgs: goPkgDir + " --flag",
			bin:        "go",
			wantArgs:   []string{"-C", goPkgDir, "run", ".", "--flag"},
		},
		{
			name:       "go package directory missing package",
			cmdAndArgs: goPkgDir,
			wantErr:    "pkg install go",
		},
		{
			name:       "directory without go.mod unchanged",
			cmdAndArgs: plainDir + " --flag",
			wantPath:   plainDir,
			wantArgs:   []string{"--flag"},
		},
		{
			name:       "rust entrypoint runs via cargo manifest",
			cmdAndArgs: rustMain + " --flag",
			bin:        "cargo",
			wantArgs: []string{
				"run", "--manifest-path",
				filepath.Join(rustProj, "Cargo.toml"), "--", "--flag",
			},
		},
		{
			name:       "rust package missing",
			cmdAndArgs: rustMain,
			wantErr:    "pkg install rust",
		},
		{
			name:       "rust entrypoint without cargo manifest errors",
			cmdAndArgs: orphanRs,
			bin:        "cargo",
			wantErr:    "no Cargo.toml",
		},
		{
			name:       "non-source path unchanged",
			cmdAndArgs: "/bin/ext --flag",
			wantPath:   "/bin/ext",
			wantArgs:   []string{"--flag"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			keys, err := auth.GenerateKeys()
			require.NoError(t, err)
			uri, err := workspaceapi.ParseURI("file:///tmp")
			require.NoError(t, err)

			dataDir := t.TempDir()
			if tt.bin != "" {
				binDir := filepath.Join(dataDir, "bin")
				require.NoError(t, os.MkdirAll(binDir, 0o755))
				require.NoError(t, os.WriteFile(
					filepath.Join(binDir, tt.bin), []byte("#!/bin/sh\n"), 0o755))
			}

			exec := &recordingExecutor{}
			runner := newWorkspaceRunner(
				exec, exec, nil, nopTrustVerifier{}, uri,
				"/tmp/ext.sock", dataDir, "/tmp/ext-install",
				[]byte("cert"), keys,
			)
			err = runner.Run("src-ext", tt.cmdAndArgs, config.NopConfig())
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Contains(t, err.Error(), "src-ext")
				return
			}
			require.NoError(t, err)

			cmd := exec.snapshotCmd()
			wantPath := tt.wantPath
			if tt.bin != "" {
				wantPath = filepath.Join(dataDir, "bin", tt.bin)
			}
			assert.Equal(t, wantPath, cmd.Path)
			assert.Equal(t, tt.wantArgs, cmd.Args)
		})
	}
}

func TestWorkspaceRunnerExpandsEnvInEntrypoint(t *testing.T) {
	goPkgDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(goPkgDir, "go.mod"), []byte("module ext\n"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(goPkgDir, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o644))
	t.Setenv("RUNE_TEST_EXT_DIR", goPkgDir)

	newRunner := func(t *testing.T) (*workspaceRunner, *recordingExecutor) {
		keys, err := auth.GenerateKeys()
		require.NoError(t, err)
		uri, err := workspaceapi.ParseURI("file:///tmp")
		require.NoError(t, err)
		dataDir := t.TempDir()
		binDir := filepath.Join(dataDir, "bin")
		require.NoError(t, os.MkdirAll(binDir, 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(binDir, "go"), []byte("#!/bin/sh\n"), 0o755))
		exec := &recordingExecutor{}
		return newWorkspaceRunner(
			exec, exec, nil, nopTrustVerifier{}, uri,
			"/tmp/ext.sock", dataDir, "/tmp/ext-install",
			[]byte("cert"), keys,
		), exec
	}

	t.Run("trust verification entrypoint", func(t *testing.T) {
		runner, _ := newRunner(t)
		got, err := runner.resolveEntrypoint("$RUNE_TEST_EXT_DIR/main.go")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(goPkgDir, "main.go"), got)
	})

	for _, tt := range []struct {
		name       string
		cmdAndArgs string
	}{
		{"source file", "$RUNE_TEST_EXT_DIR/main.go --flag"},
		{"package directory", "$RUNE_TEST_EXT_DIR --flag"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner, exec := newRunner(t)
			require.NoError(t, runner.Run("src-ext", tt.cmdAndArgs, config.NopConfig()))
			assert.Equal(t, []string{"-C", goPkgDir, "run", ".", "--flag"},
				exec.snapshotCmd().Args)
		})
	}
}

// recordingTrustVerifier records the entrypoints it is asked to verify.
type recordingTrustVerifier struct {
	mu    sync.Mutex
	paths []string
}

func (v *recordingTrustVerifier) VerifyExtensionEntrypoint(path string) (string, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.paths = append(v.paths, path)
	return "", false
}

func TestWorkspaceRunnerExpandsDataDirInEntrypoint(t *testing.T) {
	// The process environment names another data directory; the runner's
	// own is the one its extensions are installed in.
	t.Setenv("RUNE_DATADIR", "/elsewhere")
	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)
	dataDir := t.TempDir()
	pkgDir := filepath.Join(dataDir, "lib", "pkg")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(pkgDir, "go.mod"), []byte("module ext\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "bin"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dataDir, "bin", "go"), []byte("#!/bin/sh\n"), 0o755))

	for _, tt := range []struct {
		name       string
		cmdAndArgs string
		wantPath   string
		wantArgs   []string
		wantVerify string
	}{
		{
			name:       "binary",
			cmdAndArgs: "$RUNE_DATADIR/lib/pkg/ext --flag",
			wantPath:   filepath.Join(pkgDir, "ext"),
			wantArgs:   []string{"--flag"},
			wantVerify: filepath.Join(pkgDir, "ext"),
		},
		{
			name:       "package directory",
			cmdAndArgs: "$RUNE_DATADIR/lib/pkg",
			wantPath:   filepath.Join(dataDir, "bin", "go"),
			wantArgs:   []string{"-C", pkgDir, "run", "."},
			wantVerify: pkgDir,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			exec := &recordingExecutor{}
			verifier := &recordingTrustVerifier{}
			runner := newWorkspaceRunner(
				exec, exec, nil, verifier, uri,
				"/tmp/ext.sock", dataDir, "/tmp/ext-install",
				[]byte("cert"), keys,
			)
			require.NoError(t, runner.Run("ext", tt.cmdAndArgs, config.NopConfig()))
			cmd := exec.snapshotCmd()
			assert.Equal(t, tt.wantPath, cmd.Path)
			assert.Equal(t, tt.wantArgs, cmd.Args)
			assert.Equal(t, []string{tt.wantVerify}, verifier.paths)
		})
	}
}

func TestWorkspaceRunnerPythonEntrypointResolvesPackageSymlink(t *testing.T) {
	t.Parallel()

	versionDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(versionDir, "pyproject.toml"), []byte("[project]\n"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(versionDir, "uv.lock"), []byte("version = 1\n"), 0o644))
	versionMain := filepath.Join(versionDir, "main.py")
	require.NoError(t, os.WriteFile(versionMain, []byte("print()\n"), 0o644))
	versionDir, err := filepath.EvalSymlinks(versionDir)
	require.NoError(t, err)
	versionMain, err = filepath.EvalSymlinks(versionMain)
	require.NoError(t, err)

	libRoot := t.TempDir()
	libDir := filepath.Join(libRoot, "themebuilder")
	require.NoError(t, os.Symlink(versionDir, libDir))
	libMain := filepath.Join(libDir, "main.py")

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)
	dataDir := t.TempDir()
	binDir := filepath.Join(dataDir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(binDir, "uv"), []byte("#!/bin/sh\n"), 0o755))

	exec := &recordingExecutor{}
	runner := newWorkspaceRunner(
		exec, exec, nil, nopTrustVerifier{}, uri,
		"/tmp/ext.sock", dataDir, "/tmp/ext-install", []byte("cert"), keys,
	)
	require.NoError(t, runner.Run("src-ext", libMain, config.NopConfig()))

	nextVersionDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(nextVersionDir, "pyproject.toml"), []byte("[project]\n"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(nextVersionDir, "uv.lock"), []byte("version = 1\n"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(nextVersionDir, "main.py"), []byte("print('next')\n"), 0o644))
	require.NoError(t, os.Remove(libDir))
	require.NoError(t, os.Symlink(nextVersionDir, libDir))

	cmd := exec.snapshotCmd()
	assert.Equal(t, filepath.Join(dataDir, "bin", "uv"), cmd.Path)
	assert.Equal(t, []string{
		"run", "--locked", "--project", versionDir, versionMain,
	}, cmd.Args)
}

func TestWorkspaceRunnerStartCommandRoutesToWorkspaceExecutor(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)

	uri, err := workspaceapi.ParseURI("ssh://user@host/home/user/project")
	require.NoError(t, err)

	cmdExec := &recordingExecutor{}
	extExec := &recordingExecutor{}
	runner := newWorkspaceRunner(
		cmdExec,
		extExec,
		nil,
		nopTrustVerifier{},
		uri,
		"/tmp/ext.sock",
		t.TempDir(),
		"",
		[]byte("cert"),
		keys,
	)

	_, err = runner.StartCommand(context.Background(), workspaceapi.Cmd{
		Path: "/bin/zsh",
	})
	require.NoError(t, err)

	assert.NotZero(t, len(cmdExec.pids),
		"StartCommand must reach the workspace's executor so "+
			"workspace-bound stdio (pty, remote files) is routed to "+
			"the host where the workspace lives")
	assert.Zero(t, len(extExec.pids),
		"StartCommand must NOT use the local extExecutor; that "+
			"executor only knows about the IDE host's filesystem")
}

func TestWorkspaceRunnerRunRejectsDuplicateRunningID(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)

	exec0 := &recordingExecutor{}
	runner := newWorkspaceRunner(exec0, exec0, nil, nopTrustVerifier{}, uri,
		"/tmp/ext.sock", "/tmp/ext-data", "/tmp/ext-install", []byte("cert"), keys)
	require.NoError(t, runner.Run("test-extension", "/bin/ext", config.NopConfig()))

	err = runner.Run("test-extension", "/bin/ext", config.NopConfig())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrExtensionAlreadyRunning)
	assert.Contains(t, err.Error(), `extension "test-extension"`)
}

func TestWorkspaceRunnerStopExtensionMarksStateStopped(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)

	exec := &recordingExecutor{}
	runner := newWorkspaceRunner(exec, exec, nil, nopTrustVerifier{}, uri,
		"/tmp/ext.sock", "/tmp/ext-data", "/tmp/ext-install", []byte("cert"), keys)
	require.NoError(t, runner.Run("test-extension", "/bin/ext", config.NopConfig()))

	require.NoError(t, runner.stopExtensionByID("test-extension"))
	require.Eventually(t, func() bool { return exec.ctx.Err() != nil }, time.Second, 10*time.Millisecond)

	states := runner.listExtensions()
	require.Len(t, states, 1)
	assert.False(t, states[0].Running)
	assert.Equal(t, workspaceapi.Pid(0), states[0].Pid)
	assert.Nil(t, states[0].LastErr)
}

func TestWorkspaceRunnerStopExtensionRecordsReason(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)

	exec1 := &recordingExecutor{}
	runner := newWorkspaceRunner(exec1, exec1, nil, nopTrustVerifier{}, uri,
		"/tmp/ext.sock", "/tmp/ext-data", "/tmp/ext-install", []byte("cert"), keys)
	require.NoError(t, runner.Run("test-extension", "/bin/ext", config.NopConfig()))

	stopErr := errors.New("protocol write failed")
	runner.stopExtension(runner.states["test-extension"], stopErr)

	states := runner.listExtensions()
	require.Len(t, states, 1)
	assert.False(t, states[0].Running)
	assert.ErrorIs(t, states[0].LastErr, stopErr)
}

func TestWorkspaceRunnerRestartReusesStoredCommandAndConfig(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)

	exec := &protocolDrivingExecutor{extensionID: "test-extension"}
	runner := newWorkspaceRunner(exec, exec, extension.GrantAll(), nopTrustVerifier{}, uri,
		"/tmp/ext.sock", "/tmp/ext-data", "/tmp/ext-install", []byte("cert"), keys)
	cfg := config.MapConfig(map[string]any{"foo": "bar"})
	require.NoError(t, runner.Run("test-extension", "/bin/ext --serve", cfg))

	require.NoError(t, runner.restartExtension(context.Background(), "test-extension"))

	states := runner.listExtensions()
	require.Len(t, states, 1)
	assert.True(t, states[0].Running)
	assert.Equal(t, workspaceapi.Pid(2), states[0].Pid)
	assert.Equal(t, "/bin/ext --serve", states[0].CmdAndArgs)
	assert.Equal(t, 2, states[0].StartCount)
	value, err := states[0].Config.GetString("foo")
	require.NoError(t, err)
	assert.Equal(t, "bar", value)
	cmd := exec.snapshotCmd()
	assert.Equal(t, "/bin/ext", cmd.Path)
	assert.Equal(t, []string{"--serve"}, cmd.Args)
}

func TestWorkspaceRunnerStartExtensionWaitsForProtocolReady(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)

	exec := &recordingExecutor{}
	runner := newWorkspaceRunner(exec, exec, extension.GrantAll(), nopTrustVerifier{}, uri,
		"/tmp/ext.sock", "/tmp/ext-data", "/tmp/ext-install", []byte("cert"), keys)

	done := make(chan error, 1)
	go func() {
		done <- runner.startExtension(context.Background(), "test-extension", "/bin/ext", config.NopConfig())
	}()

	select {
	case err := <-done:
		t.Fatalf("startExtension returned before protocol handshake: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	meta := extensionapi.Metadata{
		DeveloperID:    "dev-id",
		DeveloperEmail: "dev@example.com",
		DeveloperKey:   "dev-key",
		ExtensionID:    "test-extension",
		ExtensionName:  "Test Extension",
		Permissions:    extensionapi.AllPermissions(),
	}
	encoded, err := json.Marshal(meta)
	require.NoError(t, err)
	_, err = exec.waitCmd(t).Stdout.Write(encoded)
	require.NoError(t, err)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("startExtension did not return after protocol handshake")
	}

	states := runner.listExtensions()
	require.Len(t, states, 1)
	assert.True(t, states[0].Running)
	assert.Nil(t, states[0].LastErr)
}

func TestWorkspaceRunnerRunsExtensionInItsOwnProcessGroup(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)

	exec := &recordingExecutor{}
	runner := newWorkspaceRunner(exec, exec, extension.GrantAll(), nopTrustVerifier{}, uri,
		"/tmp/ext.sock", "/tmp/ext-data", "/tmp/ext-install", []byte("cert"), keys)
	require.NoError(t, runner.Run("test-extension", "/bin/ext", config.NopConfig()))

	attr := exec.waitCmd(t).SysProcAttr
	require.NotNil(t, attr)
	assert.True(t, attr.Setpgid, "extension must head its own process group")
	assert.Zero(t, attr.Pgid, "extension must not join an existing group")
}

func TestWorkspaceRunnerWaitReady(t *testing.T) {
	t.Parallel()

	t.Run("blocks until protocol ready", func(t *testing.T) {
		t.Parallel()

		keys, err := auth.GenerateKeys()
		require.NoError(t, err)
		uri, err := workspaceapi.ParseURI("file:///tmp")
		require.NoError(t, err)

		exec := &recordingExecutor{}
		runner := newWorkspaceRunner(exec, exec, extension.GrantAll(), nopTrustVerifier{}, uri,
			"/tmp/ext.sock", "/tmp/ext-data", "/tmp/ext-install", []byte("cert"), keys)

		require.NoError(t, runner.Run("test-extension", "/bin/ext", config.NopConfig()))

		done := make(chan error, 1)
		go func() {
			done <- runner.WaitReady(context.Background(), "test-extension")
		}()

		select {
		case err := <-done:
			t.Fatalf("WaitReady returned before protocol handshake: %v", err)
		case <-time.After(50 * time.Millisecond):
		}

		meta := extensionapi.Metadata{
			DeveloperID:    "dev-id",
			DeveloperEmail: "dev@example.com",
			DeveloperKey:   "dev-key",
			ExtensionID:    "test-extension",
			ExtensionName:  "Test Extension",
			Permissions:    extensionapi.AllPermissions(),
		}
		encoded, err := json.Marshal(meta)
		require.NoError(t, err)
		_, err = exec.snapshotCmd().Stdout.Write(encoded)
		require.NoError(t, err)

		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("WaitReady did not return after protocol handshake")
		}
	})

	t.Run("unknown id blocks until ctx cancel", func(t *testing.T) {
		t.Parallel()

		keys, err := auth.GenerateKeys()
		require.NoError(t, err)
		uri, err := workspaceapi.ParseURI("file:///tmp")
		require.NoError(t, err)

		exec := &recordingExecutor{}
		runner := newWorkspaceRunner(exec, exec, extension.GrantAll(), nopTrustVerifier{}, uri,
			"/tmp/ext.sock", "/tmp/ext-data", "/tmp/ext-install", []byte("cert"), keys)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- runner.WaitReady(ctx, "missing")
		}()

		select {
		case err := <-done:
			t.Fatalf("WaitReady returned before registration or cancellation: %v", err)
		case <-time.After(50 * time.Millisecond):
		}

		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(time.Second):
			t.Fatal("WaitReady did not return after ctx cancel")
		}
	})

	t.Run("registration race resolves", func(t *testing.T) {
		t.Parallel()

		keys, err := auth.GenerateKeys()
		require.NoError(t, err)
		uri, err := workspaceapi.ParseURI("file:///tmp")
		require.NoError(t, err)

		exec := &recordingExecutor{}
		runner := newWorkspaceRunner(exec, exec, extension.GrantAll(), nopTrustVerifier{}, uri,
			"/tmp/ext.sock", "/tmp/ext-data", "/tmp/ext-install", []byte("cert"), keys)

		done := make(chan error, 1)
		go func() {
			done <- runner.WaitReady(context.Background(), "test-extension")
		}()

		select {
		case err := <-done:
			t.Fatalf("WaitReady returned before registration: %v", err)
		case <-time.After(50 * time.Millisecond):
		}

		require.NoError(t, runner.Run("test-extension", "/bin/ext", config.NopConfig()))

		select {
		case err := <-done:
			t.Fatalf("WaitReady returned before protocol handshake: %v", err)
		case <-time.After(50 * time.Millisecond):
		}

		meta := extensionapi.Metadata{
			DeveloperID:    "dev-id",
			DeveloperEmail: "dev@example.com",
			DeveloperKey:   "dev-key",
			ExtensionID:    "test-extension",
			ExtensionName:  "Test Extension",
			Permissions:    extensionapi.AllPermissions(),
		}
		encoded, err := json.Marshal(meta)
		require.NoError(t, err)
		_, err = exec.snapshotCmd().Stdout.Write(encoded)
		require.NoError(t, err)

		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("WaitReady did not return after registration and handshake")
		}
	})

	t.Run("runner close unblocks", func(t *testing.T) {
		t.Parallel()

		keys, err := auth.GenerateKeys()
		require.NoError(t, err)
		uri, err := workspaceapi.ParseURI("file:///tmp")
		require.NoError(t, err)

		exec := &recordingExecutor{}
		runner := newWorkspaceRunner(exec, exec, extension.GrantAll(), nopTrustVerifier{}, uri,
			"/tmp/ext.sock", "/tmp/ext-data", "/tmp/ext-install", []byte("cert"), keys)

		done := make(chan error, 1)
		go func() {
			done <- runner.WaitReady(context.Background(), "missing")
		}()

		select {
		case err := <-done:
			t.Fatalf("WaitReady returned before runner close: %v", err)
		case <-time.After(50 * time.Millisecond):
		}

		require.NoError(t, runner.Close())
		select {
		case err := <-done:
			require.Error(t, err)
		case <-time.After(time.Second):
			t.Fatal("WaitReady did not return after runner close")
		}
	})

	t.Run("ctx cancel returns", func(t *testing.T) {
		t.Parallel()

		keys, err := auth.GenerateKeys()
		require.NoError(t, err)
		uri, err := workspaceapi.ParseURI("file:///tmp")
		require.NoError(t, err)

		exec := &recordingExecutor{}
		runner := newWorkspaceRunner(exec, exec, extension.GrantAll(), nopTrustVerifier{}, uri,
			"/tmp/ext.sock", "/tmp/ext-data", "/tmp/ext-install", []byte("cert"), keys)

		require.NoError(t, runner.Run("test-extension", "/bin/ext", config.NopConfig()))

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- runner.WaitReady(ctx, "test-extension")
		}()

		select {
		case err := <-done:
			t.Fatalf("WaitReady returned before cancellation: %v", err)
		case <-time.After(50 * time.Millisecond):
		}

		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(time.Second):
			t.Fatal("WaitReady did not return after ctx cancel")
		}
	})
}

func TestWorkspaceRunnerStartExtensionReturnsProtocolError(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)

	exec := &recordingExecutor{}
	runner := newWorkspaceRunner(exec, exec, extension.GrantAll(), nopTrustVerifier{}, uri,
		"/tmp/ext.sock", "/tmp/ext-data", "/tmp/ext-install", []byte("cert"), keys)

	done := make(chan error, 1)
	go func() {
		done <- runner.startExtension(context.Background(), "test-extension", "/bin/ext", config.NopConfig())
	}()

	select {
	case err := <-done:
		t.Fatalf("startExtension returned before protocol error was emitted: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	_, err = exec.waitCmd(t).Stdout.Write([]byte(`{"developer_id":"missing-fields"}`))
	require.Error(t, err)

	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "validate metadata")
	case <-time.After(time.Second):
		t.Fatal("startExtension did not return after protocol error")
	}

	states := runner.listExtensions()
	require.Len(t, states, 1)
	assert.False(t, states[0].Running)
	assert.Error(t, states[0].LastErr)
	assert.Contains(t, states[0].LastErr.Error(), "validate metadata")
}

func TestWorkspaceRunnerStartExtensionReturnsStartCommandError(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)

	exec := startErrorExecutor{err: errors.New("boom")}
	runner := newWorkspaceRunner(exec, exec, extension.GrantAll(), nopTrustVerifier{}, uri,
		"/tmp/ext.sock", "/tmp/ext-data", "/tmp/ext-install", []byte("cert"), keys)

	err = runner.startExtension(context.Background(), "test-extension", "/bin/ext", config.NopConfig())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start command: boom")
}

func TestWorkspaceRunnerLogsLifecycle(t *testing.T) {
	t.Parallel()

	meta := extensionMetadata(t, "ext")
	started := []string{
		`level=info msg="starting /bin/ext --flag in `,
		`level=info msg="started with pid 1" source=rune`,
	}

	tests := []struct {
		name       string
		cmdAndArgs string
		startErr   error
		// run drives the started extension, and returns what it exits with.
		run     func(t *testing.T, runner *workspaceRunner, cmd workspaceapi.Cmd) error
		wantErr string
		// want are lines of the log, in order.
		want []string
	}{
		{
			name:       "handshake and exit",
			cmdAndArgs: "/bin/ext --flag",
			run: func(t *testing.T, runner *workspaceRunner, cmd workspaceapi.Cmd) error {
				_, err := cmd.Stdout.Write(meta)
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					return strings.Contains(readExtensionLog(t, runner), "handshake complete")
				}, time.Second, time.Millisecond)
				return errors.New("exit status 2")
			},
			want: append(started,
				`level=info msg="handshake complete: config sent" source=rune`,
				`level=error msg="exited: exit status 2" source=rune`),
		},
		{
			name:       "rejected handshake",
			cmdAndArgs: "/bin/ext --flag",
			run: func(t *testing.T, _ *workspaceRunner, cmd workspaceapi.Cmd) error {
				_, err := cmd.Stdout.Write([]byte(`{"developer_id":"dev-id"}`))
				require.Error(t, err)
				return errors.New("signal: killed")
			},
			want: append(started,
				`level=warning msg="stopping: validate metadata: developer email must not be missing" source=rune`,
				`level=error msg="exited: signal: killed" source=rune`),
		},
		{
			name:       "stopped",
			cmdAndArgs: "/bin/ext --flag",
			run: func(t *testing.T, runner *workspaceRunner, _ workspaceapi.Cmd) error {
				require.NoError(t, runner.stopExtensionByID("ext"))
				return nil
			},
			want: append(started,
				`level=info msg="stopping on request" source=rune`,
				`level=info msg=exited source=rune`),
		},
		{
			name:       "start command fails",
			cmdAndArgs: "/bin/ext --flag",
			startErr:   errors.New("boom"),
			wantErr:    "start command: boom",
			want: []string{
				started[0],
				`level=error msg="failed to start: start command: boom" source=rune`,
			},
		},
		{
			name:       "command cannot be made",
			cmdAndArgs: "/src/main.go",
			wantErr:    "pkg install go",
			want: []string{
				`level=error msg="failed to start: make command: extension ext requires the go package`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			keys, err := auth.GenerateKeys()
			require.NoError(t, err)
			// The log path derives from the workspace, so a workspace of its
			// own keeps parallel tests out of each other's logs.
			dir := t.TempDir()
			uri, err := workspaceapi.ParseURI("file://" + dir)
			require.NoError(t, err)
			rec := &recordingExecutor{}
			var exec schemeapi.Executor = rec
			if tt.startErr != nil {
				exec = startErrorExecutor{err: tt.startErr}
			}
			runner := newWorkspaceRunner(exec, exec, extension.GrantAll(), nopTrustVerifier{}, uri,
				"/tmp/ext.sock", dir, dir, []byte("cert"), keys)
			t.Cleanup(func() { require.NoError(t, runner.Close()) })

			err = runner.Run("ext", tt.cmdAndArgs, config.NopConfig())
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				state, ok := runner.extension("ext")
				require.True(t, ok, "an extension that fails to start is registered")
				assert.False(t, state.Running)
				assert.ErrorContains(t, state.LastErr, tt.wantErr)
				assert.ErrorContains(t, runner.WaitReady(context.Background(), "ext"), tt.wantErr)
			} else {
				require.NoError(t, err)
				cmd := rec.waitCmd(t)
				cmd.Watcher.WatchProcess() <- tt.run(t, runner, cmd)
				require.Eventually(t, func() bool {
					return strings.Contains(readExtensionLog(t, runner), tt.want[len(tt.want)-1])
				}, time.Second, time.Millisecond)
			}

			requireExtensionLog(t, runner, tt.want)
		})
	}
}

func TestWorkspaceRunnerSeesAnExitBeforeStartReturns(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	dir := t.TempDir()
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)
	exitErr := errors.New("exit status 1")
	exec := &exitingExecutor{err: exitErr}
	runner := newWorkspaceRunner(exec, exec, extension.GrantAll(), nopTrustVerifier{}, uri,
		"/tmp/ext.sock", dir, dir, []byte("cert"), keys)
	t.Cleanup(func() { require.NoError(t, runner.Close()) })
	exec.wait = func() {
		assert.Eventually(t, func() bool {
			state, ok := runner.extension("ext")
			return ok && !state.Running
		}, time.Second, time.Millisecond, "the exit must be seen before StartCommand returns")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.ErrorIs(t, runner.startExtension(ctx, "ext", "/bin/ext", config.NopConfig()), exitErr)
	state, ok := runner.extension("ext")
	require.True(t, ok)
	assert.False(t, state.Running)
	assert.Zero(t, state.Pid)
	assert.ErrorIs(t, state.LastErr, exitErr)
	assert.Equal(t, 1, state.StartCount)
}

func TestWorkspaceRunnerIgnoresTheExitOfAPreviousRun(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	dir := t.TempDir()
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)
	exec := &protocolDrivingExecutor{extensionID: "ext"}
	runner := newWorkspaceRunner(exec, exec, extension.GrantAll(), nopTrustVerifier{}, uri,
		"/tmp/ext.sock", dir, dir, []byte("cert"), keys)
	t.Cleanup(func() { require.NoError(t, runner.Close()) })
	require.NoError(t, runner.Run("ext", "/bin/ext", config.NopConfig()))
	previous := exec.waitCmd(t)

	require.NoError(t, runner.restartExtension(context.Background(), "ext"))
	previous.Watcher.WatchProcess() <- errors.New("signal: killed")

	assert.Never(t, func() bool {
		state, ok := runner.extension("ext")
		return !ok || !state.Running || state.Pid != 2 || state.LastErr != nil
	}, 100*time.Millisecond, time.Millisecond, "the previous run's exit must not end the next")
	assert.NotContains(t, readExtensionLog(t, runner), "signal: killed",
		"the previous run's exit must not be logged as the next one's")
}

func TestWorkspaceRunnerLifecycleUnderStress(t *testing.T) {
	t.Parallel()

	const workers, ops = 16, 250
	ids := []string{"ext-0", "ext-1", "ext-2", "ext-3"}
	meta := make(map[string][]byte, len(ids))
	for _, id := range ids {
		meta[id] = extensionMetadata(t, id)
	}
	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	dir := t.TempDir()
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)
	exec := newStressExecutor(meta)
	runner := newWorkspaceRunner(exec, exec, extension.GrantAll(), nopTrustVerifier{}, uri,
		"/tmp/ext.sock", dir, dir, []byte("cert"), keys)
	t.Cleanup(func() { require.NoError(t, runner.Close()) })

	var mu sync.Mutex
	var violations []string
	violate := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		violations = append(violations, fmt.Sprintf(format, args...))
	}

	done := make(chan struct{})
	checked := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(checked)
		for {
			select {
			case <-done:
				return
			default:
			}
			for _, state := range runner.listExtensions() {
				if !state.Running && state.Pid != 0 {
					violate("%s is not running but has pid %d", state.ID, state.Pid)
				}
				if state.Running && state.LastErr != nil {
					violate("%s is running but errored: %v", state.ID, state.LastErr)
				}
			}
		}
	})

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			for range ops {
				id := ids[rand.IntN(len(ids))]
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				var err error
				switch rand.IntN(4) {
				case 0:
					err = runner.startExtension(ctx, id, "/bin/ext", config.NopConfig())
				case 1:
					err = runner.restartExtension(ctx, id)
				case 2:
					err = runner.stopExtensionByID(id)
				default:
					err = runner.Run(id, "/bin/ext", config.NopConfig())
				}
				cancel()
				if errors.Is(err, context.DeadlineExceeded) {
					violate("%s was never ready nor ended", id)
				}
			}
		})
	}
	wg.Wait()
	close(done)
	<-checked
	require.Greater(t, exec.started(), workspaceapi.Pid(workers*ops/8))

	// Once every exit has been seen, the extensions that are running are
	// exactly those whose process is alive.
	running := func() map[workspaceapi.Pid]string {
		ret := make(map[workspaceapi.Pid]string)
		for _, state := range runner.listExtensions() {
			if state.Running {
				ret[state.Pid] = state.ID
			}
		}
		return ret
	}
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Equal(c, exec.alivePids(), running())
	}, 5*time.Second, time.Millisecond)

	for _, id := range ids {
		_ = runner.stopExtensionByID(id)
	}
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Empty(c, exec.alivePids(), "stopping an extension kills its process")
		assert.Empty(c, running())
	}, 5*time.Second, time.Millisecond)
	exec.exits.Wait()

	pid := regexp.MustCompile(`(?:pid|process) (\d+)`)
	for _, id := range ids {
		state, ok := runner.extension(id)
		require.True(t, ok)
		logs, err := os.ReadFile(state.LogPath)
		require.NoError(t, err)
		pids := make(map[string]bool)
		for _, m := range pid.FindAllStringSubmatch(string(logs), -1) {
			pids[m[1]] = true
		}
		assert.Lenf(t, pids, 1, "the log of %s holds only its last run:\n%s", id, logs)
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, violations)
}

func readExtensionLog(t *testing.T, runner *workspaceRunner) string {
	t.Helper()
	state, ok := runner.extension("ext")
	require.True(t, ok)
	logs, err := os.ReadFile(state.LogPath)
	require.NoError(t, err)
	return string(logs)
}

// requireExtensionLog requires the log of extension "ext" to hold want in
// order.
func requireExtensionLog(t *testing.T, runner *workspaceRunner, want []string) {
	t.Helper()
	logs := readExtensionLog(t, runner)
	rest := logs
	for _, w := range want {
		i := strings.Index(rest, w)
		require.NotEqualf(t, -1, i, "want %q after the lines before it in:\n%s", w, logs)
		rest = rest[i+len(w):]
	}
}

type testServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s testServerStream) Context() context.Context {
	return s.ctx
}

func TestExtensionIDStreamInterceptorTagsContext(t *testing.T) {
	t.Parallel()

	ctx := auth.ContextWithClaims(context.Background(), auth.UserClaims[ideauthorizer.Extension]{
		Extra: ideauthorizer.Extension{Metadata: extensionapi.Metadata{
			ExtensionID: "test-extension",
		}},
	})
	stream := testServerStream{ctx: ctx}

	called := false
	err := extensionIDStreamInterceptor()(nil, stream, &grpc.StreamServerInfo{},
		func(_ any, stream grpc.ServerStream) error {
			called = true
			extensionID, ok := processctx.ExtensionIDFromContext(stream.Context())
			require.True(t, ok)
			assert.Equal(t, "test-extension", extensionID)
			return nil
		})
	require.NoError(t, err)
	assert.True(t, called)
}

func TestExtensionIDUnaryInterceptorTagsContext(t *testing.T) {
	t.Parallel()

	ctx := auth.ContextWithClaims(context.Background(), auth.UserClaims[ideauthorizer.Extension]{
		Extra: ideauthorizer.Extension{Metadata: extensionapi.Metadata{
			ExtensionID: "test-extension",
		}},
	})

	called := false
	_, err := extensionIDUnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{},
		func(ctx context.Context, _ any) (any, error) {
			called = true
			extensionID, ok := processctx.ExtensionIDFromContext(ctx)
			require.True(t, ok)
			assert.Equal(t, "test-extension", extensionID)
			return nil, nil
		})
	require.NoError(t, err)
	assert.True(t, called)

	_, err = extensionIDUnaryInterceptor()(context.Background(), nil, &grpc.UnaryServerInfo{},
		func(ctx context.Context, _ any) (any, error) {
			_, ok := processctx.ExtensionIDFromContext(ctx)
			assert.False(t, ok, "an unauthenticated call is not tagged")
			return nil, nil
		})
	require.NoError(t, err)
}

var _ extension.Runner = (*workspaceRunner)(nil)
