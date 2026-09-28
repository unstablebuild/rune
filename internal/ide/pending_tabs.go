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

package ide

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/text"
)

// pendingTabOpener opens the tabs a workspace restored as placeholders
// (see text.PendingTabs) through the resource openers its extensions
// register.
type pendingTabOpener struct {
	comp          *text.Component
	notifications browserapi.Notifications
	sched         func(func()) bool
	// ctx ends with the workspace: an open still in flight then is not
	// shown, and what it returns is closed.
	ctx context.Context
	// timeout bounds how long a placeholder waits for its opener, once
	// asked.
	timeout time.Duration
	// opening holds the URIs of the placeholders whose opener has been
	// asked for them and has not answered yet. Event loop only.
	opening map[string]struct{}
}

func newPendingTabOpener(
	ctx context.Context,
	comp *text.Component,
	notifications browserapi.Notifications,
	sched func(func()) bool,
	timeout time.Duration,
) *pendingTabOpener {
	if sched == nil {
		panic("ide.pendingTabOpener: sched must not be nil")
	}
	return &pendingTabOpener{
		comp:          comp,
		notifications: notifications,
		sched:         sched,
		ctx:           ctx,
		timeout:       timeout,
		opening:       make(map[string]struct{}),
	}
}

func (p *pendingTabOpener) reopenAsync(scheme string) {
	go debug.CapturePanicReport(func() {
		p.reopen(scheme)
	})
}

func (p *pendingTabOpener) reopen(scheme string) {
	ctx := p.ctx
	type target struct {
		opener textapi.ResourceOpenHandler
		uris   []workspaceapi.URI
	}
	targetCh := make(chan target, 1)
	scheduled := p.sched(func() {
		opener, _ := p.comp.ResourceOpener(scheme)
		var uris []workspaceapi.URI
		for _, uri := range p.comp.PendingTabs().URIs(scheme) {
			// A registration that lands while an open is in flight, e.g.
			// from a restarted extension, must not ask for the tab twice.
			if _, opening := p.opening[uri.String()]; opening {
				continue
			}
			p.opening[uri.String()] = struct{}{}
			uris = append(uris, uri)
		}
		targetCh <- target{opener: opener, uris: uris}
	})
	if !scheduled {
		return
	}
	var t target
	select {
	case t = <-targetCh:
	case <-ctx.Done():
		return
	}
	if t.opener == nil {
		// the extension disconnected in the meantime
		p.sched(func() {
			for _, uri := range t.uris {
				delete(p.opening, uri.String())
			}
		})
		return
	}
	for _, uri := range t.uris {
		p.openTab(ctx, t.opener, uri)
		if ctx.Err() != nil {
			return
		}
	}
}

func (p *pendingTabOpener) openTab(
	ctx context.Context, opener textapi.ResourceOpenHandler,
	uri workspaceapi.URI,
) {
	openCtx, cancel := context.WithTimeout(ctx, p.timeout)
	h, err := opener.OpenResource(openCtx, uri)
	cancel()
	if ctx.Err() != nil {
		if h != nil {
			_ = h.Close()
		}
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("the extension did not open it within %s", p.timeout)
	}
	if err == nil && h == nil {
		err = errors.New("the extension returned no content")
	}
	scheduled := p.sched(func() {
		delete(p.opening, uri.String())
		if err == nil {
			if !p.comp.PendingTabs().Replace(uri, h) {
				// closed by the user
				_ = h.Close()
			}
			return
		}
		if !p.comp.PendingTabs().Fail(uri, err) {
			// closed by the user
			return
		}
		_, _ = p.notifications.Notify(browserapi.LevelError,
			"open %s: %v", uri, err)
	})
	if !scheduled && h != nil {
		_ = h.Close()
	}
}
