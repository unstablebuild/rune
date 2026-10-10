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

package langext

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
)

// Initializer wires editor open events to project-root discovery and
// dedupes language-server bring-up so InitRoot runs at most once per
// root. It is safe for concurrent use.
type Initializer struct {
	baseCtx context.Context
	fs      workspaceapi.FileSystem
	editor  textapi.Editor
	inst    Installer
	cfg     ProjectConfig

	mu           sync.Mutex
	initialized  map[string]Root
	initializing map[string]struct{}
	// unresolved caches parent dirs whose upward walk found no marker,
	// consulted and populated for change events only. Opens and creates
	// bypass it and re-walk, so a stale entry cannot wedge discovery when
	// a project is later scaffolded there.
	unresolved map[string]struct{}
}

// NewInitializer returns an Initializer for cfg that discovers roots
// under the editor's workspace via fs, resolves cfg.Tools through inst
// and brings the roots up through cfg.InitRoot. baseCtx must outlive the
// workspace session: event-driven bring-up runs under it rather than the
// per-event dispatch context, which the editor cancels as soon as Handle
// returns.
func NewInitializer(
	baseCtx context.Context, fs workspaceapi.FileSystem, editor textapi.Editor,
	inst Installer, cfg ProjectConfig,
) *Initializer {
	return &Initializer{
		baseCtx:      baseCtx,
		fs:           fs,
		editor:       editor,
		inst:         inst,
		cfg:          cfg,
		initialized:  make(map[string]Root),
		initializing: make(map[string]struct{}),
		unresolved:   make(map[string]struct{}),
	}
}

// Start subscribes the Initializer to the editor events in
// cfg.WatchEvents (defaulting to open only). Call it once per workspace.
// It returns the subscription error, if any.
func (i *Initializer) Start() error {
	return i.editor.SubscribeEvents(i.watchEvents(), i)
}

// Handle implements textapi.EventHandler. It discovers the project root
// for a language-matching file surfaced by an open, change, or create
// event and kicks off a deduped background bring-up. It always returns
// false so the subscription stays active; the bring-up happens off the
// calling goroutine under the Initializer's base context, so the editor
// is never blocked and the bring-up survives the per-event context being
// canceled.
func (i *Initializer) Handle(_ context.Context, ev textapi.Event) bool {
	if !i.watches(ev.Type) {
		return false
	}

	dir := filepath.Dir(ev.URI.Path())

	// A create may scaffold a project, so it drops any stale cache entry
	// before the walk. A created marker is not itself a source file;
	// discovery rides the source-file event that follows.
	if ev.Type == textapi.EventTypeCreate {
		i.forget(dir)
		if i.isMarker(ev.URI) {
			return false
		}
	}

	if i.cfg.FileMatch == nil || !i.cfg.FileMatch(ev.URI) {
		return false
	}

	if i.skip(ev.Type, dir) {
		return false
	}

	wsRoot, err := i.fs.URI(".")
	if err != nil {
		slog.Warn("langext: resolve workspace root uri", "lang", i.cfg.LanguageID, "error", err)
		return false
	}

	root, found := i.findRoot(wsRoot, ev.URI)
	if !found {
		i.markUnresolved(ev.Type, dir)
		return false
	}
	i.initializeAsync(i.baseCtx, root)
	return false
}

func (i *Initializer) findRoot(wsRoot, uri workspaceapi.URI) (Root, bool) {
	if i.cfg.Outermost {
		return FindOutermostProjectRoot(i.fs, wsRoot, uri, i.cfg.Markers)
	}
	return FindProjectRoot(i.fs, wsRoot, uri, i.cfg.Markers)
}

// watchEvents defaults to open-only when cfg.WatchEvents is empty.
func (i *Initializer) watchEvents() []textapi.EventType {
	if len(i.cfg.WatchEvents) == 0 {
		return []textapi.EventType{textapi.EventTypeOpen}
	}
	return i.cfg.WatchEvents
}

func (i *Initializer) watches(t textapi.EventType) bool {
	return slices.Contains(i.watchEvents(), t)
}

func (i *Initializer) isMarker(uri workspaceapi.URI) bool {
	return slices.Contains(i.cfg.Markers, filepath.Base(uri.Path()))
}

// skip reports whether dir needs no walk. The negative cache is honored
// for changes only; opens and creates always re-walk so a stale entry
// cannot wedge discovery.
func (i *Initializer) skip(t textapi.EventType, dir string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if t == textapi.EventTypeChange {
		if _, ok := i.unresolved[dir]; ok {
			return true
		}
	}
	for root := range i.initialized {
		if underDir(dir, root) {
			return true
		}
	}
	return false
}

// markUnresolved caches dir as marker-less. Only changes are cached, so
// opens and creates keep re-walking and can heal the entry.
func (i *Initializer) markUnresolved(t textapi.EventType, dir string) {
	if t != textapi.EventTypeChange {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.unresolved[dir] = struct{}{}
}

// forget drops cached entries at or under dir.
func (i *Initializer) forget(dir string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for cached := range i.unresolved {
		if underDir(cached, dir) {
			delete(i.unresolved, cached)
		}
	}
}

func underDir(path, dir string) bool {
	if path == dir {
		return true
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !hasParentPrefix(rel)
}

// InitializeAt eagerly brings up a specific root and returns the
// bring-up error, deduped against event-driven bring-up. It is used for
// the workspace-root project on startup. If the root is already
// initialized or in flight, it returns nil without running InitRoot
// again.
func (i *Initializer) InitializeAt(ctx context.Context, root Root) error {
	if !i.claim(root) {
		return nil
	}
	return i.run(ctx, root)
}

// Reinitialize re-runs InitRoot for every root brought up so far. It is
// used by consumers that must rebuild language servers after an
// out-of-band change (e.g. a Rust toolchain switch). Concurrent
// event-driven inits remain deduped: a root still initializing is
// skipped rather than run twice.
func (i *Initializer) Reinitialize(ctx context.Context) error {
	i.mu.Lock()
	roots := make([]Root, 0, len(i.initialized))
	for dir, root := range i.initialized {
		delete(i.initialized, dir)
		i.initializing[dir] = struct{}{}
		roots = append(roots, root)
	}
	i.mu.Unlock()

	var errs []error
	for _, root := range roots {
		if err := i.run(ctx, root); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Tools returns a lookup of cfg.Tools like the one bring-up gets, for
// work outside a project root such as a console command.
func (i *Initializer) Tools() *Tools {
	return &Tools{inst: i.inst, pkgID: i.cfg.LanguageID, names: i.cfg.Tools}
}

// initializeAsync claims root and, if newly claimed, spawns the bring-up
// goroutine so the editor's event delivery is never blocked.
func (i *Initializer) initializeAsync(ctx context.Context, root Root) {
	if !i.claim(root) {
		return
	}
	go debug.CapturePanicReport(func() {
		if err := i.run(ctx, root); err != nil {
			slog.Warn("langext: init project root failed",
				"lang", i.cfg.LanguageID, "root", root.Dir, "error", err)
		}
	})
}

// claim marks root as in-flight and reports whether the caller won the
// claim. A root already initialized or initializing is not re-claimed.
func (i *Initializer) claim(root Root) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, done := i.initialized[root.Dir]; done {
		return false
	}
	if _, busy := i.initializing[root.Dir]; busy {
		return false
	}
	i.initializing[root.Dir] = struct{}{}
	return true
}

// run executes InitRoot for a claimed root and transitions its state:
// on success the root becomes initialized; on failure it is released so
// a later open can retry rather than wedging the language for the
// session.
func (i *Initializer) run(ctx context.Context, root Root) error {
	err := i.cfg.InitRoot(ctx, root, i.Tools())
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.initializing, root.Dir)
	if err != nil {
		return err
	}
	i.initialized[root.Dir] = root
	return nil
}
