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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/extension/langext"
)

// writeProjectMarker makes dir look like a Python project root so
// FindProjectRoot stops there.
func writeProjectMarker(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "pyproject.toml"), []byte("[project]\n"), 0o644))
}

// newTestPyHandler builds the handler over an in-memory storage rooted
// at root, returning the setting so tests can seed or read the policy.
func newTestPyHandler(
	t *testing.T, root string, ex *fakeExecutor,
) (*envSetting, *pyHandler) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file://" + root)
	require.NoError(t, err)
	setting := newEnvSetting(storagestub.NewInMemoryService())
	_, h := newPyHandler(pyHandlerConfig{
		exec:    ex,
		notify:  newFakeNotifications(),
		fs:      realFS{root: root},
		setting: setting,
		wsRoot:  uri,
	})
	return setting, h.(*pyHandler)
}

func TestPyHandlerRouting(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCall string
	}{
		{"python install", []string{"install", "3.12"}, "uv python install 3.12"},
		{"python list", []string{"list"}, "uv python list"},
		{"python find", []string{"find"}, "uv python find"},
		{"python pin", []string{"pin", "3.12"}, "uv python pin 3.12"},
		{"python uninstall", []string{"uninstall", "3.12"}, "uv python uninstall 3.12"},
		{"init", []string{"init"}, "uv init"},
		{"add", []string{"add", "requests"}, "uv add requests"},
		{"remove", []string{"remove", "requests"}, "uv remove requests"},
		{"sync", []string{"sync"}, "uv sync"},
		{"lock", []string{"lock"}, "uv lock"},
		{"tree", []string{"tree"}, "uv tree"},
		{"build", []string{"build"}, "uv build"},
		{"run", []string{"run", "pytest"}, "uv run pytest"},
		{"tool", []string{"tool", "run", "black"}, "uv tool run black"},
		{"pip", []string{"pip", "install", "flask"}, "uv pip install flask"},
		{"venv", []string{"venv"}, "uv venv"},
		{"cache", []string{"cache", "clean"}, "uv cache clean"},
		{"self", []string{"self", "version"}, "uv self version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := newFakeExecutor()
			ex.respond(tc.wantCall, scriptedCmd{stdout: "ok\n"})
			_, handler := newTestPyHandler(t, t.TempDir(), ex)
			it, err := handler.HandleCommand(
				context.Background(),
				repl.Command{Name: "python", Args: tc.args},
				repl.NopProgressWriter(),
			)
			require.NoError(t, err)
			out, err := iterator.ToSlice(context.Background(), it)
			require.NoError(t, err)
			assert.Len(t, out, 1)
			assert.Equal(t, []string{tc.wantCall}, ex.callsSnapshot())
		})
	}
}

func TestPyHandlerUnknownSubcommand(t *testing.T) {
	ex := newFakeExecutor()
	_, handler := newTestPyHandler(t, t.TempDir(), ex)
	_, err := handler.HandleCommand(
		context.Background(),
		repl.Command{Name: "python", Args: []string{"bogus"}},
		repl.NopProgressWriter(),
	)
	require.Error(t, err)
	assert.Empty(t, ex.callsSnapshot())
}

func TestPyHandlerEmptyShowsUsage(t *testing.T) {
	ex := newFakeExecutor()
	_, handler := newTestPyHandler(t, t.TempDir(), ex)
	it, err := handler.HandleCommand(
		context.Background(),
		repl.Command{Name: "python"},
		repl.NopProgressWriter(),
	)
	require.NoError(t, err)
	out, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Len(t, out, 1)
	assert.Empty(t, ex.callsSnapshot())
}

func TestPyHandlerComplete(t *testing.T) {
	_, handler := newTestPyHandler(t, t.TempDir(), newFakeExecutor())

	t.Run("depth 0 lists subcommands", func(t *testing.T) {
		it, err := handler.Complete(context.Background(), "", nil)
		require.NoError(t, err)
		got, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Equal(t, pySubcommandNames, got)
	})

	t.Run("depth 0 filters by prefix", func(t *testing.T) {
		it, err := handler.Complete(context.Background(), "", []string{"p"})
		require.NoError(t, err)
		got, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Equal(t, []string{"pin", "pip"}, got)
	})

	t.Run("depth 1 lists nested uv subcommands", func(t *testing.T) {
		it, err := handler.Complete(context.Background(), "", []string{"pip", ""})
		require.NoError(t, err)
		got, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Equal(t,
			[]string{"check", "compile", "freeze", "install", "list", "show", "sync", "tree", "uninstall"},
			got)
	})

	t.Run("depth 1 filters nested by prefix", func(t *testing.T) {
		it, err := handler.Complete(context.Background(), "", []string{"tool", "u"})
		require.NoError(t, err)
		got, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Equal(t, []string{"uninstall", "upgrade", "update-shell"}, got)
	})

	t.Run("depth 1 for non-group subcommand is empty", func(t *testing.T) {
		it, err := handler.Complete(context.Background(), "", []string{"add", "req"})
		require.NoError(t, err)
		got, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("deeper args defer to uv (empty)", func(t *testing.T) {
		it, err := handler.Complete(context.Background(), "", []string{"pip", "install", "fl"})
		require.NoError(t, err)
		got, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestPyHandlerCompleteProjectRoots(t *testing.T) {
	root := t.TempDir()
	writeProjectMarker(t, root)
	writeProjectMarker(t, filepath.Join(root, "svc", "api"))
	writeProjectMarker(t, filepath.Join(root, "svc", "web"))
	writeProjectMarker(t, filepath.Join(root, "node_modules", "pkg"))
	// Excluded by vctrl's common excludes and by .gitignore respectively.
	writeProjectMarker(t, filepath.Join(root, "build", "gen"))
	writeProjectMarker(t, filepath.Join(root, "svc", "api", ".venv"))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, ".gitignore"), []byte("build/\n"), 0o644))
	_, handler := newTestPyHandler(t, root, newFakeExecutor())

	complete := func(t *testing.T, args []string) []string {
		t.Helper()
		it, err := handler.Complete(context.Background(), "", args)
		require.NoError(t, err)
		got, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		return got
	}

	for _, sub := range []string{"enable", "disable", "status"} {
		t.Run(sub+" completes project roots", func(t *testing.T) {
			assert.Equal(t,
				[]string{".", "svc/api", "svc/web"},
				complete(t, []string{sub, ""}))
		})
	}

	t.Run("filters roots by prefix", func(t *testing.T) {
		assert.Equal(t, []string{"svc/api"}, complete(t, []string{"enable", "svc/a"}))
	})

	t.Run("completed root resolves back to itself", func(t *testing.T) {
		for _, arg := range complete(t, []string{"enable", ""}) {
			got := handler.resolveRoot([]string{arg})
			assert.Equal(t,
				filepath.Join(root, filepath.Clean(arg)), got.Dir,
				"completion %q must resolve to the root it names", arg)
		}
	})
}

func TestPyHandlerHelp(t *testing.T) {
	_, handler := newTestPyHandler(t, t.TempDir(), newFakeExecutor())

	t.Run("root", func(t *testing.T) {
		it, err := handler.Help(context.Background(), nil)
		require.NoError(t, err)
		out, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Len(t, out, 1)
	})

	t.Run("known subcommand", func(t *testing.T) {
		it, err := handler.Help(context.Background(), []string{"sync"})
		require.NoError(t, err)
		out, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Len(t, out, 1)
	})
}

func TestPyHandlerEnvPolicySubcommands(t *testing.T) {
	ctx := context.Background()

	t.Run("disable blocks uv subcommands", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectMarker(t, dir)
		ex := newFakeExecutor()
		_, handler := newTestPyHandler(t, dir, ex)

		_, err := handler.HandleCommand(ctx,
			repl.Command{Name: "python", Args: []string{"disable"}},
			repl.NopProgressWriter())
		require.NoError(t, err)

		_, err = handler.HandleCommand(ctx,
			repl.Command{Name: "python", Args: []string{"sync"}},
			repl.NopProgressWriter())
		require.ErrorContains(t, err, "python enable")
		assert.Empty(t, ex.callsSnapshot())
	})

	t.Run("status and help stay available while disabled", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectMarker(t, dir)
		ex := newFakeExecutor()
		_, handler := newTestPyHandler(t, dir, ex)
		_, err := handler.HandleCommand(ctx,
			repl.Command{Name: "python", Args: []string{"disable"}},
			repl.NopProgressWriter())
		require.NoError(t, err)

		for _, args := range [][]string{{"status"}, {"help"}} {
			it, err := handler.HandleCommand(ctx,
				repl.Command{Name: "python", Args: args},
				repl.NopProgressWriter())
			require.NoError(t, err, args)
			out, err := iterator.ToSlice(ctx, it)
			require.NoError(t, err)
			assert.Len(t, out, 1)
		}
	})

	t.Run("not asked does not block", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectMarker(t, dir)
		ex := newFakeExecutor()
		ex.respond("uv sync", scriptedCmd{stdout: "ok\n"})
		_, handler := newTestPyHandler(t, dir, ex)

		_, err := handler.HandleCommand(ctx,
			repl.Command{Name: "python", Args: []string{"sync"}},
			repl.NopProgressWriter())
		require.NoError(t, err)
		assert.Equal(t, []string{"uv sync"}, ex.callsSnapshot())
	})

	t.Run("enable re-enables and syncs", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectMarker(t, dir)
		ex := newFakeExecutor()
		setting, handler := newTestPyHandler(t, dir, ex)
		synced := 0
		handler.syncEnv = func(context.Context, langext.Root) error {
			synced++
			return nil
		}

		_, err := handler.HandleCommand(ctx,
			repl.Command{Name: "python", Args: []string{"disable"}},
			repl.NopProgressWriter())
		require.NoError(t, err)

		_, err = handler.HandleCommand(ctx,
			repl.Command{Name: "python", Args: []string{"enable"}},
			repl.NopProgressWriter())
		require.NoError(t, err)
		assert.Equal(t, 1, synced)

		managed, known, err := setting.get(ctx, handler.resolveRoot(nil))
		require.NoError(t, err)
		assert.True(t, known)
		assert.True(t, managed)
	})

	t.Run("path argument selects the nested root", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectMarker(t, dir)
		nested := filepath.Join(dir, "services", "edge")
		writeProjectMarker(t, nested)
		ex := newFakeExecutor()
		setting, handler := newTestPyHandler(t, dir, ex)

		_, err := handler.HandleCommand(ctx,
			repl.Command{Name: "python", Args: []string{"disable", "services/edge"}},
			repl.NopProgressWriter())
		require.NoError(t, err)

		_, known, err := setting.get(ctx, handler.resolveRoot([]string{"services/edge"}))
		require.NoError(t, err)
		assert.True(t, known)

		_, known, err = setting.get(ctx, handler.resolveRoot(nil))
		require.NoError(t, err)
		assert.False(t, known)
	})

	// A loose-scripts root is never auto-managed, so `python enable` is
	// the only way in; it must leave a .venv behind like every other
	// managed kind.
	t.Run("enable on a loose scripts root creates a venv", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "main.py"), []byte("pass\n"), 0o644))
		dataDir := t.TempDir()
		fs := realFS{root: dir}
		ex := newFakeExecutor()
		ex.respond("uv python find", scriptedCmd{})
		ex.respond("uv venv --allow-existing", scriptedCmd{})
		_, handler := newTestPyHandler(t, dir, ex)
		handler.syncEnv = func(ctx context.Context, root langext.Root) error {
			return setupManagedEnvironment(ctx, fs, ex, newFakeNotifications(),
				fakeInstaller{fs: fs, root: dataDir}, config.NopConfig(), dataDir, root)
		}

		_, err := handler.HandleCommand(ctx,
			repl.Command{Name: "python", Args: []string{"enable"}},
			repl.NopProgressWriter())
		require.NoError(t, err)
		assert.Equal(t,
			[]string{"uv python find", "uv venv --allow-existing"}, ex.callsSnapshot())
	})
}
