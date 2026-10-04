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

package text

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/imageuri"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/workspace"
)

// imageViewExtensions are the formats imageuri can decode.
var imageViewExtensions = map[string]bool{
	".gif":  true,
	".jpeg": true,
	".jpg":  true,
	".png":  true,
	".webp": true,
}

func isImageFile(file workspaceapi.URI) bool {
	return imageViewExtensions[strings.ToLower(filepath.Ext(file.Path()))]
}

var _ browserapi.Handler = (*imageViewHandler)(nil)

// imageViewHandler displays an image file. It takes no input.
type imageViewHandler struct {
	comp          *imageuri.Component
	width, height int
}

func (h *imageViewHandler) setComponent(comp *imageuri.Component) {
	h.comp = comp
	h.comp.Resize(h.width, h.height)
}

func (h *imageViewHandler) Resize(width, height int) {
	h.width, h.height = width, height
	h.comp.Resize(width, height)
}

func (h *imageViewHandler) Draw(w term.Writer) {
	h.comp.Draw(w)
}

func (h *imageViewHandler) Handle(term.Event) (exit, handled bool) {
	return false, false
}

func (h *imageViewHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}

func (h *imageViewHandler) Selection() (string, bool) {
	return "", false
}

func (h *imageViewHandler) Close() error {
	return h.comp.Close()
}

var _ workspace.FlusherCloser = (*imageFlusherCloser)(nil)

// imageFlusherCloser backs an image view tab, which cannot be saved.
// Reload replaces the displayed image with a fresh read of the file.
type imageFlusherCloser struct {
	parent  *Component
	uri     workspaceapi.URI
	handler *imageViewHandler

	mu        sync.Mutex
	lastFlush time.Time
	reloading bool
	closed    bool
}

func newImageFlusherCloser(
	parent *Component, uri workspaceapi.URI,
	handler *imageViewHandler, lastFlush time.Time,
) *imageFlusherCloser {
	return &imageFlusherCloser{
		parent:    parent,
		uri:       uri,
		handler:   handler,
		lastFlush: lastFlush,
	}
}

func (m *imageFlusherCloser) Flush(context.Context) (<-chan error, error) {
	return nil, textapi.ErrInvalidSave
}

func (m *imageFlusherCloser) ForceFlush(context.Context) (<-chan error, error) {
	return nil, textapi.ErrInvalidSave
}

func (m *imageFlusherCloser) Reload(ctx context.Context) (<-chan error, error) {
	m.mu.Lock()
	if m.reloading {
		m.mu.Unlock()
		return nil, workspace.ErrFlushInProgress
	}
	if m.closed {
		m.mu.Unlock()
		return nil, textapi.ErrInvalidReload
	}
	m.reloading = true
	m.mu.Unlock()

	result := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		next, modTime, err := m.parent.loadImage(m.uri)
		if err != nil {
			m.finishReload(result, err)
			return
		}
		if err := ctx.Err(); err != nil {
			_ = next.Close()
			m.finishReload(result, err)
			return
		}

		scheduled := m.parent.config.ScheduleNextTick(func() {
			m.replaceComponent(next, modTime)
			result <- nil
			close(result)
		})
		if !scheduled {
			_ = next.Close()
			m.finishReload(result,
				errors.New("reload image: scheduleNextTick rejected callback"))
		}
	})
	return result, nil
}

func (m *imageFlusherCloser) finishReload(result chan<- error, err error) {
	m.mu.Lock()
	m.reloading = false
	m.mu.Unlock()
	result <- err
	close(result)
}

func (m *imageFlusherCloser) replaceComponent(
	next *imageuri.Component, modTime time.Time,
) {
	m.mu.Lock()
	m.reloading = false
	closed := m.closed
	if !closed {
		m.lastFlush = modTime
	}
	m.mu.Unlock()

	if closed {
		_ = next.Close()
		return
	}
	previous := m.handler.comp
	m.handler.setComponent(next)
	_ = previous.Close()
}

func (m *imageFlusherCloser) LastFlush() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastFlush
}

func (m *imageFlusherCloser) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}
