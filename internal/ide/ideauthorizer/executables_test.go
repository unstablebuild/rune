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
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func TestExecutableResolverResolve(t *testing.T) {
	t.Parallel()

	host := newFakeHost("/usr/bin/missing", "/bin/missing", "/first/missing")
	host.write("/first/plain", "x", 0o644, time.Time{})
	host.write("/first/dir", "", fs.ModeDir|0o755, time.Time{})
	r := newExecutableResolver(host, testWorkspace)
	cases := []struct {
		name     string
		dir      string
		pathList []string
		want     string
	}{
		{name: "tool", want: "/usr/bin/tool"},
		{name: "./run", dir: "/first", want: "/first/run"},
		{name: "./run", want: "/ws/run"},
		{name: "plain", pathList: []string{"/first", "/second"}, want: "/second/plain"},
		{name: "tool", pathList: []string{"/second"}, want: "/second/tool"},
		{name: "./run", pathList: []string{"/first"}, want: "/ws/run"},
		{name: "../ws/run", dir: "/first", pathList: []string{"/second"}, want: "/ws/run"},
		{name: "run", pathList: []string{"."}, want: "/ws/run"},
		{name: "run", dir: "/first", pathList: []string{"bin"}, want: "/first/bin/run"},
	}
	for _, tc := range cases {
		got, err := r.resolve(context.Background(), tc.name, tc.dir, tc.pathList)
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want, got, tc.name)
	}
	assert.Equal(t, []string{"", "/first", ""}, host.dirs,
		"the host is asked with the command's own Dir, which it defaults")
	for _, tc := range []struct {
		name     string
		pathList []string
		want     error
	}{
		{name: "missing", want: errExecutableNotFound},
		{name: "missing", pathList: []string{"/first"}, want: errExecutableNotFound},
		{name: "plain", pathList: []string{"/first"}, want: errExecutableNotFound},
		{name: "/first/dir", pathList: []string{"/first"}, want: errExecutableNotFound},
		// The shell expands these against an environment not seen here.
		{name: "~/bin/h", pathList: []string{"/first"}, want: errExecutableUnknown},
		{name: "$HOME/bin/h", pathList: []string{"/first"}, want: errExecutableUnknown},
	} {
		_, err := r.resolve(context.Background(), tc.name, "", tc.pathList)
		assert.ErrorIs(t, err, tc.want, tc.name)
		other := errExecutableUnknown
		if tc.want == errExecutableUnknown {
			other = errExecutableNotFound
		}
		assert.NotErrorIs(t, err, other, tc.name)
	}
}

func TestExecutableResolverResolveAsksTheHostEveryTime(t *testing.T) {
	t.Parallel()

	host := newFakeHost()
	host.setPATH("/old")
	r := newExecutableResolver(host, testWorkspace)
	got, err := r.resolve(context.Background(), "tool", "", nil)
	require.NoError(t, err)
	assert.Equal(t, "/old/tool", got)

	host.setPATH("/new", "/old")
	got, err = r.resolve(context.Background(), "tool", "", nil)
	require.NoError(t, err)
	assert.Equal(t, "/new/tool", got)
}

func TestExecutableResolverResolveWhatTheHostCannotTell(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		host hostAnswering
	}{
		{name: "a host that cannot tell", host: hostAnswering{err: errors.ErrUnsupported}},
		// A host that names no file by its absolute path does not tell
		// which one runs.
		{name: "a relative path", host: hostAnswering{path: "bin/tool"}},
	}
	for _, tc := range cases {
		tc.host.fakeHost = newFakeHost()
		r := newExecutableResolver(tc.host, testWorkspace)
		for _, name := range []string{"tool", "./tool"} {
			_, err := r.resolve(context.Background(), name, "", nil)
			assert.ErrorIs(t, err, errExecutableUnknown, tc.name)
			assert.NotErrorIs(t, err, errExecutableNotFound, tc.name)
		}
		got, err := r.resolve(context.Background(), "tool", "", []string{"/ws"})
		require.NoError(t, err, tc.name)
		assert.Equal(t, "/ws/tool", got, "the files are still read")
	}
}

func TestExecutableResolverHostID(t *testing.T) {
	t.Parallel()

	for uri, want := range map[string]string{
		"file:///ws":           "",
		"ssh://host/ws":        "ssh://host",
		"ssh://u@host:2222/ws": "ssh://u@host:2222",
		"rune://peer/ws":       "rune://peer",
	} {
		parsed, err := workspaceapi.ParseURI(uri)
		require.NoError(t, err)
		assert.Equal(t, want, newExecutableResolver(newFakeHost(), parsed).hostID, uri)
	}
}

func TestExecutableResolverFingerprint(t *testing.T) {
	t.Parallel()

	host := newFakeHost()
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	host.write("/usr/bin/tool", "one", 0o755, mtime)
	r := newExecutableResolver(host, testWorkspace)
	fp, err := r.fingerprint(context.Background(), "/usr/bin/tool")
	require.NoError(t, err)
	assert.Equal(t, sha256Hex("one"), fp.SHA256)
	assert.Equal(t, int64(3), fp.Size)
	assert.True(t, fp.ModTime.Equal(mtime))

	// Same size and modification time: the cached hash is kept.
	host.write("/usr/bin/tool", "two", 0o755, mtime)
	fp, err = r.fingerprint(context.Background(), "/usr/bin/tool")
	require.NoError(t, err)
	assert.Equal(t, sha256Hex("one"), fp.SHA256)

	host.write("/usr/bin/tool", "two", 0o755, mtime.Add(time.Minute))
	fp, err = r.fingerprint(context.Background(), "/usr/bin/tool")
	require.NoError(t, err)
	assert.Equal(t, sha256Hex("two"), fp.SHA256)

	host.setMissing("/usr/bin/tool", true)
	_, err = r.fingerprint(context.Background(), "/usr/bin/tool")
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

// testWorkspace is the workspace whose commands the resolvers and
// authorizers under test check.
var testWorkspace = func() workspaceapi.URI {
	uri, err := workspaceapi.ParseURI("file:///ws")
	if err != nil {
		panic(err)
	}
	return uri
}()

// fakeHost is a workspace host on which every file is an executable
// holding its own path, unless it is missing or was written, or the host
// is bare. A Cmd started on it without a Dir runs in /ws, and one whose
// Path has no slash runs the first file by that name in the directories
// of its PATH, /usr/bin:/bin unless set.
type fakeHost struct {
	workspacetest.NopScheme
	mu       sync.Mutex
	bare     bool
	pathList []string
	files    map[string]hostFile
	missing  map[string]bool
	// dirs are the Dirs the host was asked which file runs in.
	dirs []string
}

type hostFile struct {
	content string
	mode    fs.FileMode
	modTime time.Time
}

func newFakeHost(missing ...string) *fakeHost {
	h := &fakeHost{
		pathList: []string{"/usr/bin", "/bin"},
		files:    map[string]hostFile{},
		missing:  map[string]bool{},
	}
	for _, p := range missing {
		h.missing[p] = true
	}
	return h
}

// newBareHost returns a fakeHost with no files but those written.
func newBareHost() *fakeHost {
	h := newFakeHost()
	h.bare = true
	return h
}

func (h *fakeHost) setPATH(dirs ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pathList = dirs
}

// write makes p a file holding content, modified at modTime.
func (h *fakeHost) write(p, content string, mode fs.FileMode, modTime time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[p] = hostFile{content: content, mode: mode, modTime: modTime}
	delete(h.missing, p)
}

func (h *fakeHost) setMissing(p string, missing bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.missing[p] = missing
}

func (h *fakeHost) file(p string) (hostFile, bool) {
	if h.missing[p] {
		return hostFile{}, false
	}
	if f, ok := h.files[p]; ok {
		return f, true
	}
	return hostFile{content: p, mode: 0o755}, !h.bare
}

func (h *fakeHost) Stat(p string) (fs.FileInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f, ok := h.file(p)
	if !ok {
		return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
	}
	return workspacetest.FileInfo{
		Filename: path.Base(p), FileIsDir: f.mode.IsDir(), FileModTime: f.modTime,
		FileSize: int64(len(f.content)), FileMode: f.mode,
	}, nil
}

func (h *fakeHost) Open(p string) (workspaceapi.File, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f, ok := h.file(p)
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	return openedFile{File: workspacetest.NewFile(), r: strings.NewReader(f.content)}, nil
}

func (h *fakeHost) LookPath(_ context.Context, name, dir string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dirs = append(h.dirs, dir)
	var candidates []string
	switch {
	case path.IsAbs(name):
		candidates = []string{name}
	case strings.Contains(name, "/"):
		candidates = []string{path.Join(cmp.Or(dir, "/ws"), name)}
	default:
		for _, d := range h.pathList {
			candidates = append(candidates, path.Join(d, name))
		}
	}
	for _, p := range candidates {
		if f, ok := h.file(p); ok && f.mode.IsRegular() && f.mode&0o111 != 0 {
			return p, nil
		}
	}
	return "", fs.ErrNotExist
}

// openedFile reads the contents of a file of a fakeHost.
type openedFile struct {
	workspaceapi.File
	r *strings.Reader
}

func (f openedFile) Read(p []byte) (int, error) { return f.r.Read(p) }

// hostAnswering is a fakeHost that answers which file runs for any name
// with path, or fails with err.
type hostAnswering struct {
	*fakeHost
	path string
	err  error
}

func (h hostAnswering) LookPath(context.Context, string, string) (string, error) {
	return h.path, h.err
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
