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
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/extension/langext"
	"unstable.build/rune/internal/extension/langext/langexttest"
)

const testClangd = "/data/ccpp/bin/clangd"

// testHandler is a `ccpp` console handler over fakes, with the knobs a
// test turns.
type testHandler struct {
	*ccppHandler
	fs        *fakeFS
	exec      *fakeExecutor
	notify    *fakeNotifications
	restarts  int
	restarter error
}

func newTestHandler(fs *fakeFS, cfg config.Config) *testHandler {
	th := &testHandler{fs: fs, exec: newFakeExecutor(), notify: newFakeNotifications()}
	th.exec.respond(testClangd+" --version", scriptedCmd{stdout: "clangd version 21.1.0\n"})
	uri, err := workspaceapi.ParseURI("file:///ws")
	if err != nil {
		panic(err)
	}
	// No ccpp package is installed, so Bear is found on PATH or not at all.
	notInstalled := &langexttest.Installer{Err: fmt.Errorf("ccpp: %w", pkgapi.ErrNotInstalled)}
	tools := langext.NewInitializer(context.Background(), nil, nil, notInstalled, langext.ProjectConfig{
		LanguageID: ccppLanguageID, Tools: ccppToolNames,
	}).Tools()
	state := &serverState{}
	restart := func(context.Context) error {
		th.restarts++
		return th.restarter
	}
	_, h := newCcppHandler(ccppHandlerConfig{
		exec:   th.exec,
		fs:     fs,
		cfg:    cfg,
		notify: th.notify,
		wsRoot: langext.Root{Dir: "/ws", URI: "file:///ws"},
		focus:  &focusTracker{wsDir: "/ws"},
		compdb: newCompdbSetting(storagestub.NewInMemoryService(), uri),
		state:  state,
		gen: &compdbGenerator{
			fs: fs, exec: th.exec, notify: th.notify, state: state, restart: restart,
			bear: func(ctx context.Context) string {
				return resolveBear(ctx, newFakeNotifications(), th.exec, tools)
			},
		},
		clangd:  func(context.Context) string { return testClangd },
		restart: restart,
	})
	th.ccppHandler = h.(*ccppHandler)
	return th
}

// focusOn delivers the focus event the editor sends for the file at path.
func focusOn(t *testing.T, h *ccppHandler, path string) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file://" + path)
	require.NoError(t, err)
	h.focus.Handle(context.Background(), textapi.Event{Type: textapi.EventTypeFocus, URI: uri})
}

func runCcpp(t *testing.T, h *ccppHandler, args ...string) (string, error) {
	t.Helper()
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: ccppCommandName, Args: args}, repl.NopProgressWriter())
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
		{"nothing focused yet", nil, ""},
		{"a focused workspace file", []textapi.Event{event(textapi.EventTypeFocus, "/ws/src/a.c")}, "/ws/src/a.c"},
		{"the latest focus wins", []textapi.Event{
			event(textapi.EventTypeFocus, "/ws/src/a.c"),
			event(textapi.EventTypeFocus, "/ws/lib/b.cpp"),
		}, "/ws/lib/b.cpp"},
		{"a file outside the workspace is ignored", []textapi.Event{
			event(textapi.EventTypeFocus, "/ws/src/a.c"),
			event(textapi.EventTypeFocus, "/usr/include/stdio.h"),
		}, "/ws/src/a.c"},
		{"losing the focus keeps it", []textapi.Event{
			event(textapi.EventTypeFocus, "/ws/src/a.c"),
			event(textapi.EventTypeUnfocus, "/ws/src/a.c"),
			event(textapi.EventTypeOpen, "/ws/lib/b.cpp"),
		}, "/ws/src/a.c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &focusTracker{wsDir: "/ws"}
			for _, ev := range tt.events {
				assert.False(t, f.Handle(context.Background(), ev))
			}
			assert.Equal(t, tt.want, f.lastFile())
		})
	}
}

func TestCcppHandlerStatus(t *testing.T) {
	t.Run("before any bring-up, no file focused", func(t *testing.T) {
		h := newTestHandler(newFakeFS(), nil)
		out, err := runCcpp(t, h.ccppHandler, "status")
		require.NoError(t, err)
		assert.Contains(t, out, testClangd)
		assert.Contains(t, out, "clangd version 21.1.0")
		assert.Contains(t, out, "not started")
		assert.Contains(t, out, "Focus a C or C++ file")
	})
	t.Run("running, database found by clangd", func(t *testing.T) {
		fs := newFakeFS().addFile("/ws/CMakeLists.txt").addFile("/ws/build/compile_commands.json")
		h := newTestHandler(fs, nil)
		h.state.running(clangdOptions{bin: testClangd, logLevel: "error"})
		focusOn(t, h.ccppHandler, "/ws/src/main.cpp")
		out, err := runCcpp(t, h.ccppHandler, "status")
		require.NoError(t, err)
		assert.Contains(t, out, "running as "+testClangd+" --background-index")
		assert.Contains(t, out, "src/main.cpp")
		assert.Contains(t, out, "Project: . (CMakeLists.txt)")
		assert.Contains(t, out, "build/compile_commands.json (found by clangd)")
	})
	t.Run("failed bring-up", func(t *testing.T) {
		h := newTestHandler(newFakeFS(), nil)
		h.state.failed(errors.New("no clangd found"))
		out, err := runCcpp(t, h.ccppHandler, "status")
		require.NoError(t, err)
		assert.Contains(t, out, "failed to start: no clangd found")
	})
	t.Run("override and config are named as such", func(t *testing.T) {
		fs := newFakeFS().addFile("/ws/out/compile_commands.json").addFile("/ws/build/compile_commands.json")
		cfg := newStubConfig(map[string]string{"compile_commands_dir": "build"})
		h := newTestHandler(fs, cfg)
		focusOn(t, h.ccppHandler, "/ws/a.c")
		out, err := runCcpp(t, h.ccppHandler, "status")
		require.NoError(t, err)
		assert.Contains(t, out, "build/compile_commands.json (extensions.ccpp.config.compile_commands_dir)")

		require.NoError(t, h.compdb.set(context.Background(), "/ws/out"))
		out, err = runCcpp(t, h.ccppHandler, "status")
		require.NoError(t, err)
		assert.Contains(t, out, "out/compile_commands.json (set with ccpp compdb use)")
	})
	t.Run("no database points at a candidate or at how to make one", func(t *testing.T) {
		h := newTestHandler(newFakeFS().addFile("/ws/CMakeLists.txt"), nil)
		focusOn(t, h.ccppHandler, "/ws/a.c")
		out, err := runCcpp(t, h.ccppHandler, "status")
		require.NoError(t, err)
		assert.Contains(t, out, "none; clangd falls back to clang <file>")
		assert.Contains(t, out, "ccpp compdb generate writes one by running "+
			"cmake -S . -B build -DCMAKE_EXPORT_COMPILE_COMMANDS=ON")
		assert.NotContains(t, out, "bear")

		fs := newFakeFS().addFile("/ws/Makefile").addFile("/ws/cmake-build-debug/compile_commands.json").
			addReadDir("/ws", fakeDirEntry{name: "cmake-build-debug", dir: true}).
			addReadDir("/ws/cmake-build-debug")
		h = newTestHandler(fs, nil)
		focusOn(t, h.ccppHandler, "/ws/src/a.c")
		out, err = runCcpp(t, h.ccppHandler, "status")
		require.NoError(t, err)
		assert.Contains(t, out, "ccpp compdb use cmake-build-debug")
	})
	t.Run("an autotools tree is one project advised to record its build", func(t *testing.T) {
		fs := newFakeFS().
			addFile("/ws/configure.ac").addFile("/ws/configure").
			addFile("/ws/Makefile.in").addFile("/ws/Makefile").
			addFile("/ws/src/Makefile.in").addFile("/ws/src/Makefile")
		h := newTestHandler(fs, nil)
		focusOn(t, h.ccppHandler, "/ws/src/logd.c")
		out, err := runCcpp(t, h.ccppHandler, "status")
		require.NoError(t, err)
		assert.Contains(t, out, "Project: . (configure.ac, Makefile)")
		assert.Contains(t, out, "ccpp compdb generate writes one by running bear --append -- make.")
		assert.NotContains(t, out, "cmake")
		assert.NotContains(t, out, "meson")
	})
	t.Run("a project Rune cannot generate for gets the recipe", func(t *testing.T) {
		h := newTestHandler(newFakeFS().addFile("/ws/MODULE.bazel"), nil)
		focusOn(t, h.ccppHandler, "/ws/a.cc")
		out, err := runCcpp(t, h.ccppHandler, "status")
		require.NoError(t, err)
		assert.Contains(t, out, "bazel run @hedron_compile_commands//:refresh_all")
		assert.NotContains(t, out, "ccpp compdb generate")
	})
	t.Run("a non C file focused", func(t *testing.T) {
		h := newTestHandler(newFakeFS(), nil)
		focusOn(t, h.ccppHandler, "/ws/README.md")
		out, err := runCcpp(t, h.ccppHandler, "status")
		require.NoError(t, err)
		assert.Contains(t, out, "Focus a C or C++ file")
	})
}

// buildTreeFS is a workspace with a CMake project at lib/ that has two
// build directories, one pinned.
func buildTreeFS() *fakeFS {
	return newFakeFS().
		addFile("/ws/lib/CMakeLists.txt").
		addFile("/ws/lib/build/compile_commands.json").
		addFile("/ws/lib/out/Debug/compile_commands.json").
		addReadDir("/ws/lib",
			fakeDirEntry{name: "CMakeLists.txt"},
			fakeDirEntry{name: "build", dir: true},
			fakeDirEntry{name: "out", dir: true},
			fakeDirEntry{name: "src", dir: true}).
		addReadDir("/ws/lib/build").
		addReadDir("/ws/lib/out", fakeDirEntry{name: "Debug", dir: true}).
		addReadDir("/ws/lib/out/Debug")
}

func TestCcppHandlerCompdb(t *testing.T) {
	t.Run("lists the focused project's candidates", func(t *testing.T) {
		h := newTestHandler(buildTreeFS(), nil)
		focusOn(t, h.ccppHandler, "/ws/lib/src/a.cpp")
		out, err := runCcpp(t, h.ccppHandler, "compdb")
		require.NoError(t, err)
		assert.Contains(t, out, "Compilation databases below lib")
		assert.Contains(t, out, "lib/build\n")
		assert.Contains(t, out, "lib/out/Debug\n")
		assert.NotContains(t, out, "(active)")
		assert.Contains(t, out, "clangd picks the nearest one")
	})
	t.Run("use pins a candidate, restarts and marks it active", func(t *testing.T) {
		h := newTestHandler(buildTreeFS(), nil)
		h.state.running(clangdOptions{bin: testClangd})
		focusOn(t, h.ccppHandler, "/ws/lib/src/a.cpp")
		out, err := runCcpp(t, h.ccppHandler, "compdb", "use", "lib/out/Debug")
		require.NoError(t, err)
		assert.Contains(t, out, "Using lib/out/Debug/compile_commands.json. clangd restarted.")
		assert.Equal(t, 1, h.restarts)
		dir, ok, err := h.compdb.get(context.Background())
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, "/ws/lib/out/Debug", dir)

		out, err = runCcpp(t, h.ccppHandler, "compdb")
		require.NoError(t, err)
		assert.Contains(t, out, "lib/out/Debug (active)")

		out, err = runCcpp(t, h.ccppHandler, "compdb", "clear")
		require.NoError(t, err)
		assert.Contains(t, out, "Dropped the compilation database override")
		assert.Contains(t, out, "clangd restarted.")
		assert.Equal(t, 2, h.restarts)
		_, ok, err = h.compdb.get(context.Background())
		require.NoError(t, err)
		assert.False(t, ok)
	})
	t.Run("use before the server started defers to the first start", func(t *testing.T) {
		h := newTestHandler(buildTreeFS(), nil)
		out, err := runCcpp(t, h.ccppHandler, "compdb", "use", "lib/build")
		require.NoError(t, err)
		assert.Contains(t, out, "clangd will use it when it starts")
		assert.Zero(t, h.restarts)
	})
	t.Run("use rejects a directory without a database or outside the workspace", func(t *testing.T) {
		h := newTestHandler(buildTreeFS(), nil)
		_, err := runCcpp(t, h.ccppHandler, "compdb", "use", "lib/src")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no compile_commands.json in `lib/src`")
		_, err = runCcpp(t, h.ccppHandler, "compdb", "use", "../elsewhere")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "outside the workspace")
		_, err = runCcpp(t, h.ccppHandler, "compdb", "use")
		require.Error(t, err)
		_, err = runCcpp(t, h.ccppHandler, "compdb", "frobnicate")
		require.Error(t, err)
	})
	t.Run("configured directory shows as active", func(t *testing.T) {
		cfg := newStubConfig(map[string]string{"compile_commands_dir": "lib/build"})
		h := newTestHandler(buildTreeFS(), cfg)
		focusOn(t, h.ccppHandler, "/ws/lib/src/a.cpp")
		out, err := runCcpp(t, h.ccppHandler, "compdb")
		require.NoError(t, err)
		assert.Contains(t, out, "lib/build (active)")
	})
	t.Run("no candidates explain how to make one", func(t *testing.T) {
		h := newTestHandler(newFakeFS().addReadDir("/ws"), nil)
		out, err := runCcpp(t, h.ccppHandler, "compdb")
		require.NoError(t, err)
		assert.Contains(t, out, "No compile_commands.json below .")
		assert.Contains(t, out, "compile_flags.txt")
		assert.NotContains(t, out, "CMAKE_EXPORT_COMPILE_COMMANDS")

		h = newTestHandler(newFakeFS().addFile("/ws/meson.build").addReadDir("/ws"), nil)
		focusOn(t, h.ccppHandler, "/ws/a.c")
		out, err = runCcpp(t, h.ccppHandler, "compdb")
		require.NoError(t, err)
		assert.Contains(t, out, "ccpp compdb generate writes one by running meson setup build")
	})
	t.Run("a failed restart surfaces", func(t *testing.T) {
		h := newTestHandler(buildTreeFS(), nil)
		h.state.running(clangdOptions{bin: testClangd})
		h.restarter = errors.New("initialize ccpp lsp: boom")
		_, err := runCcpp(t, h.ccppHandler, "compdb", "use", "lib/build")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})
}

func TestCcppHandlerRestart(t *testing.T) {
	h := newTestHandler(newFakeFS(), nil)
	out, err := runCcpp(t, h.ccppHandler, "restart")
	require.NoError(t, err)
	assert.Contains(t, out, "clangd is not running")
	assert.Zero(t, h.restarts)

	h.state.running(clangdOptions{bin: testClangd})
	out, err = runCcpp(t, h.ccppHandler, "restart")
	require.NoError(t, err)
	assert.Contains(t, out, "clangd restarted.")
	assert.Equal(t, 1, h.restarts)
}

func TestCcppHandlerCompdbGenerate(t *testing.T) {
	// lib/ is a Make project whose Bear run writes two entries.
	newGenerating := func(t *testing.T, cfg config.Config) *testHandler {
		t.Helper()
		fs := newFakeFS().addFile("/ws/lib/Makefile").addFile("/ws/lib/src/Makefile")
		h := newTestHandler(fs, cfg)
		h.exec.onPath("make", "/usr/bin/make").onPath("bear", "/opt/homebrew/bin/bear")
		h.exec.respond("/opt/homebrew/bin/bear parse-sh", scriptedCmd{effect: func() {
			fs.addContent("/ws/lib/compile_commands.json", []byte(twoEntries))
		}})
		focusOn(t, h.ccppHandler, "/ws/lib/src/a.c")
		return h
	}
	t.Run("writes the focused project's database and restarts clangd", func(t *testing.T) {
		h := newGenerating(t, nil)
		h.state.running(clangdOptions{bin: testClangd})
		out, err := runCcpp(t, h.ccppHandler, "compdb", "generate")
		require.NoError(t, err)
		assert.Contains(t, out, "Wrote lib/compile_commands.json with 2 compile commands. clangd restarted.")
		assert.Equal(t, 1, h.restarts)
		started := h.exec.startedSnapshot()
		require.Len(t, started, 2)
		assert.Equal(t, "/ws/lib", started[0].dir, "the project is the outermost Makefile's directory")
		assert.Equal(t, "/opt/homebrew/bin/bear parse-sh", started[1].key)
	})
	t.Run("names the database clangd keeps using instead", func(t *testing.T) {
		h := newGenerating(t, newStubConfig(map[string]string{"compile_commands_dir": "out"}))
		out, err := runCcpp(t, h.ccppHandler, "compdb", "generate")
		require.NoError(t, err)
		assert.Contains(t, out, "clangd is pointed at out instead; ccpp status says by what.")
	})
	t.Run("a failure is the command's error", func(t *testing.T) {
		h := newGenerating(t, nil)
		h.exec.respond("/usr/bin/make -nBw", scriptedCmd{stderr: "make: *** missing separator.  Stop.\n", err: assertErr})
		_, err := runCcpp(t, h.ccppHandler, "compdb", "generate")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing separator")
	})
	t.Run("a project Rune cannot generate for gets the recipe", func(t *testing.T) {
		h := newTestHandler(newFakeFS().addFile("/ws/MODULE.bazel"), nil)
		focusOn(t, h.ccppHandler, "/ws/a.cc")
		out, err := runCcpp(t, h.ccppHandler, "compdb", "generate")
		require.NoError(t, err)
		assert.Contains(t, out, "Rune cannot generate a compile_commands.json for .")
		assert.Contains(t, out, "hedron_compile_commands")
		assert.Empty(t, h.exec.callsSnapshot())
	})
	t.Run("takes no arguments", func(t *testing.T) {
		h := newTestHandler(newFakeFS(), nil)
		_, err := runCcpp(t, h.ccppHandler, "compdb", "generate", "lib")
		require.EqualError(t, err, "usage: ccpp compdb generate")
	})
}

func TestCcppHandlerCompdbAuto(t *testing.T) {
	h := newTestHandler(newFakeFS(), nil)
	out, err := runCcpp(t, h.ccppHandler, "compdb", "auto")
	require.NoError(t, err)
	assert.Contains(t, out, "Rune asks before generating compile_commands.json")

	out, err = runCcpp(t, h.ccppHandler, "compdb", "auto", "on")
	require.NoError(t, err)
	assert.Contains(t, out, "gets one without asking")
	on, err := h.compdb.autoGenerate(context.Background())
	require.NoError(t, err)
	assert.True(t, on)
	out, err = runCcpp(t, h.ccppHandler, "compdb", "auto")
	require.NoError(t, err)
	assert.Contains(t, out, "gets one without asking")

	out, err = runCcpp(t, h.ccppHandler, "compdb", "auto", "off")
	require.NoError(t, err)
	assert.Contains(t, out, "Rune asks before generating")
	on, err = h.compdb.autoGenerate(context.Background())
	require.NoError(t, err)
	assert.False(t, on)

	_, err = runCcpp(t, h.ccppHandler, "compdb", "auto", "sometimes")
	require.EqualError(t, err, "usage: ccpp compdb auto [on | off]")
}

func TestCcppHandlerVersion(t *testing.T) {
	h := newTestHandler(newFakeFS(), nil)
	out, err := runCcpp(t, h.ccppHandler, "version")
	require.NoError(t, err)
	assert.Contains(t, out, "clangd version 21.1.0")
	assert.Contains(t, h.exec.callsSnapshot(), testClangd+" --version")

	h.clangd = func(context.Context) string { return "" }
	_, err = runCcpp(t, h.ccppHandler, "version")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lsp_path")
}

func TestCcppHandlerUsageAndHelp(t *testing.T) {
	h := newTestHandler(newFakeFS(), nil)
	out, err := runCcpp(t, h.ccppHandler)
	require.NoError(t, err)
	assert.Contains(t, out, "ccpp <command> [<args>]")
	assert.Contains(t, out, "compdb [use <dir> | clear | generate | auto [on | off]]")

	out, err = runCcpp(t, h.ccppHandler, "help", "compdb")
	require.NoError(t, err)
	assert.Contains(t, out, "use <dir>")
	assert.Contains(t, out, "clear")
	assert.Contains(t, out, "generate")
	assert.Contains(t, out, "auto [on | off]")

	_, err = runCcpp(t, h.ccppHandler, "frobnicate")
	require.Error(t, err)
}

func TestCcppHandlerComplete(t *testing.T) {
	h := newTestHandler(buildTreeFS(), nil)
	focusOn(t, h.ccppHandler, "/ws/lib/src/a.cpp")
	ctx := context.Background()
	complete := func(args ...string) []string {
		it, err := h.Complete(ctx, ccppCommandName, args)
		require.NoError(t, err)
		got, err := iterator.ToSlice(ctx, it)
		require.NoError(t, err)
		return got
	}
	assert.Equal(t, ccppSubcommandNames, complete())
	assert.Equal(t, []string{"compdb"}, complete("co"))
	assert.Equal(t, []string{"auto", "clear", "generate", "use"}, complete("compdb", ""))
	assert.Equal(t, []string{"use"}, complete("compdb", "u"))
	assert.Equal(t, []string{"generate"}, complete("compdb", "g"))
	assert.Equal(t, []string{"off", "on"}, complete("compdb", "auto", ""))
	assert.Equal(t, []string{"off"}, complete("compdb", "auto", "of"))
	assert.Equal(t, []string{"lib/build", "lib/out/Debug"}, complete("compdb", "use", ""))
	assert.Equal(t, []string{"lib/out/Debug"}, complete("compdb", "use", "lib/o"))
	assert.Empty(t, complete("compdb", "clear", ""))
	assert.Empty(t, complete("version", ""))
}
