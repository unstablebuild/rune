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

// Package langext packages the language-agnostic machinery a Rune
// language extension needs to discover a project root for an opened file
// and initialize its language server rooted there, deduped per root.
//
// A language plugs in through ProjectConfig: the LSP language id, the
// project-root marker files to walk for, a predicate that recognizes the
// language's files, and a callback that performs the language-specific
// bring-up for a discovered root. Initializer wires those to editor open
// events and guarantees InitRoot runs at most once per project root.
package langext

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/workspace/walkdir"
)

// ProjectConfig is the per-language plug-in contract consumed by
// Initializer. Every field is required.
type ProjectConfig struct {
	// LanguageID is the LSP language id (e.g. "python", "go", "rust").
	LanguageID string

	// Markers are project-root marker files or directories, probed at
	// each candidate directory while walking up from an opened file
	// (e.g. "pyproject.toml", "go.mod", "Cargo.toml"). A directory
	// matches if any marker Stats successfully.
	Markers []string

	// FileMatch reports whether an opened file belongs to this language
	// (e.g. its extension is ".py"). Non-matching opens are ignored
	// cheaply before any filesystem walk.
	FileMatch func(uri workspaceapi.URI) bool

	// InitRoot performs the language-specific bring-up for a discovered
	// project root: environment setup plus building and calling
	// lsp.Initialize with the nested root URI. It runs in a background
	// goroutine; Initializer guarantees exactly one call per root.
	InitRoot func(ctx context.Context, root Root) error

	// WatchEvents lists the editor event types that drive discovery,
	// defaulting to {EventTypeOpen} when empty. Include EventTypeChange
	// and EventTypeCreate for a language whose files are written
	// out-of-band, so nested projects come up without an editor buffer.
	WatchEvents []textapi.EventType
}

// Root describes a discovered project root.
type Root struct {
	// Dir is the filesystem path of the project root.
	Dir string
	// URI is the file:// URI of the project root, used for RootURI.
	URI string
	// RelPath is the root path relative to the workspace root.
	RelPath string
}

// FindProjectRoot walks upward from fileURI looking for the nearest
// directory that carries one of cfg.Markers, stopping at the workspace
// root identified by workspaceRootURI. The opened file must live inside
// the workspace root; otherwise it returns found=false without probing.
//
// The nearest enclosing marked directory wins, so a file deep in a tree
// initializes against its own project rather than an ancestor. A file
// with no enclosing marker (within the workspace) yields found=false, so
// a stray source file does not spin up a language server.
func FindProjectRoot(
	fs workspaceapi.FileSystem, workspaceRootURI workspaceapi.URI, fileURI workspaceapi.URI,
	markers []string,
) (Root, bool) {
	wsDir := filepath.Clean(workspaceRootURI.Path())
	file := filepath.Clean(fileURI.Path())

	rel, err := filepath.Rel(wsDir, file)
	if err != nil || rel == ".." || hasParentPrefix(rel) {
		return Root{}, false
	}

	// The Rel check above guarantees file is within wsDir, so the walk
	// always terminates at wsDir.
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		if dirHasMarker(fs, dir, markers) {
			return rootForDir(fs, wsDir, dir), true
		}
		if dir == wsDir {
			return Root{}, false
		}
	}
}

// dirHasMarker reports whether dir contains any of the marker files or
// directories.
func dirHasMarker(fs workspaceapi.FileSystem, dir string, markers []string) bool {
	for _, m := range markers {
		if _, err := fs.Stat(filepath.Join(dir, m)); err == nil {
			return true
		}
	}
	return false
}

// FindProjectRoots streams every project root at or below the workspace
// root, including roots nested inside another root. Each Next advances the
// walk only as far as the next root. Roots are yielded depth-first and
// pre-order, subdirectories in lexical order.
//
// Descent stops at maxDepth and skips directories ignore matches; a nil
// ignore disables filtering.
func FindProjectRoots(
	fs workspaceapi.FileSystem, workspaceRootURI workspaceapi.URI,
	markers []string, ignore walkdir.Filter, maxDepth int,
) iterator.Iterator[Root] {
	wsDir := filepath.Clean(workspaceRootURI.Path())
	stack := []scanFrame{{dir: wsDir}}
	next := func(ctx context.Context) (Root, bool, error) {
		for len(stack) > 0 {
			if err := ctx.Err(); err != nil {
				return Root{}, false, err
			}
			frame := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if frame.expanded {
				stack = expand(stack, fs, ignore, frame, maxDepth)
				continue
			}
			frame.expanded = true
			if dirHasMarker(fs, frame.dir, markers) {
				// Deferred so reaching a root never reads more than it must.
				stack = append(stack, frame)
				return rootForDir(fs, wsDir, frame.dir), true, nil
			}
			stack = expand(stack, fs, ignore, frame, maxDepth)
		}
		return Root{}, false, nil
	}
	return iterator.FromFunc(next, func() error { return nil })
}

// scanFrame is a directory the project scan has yet to finish with. rel is
// carried because the ignore filter matches workspace-relative paths.
type scanFrame struct {
	dir      string
	rel      string
	depth    int
	expanded bool
}

func expand(
	stack []scanFrame, fs workspaceapi.FileSystem,
	ignore walkdir.Filter, frame scanFrame, maxDepth int,
) []scanFrame {
	if frame.depth >= maxDepth {
		return stack
	}
	return pushSubdirs(stack, fs, ignore, frame)
}

// pushSubdirs appends frame's unignored subdirectories in reverse lexical
// order, so the caller's stack pops them in lexical order.
func pushSubdirs(
	stack []scanFrame, fs workspaceapi.FileSystem,
	ignore walkdir.Filter, frame scanFrame,
) []scanFrame {
	entries, err := fs.ReadDir(frame.dir)
	if err != nil {
		return stack
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, name := range names {
		rel := filepath.Join(frame.rel, name)
		if ignore != nil && ignore.MatchRelPath(rel, true) {
			continue
		}
		stack = append(stack, scanFrame{
			dir:   filepath.Join(frame.dir, name),
			rel:   rel,
			depth: frame.depth + 1,
		})
	}
	return stack
}

// rootForDir builds a Root for an absolute project directory.
func rootForDir(fs workspaceapi.FileSystem, wsDir, dir string) Root {
	rel, err := filepath.Rel(wsDir, dir)
	if err != nil {
		rel = ""
	}
	if rel == "." {
		rel = ""
	}
	return Root{Dir: dir, URI: uriForDir(fs, dir), RelPath: rel}
}

// uriForDir returns the file:// URI for an absolute directory. ty/ruff
// and other language servers run on the same host as the workspace
// files, so the path is rewritten to the file:// scheme they expect
// regardless of the workspace's own URI scheme.
func uriForDir(fs workspaceapi.FileSystem, dir string) string {
	if uri, err := fs.URI(dir); err == nil {
		return fmt.Sprintf("file://%s", uri.Path())
	}
	return fmt.Sprintf("file://%s", dir)
}

// hasParentPrefix reports whether a cleaned relative path escapes its
// base via a leading "../" segment.
func hasParentPrefix(rel string) bool {
	return len(rel) >= 3 && rel[0] == '.' && rel[1] == '.' && rel[2] == filepath.Separator
}
