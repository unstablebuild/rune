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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	blueauth "github.com/unstablebuild/blue/auth"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"

	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/workspace"
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

// calls returns how many times Prompt was invoked on this opener.
func (p *e2ePromptOpener) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.messages)
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

func (e2eBrowser) SetTabActivity(workspaceapi.URI, bool) error { return nil }

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

// stubPackages is the host's package manager as seen by the packages
// service: it lists paths, or fails with err.
type stubPackages struct {
	paths []string
	err   error
}

func (s stubPackages) LibDir(context.Context, string) (iterator.Iterator[string], error) {
	if s.err != nil {
		return nil, s.err
	}
	return iterator.FromSlice(s.paths), nil
}

// dialExtensionWorkspace serves res through the runner, authorizer and
// transport an extension process talks to, and returns the SDK
// workspace such a process would get, so a test crosses every layer a
// language extension's package lookup does.
func dialExtensionWorkspace(
	t *testing.T,
	res map[extensionapi.Permission]extension.ResourceRegistrar,
	prompt *e2ePromptOpener,
) *extensionapi.Workspace {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	dataDir := t.TempDir()
	workspaceDir := filepath.Join(os.TempDir(), fmt.Sprintf("rune-pkg-e2e-%d", os.Getpid()))
	_ = os.RemoveAll(workspaceDir)
	require.NoError(t, os.MkdirAll(workspaceDir, 0o755))
	t.Cleanup(func() { _ = os.RemoveAll(workspaceDir) })

	uri, err := workspaceapi.ParseURI("file://" + workspaceDir)
	require.NoError(t, err)
	execScheme, err := workspace.NewFileScheme(ctx, nil, uri)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, execScheme.Close()) })

	baseRunner, err := NewRunner(ctx, new(sync.Mutex), dataDir)
	require.NoError(t, err)

	storage := storagestub.NewInMemoryService()
	authorizer, err := ideauthorizer.NewAuthorizer(
		texttest.NopEditor(), prompt, storage,
		func(fn func()) bool { fn(); return true },
		nil, pkgtrust.NewStore(t.TempDir(), nil), ideauthorizer.Config{},
	)
	require.NoError(t, err)

	runner, err := baseRunner.WorkspaceExtensionsRunner(
		uri, res, authorizer, nopTrustVerifier{}, dataDir, dataDir, e2eBrowser{},
		execScheme, execScheme, extension.GrantAll(), texttest.NopEditor(),
		prompt, storage, func(fn func()) bool { fn(); return true },
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runner.Close()) })

	socket := baseRunner.socketPath(uri)
	require.NoError(t, waitForSocket(socket, 5*time.Second))

	signKey, err := baseRunner.keys.Sign(ctx)
	require.NoError(t, err)
	const extID = "packages-e2e-extension"
	claims := ideauthorizer.Extension{
		Metadata: extensionapi.Metadata{
			DeveloperID:   "you",
			ExtensionID:   extID,
			ExtensionName: extID,
			Permissions:   extensionapi.Permissions{extensionapi.PermissionPackages: nil},
		},
		Path: "/test/extension",
	}
	accessToken, err := blueauth.SignToken(signKey,
		claims.DeveloperID, claims.DeveloperEmail, claims, time.Hour)
	require.NoError(t, err)

	wc, ok := runner.(wrapCloser)
	require.True(t, ok, "runner should be wrapCloser")
	ws, err := extensionapi.NewWorkspace(extensionapi.Config{
		Socket:      socket,
		Certificate: wc.workspaceRunner.tlsCert,
		Token:       &oauth2.Token{AccessToken: accessToken},
		DataDir:     dataDir,
		InstallDir:  dataDir,
	}, claims.Metadata)
	require.NoError(t, err)
	t.Cleanup(func() {
		conn, ok := ws.RawConn().(*grpc.ClientConn)
		require.True(t, ok)
		_ = conn.Close()
	})
	return ws
}
