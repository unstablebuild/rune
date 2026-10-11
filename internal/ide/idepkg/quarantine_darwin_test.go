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

//go:build darwin

package idepkg

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestCopyExecutablesClearsQuarantine(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	require.NoError(t, os.Mkdir(filepath.Join(srcDir, "bin"), 0o755))
	src := filepath.Join(srcDir, "bin", "ext")
	require.NoError(t, os.WriteFile(src, []byte("binary"), 0o755))
	require.NoError(t, unix.Setxattr(src, "com.apple.quarantine",
		[]byte("0081;deadbeef;Test;"), 0))

	require.NoError(t, copyExecutables(
		[]executableEntry{{Name: "bin/ext", Mode: 0o755}}, srcDir, dstDir))

	target := filepath.Join(dstDir, "ext")
	_, err := unix.Getxattr(target, "com.apple.quarantine", make([]byte, 256))
	assert.True(t, errors.Is(err, unix.ENOATTR),
		"quarantine xattr should be cleared, got %v", err)
}

func TestClearQuarantineMissingAttrIsNoError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ext")
	require.NoError(t, os.WriteFile(path, []byte("binary"), 0o755))
	require.NoError(t, clearQuarantine(path))
}
