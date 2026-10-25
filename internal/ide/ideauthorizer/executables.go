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
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/hostenv"
)

// ErrExecutableNotFound is returned by ExecutableResolver.Resolve when no
// file runs for a name.
var ErrExecutableNotFound = errors.New("executable not found")

// ExecutableFingerprint identifies the contents of an executable.
type ExecutableFingerprint struct {
	SHA256  string
	Size    int64
	ModTime time.Time
}

// ExecutableResolver finds and fingerprints the executables that commands
// run on the workspace host.
type ExecutableResolver interface {
	// HostID names the workspace host, so that the same path on two
	// hosts is two executables. It is "" for this machine.
	HostID() string
	// HostPATH returns the PATH that the commands extensions start
	// inherit on the workspace host.
	HostPATH(ctx context.Context) ([]string, error)
	// Resolve returns the absolute path of the file name runs as on the
	// workspace host. A name with a slash is relative to dir, or to the
	// workspace root when dir is empty. Other names are looked up in
	// pathList or, when pathList is nil, the way the workspace host
	// looks up a command's Cmd.Path. It returns ErrExecutableNotFound
	// when there is no such file.
	Resolve(ctx context.Context, name, dir string, pathList []string) (string, error)
	// Fingerprint returns the fingerprint of the file at path, after
	// following symlinks.
	Fingerprint(ctx context.Context, path string) (ExecutableFingerprint, error)
}

// ExecutableFS is the part of a workspace file system that executables
// are found and read with.
type ExecutableFS interface {
	Stat(name string) (fs.FileInfo, error)
	Open(name string) (workspaceapi.File, error)
}

// WorkspaceResolverConfig configures NewWorkspaceResolver.
type WorkspaceResolverConfig struct {
	FS ExecutableFS
	// Host is the HostID of the workspace host.
	Host string
	// Root is the directory commands without a Dir run in.
	Root string
	// HostPATH returns the PATH of the workspace host. refresh asks for
	// it to be read again rather than remembered.
	HostPATH func(ctx context.Context, refresh bool) ([]string, error)
	// LookCmdPath, when set, looks a Cmd.Path up the way the workspace
	// host does. Otherwise it is looked up in HostPATH.
	LookCmdPath func(name string) (string, error)
	// Home is the home directory "~" expands to.
	Home string
	// DataDir is the Rune data directory $RUNE_DATADIR expands to.
	DataDir string
}

// NewWorkspaceResolver returns an ExecutableResolver that finds
// executables through a workspace's file system. It remembers each
// fingerprint until the size or modification time of the file changes.
func NewWorkspaceResolver(cfg WorkspaceResolverConfig) ExecutableResolver {
	return &workspaceResolver{cfg: cfg, cache: map[string]ExecutableFingerprint{}}
}

type workspaceResolver struct {
	cfg   WorkspaceResolverConfig
	mu    sync.Mutex
	cache map[string]ExecutableFingerprint
}

func (r *workspaceResolver) HostID() string {
	return r.cfg.Host
}

func (r *workspaceResolver) HostPATH(ctx context.Context) ([]string, error) {
	return r.cfg.HostPATH(ctx, false)
}

func (r *workspaceResolver) Resolve(
	ctx context.Context, name, dir string, pathList []string,
) (string, error) {
	name, err := r.expand(name)
	if err != nil {
		return "", err
	}
	if dir == "" {
		dir = r.cfg.Root
	}
	if strings.Contains(name, "/") {
		p := name
		if !path.IsAbs(p) {
			p = path.Join(dir, p)
		}
		if !r.isFile(p, false) {
			return "", fmt.Errorf("%s: %w", p, ErrExecutableNotFound)
		}
		return path.Clean(p), nil
	}
	if pathList != nil {
		return r.search(name, dir, pathList)
	}
	if r.cfg.LookCmdPath != nil {
		return r.cfg.LookCmdPath(name)
	}
	hostPATH, err := r.cfg.HostPATH(ctx, false)
	if err != nil {
		return "", fmt.Errorf("read PATH of the workspace host: %w", err)
	}
	if p, err := r.search(name, dir, hostPATH); err == nil {
		return p, nil
	}
	if hostPATH, err = r.cfg.HostPATH(ctx, true); err != nil {
		return "", fmt.Errorf("read PATH of the workspace host: %w", err)
	}
	return r.search(name, dir, hostPATH)
}

// expand expands a leading "~" and $RUNE_DATADIR in name, like the
// workspace host does for a Cmd.Path. Other variables are not known here.
func (r *workspaceResolver) expand(name string) (string, error) {
	if strings.Contains(name, "$") {
		unknown := false
		name = os.Expand(name, func(v string) string {
			if v == hostenv.DataDirVar && r.cfg.DataDir != "" {
				return r.cfg.DataDir
			}
			unknown = true
			return ""
		})
		if unknown {
			return "", fmt.Errorf("expand %s: %w", name, ErrExecutableNotFound)
		}
	}
	if name == "~" || strings.HasPrefix(name, "~/") {
		if r.cfg.Home == "" {
			return "", fmt.Errorf("expand %s: %w", name, ErrExecutableNotFound)
		}
		name = r.cfg.Home + name[1:]
	}
	return name, nil
}

func (r *workspaceResolver) search(name, dir string, pathList []string) (string, error) {
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
	return "", fmt.Errorf("%s: %w", name, ErrExecutableNotFound)
}

func (r *workspaceResolver) isFile(p string, executable bool) bool {
	info, err := r.cfg.FS.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return !executable || info.Mode().Perm()&0o111 != 0
}

func (r *workspaceResolver) Fingerprint(
	ctx context.Context, p string,
) (ExecutableFingerprint, error) {
	info, err := r.cfg.FS.Stat(p)
	if err != nil {
		return ExecutableFingerprint{}, fmt.Errorf("stat %s: %w", p, err)
	}
	r.mu.Lock()
	cached, ok := r.cache[p]
	r.mu.Unlock()
	if ok && cached.Size == info.Size() && cached.ModTime.Equal(info.ModTime()) {
		return cached, nil
	}
	f, err := r.cfg.FS.Open(p)
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

// contextReader stops reading once ctx is done, so that hashing a large
// executable on a remote host can be abandoned.
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
