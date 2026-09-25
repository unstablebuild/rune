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
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// A stale Close(fd, oldName) RPC must not be able to reach a pty that
// happened to inherit the recycled descriptor number.
func TestFileSchemeNewFileStaleNamePreservesPty(t *testing.T) {
	tmpDir := t.TempDir()
	uri, err := makeLocalURI(tmpDir)
	require.NoError(t, err)

	s, err := newTestFileScheme(uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	path := tmpDir + "/stale.txt"
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0644))

	stale, err := s.OpenFile(path, os.O_RDONLY, 0)
	require.NoError(t, err)
	staleFd, staleName := stale.Fd(), stale.Name()
	require.NoError(t, stale.Close())

	pty, err := s.NewPty(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = pty.Master.Close()
		_ = pty.Slave.Close()
	})

	var recycled workspaceapi.File
	switch staleFd {
	case pty.Master.Fd():
		recycled = pty.Master
	case pty.Slave.Fd():
		recycled = pty.Slave
	}
	require.NotNil(t, recycled, "expected the pty to reuse the closed descriptor")

	require.Nil(t, s.NewFile(staleFd, staleName),
		"stale close must not resolve to the pty that reused the descriptor")
	require.Equal(t, recycled, s.NewFile(staleFd, recycled.Name()))
}

func TestFileSchemeNewFileNameGate(t *testing.T) {
	tmpDir := t.TempDir()
	uri, err := makeLocalURI(tmpDir)
	require.NoError(t, err)

	s, err := newTestFileScheme(uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	path := tmpDir + "/a.txt"
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0644))

	f, err := s.OpenFile(path, os.O_RDONLY, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	require.Equal(t, f, s.NewFile(f.Fd(), f.Name()))
	require.Nil(t, s.NewFile(f.Fd(), tmpDir+"/other.txt"))
}

// A resize can race terminal teardown: the async pty-resize worker may
// dequeue a SetPtySize that was queued while the terminal was alive
// only after Close has released the master. The scheme must report
// that as ErrInvalidMasterPtyFd — the same condition the workspacerpc
// server reports for a remote workspace — so the ide worker can drop
// it silently instead of raising an EBADF error toast, or worse,
// ioctl'ing whichever file has recycled the descriptor number.
func TestFileSchemeSetPtySizeClosedMaster(t *testing.T) {
	tmpDir := t.TempDir()
	uri, err := makeLocalURI(tmpDir)
	require.NoError(t, err)

	s, err := newTestFileScheme(uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	pty, err := s.NewPty(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = pty.Master.Close()
		_ = pty.Slave.Close()
	})

	require.NoError(t, s.SetPtySize(pty, workspaceapi.PtySize{Columns: 80, Rows: 24}),
		"a live master must resize fine")

	require.NoError(t, pty.Master.Close())

	err = s.SetPtySize(pty, workspaceapi.PtySize{Columns: 32, Rows: 5})
	require.ErrorIs(t, err, ErrInvalidMasterPtyFd)
	require.Contains(t, err.Error(), "invalid master pty fd",
		"the message is the wire contract the ide resize worker matches on")
}
