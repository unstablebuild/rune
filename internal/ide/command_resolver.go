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

package ide

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ernestrc/sensible/find"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/workspace"
)

const hostPATHProbeTimeout = 10 * time.Second

// newCommandResolver returns the resolver that the command authorizer of
// the workspace at uri, rooted at root, finds executables with. dataDir
// is the Rune data directory of the workspace host.
func newCommandResolver(
	ws workspace.Workspace, uri workspaceapi.URI, root, dataDir string,
) ideauthorizer.ExecutableResolver {
	cfg := ideauthorizer.WorkspaceResolverConfig{FS: ws, Root: root, DataDir: dataDir}
	if home, err := ws.URI("~"); err == nil {
		cfg.Home = home.Path()
	}
	if uri.Scheme() == workspace.FileScheme {
		cfg.HostPATH = func(context.Context, bool) ([]string, error) {
			return filepath.SplitList(os.Getenv("PATH")), nil
		}
		cfg.LookCmdPath = lookLocalCmdPath
		return ideauthorizer.NewWorkspaceResolver(cfg)
	}
	cfg.Host = uri.Scheme() + "://" + uri.Host()
	if user := uri.User(); user != "" {
		cfg.Host = uri.Scheme() + "://" + user + "@" + uri.Host()
	}
	cfg.HostPATH = (&hostPATHProbe{exec: ws}).path
	return ideauthorizer.NewWorkspaceResolver(cfg)
}

// lookLocalCmdPath looks name up like the file scheme looks up a
// Cmd.Path: in the PATH Rune started with, then in the current one.
func lookLocalCmdPath(name string) (string, error) {
	if p, err := find.Executable(name); err == nil {
		return p, nil
	}
	p, err := exec.LookPath(name)
	if err != nil && !errors.Is(err, exec.ErrDot) {
		return "", fmt.Errorf("%s: %w", name, ideauthorizer.ErrExecutableNotFound)
	}
	return filepath.Abs(p)
}

// hostPATHProbe reads the PATH of a remote workspace host, which the
// commands extensions start there inherit, and remembers it.
type hostPATHProbe struct {
	exec schemeapi.Executor
	mu   sync.Mutex
	dirs []string
}

func (p *hostPATHProbe) path(ctx context.Context, refresh bool) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dirs != nil && !refresh {
		return p.dirs, nil
	}
	ctx, cancel := context.WithTimeout(ctx, hostPATHProbeTimeout)
	defer cancel()
	var stdout bytes.Buffer
	done := make(chan error, 1)
	if _, err := p.exec.StartCommand(ctx, workspaceapi.Cmd{
		Path:    "sh",
		Args:    []string{"-c", `printf %s "$PATH"`},
		Stdout:  &stdout,
		Watcher: workspaceapi.ChanProcessWatcher(done),
	}); err != nil {
		return nil, fmt.Errorf("start PATH probe: %w", err)
	}
	select {
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("PATH probe: %w", err)
		}
	case <-ctx.Done():
		return nil, fmt.Errorf("PATH probe: %w", ctx.Err())
	}
	p.dirs = strings.Split(stdout.String(), ":")
	return p.dirs, nil
}
