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

//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/extension_python/pyshim"
)

// TestE2E_OptOutLeavesEnvironmentAlone covers a project the user has
// opted out of: bring-up must not create a .venv or install a managed
// interpreter, the language server must still come up, and the uv
// passthrough subcommands must refuse until `python enable`.
func TestE2E_OptOutLeavesEnvironmentAlone(t *testing.T) {
	findUV(t)
	ctx := context.Background()

	dir := loadScenario(t, "pyproject")
	storage := storagestub.NewInMemoryService()
	require.NoError(t, newEnvSetting(storage).set(ctx, dirRoot(dir), false))

	env := runExtensionOnDirWith(t, dir, storage, nil)

	assert.NoDirExists(t, filepath.Join(dir, ".venv"),
		"an unmanaged project must not get a virtual environment")
	assert.NoFileExists(t, filepath.Join(pyshim.Dir(env.dataDir), "python3"),
		"an unmanaged project must not get the venv-aware shims")

	_, count := env.lsp.captured()
	assert.Equal(t, 1, count, "ty and ruff must still come up for an unmanaged project")

	require.NotNil(t, env.handler)
	_, err := env.handler.HandleCommand(ctx,
		repl.Command{Name: "python", Args: []string{"sync"}},
		repl.NopProgressWriter())
	require.ErrorContains(t, err, "python enable")
}

// TestE2E_EnableCreatesEnvironment covers flipping an opted-out project
// back on from the console: `python enable` syncs the environment right
// away and tells the user to reload for the server to see it.
func TestE2E_EnableCreatesEnvironment(t *testing.T) {
	findUV(t)
	ctx := context.Background()

	dir := loadScenario(t, "pyproject")
	storage := storagestub.NewInMemoryService()
	require.NoError(t, newEnvSetting(storage).set(ctx, dirRoot(dir), false))

	env := runExtensionOnDirWith(t, dir, storage, nil)
	require.NoDirExists(t, filepath.Join(dir, ".venv"))

	it, err := env.handler.HandleCommand(ctx,
		repl.Command{Name: "python", Args: []string{"enable"}},
		repl.NopProgressWriter())
	require.NoError(t, err)
	out, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, out, 1)

	assert.DirExists(t, filepath.Join(dir, ".venv"),
		"python enable must create the project environment")
	assert.Equal(t, normalize(mustReadFile(t, filepath.Join("testdata", "pyproject", "expected.txt"))),
		normalize(runPython(t, dir)))

	managed, known, err := newEnvSetting(storage).get(ctx, dirRoot(dir))
	require.NoError(t, err)
	assert.True(t, known)
	assert.True(t, managed)
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// TestE2E_Scenarios_EnvAndLSP runs the extension against each testdata
// scenario, then runs the scenario's main.py through the synced
// environment. Every scenario declares the same dependency expressed
// differently, so all must produce identical stdout — proving the
// extension works around each environment shape. It also asserts the
// fake LSP received exactly one Initialize with langID=python.
func TestE2E_Scenarios_EnvAndLSP(t *testing.T) {
	findUV(t)

	for _, name := range allScenarios {
		t.Run(name, func(t *testing.T) {
			env := runExtensionOnScenario(t, name)

			expected, err := os.ReadFile(filepath.Join("testdata", name, "expected.txt"))
			require.NoError(t, err)

			got := runPython(t, env.dir)
			assert.Equal(t, normalize(string(expected)), normalize(got),
				"scenario %s produced unexpected program output", name)

			assertShimResolvesVenv(t, env)

			params, count := env.lsp.captured()
			assert.Equal(t, 1, count, "LSP must be initialized exactly once")

			var initOpts map[string]any
			require.NoError(t, json.Unmarshal(params.InitializeOptions, &initOpts))
			assert.Equal(t, "python", initOpts["langID"])
			assert.Equal(t, "file://"+env.dir, params.RootURI)

			require.Len(t, env.manuals, 1, "the python REPL command must be registered")
			assert.Equal(t, pyCommandName, env.manuals[0].Name)
		})
	}
}

// normalize trims trailing whitespace so committed expected.txt files do
// not need an exact trailing-newline match.
func normalize(s string) string {
	return trimTrailing(s)
}

func trimTrailing(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// assertShimResolvesVenv runs the python3 shim the bring-up wrote to the
// data dir from inside the scenario workspace and asserts it executes
// the project's .venv interpreter — the terminal contract: a bare
// `python3` in a project dir resolves that project's environment.
func assertShimResolvesVenv(t *testing.T, env scenarioEnv) {
	t.Helper()
	shim := filepath.Join(pyshim.Dir(env.dataDir), "python3")
	cmd := exec.Command(shim, "-c", "import sys; print(sys.prefix)")
	cmd.Dir = env.dir
	// A controlled environment so an activated venv or dev PATH cannot
	// leak into the resolution.
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "shim must execute the scenario venv python: %s", out)

	got, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	require.NoError(t, err)
	want, err := filepath.EvalSymlinks(filepath.Join(env.dir, ".venv"))
	require.NoError(t, err)
	assert.Equal(t, want, got,
		"shim must resolve the project venv, not another interpreter")
}

// TestE2E_NestedProjectDiscovery covers a workspace with no root Python
// project but a nested one at services/edge-worker. The
// extension must not initialize a language server on startup; only after
// a nested .py is opened should it initialize exactly once, rooted at the
// nested project. Opening a .py with no enclosing marker must not
// initialize.
func TestE2E_NestedProjectDiscovery(t *testing.T) {
	findUV(t)

	env := runExtensionOnScenario(t, "nested")

	_, count := env.lsp.captured()
	assert.Equal(t, 0, count, "no project at the workspace root must not initialize on startup")
	require.Len(t, env.manuals, 1, "the python REPL command must be registered once")
	assert.Equal(t, pyCommandName, env.manuals[0].Name)

	workerRoot := filepath.Join(env.dir, "services", "edge-worker")
	env.editor.open(t, filepath.Join(workerRoot, "worker.py"))
	env.lsp.waitForInit(t, 90*time.Second)

	params, count := env.lsp.captured()
	assert.Equal(t, 1, count, "opening the nested .py must initialize exactly once")
	assert.Equal(t, "file://"+workerRoot, params.RootURI)

	var initOpts map[string]any
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &initOpts))
	assert.Equal(t, "python", initOpts["langID"])

	// A second open inside the same root must not re-initialize, and a
	// .py with no enclosing marker must not initialize a server.
	env.editor.open(t, filepath.Join(workerRoot, "worker.py"))
	env.editor.open(t, filepath.Join(env.dir, "scripts", "stray.py"))

	_, count = env.lsp.captured()
	assert.Equal(t, 1, count, "dedupe and stray opens must not add initializations")
}
