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

package symbolresolve

import (
	"context"

	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/workspace/walkdir"
)

// registry lists the language specs in resolution-preference order. Go
// is tried first to preserve existing behavior.
var registry = []*Spec{Go, Python, Rust, Zig, TypeScript, TSX, JavaScript}

// SpecFor returns the spec for a tree-sitter language id, or nil when no
// spec is registered for that language.
func SpecFor(langID string) *Spec {
	for _, s := range registry {
		if s.LangID == langID {
			return s
		}
	}
	return nil
}

// SpecForFile returns the spec whose extensions match the given path or
// URI, or nil when no registered language claims it.
func SpecForFile(path string) *Spec {
	for _, s := range registry {
		if len(s.Extensions) > 0 && s.matchesFile(path) {
			return s
		}
	}
	return nil
}

// AllSpecs returns the registered language specs in
// resolution-preference order.
func AllSpecs() []*Spec {
	specs := make([]*Spec, len(registry))
	copy(specs, registry)
	return specs
}

// DetectSpecs walks the workspace lazily and yields each registered spec whose
// extensions match at least one present file. A spec is emitted as soon as the
// first matching file is seen, so a consumer can begin resolving against it
// before the walk completes. This is the single language-detection point for
// symbol resolution.
func DetectSpecs(ctx context.Context, fs walkdir.Reader) iterator.Iterator[Spec] {
	paths, err := walkdir.ListFiles(ctx, fs, ".")
	if err != nil {
		return iterator.Empty[Spec]()
	}

	pending := make([]*Spec, len(registry))
	copy(pending, registry)

	next := func(ctx context.Context) (Spec, bool, error) {
		for {
			file, ok := paths.Next(ctx)
			if !ok {
				return Spec{}, false, paths.Err()
			}
			for i, spec := range pending {
				if spec.matchesFile(file) {
					pending = append(pending[:i], pending[i+1:]...)
					return *spec, true, nil
				}
			}
		}
	}
	return iterator.FromFunc(next, paths.Close)
}
