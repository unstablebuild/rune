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

package vctrl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/sourcegraph/go-diff/diff"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// NewGitCommand returns a Service powered by the local git installation,
// using the local git CLI.
func NewGitCommand(
	cwd workspaceapi.URI, exec workspaceapi.Executor, fs workspaceapi.FileSystem,
) Service {
	c := new(cmdGitService)
	c.exec = exec
	c.cwd = cwd
	c.fs = fs
	return c
}

type cmdGitService struct {
	exec workspaceapi.Executor
	cwd  workspaceapi.URI
	fs   workspaceapi.FileSystem
}

type gitExecError struct {
	exit   error
	stderr string
}

func (e *gitExecError) Error() string {
	return fmt.Sprintf(
		"process exit with non-zero status (%s) %v", e.exit.Error(), e.stderr,
	)
}

func (c *cmdGitService) Diff(ctx context.Context, file workspaceapi.URI) (FileDiff, error) {
	relFile, err := c.RelPath(ctx, file.Path())
	if err != nil {
		return FileDiff{}, fmt.Errorf("rel path: %w", err)
	}

	repoPath, err := c.repoPath(ctx, file.Path())
	if err != nil {
		return FileDiff{}, fmt.Errorf("repo path: %w", err)
	}

	// reminder: the `relFile` must exist relative to `repoPath` for the `git
	// diff` to work. It could be `dir1/file1.sh` and `/a/b/c/my-repo`
	// respectively or `/a/b/c/my-repo/dir` and `file1.sh`. At the moment we
	// relativize around repo root path, so it's the former.
	out, err := c.git(ctx, repoPath, []string{"diff", "-U0", "--no-ext-diff", relFile})
	if err != nil {
		return FileDiff{}, fmt.Errorf("git cmd: %w", err)
	}

	if out == "" {
		return FileDiff{}, ErrDiffNoChanges
	}

	r := diff.NewFileDiffReader(bytes.NewBufferString(out))
	d, err := r.Read()
	if err != nil {
		return FileDiff{}, fmt.Errorf("file diff reader: %w", err)
	}
	if d == nil {
		return FileDiff{}, errors.New("parse nil diff")
	}
	return gitFileDiff(d), nil
}

// gitFileDiff converts a parsed "git diff" into the hunk numbering the
// rest of this package speaks, which ConvertChangesToFileDiff — the
// source the other Service implementation diffs through — defines.
func gitFileDiff(d *diff.FileDiff) FileDiff {
	ret := FileDiff{OrigName: d.OrigName, NewName: d.NewName}
	for _, hunk := range d.Hunks {
		h := Hunk{
			OrigStartLine: hunk.OrigStartLine,
			OrigLines:     hunk.OrigLines,
			NewStartLine:  hunk.NewStartLine,
			NewLines:      hunk.NewLines,
			Section:       hunk.Section,
			Body:          string(hunk.Body),
		}
		// git reports a removal as "+c,0", naming the new-side line the
		// removed block followed. NewStartLine names the line it sat at.
		if h.NewLines == 0 {
			h.NewStartLine++
		}
		ret.Hunks = append(ret.Hunks, h)
	}
	return ret
}

// devNull is how a unified diff names the absent side of an added or
// deleted file.
const devNull = "/dev/null"

func (c *cmdGitService) WorkingDiff(
	ctx context.Context, p workspaceapi.URI, contextLines int,
) ([]FileDiff, error) {
	repoPath, err := c.repoPath(ctx, p.Path())
	if err != nil {
		return nil, fmt.Errorf("repo path: %w", err)
	}

	base, err := c.diffBase(ctx, repoPath)
	if err != nil {
		return nil, err
	}

	// The base is a commit rather than the index: a review of
	// "everything uncommitted" has to show staged and unstaged work
	// alike. core.quotePath is off so non-ASCII paths arrive verbatim
	// instead of octal-escaped and quoted.
	out, err := c.gitOutput(ctx, repoPath, []string{
		"-c", "core.quotePath=false",
		"diff", fmt.Sprintf("-U%d", max(0, contextLines)),
		"--no-ext-diff", "--no-color", "--find-renames", base,
	})
	if err != nil {
		return nil, fmt.Errorf("git cmd: %w", err)
	}

	var ret []FileDiff
	if strings.TrimSpace(out) != "" {
		parsed, err := diff.ParseMultiFileDiff([]byte(out))
		if err != nil {
			return nil, fmt.Errorf("parse multi file diff: %w", err)
		}
		for _, d := range parsed {
			if d == nil {
				continue
			}
			fd := FileDiff{
				OrigName: trimDiffPrefix(d.OrigName),
				NewName:  trimDiffPrefix(d.NewName),
			}
			if len(d.Hunks) == 0 && fd.OrigName == fd.NewName {
				// A mode-only change: no content moved and no path
				// changed, and a patch review has no way to show a
				// permission bit. A pure rename also has no hunks, but
				// its two names are the reviewable part.
				continue
			}
			for _, hunk := range d.Hunks {
				fd.Hunks = append(fd.Hunks, Hunk{
					OrigStartLine: hunk.OrigStartLine,
					OrigLines:     hunk.OrigLines,
					NewStartLine:  hunk.NewStartLine,
					NewLines:      hunk.NewLines,
					Section:       hunk.Section,
					Body:          string(hunk.Body),
				})
			}
			ret = append(ret, fd)
		}
	}

	untracked, err := c.untrackedDiffs(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	return append(ret, untracked...), nil
}

// diffBase is HEAD, or the empty tree in a repository whose first
// commit has not landed yet: work staged in an unborn branch is still
// uncommitted work, and diffing against a missing HEAD only errors out.
func (c *cmdGitService) diffBase(ctx context.Context, repoPath string) (string, error) {
	if _, err := c.git(ctx, repoPath,
		[]string{"rev-parse", "--verify", "--quiet", "HEAD"}); err == nil {
		return "HEAD", nil
	}
	empty, err := c.git(ctx, repoPath,
		[]string{"hash-object", "-t", "tree", "/dev/null"})
	if err != nil {
		return "", fmt.Errorf("git cmd: %w", err)
	}
	return empty, nil
}

// untrackedDiffs renders every file git does not track yet as an
// all-additions diff. git itself will not diff them without staging, and
// a review that hides brand new files is misleading.
func (c *cmdGitService) untrackedDiffs(ctx context.Context, repoPath string) (
	[]FileDiff, error,
) {
	out, err := c.gitOutput(ctx, repoPath,
		[]string{"ls-files", "--others", "--exclude-standard", "-z"})
	if err != nil {
		return nil, fmt.Errorf("git cmd: %w", err)
	}

	var ret []FileDiff
	for rel := range strings.SplitSeq(out, "\x00") {
		if rel == "" {
			continue
		}
		content, err := c.readFile(filepath.Join(repoPath, rel))
		if err != nil || !utf8.Valid(content) ||
			bytes.IndexByte(content, 0) >= 0 {
			continue
		}
		lines := strings.Split(string(content), "\n")
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = lines[:n-1]
		}
		if len(lines) == 0 {
			continue
		}
		var body strings.Builder
		for _, line := range lines {
			body.WriteString("+")
			body.WriteString(line)
			body.WriteString("\n")
		}
		ret = append(ret, FileDiff{
			OrigName: devNull,
			NewName:  rel,
			Hunks: []Hunk{{
				NewStartLine: 1,
				NewLines:     int32(len(lines)),
				Body:         body.String(),
			}},
		})
	}
	return ret, nil
}

func (c *cmdGitService) readFile(path string) ([]byte, error) {
	f, err := c.fs.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// trimDiffPrefix strips the a/ and b/ prefixes git puts on diff paths.
func trimDiffPrefix(name string) string {
	if name == devNull {
		return name
	}
	if rest, ok := strings.CutPrefix(name, "a/"); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(name, "b/"); ok {
		return rest
	}
	return name
}

func (c *cmdGitService) CurrentCommit(
	ctx context.Context, file workspaceapi.URI,
) (string, error) {
	_, err := c.RelPath(ctx, file.Path())
	if err != nil {
		return "", fmt.Errorf("rel path: %w", err)
	}
	out, err := c.git(ctx, file.Path(), []string{"rev-parse", "HEAD"})
	if err != nil {
		return "", err
	}
	return out, err
}

func (c *cmdGitService) ShortRef(
	ctx context.Context, file workspaceapi.URI,
) (string, error) {
	_, err := c.RelPath(ctx, file.Path())
	if err != nil {
		return "", fmt.Errorf("rel path: %w", err)
	}
	out, err := c.git(ctx, file.Path(), []string{"rev-parse", "--abbrev-ref", "HEAD"})
	if err != nil {
		return "", err
	}
	return out, err
}

func (c *cmdGitService) RemoteURL(
	ctx context.Context, file workspaceapi.URI, remoteName string,
) (string, error) {
	if remoteName == "" {
		return "", errors.New("must pass remote name")
	}
	out, err := c.git(ctx, file.Path(), []string{"remote", "get-url", remoteName})
	if err != nil {
		return "", err
	}
	return out, err
}

func (c *cmdGitService) ListRemotes(
	ctx context.Context, path workspaceapi.URI,
) ([]string, error) {
	return nil, errors.New("unimplemented")
}

func (c *cmdGitService) RelPath(ctx context.Context, file string) (
	relFile string, err error,
) {
	repo, err := c.repoPath(ctx, file)
	if err != nil {
		return relFile, fmt.Errorf("repo path: %w", err)
	}

	if !filepath.IsAbs(file) {
		file = path.Join(c.cwd.Path(), file)
	}

	// process file path since usually on macOS temp folders on
	// `/var/folders/...` are living really under `/private/var/folders/...`
	// (the former is symlinked).
	file = filepath.Clean(file)

	relFile, err = filepath.Rel(repo, file)
	if err != nil {
		return relFile, fmt.Errorf("file path rel: %w", err)
	}

	return relFile, nil
}

// git executes commands using the Git CLI.
func (c *cmdGitService) git(ctx context.Context, workPath string, args []string) (
	string, error,
) {
	out, err := c.gitOutput(ctx, workPath, args)
	return strings.TrimSpace(out), err
}

// gitOutput is git without the trailing whitespace trim, which would eat
// the gutter of a diff line holding nothing but an empty context line.
func (c *cmdGitService) gitOutput(
	ctx context.Context, workPath string, args []string,
) (string, error) {
	if workPath == "" {
		return "", errors.New("call git cmd on empty path")
	}

	if !filepath.IsAbs(workPath) {
		workPath = path.Join(c.cwd.Path(), workPath)
	}

	// ensure workDir is a directory and not a file path
	workDir := workPath
	isDir, err := c.isDir(workDir)
	if err != nil {
		return "", fmt.Errorf("check if is dir: %w", err)
	}
	if !isDir {
		workDir = filepath.Dir(workDir)
	}

	var stdout, stderr bytes.Buffer
	ch := make(chan error)
	cmd := workspaceapi.Cmd{
		Path:    "git",
		Dir:     workDir,
		Args:    args,
		Watcher: workspaceapi.ChanProcessWatcher(ch),
		Stdout:  &stdout,
		Stderr:  &stderr,
	}
	if _, err := c.exec.Start(ctx, cmd); err != nil {
		return "", fmt.Errorf("start process: %v", err)
	}
	if err := <-ch; err != nil {
		// clean any new line there might be
		return "", &gitExecError{
			exit:   err,
			stderr: strings.ReplaceAll(stderr.String(), "\n", " "),
		}
	}

	return stdout.String(), nil
}

// repoPath provides the local file path of the repository.
func (c *cmdGitService) repoPath(ctx context.Context, workPath string) (string, error) {
	p, err := c.git(ctx, workPath, []string{"rev-parse", "--show-toplevel"})
	if err != nil {
		return "", fmt.Errorf("git cmd: %w", err)
	}

	return p, err
}

// isDir detects if the passed file is a directory or not in the file system.
func (c *cmdGitService) isDir(file string) (bool, error) {
	info, err := c.fs.Stat(file)
	if err != nil {
		return false, err
	}
	return info.IsDir(), err
}
