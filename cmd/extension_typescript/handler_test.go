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

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/markdown"
)

const testServer = "/data/typescript/bin/tsgo"

// newTestTSHandler builds the console handler for the workspace at /ws,
// resolving testServer as the compiler of every server root.
func newTestTSHandler(fs *fakeFS, ex *fakeExecutor) *tsHandler {
	return newTestTSHandlerWith(fs, ex,
		func(context.Context, string) string { return testServer },
		func(context.Context) error { return nil })
}

func newTestTSHandlerWith(
	fs *fakeFS, ex *fakeExecutor,
	server func(context.Context, string) string, restart func(context.Context) error,
) *tsHandler {
	uri, err := workspaceapi.ParseURI("file:///ws")
	if err != nil {
		panic(err)
	}
	_, h := newTSHandler(tsHandlerConfig{
		exec: ex, fs: fs, wsURI: uri, focus: &focusTracker{wsDir: "/ws"},
		server: server, restart: restart,
	})
	return h.(*tsHandler)
}

// focusOn delivers the focus event the editor sends for the file at path.
func focusOn(t *testing.T, h *tsHandler, path string) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file://" + path)
	require.NoError(t, err)
	h.focus.Handle(context.Background(), textapi.Event{Type: textapi.EventTypeFocus, URI: uri})
}

func runTS(t *testing.T, h *tsHandler, args ...string) (string, error) {
	t.Helper()
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: tsCommandName, Args: args}, repl.NopProgressWriter())
	if err != nil {
		return "", err
	}
	return renderText(t, it), nil
}

// renderText draws the console output and returns its text, one line
// per row.
func renderText(t *testing.T, it iterator.Iterator[component.Responsive]) string {
	t.Helper()
	out, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	var b strings.Builder
	for _, c := range out {
		md, ok := c.(*markdown.Component)
		require.True(t, ok, "console output must be markdown, got %T", c)
		const width = 200
		height := md.Height(width)
		md.Resize(width, height)
		b.WriteString(md.TextRange(term.Coordinates{}, term.Coordinates{Y: height}))
	}
	return b.String()
}

func TestFocusTracker(t *testing.T) {
	event := func(typ textapi.EventType, path string) textapi.Event {
		uri, err := workspaceapi.ParseURI("file://" + path)
		require.NoError(t, err)
		return textapi.Event{Type: typ, URI: uri}
	}
	tests := []struct {
		name   string
		events []textapi.Event
		want   string
	}{
		{"nothing focused yet", nil, "/ws"},
		{"a focused workspace file", []textapi.Event{event(textapi.EventTypeFocus, "/ws/web/src/a.ts")}, "/ws/web/src"},
		{"the latest focus wins", []textapi.Event{
			event(textapi.EventTypeFocus, "/ws/web/src/a.ts"),
			event(textapi.EventTypeFocus, "/ws/api/b.ts"),
		}, "/ws/api"},
		{"a file outside the workspace is ignored", []textapi.Event{
			event(textapi.EventTypeFocus, "/ws/web/src/a.ts"),
			event(textapi.EventTypeFocus, "/elsewhere/c.ts"),
		}, "/ws/web/src"},
		{"losing the focus keeps it", []textapi.Event{
			event(textapi.EventTypeFocus, "/ws/web/src/a.ts"),
			event(textapi.EventTypeUnfocus, "/ws/web/src/a.ts"),
			event(textapi.EventTypeOpen, "/ws/api/b.ts"),
		}, "/ws/web/src"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &focusTracker{wsDir: "/ws"}
			for _, ev := range tt.events {
				assert.False(t, f.Handle(context.Background(), ev))
			}
			assert.Equal(t, tt.want, f.lastDir())
		})
	}
}

func TestTSHandlerCheck(t *testing.T) {
	const solution = `{
  // Vite's layout: the root only references the real projects.
  "files": [],
  "references": [{ "path": "./tsconfig.app.json" }, { "path": "./tsconfig.node.json" },],
}`
	monorepo := func() *fakeFS {
		return newFakeFS("/ws").writeFile("package.json", `{"workspaces": ["packages/*"]}`).
			writeFile("packages/a/tsconfig.json", `{}`).writeFile("packages/b/tsconfig.json", `{}`).
			addFile("packages/b/src/use.ts").
			writeFile("node_modules/dep/tsconfig.json", `{}`)
	}
	failure := scriptedCmd{
		stdout: "src/a.ts(1,14): error TS2322: Type 'string' is not assignable to type 'number'.\n",
		err:    errors.New("exit status 1"),
	}
	tests := []struct {
		name    string
		fs      func() *fakeFS
		focus   string
		args    []string
		results map[string]scriptedCmd
		wantRun []string
		want    []string
		wantErr string
	}{
		{
			name:    "workspace project",
			fs:      func() *fakeFS { return newFakeFS("/ws").writeFile("tsconfig.json", `{}`) },
			wantRun: []string{testServer + " --noEmit --pretty false -p tsconfig.json"},
			want:    []string{"No type errors in ."},
		},
		{
			name:    "named project",
			fs:      func() *fakeFS { return newFakeFS("/ws").writeFile("web/tsconfig.json", `{}`) },
			args:    []string{"web"},
			wantRun: []string{testServer + " --noEmit --pretty false -p web/tsconfig.json"},
			want:    []string{"No type errors in web"},
		},
		{
			name:    "named config file",
			fs:      func() *fakeFS { return newFakeFS("/ws").writeFile("web/tsconfig.build.json", `{}`) },
			args:    []string{"web/tsconfig.build.json"},
			wantRun: []string{testServer + " --noEmit --pretty false -p web/tsconfig.build.json"},
			want:    []string{"No type errors in web/tsconfig.build.json"},
		},
		{
			name:    "a project with references is built",
			fs:      func() *fakeFS { return newFakeFS("/ws").writeFile("tsconfig.json", solution) },
			wantRun: []string{testServer + " -b --pretty false tsconfig.json"},
			want:    []string{"No type errors in ."},
		},
		{
			name: "every project below a directory without one",
			fs:   monorepo,
			wantRun: []string{
				testServer + " --noEmit --pretty false -p packages/a/tsconfig.json",
				testServer + " --noEmit --pretty false -p packages/b/tsconfig.json",
			},
			want: []string{"No type errors in the projects below .:", "packages/a/tsconfig.json", "packages/b/tsconfig.json"},
		},
		{
			name:    "the focused file's project",
			fs:      monorepo,
			focus:   "/ws/packages/b/src/use.ts",
			wantRun: []string{testServer + " --noEmit --pretty false -p packages/b/tsconfig.json"},
			want:    []string{"No type errors in packages/b"},
		},
		{
			name:    "a named project wins over the focus",
			fs:      monorepo,
			focus:   "/ws/packages/b/src/use.ts",
			args:    []string{"packages/a"},
			wantRun: []string{testServer + " --noEmit --pretty false -p packages/a/tsconfig.json"},
			want:    []string{"No type errors in packages/a"},
		},
		{
			name: "type errors fail the command",
			fs:   func() *fakeFS { return newFakeFS("/ws").writeFile("tsconfig.json", `{}`) },
			results: map[string]scriptedCmd{
				testServer + " --noEmit --pretty false -p tsconfig.json": failure,
			},
			wantErr: "tsgo --noEmit --pretty false -p tsconfig.json: exit status 1\n" +
				"src/a.ts(1,14): error TS2322: Type 'string' is not assignable to type 'number'.",
		},
		{
			name: "every failing project is reported",
			fs:   monorepo,
			results: map[string]scriptedCmd{
				testServer + " --noEmit --pretty false -p packages/a/tsconfig.json": {},
				testServer + " --noEmit --pretty false -p packages/b/tsconfig.json": failure,
			},
			wantErr: "tsgo --noEmit --pretty false -p packages/b/tsconfig.json: exit status 1\n" +
				"src/a.ts(1,14): error TS2322: Type 'string' is not assignable to type 'number'.",
		},
		{
			name:    "no tsconfig.json anywhere",
			fs:      func() *fakeFS { return newFakeFS("/ws").addFile("src/a.ts") },
			wantErr: "no tsconfig.json in `.` or below",
		},
		{
			name:    "one project at a time",
			fs:      func() *fakeFS { return newFakeFS("/ws") },
			args:    []string{"web", "api"},
			wantErr: "usage: ts check [<project>]",
		},
		{
			name:    "a project outside the workspace",
			fs:      func() *fakeFS { return newFakeFS("/ws") },
			args:    []string{"../other"},
			wantErr: "../other is outside the workspace",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newFakeExecutor()
			for _, run := range tt.wantRun {
				ex.respond(run, scriptedCmd{})
			}
			for run, result := range tt.results {
				ex.respond(run, result)
			}
			h := newTestTSHandler(tt.fs(), ex)
			if tt.focus != "" {
				focusOn(t, h, tt.focus)
			}
			out, err := runTS(t, h, append([]string{"check"}, tt.args...)...)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			for _, w := range tt.want {
				assert.Contains(t, out, w)
			}
			assert.Equal(t, tt.wantRun, ex.callsSnapshot())
			for _, dir := range ex.dirs {
				assert.Equal(t, "/ws", dir)
			}
		})
	}
}

func TestTSHandlerServerRoot(t *testing.T) {
	fs := newFakeFS("/ws").writeFile("web/package.json", `{"workspaces": ["packages/*"]}`).
		writeFile("web/packages/b/package.json", `{}`).writeFile("web/packages/b/tsconfig.json", `{}`).
		addFile("web/packages/b/src/use.ts").addFile("scripts/gen.ts")
	tests := []struct {
		name  string
		focus string
		args  []string
		want  string
	}{
		{"a package is served from its monorepo", "/ws/web/packages/b/src/use.ts", []string{"check"}, "/ws/web"},
		{"a named package is too", "", []string{"version", "web/packages/b"}, "/ws/web"},
		{"a file outside any project is served from the workspace", "/ws/scripts/gen.ts", []string{"version"}, "/ws"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var roots []string
			ex := newFakeExecutor().
				respond(testServer+" --noEmit --pretty false -p web/packages/b/tsconfig.json", scriptedCmd{}).
				respond(testServer+" --version", scriptedCmd{stdout: "Version 7.0.2\n"})
			h := newTestTSHandlerWith(fs, ex,
				func(_ context.Context, root string) string {
					roots = append(roots, root)
					return testServer
				}, nil)
			if tt.focus != "" {
				focusOn(t, h, tt.focus)
			}
			_, err := runTS(t, h, tt.args...)
			require.NoError(t, err)
			assert.Equal(t, []string{tt.want}, roots)
		})
	}
}

func TestTSHandlerWithoutServer(t *testing.T) {
	h := newTestTSHandlerWith(newFakeFS("/ws").writeFile("tsconfig.json", `{}`), newFakeExecutor(),
		func(context.Context, string) string { return "" }, nil)
	for _, sub := range []string{"check", "version"} {
		_, err := runTS(t, h, sub)
		assert.ErrorIs(t, err, errNoServer, sub)
	}
}

func TestTSHandlerPackageManager(t *testing.T) {
	const manifest = `{"scripts": {"build": "tsc -b", "test": "vitest"}}`
	bunWorkspace := func() *fakeFS {
		return newFakeFS("/ws").writeFile("package.json", `{"workspaces": ["packages/*"], "scripts": {"build": "turbo build"}}`).
			addFile("bun.lock").
			writeFile("packages/b/package.json", manifest).addFile("packages/b/src/use.ts").
			addDir("packages/b/src/utils")
	}
	tests := []struct {
		name    string
		fs      func() *fakeFS
		focus   string
		args    []string
		wantRun string
		wantDir string
		want    string
	}{
		{
			name:    "npm install",
			fs:      func() *fakeFS { return newFakeFS("/ws").writeFile("package.json", manifest) },
			args:    []string{"install"},
			wantRun: "npm install",
		},
		{
			name: "yarn install passes its flags",
			fs: func() *fakeFS {
				return newFakeFS("/ws").writeFile("package.json", manifest).addFile("yarn.lock")
			},
			args:    []string{"install", "--frozen-lockfile"},
			wantRun: "yarn install --frozen-lockfile",
		},
		{
			name: "npm run separates the script's flags",
			fs: func() *fakeFS {
				return newFakeFS("/ws").writeFile("package.json", manifest).addFile("package-lock.json")
			},
			args:    []string{"run", "build", "--watch"},
			wantRun: "npm run build -- --watch",
		},
		{
			name: "pinned pnpm runs the script",
			fs: func() *fakeFS {
				return newFakeFS("/ws").writeFile("package.json",
					`{"packageManager": "pnpm@9.1.0", "scripts": {"build": "tsc -b"}}`)
			},
			args:    []string{"run", "build", "--watch"},
			wantRun: "pnpm run build --watch",
		},
		{
			name: "bun test runs the test script, not bun's runner",
			fs: func() *fakeFS {
				return newFakeFS("/ws").writeFile("package.json", manifest).addFile("bun.lock")
			},
			args:    []string{"test", "--coverage"},
			wantRun: "bun run test --coverage",
		},
		{
			name: "bun test without a test script runs bun's runner",
			fs: func() *fakeFS {
				return newFakeFS("/ws").writeFile("package.json", `{}`).addFile("bun.lock")
			},
			args:    []string{"test", "--bail"},
			wantRun: "bun test --bail",
		},
		{
			name: "pinned bun without a test script runs bun's runner",
			fs: func() *fakeFS {
				return newFakeFS("/ws").writeFile("package.json",
					`{"packageManager": "bun@1.4.2", "scripts": {"build": "bun build ./index.ts"}}`)
			},
			args:    []string{"test"},
			wantRun: "bun test",
		},
		{
			name:    "the focused file's package uses its workspace's manager",
			fs:      bunWorkspace,
			focus:   "/ws/packages/b/src/use.ts",
			args:    []string{"test"},
			wantRun: "bun run test",
			wantDir: "/ws/packages/b",
			want:    "In packages/b:",
		},
		{
			name:    "a named package",
			fs:      bunWorkspace,
			args:    []string{"run", "packages/b/", "build", "--watch"},
			wantRun: "bun run build --watch",
			wantDir: "/ws/packages/b",
		},
		{
			name:    "dot names the workspace root over the focus",
			fs:      bunWorkspace,
			focus:   "/ws/packages/b/src/use.ts",
			args:    []string{"run", ".", "build"},
			wantRun: "bun run build",
		},
		{
			name:    "a path that is not a package is the runner's",
			fs:      bunWorkspace,
			focus:   "/ws/packages/b/src/use.ts",
			args:    []string{"test", "src/utils/"},
			wantRun: "bun run test src/utils/",
			wantDir: "/ws/packages/b",
		},
		{
			name: "a corepack pin above the package",
			fs: func() *fakeFS {
				return newFakeFS("/ws").writeFile("package.json", `{"packageManager": "pnpm@9.1.0"}`).
					writeFile("packages/b/package.json", manifest)
			},
			args:    []string{"install", "packages/b/"},
			wantRun: "pnpm install",
			wantDir: "/ws/packages/b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newFakeExecutor().respond(tt.wantRun, scriptedCmd{stdout: "done\n"})
			h := newTestTSHandler(tt.fs(), ex)
			if tt.focus != "" {
				focusOn(t, h, tt.focus)
			}
			out, err := runTS(t, h, tt.args...)
			require.NoError(t, err)
			assert.Contains(t, out, "done")
			if tt.want != "" {
				assert.Contains(t, out, tt.want)
			}
			wantDir := tt.wantDir
			if wantDir == "" {
				wantDir = "/ws"
			}
			assert.Equal(t, []string{tt.wantRun}, ex.callsSnapshot())
			assert.Equal(t, []string{wantDir}, ex.dirs)
		})
	}
}

func TestTSHandlerScriptErrors(t *testing.T) {
	const manifest = `{"scripts": {"test": "vitest"}}`
	failure := scriptedCmd{
		stdout: "> test\n> vitest\n",
		stderr: "FAIL src/a.test.ts\n",
		err:    errors.New("exit status 1"),
	}
	tests := []struct {
		name    string
		fs      *fakeFS
		args    []string
		wantErr string
	}{
		{
			name:    "no package.json",
			fs:      newFakeFS("/ws"),
			args:    []string{"install"},
			wantErr: "no package.json in /ws",
		},
		{
			name:    "unknown script",
			fs:      newFakeFS("/ws").writeFile("package.json", manifest),
			args:    []string{"run", "deploy"},
			wantErr: `package.json has no "deploy" script`,
		},
		{
			name:    "no test script",
			fs:      newFakeFS("/ws").writeFile("package.json", `{}`),
			args:    []string{"test"},
			wantErr: `package.json has no "test" script`,
		},
		{
			name:    "a failing script reports its output",
			fs:      newFakeFS("/ws").writeFile("package.json", manifest),
			args:    []string{"test"},
			wantErr: "npm run test: exit status 1\n> test\n> vitest\nFAIL src/a.test.ts",
		},
		{
			name:    "a package's failing script names the package",
			fs:      newFakeFS("/ws").writeFile("web/package.json", manifest),
			args:    []string{"test", "web/"},
			wantErr: "npm run test (in web): exit status 1\n> test\n> vitest\nFAIL src/a.test.ts",
		},
		{
			name:    "a package's unknown script names its manifest",
			fs:      newFakeFS("/ws").writeFile("web/package.json", manifest),
			args:    []string{"run", "web/", "deploy"},
			wantErr: `web/package.json has no "deploy" script`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newFakeExecutor().respond("npm run test", failure)
			_, err := runTS(t, newTestTSHandler(tt.fs, ex), tt.args...)
			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestTSHandlerTranscriptIncludesStderr(t *testing.T) {
	fs := newFakeFS("/ws").writeFile("package.json", `{"scripts": {"test": "jest"}}`)
	ex := newFakeExecutor().respond("npm run test",
		scriptedCmd{stdout: "> test\n", stderr: "Tests: 3 passed, 3 total\n"})
	out, err := runTS(t, newTestTSHandler(fs, ex), "test")
	require.NoError(t, err)
	assert.Contains(t, out, "> test")
	assert.Contains(t, out, "Tests: 3 passed, 3 total")
}

func TestTSHandlerListScripts(t *testing.T) {
	tests := []struct {
		name  string
		fs    *fakeFS
		focus string
		want  []string
	}{
		{"scripts with their commands",
			newFakeFS("/ws").writeFile("package.json", `{"scripts": {"test": "vitest", "build": "tsc -b"}}`), "",
			[]string{"Scripts of package.json", "build", "tsc -b", "test", "vitest"}},
		{"no scripts", newFakeFS("/ws").writeFile("package.json", `{"name": "app"}`), "",
			[]string{"package.json defines no scripts."}},
		{"the focused file's package",
			newFakeFS("/ws").writeFile("package.json", `{"scripts": {"build": "turbo build"}}`).
				writeFile("web/package.json", `{"scripts": {"dev": "vite"}}`).addFile("web/src/main.ts"),
			"/ws/web/src/main.ts", []string{"Scripts of web/package.json", "dev", "vite"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newFakeExecutor()
			h := newTestTSHandler(tt.fs, ex)
			if tt.focus != "" {
				focusOn(t, h, tt.focus)
			}
			out, err := runTS(t, h, "run")
			require.NoError(t, err)
			for _, w := range tt.want {
				assert.Contains(t, out, w)
			}
			assert.Empty(t, ex.callsSnapshot())
		})
	}
}

func TestTSHandlerVersion(t *testing.T) {
	tests := []struct {
		name        string
		fs          func() *fakeFS
		focus       string
		wantProject string
	}{
		{"no project typescript", func() *fakeFS { return newFakeFS("/ws") }, "", ""},
		{
			name: "project pins an older typescript",
			fs: func() *fakeFS {
				return newFakeFS("/ws").writeFile("node_modules/typescript/package.json", `{"version": "5.6.3"}`)
			},
			wantProject: "Project: TypeScript 5.6.3 (node_modules/typescript)",
		},
		{
			name: "project runs the same compiler",
			fs: func() *fakeFS {
				return newFakeFS("/ws").writeFile("node_modules/typescript/package.json", `{"version": "7.0.2"}`)
			},
		},
		{
			name: "a package resolves its workspace's typescript",
			fs: func() *fakeFS {
				return newFakeFS("/ws").writeFile("web/node_modules/typescript/package.json", `{"version": "5.9.3"}`).
					writeFile("web/packages/b/package.json", `{}`).addFile("web/packages/b/src/use.ts")
			},
			focus:       "/ws/web/packages/b/src/use.ts",
			wantProject: "Project: TypeScript 5.9.3 (web/node_modules/typescript)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newFakeExecutor().respond(testServer+" --version", scriptedCmd{stdout: "Version 7.0.2\n"})
			h := newTestTSHandler(tt.fs(), ex)
			if tt.focus != "" {
				focusOn(t, h, tt.focus)
			}
			out, err := runTS(t, h, "version")
			require.NoError(t, err)
			assert.Contains(t, out, "Language server: TypeScript 7.0.2 ("+testServer+")")
			if tt.wantProject == "" {
				assert.NotContains(t, out, "Project:")
				return
			}
			assert.Contains(t, out, tt.wantProject)
		})
	}
}

func TestTSHandlerRestart(t *testing.T) {
	restarts := 0
	failure := errors.New("rebuild failed")
	var result error
	h := newTestTSHandlerWith(newFakeFS("/ws"), newFakeExecutor(), nil,
		func(context.Context) error { restarts++; return result })

	out, err := runTS(t, h, "restart")
	require.NoError(t, err)
	assert.Contains(t, out, "Restarted the TypeScript language servers.")

	result = failure
	_, err = runTS(t, h, "restart")
	require.ErrorIs(t, err, failure)
	assert.Equal(t, 2, restarts)
}

func TestTSHandlerUsage(t *testing.T) {
	h := newTestTSHandler(newFakeFS("/ws"), newFakeExecutor())
	_, err := runTS(t, h, "frobnicate")
	require.EqualError(t, err, "unknown command: frobnicate")

	for _, args := range [][]string{nil, {"help"}, {"help", "run"}} {
		out, err := runTS(t, h, args...)
		require.NoError(t, err, args)
		assert.Contains(t, out, "run", args)
		assert.Contains(t, out, "Without a project, a subcommand works in the nearest directory", args)
	}
}

func TestTSHandlerComplete(t *testing.T) {
	withScripts := func() *fakeFS {
		return newFakeFS("/ws").writeFile("package.json",
			`{"scripts": {"build": "tsc -b", "bench": "vitest bench", "test": "vitest"}}`)
	}
	workspace := func() *fakeFS {
		return withScripts().writeFile("tsconfig.json", `{}`).
			writeFile("packages/api/package.json", `{"scripts": {"dev": "tsx watch", "deploy": "fly deploy"}}`).
			writeFile("packages/api/tsconfig.json", `{}`).addFile("packages/api/src/main.ts").
			writeFile("packages/web/package.json", `{}`).
			writeFile("node_modules/dep/package.json", `{}`)
	}
	tests := []struct {
		name  string
		fs    func() *fakeFS
		focus string
		args  []string
		want  []string
	}{
		{"every subcommand", func() *fakeFS { return newFakeFS("/ws") }, "", nil, tsSubcommandNames},
		{"subcommand prefix", func() *fakeFS { return newFakeFS("/ws") }, "", []string{"r"},
			[]string{"restart", "run"}},
		{"script names", withScripts, "", []string{"run", ""}, []string{".", "bench", "build", "test"}},
		{"script prefix", withScripts, "", []string{"run", "b"}, []string{"bench", "build"}},
		{"no manifest", func() *fakeFS { return newFakeFS("/ws") }, "", []string{"run", ""}, nil},
		{"projects to check", workspace, "", []string{"check", ""}, []string{".", "packages/api/"}},
		{"packages to test", workspace, "", []string{"test", "p"}, []string{"packages/api/", "packages/web/"}},
		{"projects of every kind for version", workspace, "", []string{"version", ""},
			[]string{".", "packages/api/", "packages/web/"}},
		{"packages and the focused package's scripts", workspace, "/ws/packages/api/src/main.ts",
			[]string{"run", ""}, []string{".", "packages/api/", "packages/web/", "deploy", "dev"}},
		{"a named package's scripts", workspace, "", []string{"run", "packages/api/", "d"},
			[]string{"deploy", "dev"}},
		{"a script's arguments", workspace, "", []string{"run", "build", ""}, nil},
		{"other arguments", workspace, "", []string{"restart", ""}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestTSHandler(tt.fs(), newFakeExecutor())
			if tt.focus != "" {
				focusOn(t, h, tt.focus)
			}
			it, err := h.Complete(context.Background(), tsCommandName, tt.args)
			require.NoError(t, err)
			got, err := iterator.ToSlice(context.Background(), it)
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}
