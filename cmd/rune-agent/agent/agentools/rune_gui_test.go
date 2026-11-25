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

package agentools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/internal/debug"
)

// fakeRuneExec runs a fake Rune for every Start: it answers each line
// written to its stdin with respond, as --exec does, and exits with exitErr
// when stdin ends or respond returns false.
type fakeRuneExec struct {
	respond func(line string, stdout io.Writer) bool
	stderr  string
	exitErr error
	// holdOnEOF keeps a process running once stdin ends, until it is
	// killed.
	holdOnEOF bool

	mu      sync.Mutex
	cmds    []workspaceapi.Cmd
	lines   []string
	signals []syscall.Signal
	kill    sync.Once
	killed  chan struct{}
	exits   chan struct{}
}

func newFakeRuneExec(respond func(line string, stdout io.Writer) bool) *fakeRuneExec {
	return &fakeRuneExec{respond: respond, killed: make(chan struct{}), exits: make(chan struct{}, 8)}
}

// replyOK answers every line with ok.
func replyOK(_ string, stdout io.Writer) bool {
	_, _ = fmt.Fprintln(stdout, "ok")
	return true
}

func (e *fakeRuneExec) Start(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	e.mu.Lock()
	e.cmds = append(e.cmds, cmd)
	pid := workspaceapi.Pid(100 + len(e.cmds))
	e.mu.Unlock()
	go debug.CapturePanicReport(func() {
		defer func() {
			cmd.Watcher.WatchProcess() <- e.exitErr
			e.exits <- struct{}{}
		}()
		_, _ = io.WriteString(cmd.Stderr, e.stderr)
		sc := bufio.NewScanner(cmd.Stdin)
		for sc.Scan() {
			e.mu.Lock()
			e.lines = append(e.lines, sc.Text())
			e.mu.Unlock()
			if !e.respond(sc.Text(), cmd.Stdout) {
				return
			}
		}
		if sc.Err() == nil && e.holdOnEOF {
			<-e.killed
		}
	})
	return pid, nil
}

func (e *fakeRuneExec) Signal(_ workspaceapi.Pid, sig syscall.Signal) error {
	e.mu.Lock()
	e.signals = append(e.signals, sig)
	e.mu.Unlock()
	if sig == syscall.SIGKILL {
		e.kill.Do(func() { close(e.killed) })
	}
	return nil
}

func (e *fakeRuneExec) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (e *fakeRuneExec) Close() error { return nil }

func (e *fakeRuneExec) commands() []workspaceapi.Cmd {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.cmds)
}

func (e *fakeRuneExec) sent() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.lines)
}

func (e *fakeRuneExec) sentSignals() []syscall.Signal {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.signals)
}

func (e *fakeRuneExec) waitExit(t *testing.T) {
	t.Helper()
	select {
	case <-e.exits:
	case <-time.After(5 * time.Second):
		t.Fatal("the fake Rune did not exit")
	}
}

// runeGUIFixture is a RuneGUI with its directories: base holds the
// instances' data directories, host is the user's data directory and ws the
// agent's workspace. exec starts both the installed Rune and the binaries
// start names.
type runeGUIFixture struct {
	tool           *RuneGUI
	base, host, ws string
}

func newRuneGUIFixture(t *testing.T, exec workspaceapi.Executor) runeGUIFixture {
	t.Helper()
	f := runeGUIFixture{base: t.TempDir(), host: t.TempDir(), ws: t.TempDir()}
	require.NoError(t, os.MkdirAll(filepath.Join(f.host, "hud", "ember"), 0o755))
	f.tool = NewRuneGUI(exec, "/usr/local/bin/rune", f.base, f.host, f.ws, "chat-1")
	f.tool.local = exec
	f.tool.replyTimeout = 5 * time.Second
	f.tool.stopTimeout = 5 * time.Second
	return f
}

func executeRuneGUI(t *testing.T, tool *RuneGUI, ctx context.Context, command ...string) agent.ToolResult {
	t.Helper()
	args, err := json.Marshal(runeGUIArgs{Command: command})
	require.NoError(t, err)
	return tool.Execute(ctx, string(args))
}

// stopRuneGUI stops the instance of ctx, so its fake process exits.
func stopRuneGUI(t *testing.T, tool *RuneGUI, ctx context.Context) {
	t.Helper()
	result := executeRuneGUI(t, tool, ctx, "stop")
	require.False(t, result.IsError, result.Content)
}

func TestRuneGUIStart(t *testing.T) {
	tests := []struct {
		name     string
		command  []string
		wantPath string
		// wantArgs and wantDir name the session's data directory as DATA,
		// the agent's workspace as WS.
		wantArgs []string
		wantDir  string
		// wantWorkspaceExec reports that the user approves the command, as
		// the workspace's executor starts it.
		wantWorkspaceExec bool
	}{
		{
			name:     "defaults to a scratch workspace",
			command:  []string{"start"},
			wantPath: "/usr/local/bin/rune",
			wantArgs: []string{"-G", "--offscreen", "--geometry", "960x600",
				"-d", "DATA", "-w", "DATA/workspace", "-e", "-"},
			wantDir: "DATA/workspace",
		},
		{
			name: "options and files",
			command: []string{"start", "--size", "800x500@1.5", "--binary=bin/rune",
				"--workspace", ".", "main.go", "a=b.go"},
			wantPath: "WS/bin/rune",
			wantArgs: []string{"-G", "--offscreen", "--geometry", "800x500@1.5",
				"-d", "DATA", "-w", "WS", "-e", "-", "--", "main.go", "a=b.go"},
			wantDir:           "WS",
			wantWorkspaceExec: true,
		},
		{
			name:     "absolute paths",
			command:  []string{"start", "--binary", "/opt/rune/bin/rune", "--workspace", "/srv/project"},
			wantPath: "/opt/rune/bin/rune",
			wantArgs: []string{"-G", "--offscreen", "--geometry", "960x600",
				"-d", "DATA", "-w", "/srv/project", "-e", "-"},
			wantDir:           "/srv/project",
			wantWorkspaceExec: true,
		},
		{
			name:     "a project with the installed Rune",
			command:  []string{"start", "--workspace", "/srv/project"},
			wantPath: "/usr/local/bin/rune",
			wantArgs: []string{"-G", "--offscreen", "--geometry", "960x600",
				"-d", "DATA", "-w", "/srv/project", "-e", "-"},
			wantDir: "/srv/project",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The instance must not inherit what reaches the user's Rune.
			t.Setenv("RUNE_SOCKET", "unix:///tmp/rune.sock")
			t.Setenv("RUNE_TOKEN", "tok")
			t.Setenv("RUNE_CERT", "cert")
			t.Setenv("RUNE_PID", "1234")
			t.Setenv("RUNE_INSTALLDIR", "/install")
			t.Setenv("RUNE_DATADIR", "/extension-datadir")
			local, workspaceExec := newFakeRuneExec(replyOK), newFakeRuneExec(replyOK)
			f := newRuneGUIFixture(t, local)
			f.tool.workspaceExec = workspaceExec

			result := executeRuneGUI(t, f.tool, context.Background(), tt.command...)
			require.False(t, result.IsError, result.Content)
			defer stopRuneGUI(t, f.tool, context.Background())

			data := filepath.Join(f.base, "rune-chat-1")
			expand := func(s string) string {
				return strings.NewReplacer("DATA", data, "WS", f.ws).Replace(s)
			}
			var wantArgs []string
			for _, a := range tt.wantArgs {
				wantArgs = append(wantArgs, expand(a))
			}
			exec, other := local, workspaceExec
			if tt.wantWorkspaceExec {
				exec, other = workspaceExec, local
			}
			assert.Empty(t, other.commands())
			cmds := exec.commands()
			require.Len(t, cmds, 1)
			assert.Equal(t, expand(tt.wantPath), cmds[0].Path)
			assert.Equal(t, wantArgs, cmds[0].Args)
			assert.Equal(t, expand(tt.wantDir), cmds[0].Dir)
			assert.Equal(t, []string{
				"RUNE_SOCKET=", "RUNE_TOKEN=", "RUNE_CERT=", "RUNE_PID=", "RUNE_INSTALLDIR=",
				"RUNE_DATADIR=" + data,
			}, cmds[0].Env)
			assert.Contains(t, result.Content, "workspaceready")

			config, err := os.ReadFile(filepath.Join(data, "config.yaml"))
			require.NoError(t, err)
			assert.Equal(t, "include:\n  - defaults.yaml\n  - agent.yaml\n", string(config))
			agentConfig, err := os.ReadFile(filepath.Join(data, "agent.yaml"))
			require.NoError(t, err)
			assert.Equal(t, "{}\n", string(agentConfig))
			defaults, err := os.ReadFile(filepath.Join(data, "defaults.yaml"))
			require.NoError(t, err)
			assert.Contains(t, string(defaults), "  intro: false\n")
			assert.Contains(t, string(defaults), "log_path: "+strconv.Quote(filepath.Join(data, "debug.log")))
			for _, name := range []string{"hud", "etc"} {
				target, err := os.Readlink(filepath.Join(data, name))
				require.NoError(t, err)
				assert.Equal(t, filepath.Join(f.host, name), target)
			}
			assert.DirExists(t, filepath.Join(data, "hud", "ember"), "the user's HUD assets resolve")
		})
	}
}

func TestRuneGUIStartWithoutALocalWorkspace(t *testing.T) {
	tests := []struct {
		name     string
		command  []string
		wantArgs []string
		wantErr  string
	}{
		{
			name:    "scratch workspace",
			command: []string{"start"},
			wantArgs: []string{"-G", "--offscreen", "--geometry", "960x600",
				"-d", "DATA", "-w", "DATA/workspace", "-e", "-"},
		},
		{
			name:    "absolute workspace",
			command: []string{"start", "--workspace", "/srv/project"},
			wantArgs: []string{"-G", "--offscreen", "--geometry", "960x600",
				"-d", "DATA", "-w", "/srv/project", "-e", "-"},
		},
		{
			name:    "relative workspace",
			command: []string{"start", "--workspace", "project"},
			wantErr: `"project" is relative, but your workspace is not on this machine`,
		},
		{
			name:    "another binary",
			command: []string{"start", "--binary", "/opt/rune/bin/rune"},
			wantErr: "--binary needs your workspace to be on this machine",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := newFakeRuneExec(replyOK)
			f := newRuneGUIFixture(t, exec)
			f.tool.workspaceExec, f.tool.workspace = nil, ""

			result := executeRuneGUI(t, f.tool, context.Background(), tt.command...)

			if tt.wantErr != "" {
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content, tt.wantErr)
				assert.Empty(t, exec.commands())
				return
			}
			require.False(t, result.IsError, result.Content)
			defer stopRuneGUI(t, f.tool, context.Background())
			data := filepath.Join(f.base, "rune-chat-1")
			var wantArgs []string
			for _, a := range tt.wantArgs {
				wantArgs = append(wantArgs, strings.ReplaceAll(a, "DATA", data))
			}
			cmds := exec.commands()
			require.Len(t, cmds, 1)
			assert.Equal(t, "/usr/local/bin/rune", cmds[0].Path)
			assert.Equal(t, wantArgs, cmds[0].Args)
		})
	}
}

func TestRuneGUIRejectsCommand(t *testing.T) {
	tests := []struct {
		name    string
		command []string
		want    string
	}{
		{"empty", nil, "command is required"},
		{"unknown command", []string{"eval", "1"}, `"eval" is not an available command`},
		{"line break in exec", []string{"exec", "edit a\nquit"}, "must not contain line breaks"},
		{"line break in keys", []string{"keys", "ix\r"}, "must not contain line breaks"},
		{"exec without a command", []string{"exec"}, "exec needs a command line"},
		{"keys without keys", []string{"keys"}, "keys needs a key sequence"},
		{"config in pieces", []string{"config", "a: 1", "b: 2"}, "config takes the YAML config as one argument"},
		{"screenshot to a path", []string{"screenshot", "/tmp/out.png"}, "screenshot takes no arguments"},
		{"stop with arguments", []string{"stop", "now"}, "stop takes no arguments"},
		{"log of too many lines", []string{"log", "501"}, "log takes a number of lines from 1 to 500"},
		{"log of words", []string{"log", "all"}, "log takes a number of lines"},
		{"start with another data directory", []string{"start", "-d", "/tmp"}, `start takes only --size, --binary and --workspace, not "-d"`},
		{"start with a bad size", []string{"start", "--size", "big"}, `--size takes WxH or WxH@SCALE`},
		{"start without a size", []string{"start", "--size"}, "--size needs a value"},
		{"start with an empty binary", []string{"start", "--binary="}, "--binary needs a value"},
		{"exec before start", []string{"exec", "edit main.go"}, "Rune is not running"},
		{"screenshot before start", []string{"screenshot"}, "Rune is not running"},
		{"config that is not a map", []string{"config", "- a"}, "the config is not a YAML map"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := newFakeRuneExec(replyOK)
			f := newRuneGUIFixture(t, exec)

			result := executeRuneGUI(t, f.tool, context.Background(), tt.command...)

			assert.True(t, result.IsError)
			assert.Contains(t, result.Content, tt.want)
			assert.Empty(t, exec.commands(), "a rejected command starts nothing")
		})
	}
}

func TestRuneGUICommands(t *testing.T) {
	tests := []struct {
		name      string
		command   []string
		reply     string
		wantLine  string
		want      string
		wantError bool
	}{
		{
			name:     "exec",
			command:  []string{"exec", "edit main.go"},
			reply:    "ok",
			wantLine: "edit main.go",
			want:     "ok",
		},
		{
			name:     "exec arguments make one line",
			command:  []string{"exec", "windownew", "right"},
			reply:    "ok",
			wantLine: "windownew right",
			want:     "ok",
		},
		{
			name:      "error reply",
			command:   []string{"exec", "nosuchcommand"},
			reply:     `error: unknown command\nsecond line`,
			wantLine:  "nosuchcommand",
			want:      `error: unknown command\nsecond line`,
			wantError: true,
		},
		{
			name:     "keys",
			command:  []string{"keys", "ix<esc>"},
			reply:    "ok",
			wantLine: "echo ix<esc>",
			want:     "ok",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := newFakeRuneExec(func(_ string, stdout io.Writer) bool {
				_, _ = fmt.Fprintln(stdout, tt.reply)
				return true
			})
			f := newRuneGUIFixture(t, exec)
			ctx := context.Background()
			require.False(t, executeRuneGUI(t, f.tool, ctx, "start").IsError)
			defer stopRuneGUI(t, f.tool, ctx)

			result := executeRuneGUI(t, f.tool, ctx, tt.command...)

			assert.Equal(t, tt.want, result.Content)
			assert.Equal(t, tt.wantError, result.IsError)
			assert.Equal(t, []string{tt.wantLine}, exec.sent())
		})
	}
}

func TestRuneGUIRunsOneInstancePerDialogue(t *testing.T) {
	exec := newFakeRuneExec(replyOK)
	f := newRuneGUIFixture(t, exec)
	chat := context.Background()
	sub := agent.WithDialogueID(context.Background(), "sub-agent/7")

	for _, ctx := range []context.Context{chat, sub} {
		require.False(t, executeRuneGUI(t, f.tool, ctx, "start").IsError)
		defer stopRuneGUI(t, f.tool, ctx)
	}
	assert.True(t, executeRuneGUI(t, f.tool, chat, "start").IsError, "a running instance is not started twice")
	require.False(t, executeRuneGUI(t, f.tool, sub, "exec", "workspaceready").IsError)

	cmds := exec.commands()
	require.Len(t, cmds, 2)
	assert.Equal(t, "RUNE_DATADIR="+filepath.Join(f.base, "rune-chat-1"), cmds[0].Env[5])
	assert.Equal(t, "RUNE_DATADIR="+filepath.Join(f.base, "rune-sub-agent-7"), cmds[1].Env[5])
}

func TestRuneGUIScreenshot(t *testing.T) {
	pngData := screenshotPNG(t)
	tests := []struct {
		name string
		// file is the screenshot Rune saves, under the session's data
		// directory DATA or elsewhere under OUT.
		file      string
		reply     string
		wantError string
		wantKept  bool
	}{
		{
			name:  "returns the image and removes the file",
			file:  "DATA/screenshots/screenshot-1.png",
			reply: "ok DATA/screenshots/screenshot-1.png",
		},
		{
			name:      "a file outside the screenshot directory is left alone",
			file:      "OUT/elsewhere.png",
			reply:     "ok OUT/elsewhere.png",
			wantError: "outside DATA/screenshots",
			wantKept:  true,
		},
		{
			name:      "Rune reports an error",
			reply:     "error: could not take screenshot",
			wantError: "error: could not take screenshot",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := t.TempDir()
			var expand func(string) string
			exec := newFakeRuneExec(func(line string, stdout io.Writer) bool {
				reply := "ok"
				if line == "screenshot --reload" {
					if tt.file != "" {
						file := expand(tt.file)
						require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
						require.NoError(t, os.WriteFile(file, pngData, 0o600))
					}
					reply = expand(tt.reply)
				}
				_, _ = fmt.Fprintln(stdout, reply)
				return true
			})
			f := newRuneGUIFixture(t, exec)
			expand = strings.NewReplacer("DATA", filepath.Join(f.base, "rune-chat-1"), "OUT", out).Replace
			ctx := context.Background()
			require.False(t, executeRuneGUI(t, f.tool, ctx, "start").IsError)
			defer stopRuneGUI(t, f.tool, ctx)

			result := executeRuneGUI(t, f.tool, ctx, "screenshot")

			assert.Equal(t, []string{"screenshot --reload"}, exec.sent())
			if tt.wantError != "" {
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content, expand(tt.wantError))
			} else {
				require.False(t, result.IsError, result.Content)
				require.Len(t, result.MultiContent, 2)
				assert.Contains(t, result.MultiContent[0].Text, "Screenshot (")
				assert.Equal(t, llmapi.ContentPartTypeImageURL, result.MultiContent[1].Type)
				assert.True(t, strings.HasPrefix(result.MultiContent[1].ImageURL, "data:image/png;base64,"))
			}
			if tt.file != "" {
				_, err := os.Stat(expand(tt.file))
				if tt.wantKept {
					assert.NoError(t, err)
				} else {
					assert.ErrorIs(t, err, fs.ErrNotExist)
				}
			}
		})
	}
}

func TestRuneGUIStopsAnInstanceThatDoesNotReply(t *testing.T) {
	tests := []struct {
		name string
		ctx  func() context.Context
		want string
	}{
		{
			name: "timeout",
			ctx:  context.Background,
			want: `no reply to "exec slow"`,
		},
		{
			name: "canceled",
			ctx: func() context.Context {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				t.Cleanup(cancel)
				return ctx
			},
			want: `"exec slow" was canceled`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := newFakeRuneExec(func(line string, stdout io.Writer) bool {
				if line != "exec slow" {
					_, _ = fmt.Fprintln(stdout, "ok")
				}
				return true
			})
			exec.stderr = "starting\n"
			f := newRuneGUIFixture(t, exec)
			f.tool.replyTimeout = 50 * time.Millisecond
			require.False(t, executeRuneGUI(t, f.tool, context.Background(), "start").IsError)

			result := executeRuneGUI(t, f.tool, tt.ctx(), "exec", "exec slow")

			assert.True(t, result.IsError)
			assert.Contains(t, result.Content, tt.want)
			assert.Contains(t, result.Content, "stopped Rune")
			assert.Contains(t, result.Content, "starting")
			exec.waitExit(t)
			assert.Empty(t, exec.sentSignals(), "the instance exits once its stdin closes")
			next := executeRuneGUI(t, f.tool, context.Background(), "exec", "workspaceready")
			assert.Contains(t, next.Content, "Rune is not running")
		})
	}
}

func TestRuneGUIStop(t *testing.T) {
	tests := []struct {
		name      string
		holdOnEOF bool
		want      []syscall.Signal
	}{
		{name: "exits when stdin closes"},
		{name: "killed when it does not exit", holdOnEOF: true, want: []syscall.Signal{syscall.SIGKILL}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := newFakeRuneExec(replyOK)
			exec.holdOnEOF = tt.holdOnEOF
			f := newRuneGUIFixture(t, exec)
			f.tool.stopTimeout = 50 * time.Millisecond
			ctx := context.Background()
			require.False(t, executeRuneGUI(t, f.tool, ctx, "start").IsError)

			result := executeRuneGUI(t, f.tool, ctx, "stop")

			assert.Equal(t, "Stopped Rune.", result.Content)
			exec.waitExit(t)
			assert.Equal(t, tt.want, exec.sentSignals())
			assert.Equal(t, "Rune is not running.", executeRuneGUI(t, f.tool, ctx, "stop").Content)
			require.False(t, executeRuneGUI(t, f.tool, ctx, "start").IsError, "a stopped instance starts again")
			stopRuneGUI(t, f.tool, ctx)
		})
	}
}

func TestRuneGUIReportsAnUnexpectedExit(t *testing.T) {
	exec := newFakeRuneExec(func(string, io.Writer) bool { return false })
	exec.stderr = "panic: boom\n"
	exec.exitErr = errors.New("exit status 2")
	f := newRuneGUIFixture(t, exec)
	ctx := context.Background()
	require.False(t, executeRuneGUI(t, f.tool, ctx, "start").IsError)

	result := executeRuneGUI(t, f.tool, ctx, "exec", "workspaceready")

	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "Rune exited (exit status 2)")
	assert.Contains(t, result.Content, "panic: boom")
	assert.Equal(t, "stderr:\npanic: boom", executeRuneGUI(t, f.tool, ctx, "log").Content,
		"the log outlives the instance")
	require.False(t, executeRuneGUI(t, f.tool, ctx, "start").IsError, "an exited instance starts again")
}

func TestRuneGUIConfig(t *testing.T) {
	exec := newFakeRuneExec(replyOK)
	f := newRuneGUIFixture(t, exec)
	ctx := context.Background()
	agentConfig := filepath.Join(f.base, "rune-chat-1", "agent.yaml")
	const config = "include:\n  - $RUNE_DATADIR/etc/hud-ember-v2/config.yaml\n"

	result := executeRuneGUI(t, f.tool, ctx, "config", config)
	require.False(t, result.IsError, result.Content)
	assert.Contains(t, result.Content, "applies when Rune starts")
	require.False(t, executeRuneGUI(t, f.tool, ctx, "start").IsError)
	defer stopRuneGUI(t, f.tool, ctx)
	got, err := os.ReadFile(agentConfig)
	require.NoError(t, err)
	assert.Equal(t, config, string(got), "start keeps the agent's config")

	result = executeRuneGUI(t, f.tool, ctx, "config", "editor:\n  mode: vim\n")
	require.False(t, result.IsError, result.Content)
	assert.Contains(t, result.Content, "The next screenshot reloads it")
	got, err = os.ReadFile(agentConfig)
	require.NoError(t, err)
	assert.Equal(t, "editor:\n  mode: vim\n", string(got))
}

func TestRuneGUILog(t *testing.T) {
	exec := newFakeRuneExec(replyOK)
	exec.stderr = "flag: bad\n"
	f := newRuneGUIFixture(t, exec)
	ctx := context.Background()
	logPath := filepath.Join(f.base, "rune-chat-1", "debug.log")
	assert.Equal(t, "(no output)", executeRuneGUI(t, f.tool, ctx, "log").Content)
	require.NoError(t, os.MkdirAll(filepath.Dir(logPath), 0o700))
	require.NoError(t, os.WriteFile(logPath, []byte("an earlier instance\n"), 0o600))
	require.False(t, executeRuneGUI(t, f.tool, ctx, "start").IsError)
	defer stopRuneGUI(t, f.tool, ctx)
	// The fake logs before it reads its first line.
	require.False(t, executeRuneGUI(t, f.tool, ctx, "exec", "workspaceready").IsError)
	assert.Equal(t, "stderr:\nflag: bad", executeRuneGUI(t, f.tool, ctx, "log").Content,
		"start clears the log of an earlier instance")
	require.NoError(t, os.WriteFile(logPath, []byte("one\ntwo\nthree\n"), 0o600))

	assert.Equal(t, "one\ntwo\nthree\nstderr:\nflag: bad", executeRuneGUI(t, f.tool, ctx, "log").Content)
	assert.Equal(t, "two\nthree\nstderr:\nflag: bad", executeRuneGUI(t, f.tool, ctx, "log", "2").Content)
}

func TestRuneGUICloseStopsInstancesAndRemovesTheirData(t *testing.T) {
	exec := newFakeRuneExec(replyOK)
	f := newRuneGUIFixture(t, exec)
	f.tool.Close()
	assert.Empty(t, exec.commands(), "a tool that never ran has nothing to stop")

	chat := context.Background()
	sub := agent.WithDialogueID(context.Background(), "sub-1")
	for _, ctx := range []context.Context{chat, sub} {
		require.False(t, executeRuneGUI(t, f.tool, ctx, "start").IsError)
	}
	f.tool.Close()

	exec.waitExit(t)
	exec.waitExit(t)
	require.Eventually(t, func() bool {
		entries, err := os.ReadDir(f.base)
		return err == nil && len(entries) == 0
	}, 5*time.Second, 10*time.Millisecond, "the instances' data directories are removed")
	assert.DirExists(t, filepath.Join(f.host, "hud", "ember"), "the shared directories are kept")
}

func TestLookupRune(t *testing.T) {
	onPath := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(onPath, "rune"), []byte("#!/bin/sh\n"), 0o755))
	tests := []struct {
		name       string
		executable string
		want       string
		wantErr    string
	}{
		{name: "the Rune that runs the extension", executable: "/opt/rune-dev/bin/rune", want: "/opt/rune-dev/bin/rune"},
		{name: "not reported, with a rune on PATH", wantErr: "RUNE_EXECUTABLE is not set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PATH", onPath)
			t.Setenv("RUNE_EXECUTABLE", tt.executable)

			got, err := LookupRune()

			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
