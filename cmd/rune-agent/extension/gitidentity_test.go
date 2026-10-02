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

package extension

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/internal/ide/vctrl/testgit"
)

func TestParseDialogueID(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no args", args: nil, want: ""},
		{name: "bare id", args: []string{"myid"}, want: "myid"},
		{name: "prefixed id", args: []string{"worktree:myid"}, want: "myid"},
		{name: "uri prefixed", args: []string{"/home/user/proj:myid"}, want: "myid"},
		{name: "--all bare", args: []string{"--all", "myid"}, want: "myid"},
		{name: "--all prefixed", args: []string{"--all", "wt:myid"}, want: "myid"},
		{name: "--all only", args: []string{"--all"}, want: ""},
		{name: "id then model", args: []string{"myid", "gpt-4"}, want: "myid"},
		{name: "empty string arg", args: []string{""}, want: ""},
		{name: "colon only prefix", args: []string{"worktree:"}, want: ""},
		{name: "--all empty string", args: []string{"--all", ""}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := textapi.Command{Args: tt.args}
			got := parseDialogueID(cmd)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFilterAllFlag(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, filterAllFlag([]string{"a", "--all", "b"}))
	assert.Equal(t, []string{"a"}, filterAllFlag([]string{"--all", "a"}))
	assert.Empty(t, filterAllFlag([]string{"--all"}))
	assert.Equal(t, []string{"x"}, filterAllFlag([]string{"x"}))
}

func TestResolveGitIdentity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}

	// realPath resolves symlinks so paths match what git reports.
	// On macOS, os.MkdirTemp returns /var/... but git resolves to /private/var/...
	realPath := func(t *testing.T, p string) string {
		t.Helper()
		resolved, err := filepath.EvalSymlinks(p)
		require.NoError(t, err)
		return resolved
	}

	// initRepo creates a git repo with one commit in a temp dir.
	initRepo := func(t *testing.T) string {
		t.Helper()
		dir := realPath(t, t.TempDir())
		testgit.Run(t, dir, "init", "-q")
		testgit.Run(t, dir, "commit", "--allow-empty", "-m", "init", "-q")
		return dir
	}

	ctx := context.Background()
	executor := testLocalExec{}

	type testCase struct {
		name string
		// setup creates the filesystem state and returns the cwd URI
		// to pass to resolveGitIdentity plus any cleanup function.
		setup func(t *testing.T) (cwdURI workspaceapi.URI, cleanup func())
		// check validates the returned gitIdentity.
		check func(t *testing.T, id gitIdentity)
	}

	tests := []testCase{
		{
			name: "non-git directory returns empty identity",
			setup: func(t *testing.T) (workspaceapi.URI, func()) {
				dir := realPath(t, t.TempDir())
				uri, _ := workspaceapi.ParseURI("file://" + dir)
				return uri, func() {}
			},
			check: func(t *testing.T, id gitIdentity) {
				assert.Empty(t, id.commonDir)
				assert.Empty(t, id.worktrees)
			},
		},
		{
			name: "single repo with no extra worktrees",
			setup: func(t *testing.T) (workspaceapi.URI, func()) {
				dir := initRepo(t)
				uri, _ := workspaceapi.ParseURI("file://" + dir)
				return uri, func() {}
			},
			check: func(t *testing.T, id gitIdentity) {
				assert.NotEmpty(t, id.commonDir)
				assert.Len(t, id.worktrees, 1, "single repo should list itself as one worktree")
			},
		},
		{
			name: "repo with one linked worktree, queried from main",
			setup: func(t *testing.T) (workspaceapi.URI, func()) {
				main := initRepo(t)
				wt := realPath(t, t.TempDir())
				testgit.Run(t, main, "worktree", "add", "-q", wt, "-b", "feature")
				uri, _ := workspaceapi.ParseURI("file://" + main)
				return uri, func() {}
			},
			check: func(t *testing.T, id gitIdentity) {
				assert.NotEmpty(t, id.commonDir)
				assert.Len(t, id.worktrees, 2, "should see main + linked worktree")

				// Both entries must have basenames as values.
				var basenames []string
				for _, base := range id.worktrees {
					basenames = append(basenames, base)
				}
				sort.Strings(basenames)
				assert.Len(t, basenames, 2)

				// Keys must be file:// URIs.
				for key := range id.worktrees {
					assert.Contains(t, key, "file://", "worktree key must be a file URI")
				}
			},
		},
		{
			name: "repo with one linked worktree, queried from worktree",
			setup: func(t *testing.T) (workspaceapi.URI, func()) {
				main := initRepo(t)
				wt := realPath(t, t.TempDir())
				testgit.Run(t, main, "worktree", "add", "-q", wt, "-b", "feature")
				uri, _ := workspaceapi.ParseURI("file://" + wt)
				return uri, func() {}
			},
			check: func(t *testing.T, id gitIdentity) {
				assert.NotEmpty(t, id.commonDir)
				assert.Len(t, id.worktrees, 2,
					"querying from a linked worktree must still list all worktrees")
			},
		},
		{
			name: "common dir is consistent between main and linked worktree",
			setup: func(t *testing.T) (workspaceapi.URI, func()) {
				main := initRepo(t)
				wt := realPath(t, t.TempDir())
				testgit.Run(t, main, "worktree", "add", "-q", wt, "-b", "feature")
				// Return both paths via the cleanup for assertion.
				t.Setenv("TEST_WT_PATH", wt)
				t.Setenv("TEST_MAIN_PATH", main)
				uri, _ := workspaceapi.ParseURI("file://" + main)
				return uri, func() {}
			},
			check: func(t *testing.T, id gitIdentity) {
				main := os.Getenv("TEST_MAIN_PATH")
				wt := os.Getenv("TEST_WT_PATH")

				idFromMain := id
				wtURI, _ := workspaceapi.ParseURI("file://" + wt)
				idFromWT := resolveGitIdentity(ctx, executor, wtURI)

				assert.Equal(t, idFromMain.commonDir, idFromWT.commonDir,
					"commonDir must be identical regardless of which worktree we query from")
				assert.Equal(t, idFromMain.worktrees, idFromWT.worktrees,
					"worktree map must be identical regardless of which worktree we query from")

				// The main worktree URI must appear in the map.
				mainURI := "file://" + main
				assert.Contains(t, id.worktrees, mainURI)

				// The linked worktree URI must appear in the map.
				wtURIStr := "file://" + wt
				assert.Contains(t, id.worktrees, wtURIStr)
			},
		},
		{
			name: "multiple linked worktrees",
			setup: func(t *testing.T) (workspaceapi.URI, func()) {
				main := initRepo(t)
				wt1 := realPath(t, t.TempDir())
				wt2 := realPath(t, t.TempDir())
				testgit.Run(t, main, "worktree", "add", "-q", wt1, "-b", "feat-1")
				testgit.Run(t, main, "worktree", "add", "-q", wt2, "-b", "feat-2")
				uri, _ := workspaceapi.ParseURI("file://" + main)
				return uri, func() {}
			},
			check: func(t *testing.T, id gitIdentity) {
				assert.Len(t, id.worktrees, 3, "main + 2 linked worktrees")
			},
		},
		{
			name: "detached worktree is included",
			setup: func(t *testing.T) (workspaceapi.URI, func()) {
				main := initRepo(t)
				wt := realPath(t, t.TempDir())
				testgit.Run(t, main, "worktree", "add", "-q", "--detach", wt, "HEAD")
				uri, _ := workspaceapi.ParseURI("file://" + main)
				return uri, func() {}
			},
			check: func(t *testing.T, id gitIdentity) {
				assert.Len(t, id.worktrees, 2, "detached worktrees must be listed")
			},
		},
		{
			name: "worktree basenames are filepath.Base of the directory",
			setup: func(t *testing.T) (workspaceapi.URI, func()) {
				main := initRepo(t)
				wt := filepath.Join(realPath(t, t.TempDir()), "my-feature")
				require.NoError(t, os.MkdirAll(filepath.Dir(wt), 0o755))
				testgit.Run(t, main, "worktree", "add", "-q", wt, "-b", "my-feature")
				t.Setenv("TEST_WT_PATH", wt)
				uri, _ := workspaceapi.ParseURI("file://" + main)
				return uri, func() {}
			},
			check: func(t *testing.T, id gitIdentity) {
				wt := os.Getenv("TEST_WT_PATH")
				wtURIStr := "file://" + wt
				assert.Equal(t, "my-feature", id.worktrees[wtURIStr],
					"basename should be the directory name, not the branch name")
			},
		},
		{
			name: "queried from subdirectory of a worktree",
			setup: func(t *testing.T) (workspaceapi.URI, func()) {
				main := initRepo(t)
				sub := filepath.Join(main, "subdir")
				require.NoError(t, os.MkdirAll(sub, 0o755))
				// NOTE: cwd is a subdirectory, not the repo root.
				uri, _ := workspaceapi.ParseURI("file://" + sub)
				return uri, func() {}
			},
			check: func(t *testing.T, id gitIdentity) {
				// git rev-parse --git-common-dir works from subdirs too.
				assert.NotEmpty(t, id.commonDir)
				assert.Len(t, id.worktrees, 1)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwdURI, cleanup := tt.setup(t)
			defer cleanup()
			id := resolveGitIdentity(ctx, executor, cwdURI)
			tt.check(t, id)
		})
	}
}

// TestResolveGitIdentityIgnoresHookGitEnv guards resolveGitIdentity
// against the repo-location overrides git exports to hook
// subprocesses (GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE): when the
// editor or its tests run under `git commit` (pre-commit hooks,
// `git rebase -x`, ...), those variables must not redirect identity
// resolution away from the workspace directory to the hook's
// repository.
func TestResolveGitIdentityIgnoresHookGitEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}

	initRepo := func(t *testing.T) string {
		t.Helper()
		dir, err := filepath.EvalSymlinks(t.TempDir())
		require.NoError(t, err)
		testgit.Run(t, dir, "init", "-q")
		testgit.Run(t, dir, "commit", "--allow-empty", "-m", "init", "-q")
		return dir
	}
	hookRepo := initRepo(t)
	target := initRepo(t)

	t.Setenv("GIT_DIR", filepath.Join(hookRepo, ".git"))
	t.Setenv("GIT_WORK_TREE", hookRepo)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(hookRepo, ".git", "index"))

	uri, err := workspaceapi.ParseURI("file://" + target)
	require.NoError(t, err)
	id := resolveGitIdentity(context.Background(), testLocalExec{}, uri)

	assert.Equal(t, filepath.Join(target, ".git"), id.commonDir,
		"identity must come from the workspace dir, not the hook's GIT_DIR")
	assert.Equal(t,
		map[string]string{uri.String(): filepath.Base(target)},
		id.worktrees,
		"worktrees must belong to the workspace repo, not the hook's")
}

func TestCompleteWithDialoguesIterator(t *testing.T) {
	now := time.Now()

	// Dialogues are pre-sorted by UpdatedAt descending (as store.List returns).
	store := &fakeListStore{dialogues: []dialoguemanager.DialogueHeader{
		{ID: "other-chat", WorkspaceURI: "ssh://host/other/project", UpdatedAt: now},
		{ID: "sibling-chat", WorkspaceURI: "file:///my/worktree-b", UpdatedAt: now.Add(-10 * time.Minute)},
		{ID: "sub-agent-explore-fuzzy-dog", WorkspaceURI: "file:///my/workspace", SubAgent: true, UpdatedAt: now.Add(-30 * time.Minute)},
		{ID: "local-new", WorkspaceURI: "file:///my/workspace", UpdatedAt: now.Add(-1 * time.Hour)},
		{ID: "local-named", Title: "fix the flaky test", WorkspaceURI: "file:///my/workspace", UpdatedAt: now.Add(-90 * time.Minute)},
		{ID: "local-old", WorkspaceURI: "file:///my/workspace", UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "legacy", UpdatedAt: now.Add(-3 * time.Hour)},
	}}

	h := &aiEditorHandler{
		dialogueStore: store,
		gitID: gitIdentity{
			commonDir: "/my/.git",
			worktrees: map[string]string{
				"file:///my/workspace":  "workspace",
				"file:///my/worktree-b": "worktree-b",
			},
		},
	}
	h.cwd, _ = workspaceapi.ParseURI("file:///my/workspace")

	t.Run("without --all excludes other workspaces", func(t *testing.T) {
		it, err := h.completeWithDialoguesIterator(context.Background(), false)
		require.NoError(t, err)
		ids, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)

		assert.Equal(t, []string{
			"local-new",
			"'fix the flaky test (local-named)'",
			"local-old",
			"worktree-b:sibling-chat",
			"<legacy>:legacy",
		}, ids)
	})

	t.Run("with --all includes other workspaces", func(t *testing.T) {
		it, err := h.completeWithDialoguesIterator(context.Background(), true)
		require.NoError(t, err)
		ids, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)

		assert.Equal(t, []string{
			"local-new",
			"'fix the flaky test (local-named)'",
			"local-old",
			"worktree-b:sibling-chat",
			"<legacy>:legacy",
			"ssh://host/other/project:other-chat",
		}, ids)
	})
}

// fakeListStore is a minimal Store for testing the completer.
type fakeListStore struct {
	dialoguemanager.Store
	dialogues []dialoguemanager.DialogueHeader
}

func (f *fakeListStore) List(_ context.Context) (iterator.Iterator[dialoguemanager.DialogueHeader], error) {
	return iterator.FromSlice(f.dialogues), nil
}
