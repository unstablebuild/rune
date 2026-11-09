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

// Package testgit provides hardened helpers for invoking git from
// tests. It strips inherited GIT_* environment variables and points
// git at the test temp directory so that tests cannot accidentally
// mutate the developer's git repository.
package testgit

import (
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Env returns a hardened environment for invoking git in a test temp
// directory. It strips inherited GIT_* vars, points HOME and
// XDG_CONFIG_HOME at dir, disables reads of the host's global and
// system gitconfig, caps git's repo discovery at the dir's parent,
// marks every directory safe via GIT_CONFIG_COUNT, and supplies
// deterministic author and committer identity. PATH and other non-GIT
// vars are inherited so the git binary itself can be found.
func Env(dir string) []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+12)
	for _, kv := range base {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	ceiling := filepath.Dir(dir)
	env = append(env,
		"HOME="+dir,
		"XDG_CONFIG_HOME="+dir,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CEILING_DIRECTORIES="+ceiling,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=safe.directory",
		"GIT_CONFIG_VALUE_0=*",
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	return env
}

// Command returns an *exec.Cmd ready to run "git args..." in dir with
// the hardened env. Callers may further customise cmd.Env (e.g. by
// appending GIT_AUTHOR_DATE) or cmd.Stdin before running.
func Command(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = Env(dir)
	return cmd
}

// Run executes "git args..." in dir with the hardened env and fails
// the test on non-zero exit. Returns CombinedOutput.
func Run(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	out, err := Command(t, dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return out
}

// HTTPBackend returns a handler serving every repository under root
// (worktree or bare) over smart HTTP through `git http-backend`, so
// real git clients get the protocol features they negotiate, such as
// shallow fetches and protocol v2. It runs with the hardened env and
// skips the test when git is not on PATH.
func HTTPBackend(t *testing.T, root string) http.Handler {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available on PATH")
	}
	return &cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Dir:  root,
		Env:  append(Env(root), "GIT_PROJECT_ROOT="+root, "GIT_HTTP_EXPORT_ALL=1"),
	}
}
