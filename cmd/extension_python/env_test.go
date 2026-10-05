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
	"os"
	"path/filepath"
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

func readUVStderr(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "uvstderr", name))
	require.NoError(t, err)
	return string(b)
}

func TestEnsureEnvironmentFailureReportsUVDiagnostic(t *testing.T) {
	exitErr := errors.New("exit status 1")
	cases := []struct {
		name     string
		stderr   string
		want     []string
		dontWant []string
	}{
		{
			name:   "build failure names the missing build dependency",
			stderr: readUVStderr(t, "build-failure.txt"),
			want: []string{
				"`uv pip install -r requirements.txt` failed",
				"× Failed to build `psycopg2-binary==2.9.10`",
				"╰─▶ Call to `setuptools.build_meta:__legacy__.build_wheel` failed (exit status: 1)",
				"Error: pg_config executable not found.",
				"hint: Build failures usually indicate a problem with the package or the build environment",
			},
			dontWant: []string{"SetuptoolsDeprecationWarning", "running egg_info", "Resolved 18 packages"},
		},
		{
			name:   "resolution failure explains the conflict",
			stderr: readUVStderr(t, "no-solution.txt"),
			want: []string{
				"× No solution found when resolving dependencies:",
				"╰─▶ Because nonexistentpkg-zzz was not found in the package registry and you " +
					"require nonexistentpkg-zzz==1.0, we can conclude that your requirements are unsatisfiable.",
			},
		},
		{
			name:   "top-level uv error",
			stderr: "error: File not found: `requirements.txt`\n",
			want:   []string{"error: File not found: `requirements.txt`"},
		},
		{
			name:     "unrecognized output falls back to its last lines",
			stderr:   "noise 1\nnoise 2\nnoise 3\nnoise 4\nnoise 5\nnoise 6\nsomething broke\n",
			want:     []string{"something broke"},
			dontWant: []string{"noise 1"},
		},
		{
			name:   "no output keeps the exit status",
			stderr: "",
			want:   []string{"`uv pip install -r requirements.txt` failed: exit status 1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeFS().addFile("requirements.txt")
			ex := newFakeExecutor()
			ex.respond("uv python find", scriptedCmd{})
			ex.respond("uv venv --allow-existing", scriptedCmd{})
			ex.respond("uv pip install -r requirements.txt",
				scriptedCmd{stderr: tc.stderr, err: exitErr})
			notify := newFakeNotifications()

			err := ensureEnvironment(context.Background(), "uv", ex, notify, kindRequirements, fs, "", "")
			require.Error(t, err)
			require.ErrorIs(t, err, exitErr)
			for _, w := range tc.want {
				assert.Contains(t, err.Error(), w)
			}
			for _, w := range tc.dontWant {
				assert.NotContains(t, err.Error(), w)
			}
		})
	}
}

func TestEnsureEnvironmentFailureCompletesProgress(t *testing.T) {
	fs := newFakeFS().addFile("requirements.txt")
	ex := newFakeExecutor()
	ex.respond("uv python find", scriptedCmd{})
	ex.respond("uv venv --allow-existing", scriptedCmd{})
	ex.respond("uv pip install -r requirements.txt", scriptedCmd{err: errors.New("exit status 1")})
	notify := newFakeNotifications()

	err := ensureEnvironment(context.Background(), "uv", ex, notify, kindRequirements, fs, "", "")
	require.Error(t, err)

	last := notify.lastProgress()
	assert.Equal(t, last.total, last.progress, "progress must be completed on failure")
	assert.Equal(t, "Python environment setup failed", last.message)
	assertMonotonicProgress(t, notify)
}

func TestHeadTail(t *testing.T) {
	cases := []struct {
		name   string
		writes []string
		want   string
	}{
		{"fits", []string{"ab", "cd"}, "abcd"},
		{"exactly head and tail", []string{"abcdef"}, "abcdef"},
		{"drops the middle", []string{"abc", "defgh", "ij"}, "abc\n\nhij"},
		{"single large write", []string{"abcdefghij"}, "abc\n\nhij"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &headTail{limit: 3}
			for _, w := range tc.writes {
				n, err := b.Write([]byte(w))
				require.NoError(t, err)
				assert.Equal(t, len(w), n)
			}
			assert.Equal(t, tc.want, b.String())
		})
	}
}
