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
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

const (
	// SwapDirName is the data-directory entry that holds swap files
	// when they are not kept next to the file being edited.
	SwapDirName = "swap"

	// swapDirMode keeps swap directories Rune creates private: a swap
	// entry holds the full contents of whatever the user is editing.
	swapDirMode os.FileMode = 0700

	// swapNameMax is the smallest NAME_MAX in common use (ext4, APFS,
	// XFS and NFS all cap an entry name at 255 bytes).
	swapNameMax = 255

	// swapPathSeparator stands in for "/" in a mangled entry name.
	// Every path here is also parsed as a URI, which rules out the "%"
	// other editors use because url.Parse reads it as a truncated
	// percent-escape, and users type these names at a prompt, which
	// rules out "!" and "^" (history expansion, globbing), "~" and "="
	// (leading-character expansion) and "#" (comment, URI fragment).
	swapPathSeparator = "+"
)

// SwapDirectory resolves dir to a directory URI on the host that owns
// file. An empty dir keeps the swap next to the file.
//
// The path stays literal so it is resolved by the scheme that owns the
// file, keeping remote workspaces' swaps remote.
func SwapDirectory(dir string, file workspaceapi.URI) (workspaceapi.URI, error) {
	if dir == "" {
		return DefaultSwapDirectory(file)
	}
	return workspaceapi.WithPath(file, dir)
}

// swapFileName returns the directory that holds filePath's swap entry
// and the full path of that entry.
//
// An empty swapDir, or one equal to the file's own directory, selects
// the sibling layout. Any other directory is shared with unrelated
// files, where a basename-derived entry would collide.
func swapFileName(swapDir, filePath string) (string, string) {
	filePath = path.Clean(filePath)
	fileDir := path.Dir(filePath)
	if swapDir != "" {
		swapDir = path.Clean(swapDir)
	}
	if swapDir == "" || swapDir == fileDir {
		return fileDir, path.Join(fileDir,
			"."+path.Base(filePath)+SwapFileExtensionName)
	}
	return swapDir, path.Join(swapDir, mangleSwapEntryName(filePath))
}

// sharedSwapDir returns "" for a swap directory that is just the
// edited file's own directory. A configured one may not exist yet, so
// it has to be created before the entry is opened with O_EXCL.
func sharedSwapDir(swapDir, filePath string) string {
	if swapDir == "" || path.Clean(swapDir) == path.Dir(path.Clean(filePath)) {
		return ""
	}
	return swapDir
}

// mangleSwapEntryName flattens filePath into one entry name. A literal
// separator in the path is doubled so two distinct paths can never
// mangle to the same entry.
func mangleSwapEntryName(filePath string) string {
	name := strings.ReplaceAll(filePath, swapPathSeparator,
		swapPathSeparator+swapPathSeparator)
	name = strings.ReplaceAll(name, "/", swapPathSeparator)
	if len(name)+len(SwapFileExtensionName) <= swapNameMax {
		return name + SwapFileExtensionName
	}
	// Keep the tail, which is the part a human recognises, and restore
	// the uniqueness truncation destroys with a digest of the path.
	sum := sha256.Sum256([]byte(filePath))
	suffix := "-" + hex.EncodeToString(sum[:8]) + SwapFileExtensionName
	return truncateTail(name, swapNameMax-len(suffix)) + suffix
}

// truncateTail returns the last n bytes of s, dropping a leading
// partial rune so the result stays valid UTF-8.
func truncateTail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}
