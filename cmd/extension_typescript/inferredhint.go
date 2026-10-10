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

package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension/langext"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

// inferredHintTimeout bounds how long the hint waits for the server of
// an opened file to answer, which covers bringing the server up.
const inferredHintTimeout = time.Minute

// inferredHintRetry is how often the hint asks again while the server is
// not up or has not been told the file is open.
const inferredHintRetry = 250 * time.Millisecond

// tsSourceExtensions are the TypeScript dialects. A JavaScript file needs
// no tsconfig.json.
var tsSourceExtensions = []string{".ts", ".tsx", ".mts", ".cts"}

// inferredProjectHint tells the user, once per server root, that a
// TypeScript file they opened is in no tsconfig.json's project. tsgo
// checks such a file with default options, which have no path aliases
// and load no packages' types, so imports the project's own build
// resolves may not resolve in the editor.
type inferredProjectHint struct {
	// baseCtx outlives the per-event context, which the editor cancels
	// as soon as Handle returns.
	baseCtx context.Context
	lsp     semanticapi.LSP
	notify  browserapi.Notifications
	fs      workspaceapi.FileSystem
	wsURI   workspaceapi.URI

	mu     sync.Mutex
	hinted map[string]bool
}

var _ textapi.EventHandler = (*inferredProjectHint)(nil)

func newInferredProjectHint(
	baseCtx context.Context, lsp semanticapi.LSP, notify browserapi.Notifications,
	fs workspaceapi.FileSystem, wsURI workspaceapi.URI,
) *inferredProjectHint {
	return &inferredProjectHint{
		baseCtx: baseCtx, lsp: lsp, notify: notify, fs: fs, wsURI: wsURI,
		hinted: map[string]bool{},
	}
}

func (h *inferredProjectHint) Handle(_ context.Context, ev textapi.Event) bool {
	path := ev.URI.Path()
	if ev.Type != textapi.EventTypeOpen || !slices.Contains(tsSourceExtensions, filepath.Ext(path)) {
		return false
	}
	if !insideDir(h.wsURI.Path(), path) || slices.Contains(strings.Split(path, "/"), "node_modules") {
		return false
	}
	root := h.wsURI.Path()
	if r, ok := langext.FindOutermostProjectRoot(h.fs, h.wsURI, ev.URI, tsMarkers); ok {
		root = r.Dir
	}
	h.mu.Lock()
	done := h.hinted[root]
	h.mu.Unlock()
	if !done {
		go debug.CapturePanicReport(func() { h.check(ev.URI, root) })
	}
	return false
}

// check asks the TypeScript servers which project the file at uri is in
// until one knows, which takes the server being up and told the file is
// open, and hints when it is in none.
func (h *inferredProjectHint) check(uri workspaceapi.URI, root string) {
	ctx, cancel := context.WithTimeout(h.baseCtx, inferredHintTimeout)
	defer cancel()
	params := projectInfoParams{TextDocument: lspcmd.TextDocID(uri)}
	for {
		info, err := execRequest[*projectInfo](ctx, h.lsp, "custom/projectInfo", params)
		if err == nil && info != nil {
			if info.ConfigFilePath == "" && h.claim(root) {
				rel, _ := filepath.Rel(h.wsURI.Path(), uri.Path())
				_, _ = h.notify.NotifyOnce(browserapi.LevelInfo,
					"%s is in no tsconfig.json's project, so TypeScript checks it with default "+
						"options, without path aliases or packages' types such as @types/node. "+
						"Add a tsconfig.json that includes it.", rel)
			}
			return
		}
		select {
		case <-ctx.Done():
			slog.Debug("typescript project of an opened file unknown", "file", uri.Path(), "error", err)
			return
		case <-time.After(inferredHintRetry):
		}
	}
}

// claim reports whether root has not been hinted yet, marking it hinted.
func (h *inferredProjectHint) claim(root string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hinted[root] {
		return false
	}
	h.hinted[root] = true
	return true
}
