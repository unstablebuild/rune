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

package workspace

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// caseFS serves files rooted at "/" and, when insensitive, resolves the
// last element of a Stat path regardless of case.
type caseFS struct {
	schemeapi.Scheme
	files       fstest.MapFS
	insensitive bool
	err         error
	reads       int
}

func mapPath(p string) string {
	if p = strings.TrimPrefix(p, "/"); p == "" {
		return "."
	}
	return p
}

func (f *caseFS) Root() string { return "/ws" }

func (f *caseFS) ReadDir(dir string) ([]fs.DirEntry, error) {
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	return fs.ReadDir(f.files, mapPath(dir))
}

func (f *caseFS) Stat(name string) (fs.FileInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	name = mapPath(name)
	if f.insensitive {
		entries, _ := fs.ReadDir(f.files, path.Dir(name))
		for _, e := range entries {
			if strings.EqualFold(e.Name(), path.Base(name)) {
				name = path.Join(path.Dir(name), e.Name())
			}
		}
	}
	return fs.Stat(f.files, name)
}

type answeringScheme struct {
	*caseFS
}

func (answeringScheme) PathCaseSensitive() bool { return false }

func TestProbePathCaseSensitive(t *testing.T) {
	file := &fstest.MapFile{}
	tsuite := []struct {
		name        string
		dir         string
		files       fstest.MapFS
		insensitive bool
		err         error
		want        bool
	}{
		{
			name:  "swapped entry is missing",
			dir:   "/ws",
			files: fstest.MapFS{"ws/Main.go": file},
			want:  true,
		},
		{
			name:        "swapped entry resolves",
			dir:         "/ws",
			files:       fstest.MapFS{"ws/Main.go": file},
			insensitive: true,
			want:        false,
		},
		{
			name:  "swapped entry exists in its own right",
			dir:   "/ws",
			files: fstest.MapFS{"ws/Main.go": file, "ws/mAIN.GO": file},
			want:  true,
		},
		{
			name:        "no lettered entry falls back to the dir name",
			dir:         "/ws",
			files:       fstest.MapFS{"ws/123": file},
			insensitive: true,
			want:        false,
		},
		{
			name:  "dir name fallback on a sensitive fs",
			dir:   "/ws",
			files: fstest.MapFS{"ws/123": file},
			want:  true,
		},
		{
			name:        "no letters anywhere",
			dir:         "/1",
			files:       fstest.MapFS{"1/2": file},
			insensitive: true,
			want:        true,
		},
		{
			name:        "filesystem fails",
			dir:         "/ws",
			files:       fstest.MapFS{"ws/Main.go": file},
			insensitive: true,
			err:         errors.New("disconnected"),
			want:        true,
		},
	}
	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			fsys := &caseFS{files: tcase.files, insensitive: tcase.insensitive, err: tcase.err}
			assert.Equal(t, tcase.want, ProbePathCaseSensitive(fsys, tcase.dir))
		})
	}
}

func TestProbePathCaseSensitiveHost(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Probe"), nil, 0o600))
	uri, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	scheme, err := newFileScheme(uri, "", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	_, err = os.Stat(filepath.Join(dir, "pROBE"))
	assert.Equal(t, err != nil, ProbePathCaseSensitive(scheme, dir))
}

func TestSchemeWorkspacePathCaseSensitive(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///ws")
	require.NoError(t, err)
	newFS := func() *caseFS {
		return &caseFS{files: fstest.MapFS{"ws/Main.go": {}}, insensitive: true}
	}

	t.Run("probes once", func(t *testing.T) {
		fsys := newFS()
		w := NewSchemeWorkspace(uri, fsys, inlineSchedule)
		assert.False(t, w.PathCaseSensitive())
		assert.False(t, w.PathCaseSensitive())
		assert.Equal(t, 1, fsys.reads)
	})

	t.Run("defers to a scheme that knows", func(t *testing.T) {
		fsys := newFS()
		fsys.insensitive = false
		w := NewSchemeWorkspace(uri, answeringScheme{fsys}, inlineSchedule)
		assert.False(t, w.PathCaseSensitive())
		assert.Zero(t, fsys.reads)
	})
}
