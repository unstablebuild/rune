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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	blueauth "github.com/unstablebuild/blue/auth"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi/browserrpc"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/oauth"

	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/workspace"
)

func TestExtensionInterruptPermissionE2E(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	dataDir := t.TempDir()
	workspaceDir := filepath.Join(os.TempDir(), fmt.Sprintf("rune-interrupt-e2e-%d", os.Getpid()))
	_ = os.RemoveAll(workspaceDir)
	require.NoError(t, os.MkdirAll(workspaceDir, 0o755))
	t.Cleanup(func() { _ = os.RemoveAll(workspaceDir) })

	uri, err := workspaceapi.ParseURI("file://" + workspaceDir)
	require.NoError(t, err)
	execScheme, err := workspace.NewFileScheme(ctx, nil, uri)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, execScheme.Close()) })

	// Capture all events that arrive at the host-side publishEvent. In
	// production this closure delivers events to the GUI/TUI update
	// channel; here we just count them.
	var publishedCount atomic.Int64
	publishedCh := make(chan term.Event, 64)
	publishEvent := func(ev term.Event) bool {
		publishedCount.Add(1)
		select {
		case publishedCh <- ev:
		default:
		}
		return true
	}

	// Use the default (secure transport + oauth) setup so the full
	// permission flow, including the bearer token auth middleware, is
	// exercised. Insecure transport would skip the authorizer, which is
	// what we want to test.
	baseRunner, err := NewRunner(ctx, new(sync.Mutex), dataDir)
	require.NoError(t, err)

	prompt := newE2EPromptOpener(ideauthorizer.PromptOptionYes)
	storage := storagestub.NewInMemoryService()
	authorizer, err := ideauthorizer.NewAuthorizer(
		texttest.NopEditor(), prompt, storage,
		func(fn func()) bool { fn(); return true },
		nil, pkgtrust.NewStore(t.TempDir(), nil), ideauthorizer.Config{},
	)
	require.NoError(t, err)

	// Wrap e2eBrowser so PublishEvent forwards to publishEvent. This
	// mirrors production, where ex.Browser() (the text.Component) calls
	// the configured WithEventPublisher closure when PublishEvent is
	// invoked.
	hostBrowser := publishingE2EBrowser{publishEvent: publishEvent}

	runner, err := baseRunner.WorkspaceExtensionsRunner(
		uri,
		extension.BrowserResources(hostBrowser, publishEvent),
		authorizer,
		nopTrustVerifier{},
		dataDir,
		dataDir,
		hostBrowser,
		execScheme,
		execScheme, // extExecutor: e2e doesn't split the two
		extension.GrantAll(),
		texttest.NopEditor(),
		prompt,
		storage,
		func(fn func()) bool { fn(); return true },
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runner.Close()) })

	// Construct an extension-side Workspace that dials the runner's unix
	// socket. We sign a bearer token with the runner's keys so the auth
	// middleware identifies this client as a plugin extension with all
	// permissions declared.
	socket := baseRunner.socketPath(uri)
	require.NoError(t, waitForSocket(socket, 5*time.Second))

	signKey, err := baseRunner.keys.Sign(ctx)
	require.NoError(t, err)
	extID := "interrupt-e2e-extension"
	claims := ideauthorizer.Extension{
		Metadata: extensionapi.Metadata{
			DeveloperID:   "you",
			DeveloperKey:  "",
			ExtensionID:   extID,
			ExtensionName: extID,
			Permissions:   extensionapi.AllPermissions(),
		},
		Plugin: true,
		Path:   "/test/extension",
		Args:   nil,
	}
	accessToken, err := blueauth.SignToken(signKey,
		claims.DeveloperID, claims.DeveloperEmail, claims, time.Hour)
	require.NoError(t, err)

	// The runner uses a self-signed TLS cert with CommonName "ox" bound
	// to the unix socket path; extract the server cert from the running
	// runner so the client can present the corresponding CA pool.
	wc, ok := runner.(wrapCloser)
	require.True(t, ok, "runner should be wrapCloser")
	certPool := x509.NewCertPool()
	require.True(t, certPool.AppendCertsFromPEM(wc.workspaceRunner.tlsCert),
		"append runner cert to pool")
	clientTLS := credentials.NewTLS(&tls.Config{
		InsecureSkipVerify: true,
		RootCAs:            certPool,
		ServerName:         "ox",
	})

	conn, err := grpc.NewClient("passthrough:",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return net.Dial("unix", socket)
		}),
		grpc.WithTransportCredentials(clientTLS),
		grpc.WithPerRPCCredentials(oauth.TokenSource{
			TokenSource: oauth2.StaticTokenSource(&oauth2.Token{
				AccessToken: accessToken,
			}),
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	interrupter := browserrpc.NewClient(ctx, conn)

	// First call prompts; the stub auto-approves with "Yes".
	require.NoError(t, interrupter.Interrupt(ctx),
		"first interrupt must succeed after user approves the prompt")
	// Subsequent calls must bypass the prompt but still propagate the
	// EventInterrupt to publishEvent.
	require.NoError(t, interrupter.Interrupt(ctx),
		"second interrupt must succeed without prompt")
	require.NoError(t, interrupter.Interrupt(ctx),
		"third interrupt must succeed without prompt")

	// Exactly one prompt was shown.
	assert.Equal(t, 1, prompt.calls(),
		"exactly one prompt should be shown; subsequent calls use the cached once-decision")

	// All three interrupts must have reached the host's publishEvent
	// (= the GUI/TUI update channel in production). This is the
	// assertion that currently fails on main: only the first event is
	// delivered; the second and third never reach publishEvent even
	// though the RPCs succeed.
	assert.Equal(t, int64(3), publishedCount.Load(),
		"every Interrupt RPC must deliver an EventInterrupt to publishEvent")

	// Drain and verify every delivered event is an EventInterrupt.
	close(publishedCh)
	for ev := range publishedCh {
		assert.Equal(t, term.EventInterrupt, ev.Type)
		// Plain interrupts: no payload, no user func. These are
		// the "clientInterrupt" variant subject to conflation in
		// both gui.Update and tui.PublishEvent.
		assert.Nil(t, ev.Raw, "interrupt event Raw should be nil")
		assert.Nil(t, ev.UserFunc, "interrupt event UserFunc should be nil")
	}
}

// waitForSocket blocks until path exists as a unix domain socket or the
// timeout expires.
func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil {
			if info.Mode()&os.ModeSocket != 0 {
				return nil
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("socket %s did not appear within %s", path, timeout)
}

// publishingE2EBrowser is an e2eBrowser whose PublishEvent forwards to
// the given publishEvent closure. Production's ex.Browser() (a
// *text.Component) wires its PublishEvent method to the configured
// EventPublisher option the same way.
type publishingE2EBrowser struct {
	e2eBrowser
	publishEvent func(term.Event) bool
}

func (b publishingE2EBrowser) PublishEvent(ev term.Event) error {
	if !b.publishEvent(ev) {
		return fmt.Errorf("event stream not ready")
	}
	return nil
}

func TestExtensionInterruptPermissionConcurrentE2E(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	dataDir := t.TempDir()
	workspaceDir := filepath.Join(os.TempDir(),
		fmt.Sprintf("rune-interrupt-concurrent-e2e-%d", os.Getpid()))
	_ = os.RemoveAll(workspaceDir)
	require.NoError(t, os.MkdirAll(workspaceDir, 0o755))
	t.Cleanup(func() { _ = os.RemoveAll(workspaceDir) })

	uri, err := workspaceapi.ParseURI("file://" + workspaceDir)
	require.NoError(t, err)
	execScheme, err := workspace.NewFileScheme(ctx, nil, uri)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, execScheme.Close()) })

	var publishedCount atomic.Int64
	publishedCh := make(chan term.Event, 64)
	publishEvent := func(ev term.Event) bool {
		publishedCount.Add(1)
		select {
		case publishedCh <- ev:
		default:
		}
		return true
	}

	baseRunner, err := NewRunner(ctx, new(sync.Mutex), dataDir)
	require.NoError(t, err)

	// Single-consumer event loop for scheduleNextTick. This simulates
	// the GUI Update() loop that dequeues EventInterrupt{UserFunc: fn}
	// events and runs fn.
	tickCh := make(chan func(), 64)
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		for {
			select {
			case fn, ok := <-tickCh:
				if !ok {
					return
				}
				fn()
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-loopDone
	})

	scheduleNextTick := func(fn func()) bool {
		select {
		case tickCh <- fn:
			return true
		default:
			return false
		}
	}

	// dedupingPromptOpener mimics browser.Component.Prompt's
	// deduplicate-by-message behavior: only the first caller's
	// PromptHandler is stored and later invoked; later callers with the
	// same message are ignored (their handler is dropped). This is how
	// production drops concurrent callers' result channels.
	prompt := newDedupingPromptOpener(ideauthorizer.PromptOptionYes)
	storage := storagestub.NewInMemoryService()
	authorizer, err := ideauthorizer.NewAuthorizer(
		texttest.NopEditor(), prompt, storage, scheduleNextTick, nil,
		pkgtrust.NewStore(t.TempDir(), nil), ideauthorizer.Config{},
	)
	require.NoError(t, err)

	hostBrowser := publishingE2EBrowser{publishEvent: publishEvent}

	runner, err := baseRunner.WorkspaceExtensionsRunner(
		uri,
		extension.BrowserResources(hostBrowser, publishEvent),
		authorizer,
		nopTrustVerifier{},
		dataDir,
		dataDir,
		hostBrowser,
		execScheme,
		execScheme, // extExecutor: e2e doesn't split the two
		extension.GrantAll(),
		texttest.NopEditor(),
		prompt,
		storage,
		scheduleNextTick,
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runner.Close()) })

	socket := baseRunner.socketPath(uri)
	require.NoError(t, waitForSocket(socket, 5*time.Second))

	signKey, err := baseRunner.keys.Sign(ctx)
	require.NoError(t, err)
	extID := "interrupt-concurrent-e2e-extension"
	claims := ideauthorizer.Extension{
		Metadata: extensionapi.Metadata{
			DeveloperID:   "you",
			DeveloperKey:  "",
			ExtensionID:   extID,
			ExtensionName: extID,
			Permissions:   extensionapi.AllPermissions(),
		},
		Plugin: true,
		Path:   "/test/extension",
		Args:   nil,
	}
	accessToken, err := blueauth.SignToken(signKey,
		claims.DeveloperID, claims.DeveloperEmail, claims, time.Hour)
	require.NoError(t, err)

	wc, ok := runner.(wrapCloser)
	require.True(t, ok, "runner should be wrapCloser")
	certPool := x509.NewCertPool()
	require.True(t, certPool.AppendCertsFromPEM(wc.workspaceRunner.tlsCert),
		"append runner cert to pool")
	clientTLS := credentials.NewTLS(&tls.Config{
		InsecureSkipVerify: true,
		RootCAs:            certPool,
		ServerName:         "ox",
	})

	conn, err := grpc.NewClient("passthrough:",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return net.Dial("unix", socket)
		}),
		grpc.WithTransportCredentials(clientTLS),
		grpc.WithPerRPCCredentials(oauth.TokenSource{
			TokenSource: oauth2.StaticTokenSource(&oauth2.Token{
				AccessToken: accessToken,
			}),
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	interrupter := browserrpc.NewClient(ctx, conn)

	// Fire three concurrent Interrupt RPCs. Each will enter the
	// authorizer and call PromptPermission, so each will enqueue a
	// UserFunc on the event loop; each UserFunc will try to open the
	// (same-message) prompt via promptOpener.Prompt.
	const concurrency = 3
	var wg sync.WaitGroup
	errs := make([]error, concurrency)
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(i int) {
			defer wg.Done()
			cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
			defer ccancel()
			errs[i] = interrupter.Interrupt(cctx)
		}(i)
	}

	// After the fix, the authorizer coalesces concurrent prompts with
	// the same onceKey. Only the first caller opens a prompt; the rest
	// wait on the same in-flight decision. Wait for exactly one prompt
	// to reach the opener.
	require.Eventually(t, func() bool {
		return prompt.calls() == 1
	}, 15*time.Second, 10*time.Millisecond,
		"exactly one prompt should reach the opener for N concurrent Interrupts")

	// Simulate the user approving the single (deduplicated) prompt.
	// Firing the stored PromptHandler once unblocks every concurrent
	// Interrupt RPC because they all share the same pendingPrompt.
	prompt.selectStored()

	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "concurrent Interrupt #%d must not fail", i)
	}

	// Every concurrent Interrupt must deliver an EventInterrupt to the
	// host's publishEvent. This is the core assertion that fails on main:
	// after the prompt is approved, only the first of N concurrent
	// Interrupts unblocks; the rest stay stuck on their result channel
	// and never reach Publish.
	assert.Equal(t, int64(concurrency), publishedCount.Load(),
		"every concurrent Interrupt RPC must deliver an EventInterrupt to publishEvent")

	// Follow-up Interrupts must also succeed and deliver an event. This
	// protects against a regression where an approval leaks (e.g. the
	// once-decision never gets cached because the owning goroutine's
	// ctx expired first, so later animation frames would still
	// re-prompt and hang).
	for i := 0; i < concurrency; i++ {
		require.NoError(t, interrupter.Interrupt(ctx),
			"follow-up Interrupt #%d must succeed using the cached decision", i)
	}
	assert.Equal(t, 1, prompt.calls(),
		"follow-up Interrupts must use the cached once-decision, not re-prompt")
	assert.Equal(t, int64(2*concurrency), publishedCount.Load(),
		"every follow-up Interrupt must also deliver an EventInterrupt")
}

// dedupingPromptOpener simulates browser.Component.Prompt's
// deduplicate-by-message behaviour. Only the first caller with a given
// message has its PromptHandler stored; later callers' handlers are
// dropped. selectStored simulates the user clicking the approval option,
// firing the stored handler's OnSelect exactly once.
type dedupingPromptOpener struct {
	option string

	mu     sync.Mutex
	count  int
	stored map[string]handler.PromptHandler
	optIdx map[string]int
}

func newDedupingPromptOpener(option string) *dedupingPromptOpener {
	return &dedupingPromptOpener{
		option: option,
		stored: make(map[string]handler.PromptHandler),
		optIdx: make(map[string]int),
	}
}

func (p *dedupingPromptOpener) Prompt(
	message string, options []string,
	bindings []term.KeyComb,
	promptHandler handler.PromptHandler,
) browser.Window {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.count++
	if _, exists := p.stored[message]; exists {
		// Dedup: existing prompt window is reused; the new caller's
		// PromptHandler is silently dropped. This matches
		// browser.Component.Prompt when the same message is passed
		// multiple times.
		return browsertest.NopWindow()
	}
	p.stored[message] = promptHandler
	for i, opt := range options {
		if opt == p.option {
			p.optIdx[message] = i
			break
		}
	}
	return browsertest.NopWindow()
}

func (p *dedupingPromptOpener) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count
}

// selectStored fires OnSelect on every stored prompt handler exactly
// once, simulating the user approving each displayed prompt. In
// production only the first handler is stored per message (dedup), so
// selectStored fires exactly once per unique message.
func (p *dedupingPromptOpener) selectStored() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for message, h := range p.stored {
		h.OnSelect(p.optIdx[message], p.option)
	}
}
