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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
)

// fakeNotifications records notify and progress calls for assertions.
type fakeNotifications struct {
	mu       sync.Mutex
	openID   string
	notifs   []string
	progress []progressSample
	nextID   int
}

type progressSample struct {
	id       string
	message  string
	progress int64
	total    int64
}

func newFakeNotifications() *fakeNotifications {
	return &fakeNotifications{openID: "notif-1", nextID: 1}
}

func (n *fakeNotifications) Notify(_ browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notifs = append(n.notifs, msg)
	return n.openID, nil
}

func (n *fakeNotifications) NotifyOnce(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n *fakeNotifications) UpdateNotificationProgress(id, message string, progress, total int64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.progress = append(n.progress, progressSample{id, message, progress, total})
	return nil
}

func (n *fakeNotifications) lastProgress() progressSample {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.progress) == 0 {
		return progressSample{}
	}
	return n.progress[len(n.progress)-1]
}

func (n *fakeNotifications) progressMessages() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.progress))
	for i, p := range n.progress {
		out[i] = p.message
	}
	return out
}

// assertMonotonicProgress checks the displayed fraction never decreases,
// so the bar cannot move backward across steps with different totals.
func assertMonotonicProgress(t *testing.T, n *fakeNotifications) {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	prev := 0.0
	for _, p := range n.progress {
		require.NotZero(t, p.total)
		frac := float64(p.progress) / float64(p.total)
		assert.GreaterOrEqual(t, frac, prev,
			"progress fraction regressed at %q (%d/%d)", p.message, p.progress, p.total)
		prev = frac
	}
}

func TestDetectProject(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeFS)
		want  projectKind
	}{
		{"empty", func(*fakeFS) {}, kindNone},
		{"pyproject", func(f *fakeFS) { f.addFile("pyproject.toml") }, kindProject},
		{
			"pyproject wins over requirements and venv",
			func(f *fakeFS) {
				f.addFile("pyproject.toml")
				f.addFile("requirements.txt")
				f.addDir(".venv")
			},
			kindProject,
		},
		{"requirements txt", func(f *fakeFS) { f.addFile("requirements.txt") }, kindRequirements},
		{"requirements lock", func(f *fakeFS) { f.addFile("requirements.lock") }, kindRequirements},
		{"requirements in", func(f *fakeFS) { f.addFile("requirements.in") }, kindRequirements},
		{"venv only", func(f *fakeFS) { f.addDir(".venv") }, kindVenvOnly},
		{"script only", func(f *fakeFS) { f.addEntry("main.py", false) }, kindScript},
		{"non-python files only", func(f *fakeFS) { f.addEntry("README.md", false) }, kindNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeFS()
			tc.setup(fs)
			assert.Equal(t, tc.want, detectProjectAt(context.Background(), fs, "."))
		})
	}
}

// TestDetectProjectAtNested checks that markers are probed within the
// given directory, so a nested project is classified the same way the
// workspace-root project is.
func TestDetectProjectAtNested(t *testing.T) {
	const dir = "services/edge-worker"
	cases := []struct {
		name  string
		setup func(*fakeFS)
		want  projectKind
	}{
		{
			"nested pyproject",
			func(f *fakeFS) { f.addFile(dir + "/pyproject.toml") },
			kindProject,
		},
		{
			"nested requirements",
			func(f *fakeFS) { f.addFile(dir + "/requirements.txt") },
			kindRequirements,
		},
		{
			"nested venv",
			func(f *fakeFS) { f.addDir(dir + "/.venv") },
			kindVenvOnly,
		},
		{
			"root marker does not satisfy nested dir",
			func(f *fakeFS) { f.addFile("pyproject.toml") },
			kindNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeFS()
			tc.setup(fs)
			assert.Equal(t, tc.want, detectProjectAt(context.Background(), fs, dir))
		})
	}
}

// TestEnsureEnvironmentRunsInProjectDir asserts that every uv invocation
// for a nested project carries Cmd.Dir set to that project directory, so
// uv operates on the nested project's environment rather than the
// workspace root.
func TestEnsureEnvironmentRunsInProjectDir(t *testing.T) {
	const dir = "services/edge-worker"
	fs := newFakeFS().addFile(dir + "/requirements.txt")
	ex := newFakeExecutor()
	ex.respond("uv python find", scriptedCmd{})
	ex.respond("uv venv --allow-existing", scriptedCmd{})
	ex.respond("uv pip install -r requirements.txt", scriptedCmd{})
	notify := newFakeNotifications()

	err := ensureEnvironment(context.Background(), "uv", ex, notify, kindRequirements, fs, dir, "")
	require.NoError(t, err)

	for _, key := range ex.callsSnapshot() {
		assert.Equal(t, dir, ex.dirFor(key), "uv %q must run in the project dir", key)
	}
}

// TestRunUVWorkspaceRootLeavesDirEmpty asserts a workspace-root project
// (dir "" or ".") leaves Cmd.Dir empty so the executor falls back to its
// own resolved workspace path instead of a redundant or doubled path.
func TestRunUVWorkspaceRootLeavesDirEmpty(t *testing.T) {
	for _, dir := range []string{"", "."} {
		t.Run("dir="+dir, func(t *testing.T) {
			ex := newFakeExecutor()
			ex.respond("uv sync", scriptedCmd{})
			require.NoError(t, runUV(context.Background(), "uv", ex, dir, "sync"))
			assert.Empty(t, ex.dirFor("uv sync"))
		})
	}
}

func TestEnsureEnvironmentCommands(t *testing.T) {
	cases := []struct {
		name      string
		kind      projectKind
		setupFS   func(*fakeFS)
		responses map[string]scriptedCmd
		wantCalls []string
		wantTotal int64
		wantSync  string
	}{
		{
			name: "project runs uv sync",
			kind: kindProject,
			responses: map[string]scriptedCmd{
				"uv python find": {},
				"uv sync":        {},
			},
			wantCalls: []string{"uv python find", "uv sync"},
			wantTotal: 3,
			wantSync:  "Syncing project dependencies",
		},
		{
			name:    "requirements creates venv then pip install",
			kind:    kindRequirements,
			setupFS: func(f *fakeFS) { f.addFile("requirements.txt") },
			responses: map[string]scriptedCmd{
				"uv python find":                     {},
				"uv venv --allow-existing":           {},
				"uv pip install -r requirements.txt": {},
			},
			wantCalls: []string{"uv python find", "uv venv --allow-existing", "uv pip install -r requirements.txt"},
			wantTotal: 3,
			wantSync:  "Installing requirements",
		},
		{
			name: "venv only verifies interpreter",
			kind: kindVenvOnly,
			responses: map[string]scriptedCmd{
				"uv python find": {},
			},
			wantCalls: []string{"uv python find", "uv python find"},
			wantTotal: 3,
			wantSync:  "Verifying virtual environment",
		},
		{
			name: "script only creates venv",
			kind: kindScript,
			responses: map[string]scriptedCmd{
				"uv python find":           {},
				"uv venv --allow-existing": {},
			},
			wantCalls: []string{"uv python find", "uv venv --allow-existing"},
			wantTotal: 3,
			wantSync:  "Creating virtual environment",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeFS()
			if tc.setupFS != nil {
				tc.setupFS(fs)
			}
			ex := newFakeExecutor()
			for k, v := range tc.responses {
				ex.respond(k, v)
			}
			notify := newFakeNotifications()
			err := ensureEnvironment(context.Background(), "uv", ex, notify, tc.kind, fs, "", "")
			require.NoError(t, err)
			assert.Equal(t, tc.wantCalls, ex.callsSnapshot())

			last := notify.lastProgress()
			assert.Equal(t, tc.wantTotal, last.total)
			assert.Equal(t, tc.wantTotal, last.progress,
				"final progress must reach total")
			assert.Equal(t, "Python environment ready", last.message)
			assertMonotonicProgress(t, notify)

			msgs := notify.progressMessages()
			assert.Equal(t, "Finding Python interpreter", msgs[0])
			if tc.wantSync != "" {
				assert.Contains(t, msgs, tc.wantSync)
			}
		})
	}
}

func TestEnsureEnvironmentInstallsInterpreterOnFirstRun(t *testing.T) {
	fs := newFakeFS()
	ex := newFakeExecutor()
	ex.respond("uv python find", scriptedCmd{err: assertErr})
	ex.respond("uv python install --default", scriptedCmd{})
	ex.respond("uv sync", scriptedCmd{})
	notify := newFakeNotifications()
	err := ensureEnvironment(context.Background(), "uv", ex, notify, kindProject, fs, "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"uv python find", "uv python install --default", "uv sync"}, ex.callsSnapshot())

	last := notify.lastProgress()
	assert.Equal(t, int64(4), last.total, "installing an interpreter adds a step")
	assert.Equal(t, int64(4), last.progress)
	assert.Contains(t, notify.progressMessages(), "Installing Python interpreter")
	assertMonotonicProgress(t, notify)
}

// TestEnsureInterpreterRelinksMissingManagedFallback covers installs
// migrated from the layout where uv's links lived in python/bin: the
// managed interpreter is installed and found, but the shim's uvbin
// fallback target is absent, so an install must run to relink it.
func TestEnsureInterpreterRelinksMissingManagedFallback(t *testing.T) {
	fs := newFakeFS().addDir("/data/python/python")
	ex := newFakeExecutor()
	ex.respond("uv python find", scriptedCmd{})
	ex.respond("uv python install --default", scriptedCmd{})
	ex.respond("uv sync", scriptedCmd{})
	notify := newFakeNotifications()
	err := ensureEnvironment(context.Background(), "uv", ex, notify, kindProject, fs, "", "/data")
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"uv python find", "uv python install --default", "uv sync"},
		ex.callsSnapshot())
}

// TestEnsureInterpreterSkipsInstallWithoutManagedInterpreter pins the
// no-download guarantee: uv found an interpreter and none is managed, so
// there is nothing to relink and no CPython to fetch.
func TestEnsureInterpreterSkipsInstallWithoutManagedInterpreter(t *testing.T) {
	fs := newFakeFS()
	ex := newFakeExecutor()
	ex.respond("uv python find", scriptedCmd{})
	ex.respond("uv sync", scriptedCmd{})
	notify := newFakeNotifications()
	err := ensureEnvironment(context.Background(), "uv", ex, notify, kindProject, fs, "", "/data")
	require.NoError(t, err)
	assert.Equal(t, []string{"uv python find", "uv sync"}, ex.callsSnapshot())
}

func TestEnsureInterpreterSkipsInstallWhenFallbackPresent(t *testing.T) {
	fs := newFakeFS().addFile("/data/python/uvbin/python3").addDir("/data/python/python")
	ex := newFakeExecutor()
	ex.respond("uv python find", scriptedCmd{})
	ex.respond("uv sync", scriptedCmd{})
	notify := newFakeNotifications()
	err := ensureEnvironment(context.Background(), "uv", ex, notify, kindProject, fs, "", "/data")
	require.NoError(t, err)
	assert.Equal(t, []string{"uv python find", "uv sync"}, ex.callsSnapshot())
}

var assertErr = &interpreterMissingError{}

type interpreterMissingError struct{}

func (*interpreterMissingError) Error() string { return "no interpreter" }
