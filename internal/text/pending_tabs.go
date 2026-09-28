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
	"fmt"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
)

var _ browserapi.Handler = (*pendingHandler)(nil)

// pendingHandler is the content of a tab restored before the extension
// owning its URI's scheme registered a resource opener. It tells the user
// what the tab is waiting for, or why it could not be opened, until the
// content the extension opened for the URI takes its place through
// PendingTabs.Replace.
type pendingHandler struct {
	uri     workspaceapi.URI
	message *component.ResponsiveString
	width   int
	height  int
	// done is closed once the tab no longer waits, which stops its
	// loading spinner.
	done     chan struct{}
	doneOnce sync.Once
}

func newPendingHandler(uri workspaceapi.URI) *pendingHandler {
	h := &pendingHandler{uri: uri, done: make(chan struct{})}
	h.show(fmt.Sprintf("Waiting for the owning extension to open\n%s", uri))
	return h
}

// fail shows err as the reason the tab could not be opened.
func (h *pendingHandler) fail(err error) {
	h.show(fmt.Sprintf("Could not open\n%s\n\n%v", h.uri, err))
	h.settle()
}

func (h *pendingHandler) show(message string) {
	h.message = component.NewResponsiveString(message, component.StringResponsiveConfig{
		NoSplitWords: true,
		StringConfig: component.StringConfig{Alignment: component.AlignmentCentered},
	})
	h.message.Resize(h.width, h.height)
}

func (h *pendingHandler) settle() {
	h.doneOnce.Do(func() { close(h.done) })
}

func (h *pendingHandler) URI() workspaceapi.URI { return h.uri }

func (h *pendingHandler) Close() error {
	h.settle()
	return nil
}

func (h *pendingHandler) Resize(width, height int) {
	h.width, h.height = width, height
	h.message.Resize(width, height)
}

func (h *pendingHandler) Dimensions() (width, height int) {
	return h.width, h.height
}

func (h *pendingHandler) Draw(w term.Writer) { h.message.Draw(w) }

func (h *pendingHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}

func (h *pendingHandler) Selection() (string, bool) { return "", false }

func (h *pendingHandler) Handle(term.Event) (exit, handled bool) { return false, false }

// PendingTabs are the tabs a session restored before the extension owning
// their URI's scheme registered a resource opener. Each shows a placeholder
// until the content the extension opens takes its place.
type PendingTabs struct {
	tabs *browser.Component
	// animateLoading shows the tab of uri as loading until done is closed.
	animateLoading func(uri workspaceapi.URI, done <-chan struct{})
}

func newPendingTabs(
	tabs *browser.Component,
	animateLoading func(uri workspaceapi.URI, done <-chan struct{}),
) PendingTabs {
	return PendingTabs{tabs: tabs, animateLoading: animateLoading}
}

// Open creates the tab of uri with a placeholder as its content, which
// shows that the tab waits for the extension owning uri's scheme. The tab
// is returned free, in the tab bar; if uri is already open, its tab is
// returned instead.
func (p *PendingTabs) Open(
	uri workspaceapi.URI, icon rune, name string,
) *browser.Tab {
	if t, ok := p.tabs.Tab(uri); ok {
		return t
	}
	h := newPendingHandler(uri)
	t := p.tabs.NewTab(uri, icon, name, h, nil)
	go debug.CapturePanicReport(func() {
		p.animateLoading(uri, h.done)
	})
	return t
}

// URIs returns the URIs of the tabs of scheme that still show a
// placeholder, in tab order.
func (p *PendingTabs) URIs(scheme string) []workspaceapi.URI {
	var ret []workspaceapi.URI
	for _, t := range p.tabs.Tabs() {
		if !isPendingTab(t) || t.URI().Scheme() != scheme {
			continue
		}
		ret = append(ret, t.URI())
	}
	return ret
}

// Contains reports whether the tab of uri still shows a placeholder.
func (p *PendingTabs) Contains(uri workspaceapi.URI) bool {
	t, ok := p.tabs.Tab(uri)
	return ok && isPendingTab(t)
}

// Fail shows err as the reason the tab of uri could not be opened, and
// reports whether it did so: false means the tab is gone or no longer a
// placeholder. The tab keeps its place, so a later attempt to open it can
// still take it.
func (p *PendingTabs) Fail(uri workspaceapi.URI, err error) bool {
	_, h, ok := p.placeholder(uri)
	if !ok {
		return false
	}
	h.fail(err)
	return true
}

// Replace installs h, the content an extension opened for uri, in the
// place of uri's placeholder, wherever the user keeps the tab by now. It
// reports whether it did so: false means the tab is gone or no longer a
// placeholder, and h was left untouched for the caller to close.
func (p *PendingTabs) Replace(
	uri workspaceapi.URI, h browserapi.Handler,
) bool {
	t, placeholder, ok := p.placeholder(uri)
	if !ok {
		return false
	}
	width, height := placeholder.Dimensions()
	// the only thing SetHandler can fail on is closing the placeholder,
	// which never fails
	_ = t.SetHandler(h, nil)
	if width > 0 && height > 0 {
		h.Resize(width, height)
	}
	return true
}

func (p *PendingTabs) placeholder(
	uri workspaceapi.URI,
) (*browser.Tab, *pendingHandler, bool) {
	t, ok := p.tabs.Tab(uri)
	if !ok {
		return nil, nil, false
	}
	h, ok := t.Handler().(*pendingHandler)
	if !ok {
		return nil, nil, false
	}
	return t, h, true
}

func isPendingTab(t *browser.Tab) bool {
	_, ok := t.Handler().(*pendingHandler)
	return ok
}
