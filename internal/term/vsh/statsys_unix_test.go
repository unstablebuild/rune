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

//go:build unix

package vsh

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sysInfo is a FileInfo with a chosen mode, size and Sys.
type sysInfo struct {
	mode fs.FileMode
	size int64
	sys  any
}

func (i sysInfo) Name() string       { return "f" }
func (i sysInfo) Size() int64        { return i.size }
func (i sysInfo) Mode() fs.FileMode  { return i.mode }
func (i sysInfo) ModTime() time.Time { return time.Time{} }
func (i sysInfo) IsDir() bool        { return i.mode.IsDir() }
func (i sysInfo) Sys() any           { return i.sys }

func TestSysStat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		info fs.FileInfo
		size int64
	}{
		{name: "a file", info: sysInfo{mode: 0o644, size: 42}, size: 42},
		{name: "an empty file", info: sysInfo{mode: 0o644}},
		{name: "a directory", info: sysInfo{mode: fs.ModeDir | 0o755, size: 4096}, size: 4096},
		{name: "a file whose mode the scheme left 0", info: sysInfo{size: 3}, size: 3},
		{name: "another owner's Stat_t is replaced", info: sysInfo{mode: 0o600, size: 7, sys: &syscall.Stat_t{Uid: 4242, Gid: 4242, Size: 1}}, size: 7},
		{name: "a scheme's own Sys is replaced", info: sysInfo{mode: 0o600, size: 9, sys: struct{ Offset int64 }{5}}, size: 9},
		{name: "a device", info: deviceInfo("null")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st, ok := sysStat(tc.info).(*syscall.Stat_t)
			require.True(t, ok, "the interpreter's -O and -G type-assert a *syscall.Stat_t")
			assert.Equal(t, uint32(os.Getuid()), st.Uid)
			assert.Equal(t, uint32(os.Getgid()), st.Gid)
			assert.Equal(t, tc.size, st.Size)
			assert.Equal(t, st, statInfo{FileInfo: tc.info}.Sys())
		})
	}
}

func TestSysStatOfHostFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	require.NoError(t, os.WriteFile(p, []byte("12345"), 0o600))
	for _, name := range []string{p, dir} {
		info, err := os.Stat(name)
		require.NoError(t, err)
		st, ok := sysStat(info).(*syscall.Stat_t)
		require.True(t, ok)
		assert.Equal(t, uint32(os.Getuid()), st.Uid)
		assert.Equal(t, uint32(os.Getgid()), st.Gid)
		assert.Equal(t, info.Size(), st.Size)
	}
}
