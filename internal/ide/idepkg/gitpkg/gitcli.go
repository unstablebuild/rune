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

package gitpkg

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"unstable.build/rune/internal/ide/procattr"
	"unstable.build/rune/internal/ide/vctrl/gitenv"
)

// maxStderrTail bounds how much of git's stderr is folded into errors.
const maxStderrTail = 1024

// lookGit resolves the git executable from PATH on every call, so
// installing or removing git takes effect without restarting Rune. ok
// is false when git is unavailable, which leaves go-git's result final.
func lookGit() (path string, ok bool) {
	path, err := exec.LookPath("git")
	return path, err == nil
}

// withCLIFallback runs goGit, and when it fails, cli with the git
// executable on PATH: go-git avoids subprocess overhead, while the
// CLI reaches what the user's git setup can reach (credential helpers,
// SSH, URL rewrites, proxies), such as private repositories. A failure
// caused by ctx ending is not retried, and without git on PATH go-git's
// error is returned. When both fail, the error wraps both causes.
func withCLIFallback[T any](
	ctx context.Context, pkgID string,
	goGit func() (T, error), cli func(gitBin string) (T, error),
) (T, error) {
	v, goGitErr := goGit()
	if goGitErr == nil || ctx.Err() != nil {
		return v, goGitErr
	}
	gitBin, ok := lookGit()
	if !ok {
		return v, goGitErr
	}
	log.WithField(logging.KeyClass, "gitpkg.manager").WithError(goGitErr).
		Infof("go-git failed for %s, retrying with the git CLI", pkgID)
	v, err := cli(gitBin)
	if err != nil {
		return v, fmt.Errorf("%w; git CLI fallback: %w", goGitErr, err)
	}
	return v, nil
}

// cliListRefs lists the remote's HEAD and tags like `git ls-remote`.
// HEAD is reported as the commit it resolves to, and peeled (^{})
// entries are omitted to match go-git's default listing.
func cliListRefs(ctx context.Context, gitBin, url string) ([]*plumbing.Reference, error) {
	out, err := runGit(ctx, gitBin, os.TempDir(),
		"ls-remote", "--", url, "HEAD", "refs/tags/*")
	if err != nil {
		return nil, err
	}
	var refs []*plumbing.Reference
	for line := range strings.Lines(out) {
		hex, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || strings.HasSuffix(name, "^{}") {
			continue
		}
		hash, ok := plumbing.FromHex(hex)
		if !ok {
			return nil, fmt.Errorf("unexpected ls-remote output %q", line)
		}
		refs = append(refs, plumbing.NewHashReference(plumbing.ReferenceName(name), hash))
	}
	return refs, nil
}

// cliClone clones url into the empty directory dir at rv and returns
// the checked-out commit's committer time. A failure may leave a
// partial clone in dir.
func cliClone(
	ctx context.Context, gitBin, dir, url string, rv resolvedVersion,
) (time.Time, error) {
	var steps [][]string
	switch {
	case rv.head:
		steps = [][]string{{"clone", "--quiet", "--depth=1", "--no-tags", "--", url, "."}}
	case rv.tag != "":
		// clone --branch would pick a branch named like the tag over it.
		steps = [][]string{
			{"init", "--quiet"},
			{"fetch", "--quiet", "--depth=1", "--no-tags", "--", url, "refs/tags/" + rv.tag},
			{"checkout", "--quiet", "--detach", "FETCH_HEAD"},
		}
	default:
		// The commit may be unreachable from any tip of a shallow clone.
		steps = [][]string{
			{"clone", "--quiet", "--no-checkout", "--", url, "."},
			{"checkout", "--quiet", "--detach", rv.commitRev, "--"},
		}
	}
	for _, args := range steps {
		if _, err := runGit(ctx, gitBin, dir, args...); err != nil {
			return time.Time{}, err
		}
	}
	return cliCommitTime(ctx, gitBin, dir), nil
}

// cliCommitTime returns the committer time of HEAD in dir, or the zero
// time when it cannot be read, matching commitTime.
func cliCommitTime(ctx context.Context, gitBin, dir string) time.Time {
	out, err := runGit(ctx, gitBin, dir,
		"log", "-1", "--no-show-signature", "--format=%cI", "HEAD")
	if err != nil {
		return time.Time{}
	}
	createdAt, err := time.Parse(time.RFC3339, strings.TrimSpace(out))
	if err != nil {
		return time.Time{}
	}
	return createdAt
}

// emptyDir removes everything inside dir, keeping dir itself.
func emptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// runGit runs git in dir and returns its stdout. Rune owns the
// terminal, so git must never prompt on it: terminal prompts are
// disabled and git, along with any ssh it spawns, runs detached from
// the controlling terminal. Credential helpers and agents still apply.
// Checkouts keep the repository's line endings whatever the user's
// core.autocrlf and core.eol, matching go-git. A cancelled ctx is
// returned as is.
func runGit(ctx context.Context, gitBin, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, gitBin, append([]string{
		"-c", "core.autocrlf=false", "-c", "core.eol=lf",
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(gitenv.Sanitize(cmd.Environ()), "GIT_TERMINAL_PROMPT=0")
	cmd.SysProcAttr = procattr.NewSession(true, false)
	// git hands transfers to helpers (git-remote-https, ssh) that
	// killing git alone would leave running.
	cmd.Cancel = func() error { return procattr.KillGroup(cmd.Process) }
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("git %s: %w%s", args[0], err, stderrTail(stderr.Bytes()))
	}
	return stdout.String(), nil
}

func stderrTail(stderr []byte) string {
	stderr = bytes.TrimSpace(stderr)
	if len(stderr) == 0 {
		return ""
	}
	if len(stderr) > maxStderrTail {
		stderr = stderr[len(stderr)-maxStderrTail:]
	}
	return ": " + string(stderr)
}
