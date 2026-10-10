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
	"slices"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/workspace/walkdir"
)

type cachingPkgManager struct {
	pkg   syntax.PkgManager
	files sync.Map
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
	if files, ok := m.files.Load(pkgID); ok {
		return iterator.FromSlice(files.([]string)), nil
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
	m.files.Store(pkgID, slices.Clone(files))
}
