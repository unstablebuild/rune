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

package vte

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/workspace"
)

// localFS returns a scheme rooted at a temporary directory, so the
// medium tests exercise the real filesystem including symlinks.
func localFS(t *testing.T) (schemeapi.FileSystem, string) {
	t.Helper()
	dir := t.TempDir()
	uri, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	s, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

// memoryFS returns an in-memory filesystem, standing in for a workspace
// whose paths need not exist on the machine running the test.
func memoryFS(t *testing.T) schemeapi.FileSystem {
	t.Helper()
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	s, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func writeFile(t *testing.T, fs schemeapi.FileSystem, name string, data []byte) {
	t.Helper()
	f, err := fs.Create(name)
	require.NoError(t, err)
	_, err = f.Write(data)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

// lockProbeFS counts the filesystem calls made while the terminal lock
// is held.
type lockProbeFS struct {
	schemeapi.FileSystem
	mu     *sync.Mutex
	calls  int
	locked int
}

func (fs *lockProbeFS) probe() {
	fs.calls++
	if !fs.mu.TryLock() {
		fs.locked++
		return
	}
	fs.mu.Unlock()
}

func (fs *lockProbeFS) Lstat(name string) (os.FileInfo, error) {
	fs.probe()
	return fs.FileSystem.Lstat(name)
}

func (fs *lockProbeFS) OpenFile(name string, flag int, perm os.FileMode) (workspaceapi.File, error) {
	fs.probe()
	return fs.FileSystem.OpenFile(name, flag, perm)
}

func (fs *lockProbeFS) Remove(name string) error {
	fs.probe()
	return fs.FileSystem.Remove(name)
}

// pixels is a 2x2 opaque red RGBA raster.
func pixels() []byte {
	out := make([]byte, 0, 16)
	for range 4 {
		out = append(out, 255, 0, 0, 255)
	}
	return out
}

func TestGraphicsFileMedium(t *testing.T) {
	fs, dir := localFS(t)
	h := newGraphicsHarnessFS(t, testCell, fs)
	p := filepath.Join(dir, "img.rgba")
	require.NoError(t, os.WriteFile(p, pixels(), 0o600))

	h.apc("a=t,i=1,s=2,v=2,t=f", []byte(p))
	assert.Equal(t, "\x1b_Gi=1;OK\x1b\\", h.responses())
	assert.FileExists(t, p, "t=f never deletes")

	t.Run("S and O select a range", func(t *testing.T) {
		padded := filepath.Join(dir, "padded.rgba")
		require.NoError(t, os.WriteFile(padded, append([]byte("HEAD"), pixels()...), 0o600))
		h.apc("a=t,i=2,s=2,v=2,t=f,O=4,S=16", []byte(padded))
		assert.Equal(t, "\x1b_Gi=2;OK\x1b\\", h.responses())
	})

	t.Run("a missing file is refused", func(t *testing.T) {
		h.apc("a=t,i=3,s=2,v=2,t=f", []byte(filepath.Join(dir, "nope")))
		assert.Equal(t, "\x1b_Gi=3;EBADF:Failed to read image file\x1b\\", h.responses())
	})

	t.Run("a directory is refused", func(t *testing.T) {
		h.apc("a=t,i=4,s=2,v=2,t=f", []byte(dir))
		assert.Equal(t, "\x1b_Gi=4;EBADF:Failed to read image file\x1b\\", h.responses())
	})

	t.Run("a relative path is refused", func(t *testing.T) {
		h.apc("a=t,i=5,s=2,v=2,t=f", []byte("img.rgba"))
		assert.Equal(t, "\x1b_Gi=5;EBADF:Failed to read image file\x1b\\", h.responses())
	})

	t.Run("a symlink into /proc is refused", func(t *testing.T) {
		link := filepath.Join(dir, "sneaky")
		require.NoError(t, os.Symlink("/proc/self/environ", link))
		h.apc("a=t,i=6,s=2,v=2,t=f", []byte(link))
		assert.Equal(t, "\x1b_Gi=6;EBADF:Failed to read image file\x1b\\", h.responses())
	})

	t.Run("a symlinked directory into /proc is refused", func(t *testing.T) {
		link := filepath.Join(dir, "procdir")
		require.NoError(t, os.Symlink("/proc/self", link))
		h.apc("a=t,i=10,s=2,v=2,t=f", []byte(link+"/environ"))
		assert.Equal(t, "\x1b_Gi=10;EBADF:Failed to read image file\x1b\\", h.responses())
	})

	t.Run("a symlink loop is refused", func(t *testing.T) {
		a := filepath.Join(dir, "loop-a")
		b := filepath.Join(dir, "loop-b")
		require.NoError(t, os.Symlink(b, a))
		require.NoError(t, os.Symlink(a, b))
		h.apc("a=t,i=7,s=2,v=2,t=f", []byte(a))
		assert.Equal(t, "\x1b_Gi=7;EBADF:Failed to read image file\x1b\\", h.responses())
	})

	t.Run("a symlink to a regular file is followed", func(t *testing.T) {
		link := filepath.Join(dir, "link.rgba")
		require.NoError(t, os.Symlink(p, link))
		h.apc("a=t,i=8,s=2,v=2,t=f", []byte(link))
		assert.Equal(t, "\x1b_Gi=8;OK\x1b\\", h.responses())
	})

	t.Run("a name longer than the limit is refused", func(t *testing.T) {
		long := make([]byte, 2049)
		for i := range long {
			long[i] = 'x'
		}
		h.apc("a=t,i=9,s=2,v=2,t=f", long)
		assert.Equal(t, "\x1b_Gi=9;EINVAL:Filename too long\x1b\\", h.responses())
	})
}

// TestGraphicsMediumReadOutsideLock pins that a transmission is read
// without the terminal lock: drawing waits on that lock, and the
// workspace filesystem may be remote.
func TestGraphicsMediumReadOutsideLock(t *testing.T) {
	fs, dir := localFS(t)
	probe := &lockProbeFS{FileSystem: fs}
	h := newGraphicsHarnessFS(t, testCell, probe)
	probe.mu = h.ph.sync.mu.(*sync.Mutex)
	p := filepath.Join(dir, "img.rgba")
	require.NoError(t, os.WriteFile(p, pixels(), 0o600))
	tmp := filepath.Join(os.TempDir(), "tty-graphics-protocol-lock.rgba")
	require.NoError(t, os.WriteFile(tmp, pixels(), 0o600))
	t.Cleanup(func() { _ = os.Remove(tmp) })
	h.ph.tempDir = os.TempDir()

	h.apc("a=T,i=1,s=2,v=2,t=f", []byte(p))
	assert.Equal(t, "\x1b_Gi=1;OK\x1b\\", h.responses())
	h.apc("a=f,i=1,s=2,v=2,t=t", []byte(tmp))
	assert.Equal(t, "\x1b_Gi=1,r=2;OK\x1b\\", h.responses())
	assert.NoFileExists(t, tmp)

	assert.Positive(t, probe.calls)
	assert.Zero(t, probe.locked, "the filesystem was used under the terminal lock")
}

// TestGraphicsMediumRealPath checks realPath against
// filepath.EvalSymlinks: a link naming a directory on the way is
// resolved too, and ".." applies to where a link led.
func TestGraphicsMediumRealPath(t *testing.T) {
	fs, dir := localFS(t)
	target := filepath.Join(dir, "target")
	require.NoError(t, os.Mkdir(target, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(target, "file"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sibling"), nil, 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "nested"), 0o700))
	require.NoError(t, os.Symlink("../target", filepath.Join(dir, "nested", "up")))
	require.NoError(t, os.Symlink("target", filepath.Join(dir, "rel")))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "abs")))
	require.NoError(t, os.Symlink("rel/file", filepath.Join(dir, "chain")))

	for _, name := range []string{
		"target/file",
		"rel/file",
		"abs/file",
		"chain",
		"nested/up/file",
		"abs/../sibling",
		"nested/up/../sibling",
		"./rel/./file",
	} {
		t.Run(name, func(t *testing.T) {
			p := dir + "/" + name
			want, err := filepath.EvalSymlinks(p)
			require.NoError(t, err)
			got, err := realPath(fs, p)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

func TestGraphicsTempFileMedium(t *testing.T) {
	fs, dir := localFS(t)
	h := newGraphicsHarnessFS(t, testCell, fs)
	h.ph.tempDir = os.TempDir()

	keep := filepath.Join(dir, "plain.rgba")
	require.NoError(t, os.WriteFile(keep, pixels(), 0o600))
	h.apc("a=t,i=1,s=2,v=2,t=t", []byte(keep))
	assert.Equal(t, "\x1b_Gi=1;OK\x1b\\", h.responses())
	assert.FileExists(t, keep, "a file outside a temp dir is kept")

	tmp := filepath.Join(os.TempDir(), "tty-graphics-protocol-test.rgba")
	require.NoError(t, os.WriteFile(tmp, pixels(), 0o600))
	t.Cleanup(func() { _ = os.Remove(tmp) })
	h.apc("a=t,i=2,s=2,v=2,t=t", []byte(tmp))
	assert.Equal(t, "\x1b_Gi=2;OK\x1b\\", h.responses())
	assert.NoFileExists(t, tmp, "a marked file in a temp dir is deleted")
}

// TestGraphicsTempFileLocation pins which t=t files are deleted. The
// workspace filesystem may be remote, so this process's TMPDIR says
// nothing about it: only /tmp, /dev/shm and the temp dir configured for
// the workspace count.
func TestGraphicsTempFileLocation(t *testing.T) {
	t.Setenv("TMPDIR", "/local/tmp")
	tests := []struct {
		name    string
		path    string
		tempDir string
		deleted bool
	}{
		{"marked file in /tmp", "/tmp/tty-graphics-protocol-a.rgba", "", true},
		{"marked file in /dev/shm", "/dev/shm/tty-graphics-protocol-a.rgba", "", true},
		{"marked file in the workspace temp dir", "/work/tmp/tty-graphics-protocol-a.rgba", "/work/tmp", true},
		{"marked file in the local TMPDIR", "/local/tmp/tty-graphics-protocol-a.rgba", "", false},
		{"marked file outside a temp dir", "/work/tty-graphics-protocol-a.rgba", "/work/tmp", false},
		{"unmarked file in /tmp", "/tmp/image.rgba", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := memoryFS(t)
			writeFile(t, fs, tt.path, pixels())
			h := newGraphicsHarnessFS(t, testCell, fs)
			h.ph.tempDir = tt.tempDir
			h.apc("a=t,i=1,s=2,v=2,t=t", []byte(tt.path))
			assert.Equal(t, "\x1b_Gi=1;OK\x1b\\", h.responses())
			_, err := fs.Lstat(tt.path)
			if tt.deleted {
				assert.ErrorIs(t, err, os.ErrNotExist)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGraphicsMediumWithoutFilesystem(t *testing.T) {
	h := newGraphicsHarness(t, testCell)
	h.apc("a=t,i=1,s=2,v=2,t=f", []byte("/tmp/x.rgba"))
	assert.Equal(t, "\x1b_Gi=1;EBADF:Failed to read image file\x1b\\", h.responses())
}

func TestGraphicsSharedMemoryName(t *testing.T) {
	fs := memoryFS(t)
	writeFile(t, fs, "/dev/shm/kitty-shm", nil)
	writeFile(t, fs, "/proc/self/environ", nil)
	require.NoError(t, fs.Symlink("/proc/self/environ", "/dev/shm/link"))
	tests := []struct {
		name string
		want bool
	}{
		{"kitty-shm", true},
		{"/kitty-shm", true},
		{"sub/dir", false},
		{"", false},
		{"missing", false},
		// shm_open does not follow a symbolic link, which could lead
		// anywhere.
		{"link", false},
	}
	for _, tt := range tests {
		p, err := graphicsMediumPath(fs, 's', tt.name)
		if !tt.want {
			assert.Error(t, err, tt.name)
			continue
		}
		require.NoError(t, err, tt.name)
		assert.Equal(t, "/dev/shm/kitty-shm", p)
	}
}
