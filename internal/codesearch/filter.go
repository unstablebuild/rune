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

package codesearch

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/codesearch/index"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/workspace/walkdir"
)

// filter is what a search leaves out: hidden folders, excluded folders
// and what git ignores.
type filter struct {
	ignore   vctrl.Matcher
	excluded []string
}

func newFilter(fsys workspaceapi.FileSystem, excluded []string) *filter {
	f := &filter{}
	if m, err := vctrl.LoadGitignore(fsys); err == nil {
		f.ignore = m
	}
	for _, dir := range excluded {
		if dir = strings.Trim(path.Clean(filepath.ToSlash(dir)), "/"); dir != "" && dir != "." {
			f.excluded = append(f.excluded, dir)
		}
	}
	return f
}

// MatchRelPath implements walkdir.Filter.
func (f *filter) MatchRelPath(rel string, isDir bool) bool {
	rel = filepath.ToSlash(rel)
	if isDir {
		if strings.HasPrefix(path.Base(rel), ".") || slices.ContainsFunc(f.excluded, func(dir string) bool {
			return rel == dir || strings.HasPrefix(rel, dir+"/")
		}) {
			return true
		}
	}
	return f.ignore != nil && f.ignore.MatchRelPath(rel, isDir)
}

// skips reports whether a walk of the workspace leaves out the file rel.
func (f *filter) skips(rel string) bool {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		if f.MatchRelPath(strings.Join(parts[:i], "/"), true) {
			return true
		}
	}
	return f.MatchRelPath(rel, false)
}

// leavesOut reports whether a walk of the workspace leaves out root, a
// folder or file, or what is in it.
func (f *filter) leavesOut(root string) bool {
	return root != "." && (f.skips(root) || f.MatchRelPath(root, true))
}

// below returns the filter of a walk of root, a folder f leaves out. As
// ripgrep does, it filters only what is below root: hidden folders, the
// excluded folders in root and, unless git ignores root, what git
// ignores. Git cannot tell whether it ignores a path for itself or for
// root, so below a root it ignores nothing is.
func (f *filter) below(root string) walkdir.Filter {
	b := &filter{}
	if f.ignore != nil && !f.ignore.MatchRelPath(root, true) {
		b.ignore = f.ignore
	}
	for _, dir := range f.excluded {
		if dir != root && under(dir, []string{root}) {
			b.excluded = append(b.excluded, dir)
		}
	}
	return b
}

// walk lists the files under roots, workspace-relative folders or files
// ("." for the whole workspace), sorted in index order. A root that does
// not exist has no files. A root the filter leaves out is walked all
// the same: only what is below it is filtered, as ripgrep does.
func (f *filter) walk(ctx context.Context, fsys workspaceapi.FileSystem, roots []string) ([]string, error) {
	var out []string
	var walkErr error
	for _, root := range roots {
		var flt walkdir.Filter = f
		if root != "." {
			info, err := fsys.Stat(root)
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					walkErr = errors.Join(walkErr, err)
				}
				continue
			}
			if !info.IsDir() {
				if info.Mode().IsRegular() {
					out = append(out, root)
				}
				continue
			}
			if f.leavesOut(root) {
				flt = f.below(root)
			}
		}
		wctx := walkdir.WithContextFilter(ctx, flt)
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
			out = append(out, path.Clean(filepath.ToSlash(rel)))
		}
		walkErr = errors.Join(walkErr, it.Err(), it.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(out, comparePaths)
	return slices.Compact(out), walkErr
}

func comparePaths(a, b string) int {
	return index.MakePath(a).Compare(index.MakePath(b))
}

// cleanRoots returns roots as workspace-relative slash paths, "." for
// the whole workspace, which is what no roots search.
func cleanRoots(roots []string) []string {
	var out []string
	for _, r := range roots {
		r = strings.Trim(path.Clean(filepath.ToSlash(r)), "/")
		if r == "" {
			r = "."
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return []string{"."}
	}
	return out
}

// outermost returns roots without those in another of them.
func outermost(roots []string) []string {
	if slices.Contains(roots, ".") {
		return []string{"."}
	}
	// A folder sorts before what is in it.
	sorted := slices.Sorted(slices.Values(roots))
	var kept []string
	for _, r := range sorted {
		if !under(r, kept) {
			kept = append(kept, r)
		}
	}
	return kept
}

// under reports whether rel is one of roots or in one of them.
func under(rel string, roots []string) bool {
	for _, r := range roots {
		if r == "." || rel == r || strings.HasPrefix(rel, r+"/") {
			return true
		}
	}
	return false
}
