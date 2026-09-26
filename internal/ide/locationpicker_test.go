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

package ide

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/locationsearch"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
)

func TestLocationPickerAliasesFromRuneStar(t *testing.T) {
	aliases := loadRuneStarAliases(t)

	cases := []struct {
		alias     string
		userArgs  []string
		wantShell string
	}{
		{
			alias:     "grep",
			userArgs:  []string{"TODO"},
			wantShell: "grep -n -R TODO",
		},
		{
			alias:     "grep",
			userArgs:  []string{"foo bar"},
			wantShell: `grep -n -R "foo bar"`,
		},
		{
			alias:     "todogrep",
			userArgs:  nil,
			wantShell: `grep -n -R -E "(TODO|FIXME)"`,
		},
		{
			alias:    "conflicts",
			userArgs: nil,
			wantShell: `git grep -n --column -E ` +
				`"^(<<<<<<<|=======$|>>>>>>>)"`,
		},
		{
			alias:     "gitgrep",
			userArgs:  []string{"TODO"},
			wantShell: "git grep -n --column -- TODO",
		},
		{
			alias:     "gitgrep",
			userArgs:  []string{"foo bar"},
			wantShell: `git grep -n --column -- "foo bar"`,
		},
		{
			// gitchanges hands awk a multi-line script as a
			// single token. After Layer 1 strips the wrapping
			// single quotes, locationpicker re-shell-quotes the
			// script in double quotes; the inner double quotes
			// from the alias body (`\"+++ b/\"` etc.) come
			// through Layer 1 as literal `"`s, which need
			// backslash-escaping when re-wrapped under
			// reshellQuoteArgs' double-quote scheme.
			alias:    "gitchanges",
			userArgs: nil,
			wantShell: `awk ` +
				`"BEGIN { cmd = \"git diff HEAD --no-color -U0\"` +
				`; while ((cmd | getline line) > 0) { if (` +
				`substr(line, 1, 6) == \"+++ b/\") f = substr(line` +
				`, 7); else if (substr(line, 1, 2) == \"@@\") { ` +
				`match(line, /[+][0-9]+/); print f \":\" substr(` +
				`line, RSTART+1, RLENGTH-1) \":1:\" line } } }"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.alias+" "+joinArgsForName(tc.userArgs), func(t *testing.T) {
			body, ok := aliases[tc.alias]
			require.True(t, ok, "alias %q missing from rune.star", tc.alias)
			require.Lenf(t, body, 1,
				"alias %q should expand to a single command", tc.alias)

			loader := newShellCmdCapturer()
			e := newExForTestingWithWorkspace(t, loader,
				texttest.NopEditor(), vte.DefaultConfig(),
				nopPublishEvent, clipboard.NewInMemory(),
				text.WithCommandKey(testCommandKey),
				text.WithCommandAliases(map[string]text.CommandAlias{
					tc.alias: {Commands: body},
				}),
			)

			_ = e.dispatchCommand(tc.alias, tc.userArgs...)

			cmd, ok := loader.wait(2 * time.Second)
			require.True(t, ok,
				":locationpicker for %q must reach the workspace executor",
				tc.alias)
			require.Equal(t, "-c", cmd.Args[0],
				"executor must receive a `$SHELL -c <cmd>` invocation")
			assert.Equal(t, tc.wantShell, cmd.Args[1],
				"shell command line for alias %q with args %v",
				tc.alias, tc.userArgs)
			loader.drain(time.Second)
			_ = e
		})
	}
}

func loadRuneStarAliases(t *testing.T) map[string][]string {
	t.Helper()
	data := readRuneStar(t)
	cfg, err := decodeStarlarkConfig(starlarkConfigSource{
		src:      data,
		filename: "rune.star",
		params:   map[string]any{"mode": "modal", "tui": false, "os": "darwin"},
	})
	require.NoError(t, err)

	cmd, ok := cfg["command"].(map[string]any)
	require.True(t, ok, "rune.star: missing `command` section")
	rawAliases, ok := cmd["aliases"].(map[string]any)
	require.True(t, ok, "rune.star: missing `command.aliases`")

	want := []string{"grep", "todogrep", "gitgrep", "conflicts", "gitchanges"}
	out := make(map[string][]string, len(want))
	for _, name := range want {
		v, ok := rawAliases[name]
		require.Truef(t, ok, "rune.star: missing alias %q", name)
		switch body := v.(type) {
		case string:
			out[name] = []string{body}
		case []any:
			cmds := make([]string, 0, len(body))
			for _, e := range body {
				cmds = append(cmds, e.(string))
			}
			out[name] = cmds
		default:
			t.Fatalf("rune.star: alias %q has unsupported type %T", name, v)
		}
	}
	return out
}

type shellCmdCapturer struct {
	testLoader
	mu   sync.Mutex
	cmd  *workspaceapi.Cmd
	ch   chan struct{}
	done chan struct{}
}

func newShellCmdCapturer() *shellCmdCapturer {
	return &shellCmdCapturer{
		ch:   make(chan struct{}, 1),
		done: make(chan struct{}, 1),
	}
}

func (c *shellCmdCapturer) StartCommand(
	_ context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	c.mu.Lock()
	if c.cmd == nil {
		copy := cmd
		c.cmd = &copy
		select {
		case c.ch <- struct{}{}:
		default:
		}
	}
	c.mu.Unlock()
	closeIfCloser(cmd.Stdout)
	closeIfCloser(cmd.Stderr)
	if cmd.Watcher != nil {
		go func() {
			defer func() { _ = recover() }()
			cmd.Watcher.WatchProcess() <- nil
			select {
			case c.done <- struct{}{}:
			default:
			}
		}()
	}
	return workspaceapi.Pid(1), nil
}

func closeIfCloser(w io.Writer) {
	if c, ok := w.(io.Closer); ok {
		_ = c.Close()
	}
}

func (c *shellCmdCapturer) wait(d time.Duration) (workspaceapi.Cmd, bool) {
	select {
	case <-c.ch:
	case <-time.After(d):
		return workspaceapi.Cmd{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return *c.cmd, true
}

func (c *shellCmdCapturer) drain(d time.Duration) {
	select {
	case <-c.done:
	case <-time.After(d):
	}
	time.Sleep(200 * time.Millisecond)
}

func joinArgsForName(args []string) string {
	if len(args) == 0 {
		return "no_args"
	}
	return strings.Join(args, "_")
}

// TestLocationPickerDispatchesCursorToMatch reproduces the bug where
// the gitgrep alias (and any :locationpicker invocation that emits
// `path:line:col:matched-content` lines) opened the target file but
// failed to position the editor cursor at the match. The preview pane
// works because it reads the file via the FileSystem directly, but
// pressing <enter> on the entry must also dispatch SetCursor to the
// editor that ends up in the previously focused window.
func TestLocationPickerDispatchesCursorToMatch(t *testing.T) {
	clip := clipboard.NewInMemory()
	e, fileScheme, tempDir := newExForTestingFileWorkspace(t, clip)
	defer func() { require.NoError(t, e.Close()) }()
	defer func() { require.NoError(t, fileScheme.Close()) }()

	const fileBody = "alpha line\nbeta line\ngamma line\n"
	relPath := "hello.go"
	require.NoError(t, os.WriteFile(
		filepath.Join(tempDir, relPath), []byte(fileBody), 0o644))

	// Resize before driving the locationpicker so the editor that
	// opens has non-zero scroll dimensions when SetCursorAtScroll
	// is called.
	e.Resize(80, 24)

	// Use `echo` to emit a single git-grep style line. The shell
	// invocation goes through the same wiring as :gitgrep:
	//   $SHELL -c "echo 'hello.go:2:3:beta line'"
	pickerLine := relPath + ":2:3:beta line"
	require.NoError(t, e.dispatchCommand(
		"locationpicker", "echo", pickerLine))

	// Wait for the picker to populate, then send <enter> so the
	// inner finder dispatches openResource → setContent → SetCursor.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if floatingHasSelection(e) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.True(t, floatingHasSelection(e),
		"locationpicker did not surface any entries within deadline")

	_, _ = e.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	// After <enter> the previously focused window should hold the
	// opened tab and its editor's cursor should be at line 2 col 3
	// (1-indexed in input → 0-indexed coords {Y:1, X:2}).
	uri, err := e.workspace.URI(relPath)
	require.NoError(t, err)

	// Look up the editor handler via the text.Component (which knows
	// how to find tabs by URI). The IDE-level fix wires the
	// locationpicker's Clients.Editor through the same
	// text.Component-backed adapter.
	deadline = time.Now().Add(2 * time.Second)
	var got term.Coordinates
	var found bool
	for time.Now().Before(deadline) {
		eh, err := e.comp.Editor(uri)
		if err == nil {
			got = eh.CursorAtScroll()
			found = true
			if got == (term.Coordinates{Y: 1, X: 2}) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	require.True(t, found,
		"editor handler for %q never became available after <enter>",
		uri.String())
	assert.Equal(t, term.Coordinates{Y: 1, X: 2}, got,
		"cursor must be dispatched to (line=2, col=3) when "+
			"locationpicker selects a path:line:col:matched entry; "+
			"if this fails the gitgrep alias regression is back")
}

// floatingHasSelection returns true when the locationpicker's inner
// finder has populated at least one entry. We type-assert the focused
// window's content back to a *locationsearch.Handler to peek at its
// Selection without exercising any UI-level hacks.
func floatingHasSelection(e testEx) bool {
	win, _ := e.Browser().Focus()
	if win == nil {
		return false
	}
	content, err := win.Content()
	if err != nil {
		return false
	}
	h, ok := content.(*locationsearch.Handler)
	if !ok {
		return false
	}
	_, ok = h.Selection()
	return ok
}
