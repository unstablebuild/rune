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

package llmsearch

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/workspace/walkdir"
)

// candidate is a file the crawl decided to index.
type candidate struct {
	path          string
	size, modTime int64
	inode, device uint64
}

// crawlReport counts what a crawl left out, by reason.
type crawlReport struct {
	skipped        map[string]int
	skippedFolders map[string]int
}

// Reasons a folder is not walked into.
const (
	skipDeveloperFolder = "developer folder"
	skipNoindexFolder   = ".noindex folder"
	skipExcludedFolder  = "excluded folder"
	skipVendoredFolder  = "vendored or built code"
	skipSubmodule       = "submodule or worktree"
	skipSpecialFile     = "link or special file"
)

// crawlFilter is the walk's filter: hidden folders, the folders code
// search does not go into (counted by reason), and what git ignores.
type crawlFilter struct {
	fs       workspaceapi.FileSystem
	ignore   vctrl.Matcher
	excluded []string

	mu      sync.Mutex
	folders map[string]int
}

func newCrawlFilter(fsys workspaceapi.FileSystem, opts Options) *crawlFilter {
	f := &crawlFilter{fs: fsys, folders: map[string]int{}}
	if m, err := vctrl.LoadGitignore(fsys); err == nil {
		f.ignore = m
	}
	for _, dir := range opts.ExcludedFolders {
		if dir = strings.Trim(path.Clean(dir), "/"); dir != "" && dir != "." {
			f.excluded = append(f.excluded, dir)
		}
	}
	return f
}

// MatchRelPath implements walkdir.Filter.
func (f *crawlFilter) MatchRelPath(rel string, isDir bool) bool {
	if isDir {
		if strings.HasPrefix(path.Base(rel), ".") {
			return true
		}
		if reason := f.folderReason(rel); reason != "" {
			f.mu.Lock()
			f.folders[reason]++
			f.mu.Unlock()
			return true
		}
	}
	return f.ignore != nil && f.ignore.MatchRelPath(rel, isDir)
}

func (f *crawlFilter) folderReason(rel string) string {
	name := path.Base(rel)
	switch {
	case skippedFolderNames[name]:
		return skipDeveloperFolder
	case strings.HasSuffix(name, ".noindex"):
		return skipNoindexFolder
	case slices.ContainsFunc(f.excluded, func(dir string) bool {
		return rel == dir || strings.HasPrefix(rel, dir+"/")
	}):
		return skipExcludedFolder
	case codeSkippedFolderNames[name]:
		return skipVendoredFolder
	}
	// A submodule's or a worktree's .git is a file: someone else's code,
	// or a copy of the repo's own.
	if info, err := f.fs.Stat(path.Join(rel, ".git")); err == nil && !info.IsDir() {
		return skipSubmodule
	}
	return ""
}

// walked reports whether a walk from the workspace root reaches rel: none
// of the folders it is in is filtered out.
func (f *crawlFilter) walked(rel string) bool {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		if f.MatchRelPath(strings.Join(parts[:i], "/"), true) {
			return false
		}
	}
	return true
}

// crawl lists the code files under roots (workspace-relative files or
// folders, "." for the whole workspace), sorted by path, and what it left
// out. A root that does not exist has no files. A non-nil error means a
// walk did not finish: files it did not list may still exist.
func crawl(
	ctx context.Context, fsys workspaceapi.FileSystem, roots []string, opts Options,
) ([]candidate, crawlReport, error) {
	filter := newCrawlFilter(fsys, opts)
	report := crawlReport{skipped: map[string]int{}}
	var out []candidate
	seen := map[string]bool{}
	add := func(rel string) {
		if seen[rel] {
			return
		}
		seen[rel] = true
		info, err := fsys.Stat(rel)
		if err != nil {
			return
		}
		if !info.Mode().IsRegular() {
			report.skipped[skipSpecialFile]++
			return
		}
		open := func() (io.ReadCloser, error) { return fsys.OpenFile(rel, os.O_RDONLY, 0) }
		if reason := classifyCode(path.Base(rel), info.Size(), open, opts); reason != "" {
			report.skipped[reason]++
			return
		}
		inode, device := fileID(info)
		out = append(out, candidate{
			path: rel, size: info.Size(), modTime: info.ModTime().UnixNano(), inode: inode, device: device,
		})
	}

	var walkErr error
	wctx := walkdir.WithContextFilter(ctx, filter)
	for _, root := range roots {
		root = path.Clean(root)
		if root != "." {
			if !filter.walked(root) {
				continue
			}
			info, err := fsys.Stat(root)
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					walkErr = errors.Join(walkErr, err)
				}
				continue
			}
			if !info.IsDir() {
				if !filter.MatchRelPath(root, false) {
					add(root)
				}
				continue
			}
			if filter.MatchRelPath(root, true) {
				continue
			}
		}
		it, err := walkdir.ListFiles(wctx, fsys, root)
		if err != nil {
			walkErr = errors.Join(walkErr, err)
			continue
		}
		for {
			rel, ok := it.Next(wctx)
			if !ok {
				break
			}
			add(path.Clean(rel))
		}
		walkErr = errors.Join(walkErr, it.Err(), it.Close())
	}
	if err := ctx.Err(); err != nil {
		walkErr = err
	}
	slices.SortFunc(out, func(a, b candidate) int { return strings.Compare(a.path, b.path) })
	report.skippedFolders = filter.folders
	return out, report, walkErr
}
