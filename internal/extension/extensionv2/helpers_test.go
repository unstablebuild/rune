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

package extensionv2

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
)

// nopTrustVerifier is a TrustVerifier that trusts nothing, so tests exercise the
// prompt flows for unverified extensions.
type nopTrustVerifier struct{}

func (nopTrustVerifier) VerifyExtensionEntrypoint(string) (string, bool) { return "", false }

type e2ePromptOpener struct {
	option string

	mu       sync.Mutex
	messages []string
}

func newE2EPromptOpener(option string) *e2ePromptOpener {
	return &e2ePromptOpener{option: option}
}

func (p *e2ePromptOpener) Prompt(
	message string, options []string,
	bindings []term.KeyComb,
	promptHandler handler.PromptHandler,
) browser.Window {
	p.mu.Lock()
	p.messages = append(p.messages, message)
	p.mu.Unlock()
	for i, opt := range options {
		if opt == p.option {
			promptHandler.OnSelect(i, opt)
			return browsertest.NopWindow()
		}
	}
	panic("test prompt option was not provided")
}

func (p *e2ePromptOpener) message(t *testing.T, wantPrompts int) string {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	require.Len(t, p.messages, wantPrompts)
	return p.messages[0]
}

type e2eBrowser struct{}

func (e2eBrowser) Focus() (browser.Window, error) { return browsertest.NopWindow(), nil }

func (e2eBrowser) SetFocus(browser.Window) (browser.Window, error) {
	return browsertest.NopWindow(), nil
}

func (e2eBrowser) Window(uint64) (browser.Window, bool) { return browsertest.NopWindow(), true }

func (e2eBrowser) IterateWindows(func(browser.Window)) {}

func (e2eBrowser) Split(
	browserapi.Orientation, browser.Window, browserapi.Handler,
) (browser.Window, error) {
	return browsertest.NopWindow(), nil
}

func (e2eBrowser) Floating(
	browser.Floating, browserapi.FloatingConfig,
) (browser.Window, error) {
	return browsertest.NopWindow(), nil
}

func (e2eBrowser) Bar(browserapi.BarConfig, tui.Handler) error { return nil }

func (e2eBrowser) SetTabName(workspaceapi.URI, string, term.Attributes) error { return nil }

func (e2eBrowser) OnTabExit(workspaceapi.URI) bool { return false }

func (e2eBrowser) Tab(
	workspaceapi.URI, rune, string, browserapi.Handler,
) (browserapi.Handler, error) {
	return browsertest.NewTestHandler(), nil
}

func (e2eBrowser) SetWindowContent(browser.Window, browserapi.Handler) error { return nil }

func (e2eBrowser) CloseWindow(browser.Window) error { return nil }

func (e2eBrowser) Open(workspaceapi.URI) (browserapi.Handler, error) {
	return browsertest.NewTestHandler(), nil
}

func (e2eBrowser) Resource(workspaceapi.URI) (browserapi.Handler, bool) {
	return browsertest.NewTestHandler(), true
}

func (e2eBrowser) PublishEvent(term.Event) error { return nil }

func (e2eBrowser) DragHover(term.Coordinates) bool { return false }

func (e2eBrowser) DragCancel() {}

func (e2eBrowser) DragDrop(term.Coordinates, []string) bool { return false }

func (e2eBrowser) Close() error { return nil }

func (e2eBrowser) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (e2eBrowser) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (e2eBrowser) UpdateNotificationProgress(string, string, int64, int64) error {
	return nil
}
