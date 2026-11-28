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

package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/workspace/walkdir"
)

// cachingPkgManager remembers the files a package lists until one of the
// directories holding them changes, so highlighting a stream of snippets
// does not list the package for each one. Installing or switching to
// another version points the package's lib dir at another directory, and
// adding or removing a file changes the modification time of its
// directory.
type cachingPkgManager struct {
	pkg   syntax.PkgManager
	files sync.Map // map[string]cachedFiles
}

type cachedFiles struct {
	files []string
	dirs  []cachedDir
}

type cachedDir struct {
	path string
	info os.FileInfo
}

// current reports whether every directory holding the files is the one
// they were listed from, unchanged.
func (c cachedFiles) current() bool {
	for _, d := range c.dirs {
		info, err := os.Stat(d.path)
		if err != nil || !os.SameFile(info, d.info) || !info.ModTime().Equal(d.info.ModTime()) {
			return false
		}
	}
	return true
}

type cacheablePkgFilesIterator struct {
	iterator.Iterator[string]
	pkg   *cachingPkgManager
	pkgID string
}

func newCachingPkgManager(pkg syntax.PkgManager) *cachingPkgManager {
	return &cachingPkgManager{pkg: pkg}
}

func (m *cachingPkgManager) LibDir(ctx context.Context, pkgID string) (iterator.Iterator[string], error) {
	if cached, ok := m.files.Load(pkgID); ok && cached.(cachedFiles).current() {
		return iterator.FromSlice(cached.(cachedFiles).files), nil
	}

	ctx = walkdir.ContextWithWorkerCount(ctx, 1)
	it, err := m.pkg.LibDir(ctx, pkgID)
	if err != nil {
		return nil, err
	}
	return cacheablePkgFilesIterator{Iterator: it, pkg: m, pkgID: pkgID}, nil
}

func (it cacheablePkgFilesIterator) cache(files []string) {
	it.pkg.cache(it.pkgID, files)
}

func (m *cachingPkgManager) cache(pkgID string, files []string) {
	var dirs []cachedDir
	for _, file := range files {
		dir := filepath.Dir(file)
		if slices.ContainsFunc(dirs, func(d cachedDir) bool { return d.path == dir }) {
			continue
		}
		info, err := os.Stat(dir)
		if err != nil {
			return
		}
		dirs = append(dirs, cachedDir{path: dir, info: info})
	}
	m.files.Store(pkgID, cachedFiles{files: slices.Clone(files), dirs: dirs})
}
