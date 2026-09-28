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
	"errors"
	"io"
	"os"
	"path"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/term/vte/vtegraphics"
)

// shmDir is where a POSIX shared memory object named by a t=s
// transmission is visible as a file.
const shmDir = "/dev/shm"

// maxSymlinkHops bounds symlink resolution so a loop is reported as a
// failure rather than spinning (spec §4.4).
const maxSymlinkHops = 32

// errRefused is the single failure every medium read reports: the
// caller turns it into one response so a client cannot use the
// terminal to probe the filesystem (spec §4.4).
var errRefused = errors.New("refused")

// graphicsMediumPath resolves the name a transmission carries and
// refuses locations a client has no business asking the terminal to
// read (spec §4.4).
func graphicsMediumPath(fs schemeapi.FileSystem, medium byte, name string) (string, error) {
	if name == "" {
		return "", errRefused
	}
	if medium == 's' {
		// A POSIX shared memory name is a single path component.
		name = strings.TrimPrefix(name, "/")
		if name == "" || strings.ContainsRune(name, '/') {
			return "", errRefused
		}
		p := path.Join(shmDir, name)
		// shm_open does not follow a symbolic link (O_NOFOLLOW), which
		// could lead the read anywhere, past the checks below.
		info, err := fs.Lstat(p)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", errRefused
		}
		return p, nil
	}
	if !path.IsAbs(name) {
		return "", errRefused
	}
	p, err := realPath(fs, name)
	if err != nil {
		return "", errRefused
	}
	for _, dir := range []string{"/proc", "/sys", "/dev"} {
		if p == dir || strings.HasPrefix(p, dir+"/") {
			if dir == "/dev" && strings.HasPrefix(p, shmDir+"/") {
				continue
			}
			return "", errRefused
		}
	}
	return p, nil
}

// realPath resolves every symbolic link in an absolute path, those
// naming a directory on the way included, so the location checks apply
// to the file that is actually opened. As in realpath(3), a ".."
// applies to where a link led.
func realPath(fs schemeapi.FileSystem, name string) (string, error) {
	resolved, rest, hops := "/", name, 0
	for rest != "" {
		var elem string
		elem, rest, _ = strings.Cut(rest, "/")
		switch elem {
		case "", ".":
			continue
		case "..":
			resolved = path.Dir(resolved)
			continue
		}
		p := path.Join(resolved, elem)
		info, err := fs.Lstat(p)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = p
			continue
		}
		if hops++; hops > maxSymlinkHops {
			return "", errRefused
		}
		target, err := fs.Readlink(p)
		if err != nil {
			return "", err
		}
		if path.IsAbs(target) {
			resolved = "/"
		}
		rest = target + "/" + rest
	}
	return resolved, nil
}

// readGraphicsFile reads size bytes at offset, or the rest of the file
// when size is 0. The open handle's mode is what rejects a FIFO, device
// or socket, whose read could block the terminal or have side effects.
func readGraphicsFile(f workspaceapi.File, offset, size int64) ([]byte, error) {
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errRefused
	}
	if offset < 0 || offset > info.Size() {
		return nil, errRefused
	}
	avail := info.Size() - offset
	if size <= 0 || size > avail {
		size = avail
	}
	if size > vtegraphics.MaxDataSize {
		return nil, errRefused
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, errRefused
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(f, data); err != nil {
		return nil, errRefused
	}
	return data, nil
}

// isGraphicsTempFile reports whether a t=t file is one the protocol
// says the terminal may delete: marked as belonging to the protocol and
// in a temp directory of the machine fs serves (spec §4.4). That
// machine may be remote, so its own temp directory is tempDir, when
// known, rather than this process's TMPDIR. p is resolved, so the
// directories are too.
func isGraphicsTempFile(fs schemeapi.FileSystem, p, tempDir string) bool {
	if !strings.Contains(path.Base(p), "tty-graphics-protocol") {
		return false
	}
	for _, dir := range []string{"/tmp", shmDir, tempDir} {
		if !path.IsAbs(dir) {
			continue
		}
		resolved, err := realPath(fs, dir)
		if err == nil && strings.HasPrefix(p, resolved+"/") {
			return true
		}
	}
	return false
}
