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

package extension

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/vctrl/gitenv"
)

const gitTimeout = 10 * time.Second

// gitIdentity captures the identity of a git repository and its worktrees.
// Two worktrees of the same repo share the same commonDir.
type gitIdentity struct {
	commonDir string            // absolute path from `git rev-parse --git-common-dir`
	worktrees map[string]string // workspace URI string → filepath.Base(path)
}

// resolveGitIdentity discovers the git common directory and all worktrees
// for the repository at cwd. The worktrees map is keyed by full URI strings
// (matching the scheme/host of cwdURI) so it can be compared directly
// against Dialogue.WorkspaceURI. Returns an empty gitIdentity if cwd is not
// inside a git repo or on any error.
func resolveGitIdentity(ctx context.Context, exec workspaceapi.Executor, cwdURI workspaceapi.URI) gitIdentity {
	cwd := cwdURI.Path()
	commonDir, err := runGitOutput(ctx, exec, cwd, "rev-parse", "--git-common-dir")
	if err != nil {
		return gitIdentity{}
	}
	commonDir = strings.TrimSpace(commonDir)
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(cwd, commonDir)
	}
	commonDir = filepath.Clean(commonDir)

	porcelain, err := runGitOutput(ctx, exec, cwd, "worktree", "list", "--porcelain")
	if err != nil {
		return gitIdentity{}
	}

	wts := make(map[string]string)
	for _, line := range strings.Split(porcelain, "\n") {
		if wtPath, ok := strings.CutPrefix(line, "worktree "); ok {
			wtPath = strings.TrimSpace(wtPath)
			if wtPath == "" {
				continue
			}
			// Build a URI with the same scheme/host as cwdURI but the
			// worktree's filesystem path, so it matches Dialogue.WorkspaceURI.
			wtURI, err := workspaceapi.ParseURI(
				cwdURI.Scheme() + "://" + cwdURI.Host() + wtPath,
			)
			if err != nil {
				continue
			}
			wts[wtURI.String()] = filepath.Base(wtPath)
		}
	}

	return gitIdentity{commonDir: commonDir, worktrees: wts}
}

// runGitOutput runs a git command in the given directory and returns its stdout.
func runGitOutput(ctx context.Context, exec workspaceapi.Executor, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	watcher := workspaceapi.ChanProcessWatcher(make(chan error, 1))

	_, err := exec.Start(ctx, workspaceapi.Cmd{
		Path:    "git",
		Args:    args,
		Dir:     dir,
		Env:     gitenv.Environ(),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Watcher: watcher,
	})
	if err != nil {
		return "", err
	}

	select {
	case procErr := <-watcher.WatchProcess():
		if procErr != nil {
			return "", procErr
		}
	case <-ctx.Done():
		return "", ctx.Err()
	}

	return stdout.String(), nil
}
