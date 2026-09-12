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
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/debug"
	hmarkdown "unstable.build/rune/internal/handler/markdown"
	"unstable.build/rune/internal/workspace"
)

var _ workspace.FlusherCloser = (*markdownFlusherCloser)(nil)

type markdownFlusherCloser struct {
	parent    *Component
	uri       workspaceapi.URI
	handler   *hmarkdown.Handler
	component *markdown.Component

	mu        sync.Mutex
	lastFlush time.Time
	reloading bool
	closed    bool
}

func newMarkdownFlusherCloser(
	parent *Component,
	uri workspaceapi.URI,
	handler *hmarkdown.Handler,
	component *markdown.Component,
	lastFlush time.Time,
) *markdownFlusherCloser {
	return &markdownFlusherCloser{
		parent:    parent,
		uri:       uri,
		handler:   handler,
		component: component,
		lastFlush: lastFlush,
	}
}

func (m *markdownFlusherCloser) Flush(context.Context) (<-chan error, error) {
	return nil, textapi.ErrInvalidSave
}

func (m *markdownFlusherCloser) ForceFlush(context.Context) (<-chan error, error) {
	return nil, textapi.ErrInvalidSave
}

func (m *markdownFlusherCloser) Reload(ctx context.Context) (<-chan error, error) {
	m.mu.Lock()
	if m.reloading {
		m.mu.Unlock()
		return nil, workspace.ErrFlushInProgress
	}
	if m.closed {
		m.mu.Unlock()
		return nil, textapi.ErrInvalidReload
	}
	offset := m.component.SeekOffset()
	searchQuery := m.component.SearchQuery()
	m.reloading = true
	m.mu.Unlock()

	result := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		next, modTime, err := m.parent.loadMarkdown(m.uri)
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
			m.replaceComponent(next, modTime, searchQuery, offset)
			result <- nil
			close(result)
		})
		if !scheduled {
			_ = next.Close()
			m.finishReload(
				result,
				errors.New("reload markdown: scheduleNextTick rejected callback"),
			)
		}
	})
	return result, nil
}

func (m *markdownFlusherCloser) finishReload(result chan<- error, err error) {
	m.mu.Lock()
	m.reloading = false
	m.mu.Unlock()
	result <- err
	close(result)
}

func (m *markdownFlusherCloser) replaceComponent(
	next *markdown.Component,
	modTime time.Time,
	searchQuery string,
	offset int,
) {
	m.mu.Lock()
	if m.closed {
		m.reloading = false
		m.mu.Unlock()
		_ = next.Close()
		return
	}

	previous := m.component
	m.handler.SetComponent(next)
	next.Search(searchQuery)
	next.SeekTo(offset)
	m.component = next
	m.lastFlush = modTime
	m.reloading = false
	m.mu.Unlock()

	_ = previous.Close()
}

func (m *markdownFlusherCloser) LastFlush() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastFlush
}

func (m *markdownFlusherCloser) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}
