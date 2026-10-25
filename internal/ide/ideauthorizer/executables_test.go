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

package ideauthorizer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

type osExecutableFS struct{}

func (osExecutableFS) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }

func (osExecutableFS) Open(name string) (workspaceapi.File, error) { return os.Open(name) }

func writeExecutable(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), mode))
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func staticHostPATH(dirs ...string) func(context.Context, bool) ([]string, error) {
	return func(context.Context, bool) ([]string, error) { return dirs, nil }
}

func TestWorkspaceResolverResolve(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	writeExecutable(t, filepath.Join(first, "plain"), "x", 0o644)
	writeExecutable(t, filepath.Join(second, "plain"), "x", 0o755)
	writeExecutable(t, filepath.Join(first, "tool"), "1", 0o755)
	writeExecutable(t, filepath.Join(second, "tool"), "2", 0o755)
	writeExecutable(t, filepath.Join(root, "ws", "run"), "r", 0o755)
	writeExecutable(t, filepath.Join(root, "home", "bin", "h"), "h", 0o755)
	writeExecutable(t, filepath.Join(root, "data", "bin", "d"), "d", 0o755)
	require.NoError(t, os.MkdirAll(filepath.Join(first, "dir"), 0o755))

	r := NewWorkspaceResolver(WorkspaceResolverConfig{
		FS:       osExecutableFS{},
		Root:     filepath.Join(root, "ws"),
		HostPATH: staticHostPATH(first, second),
		Home:     filepath.Join(root, "home"),
		DataDir:  filepath.Join(root, "data"),
	})
	cases := []struct {
		name     string
		dir      string
		pathList []string
		want     string
	}{
		{name: "tool", want: filepath.Join(first, "tool")},
		{name: "plain", want: filepath.Join(second, "plain")},
		{name: "tool", pathList: []string{second}, want: filepath.Join(second, "tool")},
		{name: "./run", want: filepath.Join(root, "ws", "run")},
		{name: "../ws/run", dir: first, want: filepath.Join(root, "ws", "run")},
		{name: "run", pathList: []string{"."}, want: filepath.Join(root, "ws", "run")},
		{name: "~/bin/h", want: filepath.Join(root, "home", "bin", "h")},
		{name: "$RUNE_DATADIR/bin/d", want: filepath.Join(root, "data", "bin", "d")},
		{name: "${RUNE_DATADIR}/bin/d", want: filepath.Join(root, "data", "bin", "d")},
	}
	for _, tc := range cases {
		got, err := r.Resolve(context.Background(), tc.name, tc.dir, tc.pathList)
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want, got, tc.name)
	}
	for _, name := range []string{"missing", "dir", "$HOME/bin/h", filepath.Join(first, "dir")} {
		_, err := r.Resolve(context.Background(), name, "", nil)
		assert.ErrorIs(t, err, ErrExecutableNotFound, name)
	}
}

func TestWorkspaceResolverResolveRefreshesHostPATHOnMiss(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeExecutable(t, filepath.Join(root, "new", "tool"), "x", 0o755)
	var refreshed bool
	r := NewWorkspaceResolver(WorkspaceResolverConfig{
		FS: osExecutableFS{},
		HostPATH: func(_ context.Context, refresh bool) ([]string, error) {
			if refresh {
				refreshed = true
				return []string{filepath.Join(root, "new")}, nil
			}
			return []string{filepath.Join(root, "old")}, nil
		},
	})
	got, err := r.Resolve(context.Background(), "tool", "", nil)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "new", "tool"), got)
	assert.True(t, refreshed)
}

func TestWorkspaceResolverResolveUsesLookCmdPath(t *testing.T) {
	t.Parallel()

	r := NewWorkspaceResolver(WorkspaceResolverConfig{
		FS:          osExecutableFS{},
		HostPATH:    staticHostPATH(),
		LookCmdPath: func(name string) (string, error) { return "/looked/" + name, nil },
	})
	got, err := r.Resolve(context.Background(), "git", "", nil)
	require.NoError(t, err)
	assert.Equal(t, "/looked/git", got)
}

func TestWorkspaceResolverFingerprint(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")
	writeExecutable(t, target, "one", 0o755)
	require.NoError(t, os.Symlink(target, link))
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, os.Chtimes(target, mtime, mtime))

	r := NewWorkspaceResolver(WorkspaceResolverConfig{FS: osExecutableFS{}})
	fp, err := r.Fingerprint(context.Background(), link)
	require.NoError(t, err)
	assert.Equal(t, sha256Hex("one"), fp.SHA256)
	assert.Equal(t, int64(3), fp.Size)
	assert.True(t, fp.ModTime.Equal(mtime))

	// Same size and modification time: the cached hash is kept.
	require.NoError(t, os.WriteFile(target, []byte("two"), 0o755))
	require.NoError(t, os.Chtimes(target, mtime, mtime))
	fp, err = r.Fingerprint(context.Background(), link)
	require.NoError(t, err)
	assert.Equal(t, sha256Hex("one"), fp.SHA256)

	later := mtime.Add(time.Minute)
	require.NoError(t, os.Chtimes(target, later, later))
	fp, err = r.Fingerprint(context.Background(), link)
	require.NoError(t, err)
	assert.Equal(t, sha256Hex("two"), fp.SHA256)

	_, err = r.Fingerprint(context.Background(), filepath.Join(root, "missing"))
	assert.Error(t, err)
}
