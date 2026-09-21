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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

func TestDefaultSwapDirectory(t *testing.T) {
	tsuite := []struct {
		file    string
		wantDir string
		wantErr bool
	}{
		{"other:///tmp/a.go", "other:///tmp", false},
		{"file:///a.go", "file:///", false},
		{"file:///tmp/a.go", "file:///tmp", false},
		{"file://tmp/a.go", "file://tmp/", false},
		{"ssh://unstable.build/tmp/a.go", "ssh://unstable.build/tmp", false},
	}

	for _, tcase := range tsuite {
		t.Run(fmt.Sprintf("DefaultLocalSwapDirectory of %s", tcase.file), func(t *testing.T) {
			uri, err := workspaceapi.ParseURI(tcase.file)
			require.NoError(t, err)
			out, err := DefaultSwapDirectory(uri)
			if tcase.wantErr {
				assert.Error(t, err)
			} else {
				assert.Equal(t, tcase.wantDir, out.String())
			}
		})
	}
}

func TestDefaultSwapFile(t *testing.T) {
	tsuite := []struct {
		fileIn       string
		swapDirIn    string
		wantSwapFile string
		wantErr      bool
	}{
		// A swap directory that is the file's own directory keeps the
		// sibling name; any other directory is shared, so the entry is
		// named after the file's full path.
		{"other:///tmp/a.go", "other:///tmp", "other:///tmp/.a.go.rswp", false},
		{"file:///a.go", "file:///tmp", "file:///tmp/+a.go.rswp", false},
		{"file:///a.go", "file:///", "file:///.a.go.rswp", false},
		{"file:///tmp/a.go", "file:///tmp", "file:///tmp/.a.go.rswp", false},
		{"file:///tmp/a.go", "file:///", "file:///+tmp+a.go.rswp", false},
		{"file://./tmp/a.go", "file://./", "file://./+tmp+a.go.rswp", false},
		{"file://./a.go", "file://./tmp", "file://./tmp/+a.go.rswp", false},
		{"ssh:///a.go", "ssh://my_host/tmp", "", true},
		{"ssh://my_host/a.go", "ssh:///tmp", "", true},
		{"ssh://my_host/a.go", "ssh://creepy_host/tmp", "", true},
		{"ssh://unstablebuild@my_host/a.go", "ssh://jj.furman@my_host/tmp", "", true},
		{"ssh://user@my_host/a.go", "ssh://user@my_host/tmp", "ssh://user@my_host/tmp/+a.go.rswp", false},
		{"ssh://my_host/a.go", "ssh://my_host/tmp", "ssh://my_host/tmp/+a.go.rswp", false},
		{"ssh://my_host/./a.go", "ssh://my_host/./tmp", "ssh://my_host/tmp/+a.go.rswp", false},
		{"ssh://my_host/tmp/a.go", "ssh://my_host/tmp", "ssh://my_host/tmp/.a.go.rswp", false},
	}

	for i, tcase := range tsuite {
		desc := fmt.Sprintf("DefaultLocalSwapFile %d of %s", i, tcase.fileIn)
		t.Run(desc, func(t *testing.T) {
			uri, err := workspaceapi.ParseURI(tcase.fileIn)
			require.NoError(t, err)
			swapUri, err := workspaceapi.ParseURI(tcase.swapDirIn)
			require.NoError(t, err)

			// sut
			out, err := DefaultSwapFile(swapUri, uri)

			if tcase.wantErr {
				assert.Error(t, err)
			} else {
				assert.Equal(t, tcase.wantSwapFile, out.String())
			}
		})
	}
}

func TestCanWorkspaceURI(t *testing.T) {
	tsuite := []struct {
		workspaceURI string
		uri          string
		expectedOut  bool
	}{
		{"file:///", "file:///tmp", true},
		{"file:///tmp", "file:///tmp", true},
		{"file:///var", "file:///tmp/file", true}, // different folder but workspace can handle it
		{"file:///var", "file:///var/file", true},
		{"file:///var", "file:///var/dir/dir/dir/file", true},
		{"file:///var/", "file:///var/file", true},
		{"file:///", "ssh:///tmp", false},
	}

	for i, tcase := range tsuite {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			inURI, err := workspaceapi.ParseURI(tcase.uri)
			require.NoError(t, err)

			inWorkspaceURI, err := workspaceapi.ParseURI(tcase.workspaceURI)
			require.NoError(t, err)

			fileScheme, err := newTestFileScheme(inWorkspaceURI)
			require.NoError(t, err)
			inWorkspace := NewSchemeWorkspace(inWorkspaceURI, fileScheme, inlineSchedule)

			// sut
			actualOut, err := CanWorkspaceURI(inWorkspace, inURI)
			require.NoError(t, err)
			assert.Equal(t, tcase.expectedOut, actualOut)
		})
	}
}

func TestCanWorkspaceURIFileSchemeCanManageOutOfTreeFile(t *testing.T) {
	workspaceURI, err := workspaceapi.ParseURI("file:///project")
	require.NoError(t, err)

	uri, err := workspaceapi.ParseURI("file:///Users/example/go/pkg/mod/dep/file.go")
	require.NoError(t, err)

	fileScheme, err := newTestFileScheme(workspaceURI)
	require.NoError(t, err)
	inWorkspace := NewSchemeWorkspace(workspaceURI, fileScheme, inlineSchedule)

	actualOut, err := CanWorkspaceURI(inWorkspace, uri)
	require.NoError(t, err)
	assert.True(t, actualOut)
}

func TestIsWorkspaceURI(t *testing.T) {
	workspaceURI, err := workspaceapi.ParseURI("file:///project")
	require.NoError(t, err)

	fileScheme, err := newTestFileScheme(workspaceURI)
	require.NoError(t, err)
	inWorkspace := NewSchemeWorkspace(workspaceURI, fileScheme, inlineSchedule)

	t.Run("in tree", func(t *testing.T) {
		uri, err := workspaceapi.ParseURI("file:///project/pkg/file.go")
		require.NoError(t, err)

		actualOut, err := IsWorkspaceURI(inWorkspace, uri)
		require.NoError(t, err)
		assert.True(t, actualOut)
	})

	t.Run("out of tree", func(t *testing.T) {
		uri, err := workspaceapi.ParseURI("file:///Users/example/go/pkg/mod/dep/file.go")
		require.NoError(t, err)

		actualOut, err := IsWorkspaceURI(inWorkspace, uri)
		require.NoError(t, err)
		assert.False(t, actualOut)
	})
}

func TestURIUnderPrefix(t *testing.T) {
	for _, tc := range []struct {
		uri    string
		prefix string
		want   bool
	}{
		{"memory:///gitshow", "memory:///gitshow", true},
		{"memory:///gitshow/a.go.diff", "memory:///gitshow", true},
		{"memory:///gitshow/a/b/c.go.diff?n=2", "memory:///gitshow", true},
		{"memory:///gitshow/a.go.diff", "memory:///gitshow/", true},
		{"memory:///fexplorer", "memory:///fexplorer", true},
		// A sibling that merely shares a textual prefix is unrelated:
		// the file explorer's tests open memory:///fexplorer-test-1.
		{"memory:///fexplorer-test-1", "memory:///fexplorer", false},
		{"memory:///gitshowcase", "memory:///gitshow", false},
		{"memory:///other/a.go", "memory:///gitshow", false},
		{"file:///gitshow/a.go.diff", "memory:///gitshow", false},
		{"memory://host/gitshow/a.go", "memory:///gitshow", false},
	} {
		t.Run(fmt.Sprintf("%s in %s", tc.uri, tc.prefix), func(t *testing.T) {
			uri, err := workspaceapi.ParseURI(tc.uri)
			require.NoError(t, err)
			prefix, err := workspaceapi.ParseURI(tc.prefix)
			require.NoError(t, err)
			assert.Equal(t, tc.want, URIUnderPrefix(uri, prefix))
		})
	}
}
