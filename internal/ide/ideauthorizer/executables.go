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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/workspace"
)

// errExecutableNotFound is returned by executableResolver.resolve when no
// file runs for a name.
var errExecutableNotFound = errors.New("executable not found")

// errExecutableUnknown is returned by executableResolver.resolve when
// what file runs for a name cannot be known, so any file may.
var errExecutableUnknown = errors.New("executable unknown")

// ExecutableFingerprint identifies the contents of an executable.
type ExecutableFingerprint struct {
	SHA256  string
	Size    int64
	ModTime time.Time
}

// executableResolver finds and fingerprints the executables the commands
// of a workspace run on its host. Which file a command runs is asked of
// the host every time, so that a change to its environment cannot make an
// approval cover another file. Each fingerprint is remembered until the
// size or modification time of the file changes.
type executableResolver struct {
	host schemeapi.Scheme
	// hostID names the workspace host, so that the same path on two hosts
	// is two executables. It is "" for this machine.
	hostID string
	// root is the directory the programs started without one run in.
	root  string
	mu    sync.Mutex
	cache map[string]ExecutableFingerprint
}

func newExecutableResolver(host schemeapi.Scheme, workspaceURI workspaceapi.URI) *executableResolver {
	r := &executableResolver{
		host:  host,
		root:  workspaceURI.Path(),
		cache: map[string]ExecutableFingerprint{},
	}
	if workspaceURI.Scheme() != workspace.FileScheme {
		r.hostID = workspaceURI.Scheme() + "://" + workspaceURI.Host()
		if user := workspaceURI.User(); user != "" {
			r.hostID = workspaceURI.Scheme() + "://" + user + "@" + workspaceURI.Host()
		}
	}
	return r
}

func (r *executableResolver) resolve(
	ctx context.Context, name, dir string, pathList []string,
) (string, error) {
	if pathList == nil {
		p, err := r.host.LookPath(ctx, name, dir)
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %w", errExecutableNotFound, err)
		}
		if err != nil {
			return "", fmt.Errorf("%w: %w", errExecutableUnknown, err)
		}
		if !path.IsAbs(p) {
			return "", fmt.Errorf("%s runs %s: %w", name, p, errExecutableUnknown)
		}
		return path.Clean(p), nil
	}
	if dir == "" {
		dir = r.root
	}
	// What a shell expands "~" and variables to depends on an
	// environment this side does not see.
	if strings.HasPrefix(name, "~") || strings.Contains(name, "$") {
		return "", fmt.Errorf("%s: %w", name, errExecutableUnknown)
	}
	if strings.Contains(name, "/") {
		p := name
		if !path.IsAbs(p) {
			p = path.Join(dir, p)
		}
		if !r.isFile(p, false) {
			return "", fmt.Errorf("%s: %w", p, errExecutableNotFound)
		}
		return path.Clean(p), nil
	}
	return r.search(name, dir, pathList)
}

func (r *executableResolver) search(name, dir string, pathList []string) (string, error) {
	for _, d := range pathList {
		if d == "" {
			d = "."
		}
		if !path.IsAbs(d) {
			d = path.Join(dir, d)
		}
		if p := path.Join(d, name); r.isFile(p, true) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: %w", name, errExecutableNotFound)
}

func (r *executableResolver) isFile(p string, executable bool) bool {
	info, err := r.host.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return !executable || info.Mode().Perm()&0o111 != 0
}

func (r *executableResolver) fingerprint(
	ctx context.Context, p string,
) (ExecutableFingerprint, error) {
	info, err := r.host.Stat(p)
	if err != nil {
		return ExecutableFingerprint{}, fmt.Errorf("stat %s: %w", p, err)
	}
	r.mu.Lock()
	cached, ok := r.cache[p]
	r.mu.Unlock()
	if ok && cached.Size == info.Size() && cached.ModTime.Equal(info.ModTime()) {
		return cached, nil
	}
	f, err := r.host.Open(p)
	if err != nil {
		return ExecutableFingerprint{}, fmt.Errorf("open %s: %w", p, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, contextReader{ctx: ctx, r: f}); err != nil {
		return ExecutableFingerprint{}, fmt.Errorf("read %s: %w", p, err)
	}
	fp := ExecutableFingerprint{
		SHA256:  hex.EncodeToString(h.Sum(nil)),
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}
	r.mu.Lock()
	r.cache[p] = fp
	r.mu.Unlock()
	return fp, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
