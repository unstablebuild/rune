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

package ideauthorizer

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	blueauth "github.com/unstablebuild/blue/auth"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi/workspacerpc"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"google.golang.org/grpc/peer"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension/extensionv2/peerprocess"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/standard"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/workspace"
)

const testWindowManagerResource = "/browser.WindowManager/NewWindow"

// stubPromptOpener is a test PromptOpener that always selects the option
// whose label maps to decision. It records each prompt message so tests
// can assert what the user would see. Its scope pickers pick the scopes
// labeled picks that they offer, or confirm the preselected ones when
// picks is nil, or are cancelled.
type stubPromptOpener struct {
	decision PermissionDecision
	// renewal is the option picked in renewal prompts.
	renewal  string
	calls    int
	messages []string
	// options are the options of each prompt.
	options [][]string
	picks   []string
	cancel  bool
	// floatingErr fails the scope pickers opened.
	floatingErr error
	// offered are the rows of each picker opened, as the branch of the
	// tree followed by "[x] label" when preselected or "[ ] label".
	offered [][]string
}

func (s *stubPromptOpener) Prompt(
	message string, options []string,
	_ []term.KeyComb, promptHandler handler.PromptHandler,
) browser.Window {
	s.calls++
	s.messages = append(s.messages, message)
	s.options = append(s.options, options)
	want := optionForDecision(s.decision)
	if slices.Contains(options, PromptOptionRenew) {
		want = cmp.Or(s.renewal, PromptOptionNo)
	}
	for i, opt := range options {
		if opt == want {
			promptHandler.OnSelect(i, opt)
			return browsertest.NopWindow()
		}
	}
	panic("test prompt option was not provided: " + want)
}

func (s *stubPromptOpener) Floating(
	h browser.Floating, _ browserapi.FloatingConfig,
) (browser.Window, error) {
	if s.floatingErr != nil {
		return nil, s.floatingErr
	}
	p := h.(*scopePicker)
	var rows []string
	p.list.Iterate(func(c tui.Component) {
		r := c.(*scopeRow)
		box := "[ ] "
		if p.selected[r.scope] {
			box = "[x] "
		}
		rows = append(rows, r.tree+box+p.text(r.scope))
	})
	s.offered = append(s.offered, rows)
	if s.cancel {
		_ = p.Close()
		return browsertest.NopWindow(), nil
	}
	if s.picks != nil {
		for i, sc := range p.req.Scopes {
			p.selected[i] = slices.Contains(s.picks, sc.Label)
		}
	}
	p.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	return browsertest.NopWindow(), nil
}

// fakeResolver is a host where every file exists unless it is missing,
// and holds its own path unless its contents were set. Programs are
// looked up in hostPATH.
type fakeResolver struct {
	mu       sync.Mutex
	host     string
	hostPATH []string
	missing  map[string]bool
	contents map[string]string
	modified map[string]time.Time
}

func newFakeResolver(missing ...string) *fakeResolver {
	r := &fakeResolver{
		hostPATH: []string{"/usr/bin", "/bin"},
		missing:  map[string]bool{},
		contents: map[string]string{},
		modified: map[string]time.Time{},
	}
	for _, m := range missing {
		r.missing[m] = true
	}
	return r
}

func (r *fakeResolver) setContent(p, content string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.contents[p] = content
	r.modified[p] = time.Date(2026, 10, 25, 14, 2, 0, 0, time.Local)
}

func (r *fakeResolver) HostID() string { return r.host }

func (r *fakeResolver) setMissing(p string, missing bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.missing[p] = missing
}

func (r *fakeResolver) HostPATH(context.Context) ([]string, error) {
	return r.hostPATH, nil
}

func (r *fakeResolver) Resolve(
	_ context.Context, name, dir string, pathList []string,
) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if dir == "" {
		dir = "/ws"
	}
	var candidates []string
	switch {
	case path.IsAbs(name):
		candidates = []string{path.Clean(name)}
	case strings.Contains(name, "/"):
		candidates = []string{path.Join(dir, name)}
	default:
		if pathList == nil {
			pathList = r.hostPATH
		}
		for _, d := range pathList {
			if !path.IsAbs(d) {
				d = path.Join(dir, d)
			}
			candidates = append(candidates, path.Join(d, name))
		}
	}
	for _, c := range candidates {
		if !r.missing[c] {
			return c, nil
		}
	}
	return "", ErrExecutableNotFound
}

func (r *fakeResolver) Fingerprint(_ context.Context, p string) (ExecutableFingerprint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.missing[p] {
		return ExecutableFingerprint{}, ErrExecutableNotFound
	}
	content, ok := r.contents[p]
	if !ok {
		content = p
	}
	return ExecutableFingerprint{
		SHA256:  sha256Hex(content),
		Size:    int64(len(content)),
		ModTime: r.modified[p],
	}, nil
}

// storedCommandDecisions returns the command decisions in storage.
func storedCommandDecisions(t *testing.T, storage storageapi.Service) []storedPermissionDecision {
	t.Helper()
	it, err := storage.List(context.Background(), nil)
	require.NoError(t, err)
	defer it.Close()
	var ret []storedPermissionDecision
	for it.HasNext() {
		var stored storedPermissionDecision
		require.NoError(t, it.NextTo(&stored))
		if isPermissionStorageKey(stored.Key) && stored.Command.Path != "" {
			ret = append(ret, stored)
		}
	}
	return ret
}

// storedScopes maps the scopes of the command decisions in storage to
// their decisions.
func storedScopes(t *testing.T, storage storageapi.Service) map[string]string {
	t.Helper()
	ret := map[string]string{}
	for _, d := range storedCommandDecisions(t, storage) {
		ret[d.Scope] = d.Decision
	}
	return ret
}

// optionForDecision returns the prompt option label that maps to decision.
func optionForDecision(decision PermissionDecision) string {
	switch decision {
	case PermissionAllowOnce:
		return PromptOptionYes
	case PermissionAllowAlways:
		return PromptOptionYesAlways
	case PermissionDenyOnce:
		return PromptOptionNo
	case PermissionDenyAlways:
		return PromptOptionNoNever
	}
	return ""
}

// syncScheduleNextTick runs fn on the calling goroutine so tests don't need
// a separate event loop.
func syncScheduleNextTick(fn func()) bool {
	fn()
	return true
}

type errorStorage struct {
	storageapi.Service
	getErr error
	setErr error
}

func (s errorStorage) Get(ctx context.Context, ID string, doc any) error {
	if s.getErr != nil {
		return s.getErr
	}
	return s.Service.Get(ctx, ID, doc)
}

func (s errorStorage) Set(ctx context.Context, ID string, doc any) error {
	if s.setErr != nil {
		return s.setErr
	}
	return s.Service.Set(ctx, ID, doc)
}

func TestAuthorizerRegularExtensionPromptsForClaimedPermission(t *testing.T) {
	t.Parallel()

	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	status := &capturingStatus{}
	a := mustNewAuthorizerWithStatus(t, opener,
		storagestub.NewInMemoryService(), texttest.NopEditor(), status)
	ext := testRegularExtension(extensionapi.NewPermissions(
		extensionapi.PermissionBrowserWindowManager,
	))

	err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{Extra: ext},
		testWindowManagerResource)
	require.NoError(t, err)
	require.Equal(t, 1, opener.calls)
	// Extension prompts include the extension name, developer id, and the
	// rendered permission action text.
	assert.Contains(t, opener.messages[0], "Test Extension")
	assert.Contains(t, opener.messages[0], "dev-id")
	assert.Contains(t, opener.messages[0],
		PermissionActionText(extensionapi.PermissionBrowserWindowManager))

	// The prompt is reported as a blocked status with the relevant scope
	// so the workspace shows it needs the user.
	blocked := status.blocked()
	require.Len(t, blocked, 1)
	assert.Contains(t, blocked[0].Msg, "authorization")
	assert.Contains(t, blocked[0].Msg, "Test Extension")

	err = a.Authorize(context.Background(), blueauth.UserClaims[Extension]{Extra: ext},
		testWindowManagerResource)
	require.NoError(t, err)
	assert.Equal(t, 1, opener.calls)
	// The cached once-decision prevents a second prompt and thus a
	// second blocked status.
	assert.Len(t, status.blocked(), 1)
}

func TestAuthorizerRegularExtensionMissingClaimForbiddenWithoutPrompt(t *testing.T) {
	t.Parallel()

	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a := mustNewAuthorizer(t, opener, storagestub.NewInMemoryService(), texttest.NopEditor())
	ext := testRegularExtension(extensionapi.NewPermissions(extensionapi.PermissionStorage))

	err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{Extra: ext},
		testWindowManagerResource)
	assert.ErrorIs(t, err, blueauth.ErrForbidden)
	assert.Zero(t, opener.calls)
}

func TestAuthorizerUnknownResourceForbidden(t *testing.T) {
	t.Parallel()

	a := mustNewAuthorizer(t, nil, nil, texttest.NopEditor())
	err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{},
		"/unknown.Service/Method")
	assert.ErrorIs(t, err, blueauth.ErrForbidden)
}

// mustNewAutoAuthorizer is like mustNewAuthorizer but enables
// auto-authorization, so permission requests are granted without
// prompting.
func mustNewAutoAuthorizer(
	t *testing.T, opener PromptOpener,
	storage storageapi.Service, editor text.Editor,
) *Authorizer {
	t.Helper()
	a, err := NewAuthorizer(editor, opener, storage, syncScheduleNextTick, &capturingStatus{},
		testTrustStore(t), newFakeResolver(), Config{
			AutoAuthorizeExtensions: true,
			AutoAuthorizeCommands:   true,
		})
	require.NoError(t, err)
	return a
}

func TestAuthorizerAutoAuthorizeScopes(t *testing.T) {
	t.Parallel()

	// Commands auto-authorize only when AutoAuthorizeCommands AND the
	// extension is from a verified publisher. These cases use an
	// unverified plugin, so the command always prompts regardless of
	// AutoAuthorizeCommands.
	for _, tc := range []struct {
		name                 string
		config               Config
		wantExtensionPrompts int
		wantCommandPrompts   int
	}{
		{"neither", Config{}, 1, 1},
		{"extensions only", Config{AutoAuthorizeExtensions: true}, 0, 1},
		{"commands only", Config{AutoAuthorizeCommands: true}, 1, 1},
		{"both", Config{
			AutoAuthorizeExtensions: true,
			AutoAuthorizeCommands:   true,
		}, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opener := &stubPromptOpener{decision: PermissionAllowOnce}
			a, err := NewAuthorizer(texttest.NopEditor(), opener,
				storagestub.NewInMemoryService(), syncScheduleNextTick, &capturingStatus{},
				testTrustStore(t), newFakeResolver(), tc.config)
			require.NoError(t, err)
			ext := testPluginExtension(nil)

			err = a.Authorize(context.Background(), blueauth.UserClaims[Extension]{Extra: ext},
				testWindowManagerResource)
			require.NoError(t, err)
			assert.Equal(t, tc.wantExtensionPrompts, opener.calls)

			ctx := blueauth.ContextWithClaims(context.Background(),
				blueauth.UserClaims[Extension]{Extra: ext})
			cmd := workspaceapi.Cmd{Path: "/bin/grep", Args: []string{"foo"}, Dir: "/tmp"}
			err = a.AuthorizeCommand(ctx, cmd)
			require.NoError(t, err)
			assert.Equal(t, tc.wantExtensionPrompts+tc.wantCommandPrompts, opener.calls)
		})
	}
}

func TestAuthorizerAutoAuthorizeGrantsWithoutPrompt(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		ext  Extension
	}{
		{name: "plugin", ext: testPluginExtension(nil)},
		{name: "regular extension with claim",
			ext: testRegularExtension(extensionapi.NewPermissions(
				extensionapi.PermissionBrowserWindowManager))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opener := &stubPromptOpener{decision: PermissionDenyOnce}
			a := mustNewAutoAuthorizer(t, opener,
				storagestub.NewInMemoryService(), texttest.NopEditor())

			err := a.Authorize(context.Background(),
				blueauth.UserClaims[Extension]{Extra: tc.ext},
				testWindowManagerResource)
			assert.NoError(t, err)
			assert.Zero(t, opener.calls)
		})
	}
}

func TestAuthorizerAutoAuthorizeMissingClaimStillForbidden(t *testing.T) {
	t.Parallel()

	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a := mustNewAutoAuthorizer(t, opener,
		storagestub.NewInMemoryService(), texttest.NopEditor())
	ext := testRegularExtension(extensionapi.NewPermissions(extensionapi.PermissionStorage))

	err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{Extra: ext},
		testWindowManagerResource)
	assert.ErrorIs(t, err, blueauth.ErrForbidden)
	assert.Zero(t, opener.calls)
}

func TestAuthorizerAutoAuthorizeStartCommandGrantsWithoutPrompt(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{decision: PermissionDenyOnce}
	// Commands auto-authorize only for a verified publisher when command
	// auto-authorization is also enabled.
	trustedFingerprint := "D3F9E65DE72888CC03D45CF5064D4ABCFA6D9338"
	a, err := NewAuthorizer(texttest.NopEditor(), opener, storage,
		syncScheduleNextTick, &capturingStatus{}, testTrustStore(t), newFakeResolver(), Config{
			AutoAuthorizeCommands:          true,
			AutoAuthorizeVerifiedPublisher: true,
		})
	require.NoError(t, err)
	ext := testRegularExtension(extensionapi.NewPermissions(
		extensionapi.PermissionExecute))
	ext.VerifiedPublisher = trustedFingerprint
	ctx := blueauth.ContextWithClaims(context.Background(),
		blueauth.UserClaims[Extension]{Extra: ext})
	cmd := workspaceapi.Cmd{Path: "/bin/grep", Args: []string{"foo"}, Dir: "/tmp"}

	require.NoError(t, a.AuthorizeCommand(ctx, cmd))
	assert.Zero(t, opener.calls)

	// Auto-granted decisions are not persisted, so disabling automatic
	// command authorization later prompts again.
	assert.Empty(t, storedCommandDecisions(t, storage))
}

func TestAuthorizerAutoAuthorizeHonorsPersistedDeny(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	ext := testPluginExtension(nil)

	// Persist a deny-always decision through the prompting path.
	denyOpener := &stubPromptOpener{decision: PermissionDenyAlways}
	denying := mustNewAuthorizer(t, denyOpener, storage, texttest.NopEditor())
	err := denying.Authorize(context.Background(),
		blueauth.UserClaims[Extension]{Extra: ext}, testWindowManagerResource)
	require.ErrorIs(t, err, blueauth.ErrForbidden)
	require.Equal(t, 1, denyOpener.calls)

	// Auto-authorize does not override the persisted deny.
	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a := mustNewAutoAuthorizer(t, opener, storage, texttest.NopEditor())
	err = a.Authorize(context.Background(),
		blueauth.UserClaims[Extension]{Extra: ext}, testWindowManagerResource)
	assert.ErrorIs(t, err, blueauth.ErrForbidden)
	assert.Zero(t, opener.calls)
}

func TestAuthorizerVerifiedPublisherAutoAuthorizes(t *testing.T) {
	t.Parallel()

	trustedFingerprint := "D3F9E65DE72888CC03D45CF5064D4ABCFA6D9338"
	ext := testRegularExtension(extensionapi.NewPermissions(
		extensionapi.PermissionBrowserWindowManager,
		extensionapi.PermissionExecute,
	))
	ext.VerifiedPublisher = trustedFingerprint
	opener := &stubPromptOpener{decision: PermissionDenyOnce}
	a, err := NewAuthorizer(texttest.NopEditor(), opener,
		storagestub.NewInMemoryService(), syncScheduleNextTick, &capturingStatus{},
		testTrustStore(t), newFakeResolver(), Config{
			AutoAuthorizeVerifiedPublisher: true,
			AutoAuthorizeCommands:          true,
		})
	require.NoError(t, err)

	require.NoError(t, a.Authorize(context.Background(), blueauth.UserClaims[Extension]{Extra: ext},
		testWindowManagerResource))
	ctx := blueauth.ContextWithClaims(context.Background(), blueauth.UserClaims[Extension]{Extra: ext})
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{Path: "/bin/true"}))
	assert.Zero(t, opener.calls)
}

func TestAuthorizerVerifiedPublisherAloneStillPromptsCommands(t *testing.T) {
	t.Parallel()

	trustedFingerprint := "D3F9E65DE72888CC03D45CF5064D4ABCFA6D9338"
	ext := testRegularExtension(extensionapi.NewPermissions(
		extensionapi.PermissionExecute,
	))
	ext.VerifiedPublisher = trustedFingerprint
	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a, err := NewAuthorizer(texttest.NopEditor(), opener,
		storagestub.NewInMemoryService(), syncScheduleNextTick, &capturingStatus{},
		testTrustStore(t), newFakeResolver(), Config{
			AutoAuthorizeVerifiedPublisher: true,
		})
	require.NoError(t, err)

	ctx := blueauth.ContextWithClaims(context.Background(), blueauth.UserClaims[Extension]{Extra: ext})
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{Path: "/bin/true"}))
	assert.Equal(t, 1, opener.calls)
}

func TestAuthorizerOnboardingCommandAutoAuthorize(t *testing.T) {
	t.Parallel()

	trustedFingerprint := "D3F9E65DE72888CC03D45CF5064D4ABCFA6D9338"
	verified := func() Extension {
		ext := testRegularExtension(extensionapi.NewPermissions(
			extensionapi.PermissionExecute))
		ext.VerifiedPublisher = trustedFingerprint
		return ext
	}
	cases := []struct {
		name        string
		ext         Extension
		onboarding  bool
		wantPrompts int
	}{
		{"verified during onboarding", verified(), true, 0},
		{"verified after onboarding", verified(), false, 1},
		{"unverified during onboarding",
			testRegularExtension(extensionapi.NewPermissions(
				extensionapi.PermissionExecute)), true, 1},
		{"plugin during onboarding", testPluginExtension(nil), true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			storage := storagestub.NewInMemoryService()
			opener := &stubPromptOpener{decision: PermissionAllowOnce}
			a, err := NewAuthorizer(texttest.NopEditor(), opener, storage,
				syncScheduleNextTick, &capturingStatus{}, testTrustStore(t), newFakeResolver(), Config{
					AutoAuthorizeVerifiedPublisher: true,
					OnboardingActive:               func() bool { return tc.onboarding },
				})
			require.NoError(t, err)

			ctx := blueauth.ContextWithClaims(context.Background(),
				blueauth.UserClaims[Extension]{Extra: tc.ext})
			cmd := workspaceapi.Cmd{Path: "/bin/grep", Args: []string{"foo"}, Dir: "/tmp"}
			require.NoError(t, a.AuthorizeCommand(ctx, cmd))
			assert.Equal(t, tc.wantPrompts, opener.calls)

			// Onboarding grants are never persisted: once onboarding ends
			// the same command must prompt again.
			assert.Empty(t, storedCommandDecisions(t, storage))
		})
	}
}

func TestAuthorizerVerifiedPublisherToggleAndStoredDeny(t *testing.T) {
	t.Parallel()

	trustedFingerprint := "D3F9E65DE72888CC03D45CF5064D4ABCFA6D9338"
	ext := testRegularExtension(extensionapi.NewPermissions(extensionapi.PermissionBrowserWindowManager))
	ext.VerifiedPublisher = trustedFingerprint
	storage := storagestub.NewInMemoryService()
	denyOpener := &stubPromptOpener{decision: PermissionDenyAlways}
	denying := mustNewAuthorizer(t, denyOpener, storage, texttest.NopEditor())
	require.ErrorIs(t, denying.Authorize(context.Background(), blueauth.UserClaims[Extension]{Extra: ext},
		testWindowManagerResource), blueauth.ErrForbidden)

	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a, err := NewAuthorizer(texttest.NopEditor(), opener, storage,
		syncScheduleNextTick, &capturingStatus{}, testTrustStore(t), newFakeResolver(),
		Config{AutoAuthorizeVerifiedPublisher: true})
	require.NoError(t, err)
	assert.ErrorIs(t, a.Authorize(context.Background(), blueauth.UserClaims[Extension]{Extra: ext},
		testWindowManagerResource), blueauth.ErrForbidden)
	assert.Zero(t, opener.calls)

	noToggleOpener := &stubPromptOpener{decision: PermissionAllowOnce}
	withoutToggle, err := NewAuthorizer(texttest.NopEditor(), noToggleOpener,
		storagestub.NewInMemoryService(), syncScheduleNextTick, &capturingStatus{},
		testTrustStore(t), newFakeResolver(), Config{})
	require.NoError(t, err)
	require.NoError(t, withoutToggle.Authorize(context.Background(),
		blueauth.UserClaims[Extension]{Extra: ext}, testWindowManagerResource))
	assert.Equal(t, 1, noToggleOpener.calls)
}

func TestAuthorizerPluginPromptsAndIgnoresClaimsPermissions(t *testing.T) {
	t.Parallel()

	opener := &stubPromptOpener{
		decision: PermissionAllowOnce,
	}
	status := &capturingStatus{}
	a := mustNewAuthorizerWithStatus(t, opener,
		storagestub.NewInMemoryService(), texttest.NopEditor(), status)
	ext := testPluginExtension(extensionapi.NewPermissions(extensionapi.PermissionStorage))

	err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{Extra: ext},
		testWindowManagerResource)
	require.NoError(t, err)
	require.Equal(t, 1, opener.calls)
	require.Len(t, opener.messages, 1)
	// Plugin prompts render the program path, its args, and the permission
	// action text for the claimed resource.
	assert.Contains(t, opener.messages[0], "/bin/test")
	assert.Contains(t, opener.messages[0], "[--flag]")
	assert.Contains(t, opener.messages[0],
		PermissionActionText(extensionapi.PermissionBrowserWindowManager))
	assert.Equal(t, []string{"--flag"}, ext.Args)

	blocked := status.blocked()
	require.Len(t, blocked, 1)
	assert.Contains(t, blocked[0].Msg, "authorization")
	assert.Contains(t, blocked[0].Msg, "/bin/test")
}

func TestAuthorizerPluginUsesPeerProcessForPromptAndStorageKey(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{decision: PermissionAllowAlways}
	a := mustNewAuthorizer(t, opener, storage, texttest.NopEditor())
	ext := Extension{
		Metadata: extensionapi.Metadata{Permissions: extensionapi.AllPermissions()},
		Plugin:   true,
		Path:     "/bin/zsh",
		Args:     []string{"--login"},
	}
	ctx := contextWithPeerProcess(context.Background(), peerprocess.Process{
		PID:  123,
		UID:  501,
		Exe:  "/usr/local/bin/trusted-cli",
		Argv: []string{"trusted-cli", "run", "--verbose"},
	})

	err := a.Authorize(ctx, blueauth.UserClaims[Extension]{Extra: ext},
		testWindowManagerResource)
	require.NoError(t, err)
	require.Equal(t, 1, opener.calls)
	require.Len(t, opener.messages, 1)
	// The peer process identity, not the claim, is what the prompt reports
	// as the requesting program. The claim's Path/Args are shown as the
	// launcher that started the peer process.
	assert.Contains(t, opener.messages[0], "/usr/local/bin/trusted-cli")
	assert.Contains(t, opener.messages[0], "[run --verbose]")
	assert.Contains(t, opener.messages[0], "running inside")
	assert.Contains(t, opener.messages[0], "/bin/zsh")
	assert.Contains(t, opener.messages[0], "[--login]")

	peerKey := pluginPermissionProgramStorageKey("/usr/local/bin/trusted-cli",
		extensionapi.PermissionBrowserWindowManager)
	var stored storedPermissionDecision
	require.NoError(t, storage.Get(context.Background(), peerKey, &stored))
	assert.Equal(t, pluginPermissionDecisionAllow, stored.Decision)

	launcherKey := pluginPermissionProgramStorageKey("/bin/zsh",
		extensionapi.PermissionBrowserWindowManager)
	err = storage.Get(context.Background(), launcherKey, &stored)
	assert.ErrorIs(t, err, storageapi.ErrNotFound)
}

func TestAuthorizerPluginOmitsLauncherWhenPeerMatchesClaim(t *testing.T) {
	t.Parallel()

	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a := mustNewAuthorizer(t, opener, storagestub.NewInMemoryService(), texttest.NopEditor())
	ext := Extension{
		Plugin: true,
		Path:   "/usr/local/bin/runectl",
		Args:   []string{"wm", "focus"},
	}
	ctx := contextWithPeerProcess(context.Background(), peerprocess.Process{
		Exe:  "/usr/local/bin/runectl",
		Argv: []string{"runectl", "wm", "focus"},
	})

	err := a.Authorize(ctx, blueauth.UserClaims[Extension]{Extra: ext},
		testWindowManagerResource)
	require.NoError(t, err)
	require.Equal(t, 1, opener.calls)
	// When the peer process matches the claimed program, no launcher is
	// shown in the prompt.
	assert.Contains(t, opener.messages[0], "/usr/local/bin/runectl")
	assert.Contains(t, opener.messages[0], "[wm focus]")
	assert.NotContains(t, opener.messages[0], "running inside")
}

func TestAuthorizerPluginAlwaysPersistsAcrossDifferentArgs(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{decision: PermissionAllowAlways}
	a := mustNewAuthorizer(t, opener, storage, texttest.NopEditor())
	ext := Extension{
		Metadata: extensionapi.Metadata{Permissions: extensionapi.AllPermissions()},
		Plugin:   true,
		Path:     "/usr/local/bin/runectl",
	}

	firstCtx := contextWithPeerProcess(context.Background(), peerprocess.Process{
		Exe:  "/usr/local/bin/runectl",
		Argv: []string{"runectl", "lsp", "definition", "--file", "a"},
	})
	require.NoError(t, a.Authorize(firstCtx,
		blueauth.UserClaims[Extension]{Extra: ext}, testWindowManagerResource))
	require.Equal(t, 1, opener.calls)

	secondCtx := contextWithPeerProcess(context.Background(), peerprocess.Process{
		Exe:  "/usr/local/bin/runectl",
		Argv: []string{"runectl", "lsp", "hover", "--file", "b"},
	})
	require.NoError(t, a.Authorize(secondCtx,
		blueauth.UserClaims[Extension]{Extra: ext}, testWindowManagerResource))
	assert.Equal(t, 1, opener.calls,
		"an Always grant must persist across invocations with different args")
}

func TestAuthorizerPluginPeerDecisionIsIndependentFromLauncherDecision(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	ext := Extension{
		Plugin: true,
		Path:   "/bin/zsh",
		Args:   []string{"--login"},
	}
	launcherKey := pluginPermissionProgramStorageKey(ext.Path,
		extensionapi.PermissionBrowserWindowManager)
	require.NoError(t, storage.Set(context.Background(), launcherKey,
		storedPermissionDecision{Decision: pluginPermissionDecisionAllow}))
	opener := &stubPromptOpener{decision: PermissionDenyOnce}
	a := mustNewAuthorizer(t, opener, storage, texttest.NopEditor())
	ctx := contextWithPeerProcess(context.Background(), peerprocess.Process{
		Exe:  "/usr/local/bin/untrusted-cli",
		Argv: []string{"untrusted-cli"},
	})

	err := a.Authorize(ctx, blueauth.UserClaims[Extension]{Extra: ext},
		testWindowManagerResource)
	assert.ErrorIs(t, err, blueauth.ErrForbidden)
	assert.Equal(t, 1, opener.calls)
	assert.Contains(t, opener.messages[0], "/usr/local/bin/untrusted-cli")
}

func TestAuthorizerPluginFallsBackWhenPeerProcessUnavailable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		ctx  context.Context
	}{
		{name: "no peer", ctx: context.Background()},
		{name: "peer error", ctx: peer.NewContext(context.Background(), &peer.Peer{
			Addr: PeerProcessAddr{
				Process: peerprocess.Process{Exe: "/usr/local/bin/client", Argv: []string{"client"}},
				Err:     errors.New("attribution failed"),
			},
		})},
		{name: "peer without program", ctx: contextWithPeerProcess(context.Background(),
			peerprocess.Process{PID: 123})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opener := &stubPromptOpener{
				decision: PermissionAllowOnce,
			}
			a := mustNewAuthorizer(t, opener, storagestub.NewInMemoryService(), texttest.NopEditor())
			ext := testPluginExtension(nil)

			err := a.Authorize(tc.ctx, blueauth.UserClaims[Extension]{Extra: ext},
				testWindowManagerResource)
			require.NoError(t, err)
			require.Equal(t, 1, opener.calls)
			// With no usable peer process info, the prompt falls back to the
			// claim's program path/args and does not show a launcher.
			assert.Contains(t, opener.messages[0], ext.Path)
			assert.Contains(t, opener.messages[0],
				fmt.Sprintf("%v", ext.Args))
			assert.NotContains(t, opener.messages[0], "running inside")
		})
	}
}

func TestAuthorizerPluginPromptDenialForbidden(t *testing.T) {
	t.Parallel()

	opener := &stubPromptOpener{decision: PermissionDenyOnce}
	a := mustNewAuthorizer(t, opener, storagestub.NewInMemoryService(), texttest.NopEditor())
	err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{
		Extra: testPluginExtension(extensionapi.AllPermissions()),
	}, testWindowManagerResource)
	assert.ErrorIs(t, err, blueauth.ErrForbidden)
}

func TestAuthorizerPluginCachesOnceDecisionsForPeerProcess(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		decision  PermissionDecision
		wantError error
	}{
		{name: "allow once", decision: PermissionAllowOnce},
		{name: "deny once", decision: PermissionDenyOnce,
			wantError: blueauth.ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opener := &stubPromptOpener{decision: tc.decision}
			a := mustNewAuthorizer(t, opener, storagestub.NewInMemoryService(), texttest.NopEditor())
			ext := testPluginExtension(nil)
			ctx := contextWithPeerProcess(context.Background(), peerprocess.Process{
				PID:  123,
				UID:  501,
				Exe:  "/usr/local/bin/plugin-cli",
				Argv: []string{"plugin-cli", "wm", "focus"},
			})

			for range 2 {
				err := a.Authorize(ctx, blueauth.UserClaims[Extension]{Extra: ext},
					testWindowManagerResource)
				if tc.wantError != nil {
					assert.ErrorIs(t, err, tc.wantError)
				} else {
					assert.NoError(t, err)
				}
			}
			assert.Equal(t, 1, opener.calls)
		})
	}
}

func TestAuthorizerPluginOnceDecisionCacheIncludesPeerPID(t *testing.T) {
	t.Parallel()

	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a := mustNewAuthorizer(t, opener, storagestub.NewInMemoryService(), texttest.NopEditor())
	ext := testPluginExtension(nil)
	ctxA := contextWithPeerProcess(context.Background(), peerprocess.Process{
		PID:  123,
		UID:  501,
		Exe:  "/usr/local/bin/plugin-cli",
		Argv: []string{"plugin-cli", "wm", "focus"},
	})
	ctxB := contextWithPeerProcess(context.Background(), peerprocess.Process{
		PID:  456,
		UID:  501,
		Exe:  "/usr/local/bin/plugin-cli",
		Argv: []string{"plugin-cli", "wm", "focus"},
	})

	require.NoError(t, a.Authorize(ctxA, blueauth.UserClaims[Extension]{Extra: ext},
		testWindowManagerResource))
	require.NoError(t, a.Authorize(ctxB, blueauth.UserClaims[Extension]{Extra: ext},
		testWindowManagerResource))
	assert.Equal(t, 2, opener.calls)
}

func TestAuthorizerPluginOnceDecisionCacheRequiresPeerPID(t *testing.T) {
	t.Parallel()

	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a := mustNewAuthorizer(t, opener, storagestub.NewInMemoryService(), texttest.NopEditor())
	ext := testPluginExtension(nil)

	for range 2 {
		require.NoError(t, a.Authorize(context.Background(),
			blueauth.UserClaims[Extension]{Extra: ext}, testWindowManagerResource))
	}
	assert.Equal(t, 2, opener.calls)
}

func TestPluginPermissionOnceDecisionExpires(t *testing.T) {
	t.Parallel()

	a := newTestAuthorizerCore(nil, nil)
	now := time.Now()
	key := "once-key"
	a.setOnceDecision(key, pluginPermissionIdentity{Path: "/bin/test"},
		extensionapi.PermissionLSP, nil, pluginPermissionDecisionAllow,
		now.Add(-pluginPermissionOnceTTL-time.Second))

	_, ok := a.getOnceDecision(key, now)
	assert.False(t, ok)
}

func TestPluginPermissionOnceKeyIncludesProcessIdentity(t *testing.T) {
	t.Parallel()

	base := pluginPermissionIdentity{
		PID:  123,
		UID:  501,
		Path: "/usr/local/bin/plugin-cli",
		Args: []string{"wm", "focus"},
	}
	baseKey := pluginPermissionOnceKey(base, extensionapi.PermissionBrowserWindowManager, nil)
	assert.NotEmpty(t, baseKey)

	changedPID := base
	changedPID.PID = 456
	assert.NotEqual(t, baseKey,
		pluginPermissionOnceKey(changedPID, extensionapi.PermissionBrowserWindowManager, nil))

	changedUID := base
	changedUID.UID = 502
	assert.NotEqual(t, baseKey,
		pluginPermissionOnceKey(changedUID, extensionapi.PermissionBrowserWindowManager, nil))

	changedPermission := base
	assert.NotEqual(t, baseKey,
		pluginPermissionOnceKey(changedPermission, extensionapi.PermissionStorage, nil))
}

func TestAuthorizerPluginPersistedDecisionsSkipPrompt(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		stored     string
		wantErrIs  error
		wantPrompt int
	}{
		{name: "stored allow", stored: pluginPermissionDecisionAllow},
		{name: "stored deny", stored: pluginPermissionDecisionDeny,
			wantErrIs: blueauth.ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			storage := storagestub.NewInMemoryService()
			ext := testPluginExtension(nil)
			key := pluginPermissionProgramStorageKey(ext.Path,
				extensionapi.PermissionBrowserWindowManager)
			require.NoError(t, storage.Set(context.Background(), key,
				storedPermissionDecision{Decision: tc.stored}))
			opener := &stubPromptOpener{
				decision: PermissionDenyOnce,
			}
			a := mustNewAuthorizer(t, opener, storage, texttest.NopEditor())

			err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{
				Extra: ext,
			}, testWindowManagerResource)
			if tc.wantErrIs != nil {
				assert.ErrorIs(t, err, tc.wantErrIs)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tc.wantPrompt, opener.calls)
		})
	}
}

func TestAuthorizerPluginStoredUnknownDecisionReturnsError(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	ext := testPluginExtension(nil)
	key := pluginPermissionProgramStorageKey(ext.Path,
		extensionapi.PermissionBrowserWindowManager)
	require.NoError(t, storage.Set(context.Background(), key,
		storedPermissionDecision{Decision: "maybe"}))
	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a := mustNewAuthorizer(t, opener, storage, texttest.NopEditor())

	err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{Extra: ext},
		testWindowManagerResource)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown stored plugin permission decision")
	assert.Zero(t, opener.calls)
}

func TestAuthorizerPluginStorageGetErrorReturnsError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("get failed")
	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a := mustNewAuthorizer(t, opener, errorStorage{
		Service: storagestub.NewInMemoryService(),
		getErr:  wantErr,
	}, texttest.NopEditor())

	err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{
		Extra: testPluginExtension(nil),
	}, testWindowManagerResource)
	assert.ErrorIs(t, err, wantErr)
	assert.Zero(t, opener.calls)
}

func TestAuthorizerPluginPersistsAlwaysDecisions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		decision   PermissionDecision
		wantStored string
		wantErrIs  error
	}{
		{name: "allow always", decision: PermissionAllowAlways,
			wantStored: pluginPermissionDecisionAllow},
		{name: "deny always", decision: PermissionDenyAlways,
			wantStored: pluginPermissionDecisionDeny, wantErrIs: blueauth.ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			storage := storagestub.NewInMemoryService()
			ext := testPluginExtension(nil)
			a := mustNewAuthorizer(t, &stubPromptOpener{decision: tc.decision}, storage, texttest.NopEditor())

			err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{Extra: ext},
				testWindowManagerResource)
			if tc.wantErrIs != nil {
				assert.ErrorIs(t, err, tc.wantErrIs)
			} else {
				assert.NoError(t, err)
			}

			key := pluginPermissionProgramStorageKey(ext.Path,
				extensionapi.PermissionBrowserWindowManager)
			var stored storedPermissionDecision
			require.NoError(t, storage.Get(context.Background(), key, &stored))
			assert.Equal(t, tc.wantStored, stored.Decision)
		})
	}
}

func TestAuthorizerPluginPersistSetErrorReturnsError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("set failed")
	cases := []PermissionDecision{
		PermissionAllowAlways,
		PermissionDenyAlways,
	}
	for _, decision := range cases {
		t.Run(string(decision), func(t *testing.T) {
			t.Parallel()

			a := mustNewAuthorizer(t, &stubPromptOpener{decision: decision},
				errorStorage{
					Service: storagestub.NewInMemoryService(),
					setErr:  wantErr,
				}, texttest.NopEditor())
			err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{
				Extra: testPluginExtension(nil),
			}, testWindowManagerResource)
			assert.ErrorIs(t, err, wantErr)
		})
	}
}

func TestAuthorizerPluginDoesNotPersistOnceDecisions(t *testing.T) {
	t.Parallel()

	cases := []PermissionDecision{
		PermissionAllowOnce,
		PermissionDenyOnce,
	}
	for _, decision := range cases {
		t.Run(string(decision), func(t *testing.T) {
			t.Parallel()

			storage := storagestub.NewInMemoryService()
			ext := testPluginExtension(nil)
			a := mustNewAuthorizer(t, &stubPromptOpener{decision: decision}, storage, texttest.NopEditor())

			_ = a.Authorize(context.Background(), blueauth.UserClaims[Extension]{Extra: ext},
				testWindowManagerResource)
			key := pluginPermissionProgramStorageKey(ext.Path,
				extensionapi.PermissionBrowserWindowManager)
			var stored storedPermissionDecision
			err := storage.Get(context.Background(), key, &stored)
			assert.ErrorIs(t, err, storageapi.ErrNotFound)
		})
	}
}

func TestAuthorizerPluginWithoutStorage(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		decision  PermissionDecision
		wantError error
	}{
		{name: "allow always without storage", decision: PermissionAllowAlways},
		{name: "deny always without storage", decision: PermissionDenyAlways,
			wantError: blueauth.ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			a := mustNewAuthorizer(t, &stubPromptOpener{decision: tc.decision}, nil, texttest.NopEditor())
			err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{
				Extra: testPluginExtension(nil),
			}, testWindowManagerResource)
			if tc.wantError != nil {
				assert.ErrorIs(t, err, tc.wantError)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestAuthorizerPluginWithoutPrompterForbidden(t *testing.T) {
	t.Parallel()

	a := mustNewAuthorizer(t, nil, nil, texttest.NopEditor())
	err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{
		Extra: testPluginExtension(extensionapi.AllPermissions()),
	}, testWindowManagerResource)
	assert.ErrorIs(t, err, blueauth.ErrForbidden)
}

func TestPluginPermissionStorageKeyChangesWhenArgsChange(t *testing.T) {
	t.Parallel()

	keyA := pluginPermissionStorageKey("/bin/test", []string{"a"},
		extensionapi.PermissionBrowserWindowManager)
	keyB := pluginPermissionStorageKey("/bin/test", []string{"b"},
		extensionapi.PermissionBrowserWindowManager)
	assert.NotEqual(t, keyA, keyB)
}

func TestPluginPermissionStorageKeyUsesColonSeparator(t *testing.T) {
	t.Parallel()

	key := pluginPermissionStorageKey("/usr/local/bin/test", []string{"a/b", "c"},
		extensionapi.PermissionBrowserWindowManager)
	assert.NotContains(t, key, "/")
	assert.Contains(t, key, ":")
	assert.Contains(t, key, "extensionv2:plugin-permissions:")
}

func TestPluginPermissionStorageKeyChangesWhenPermissionChanges(t *testing.T) {
	t.Parallel()

	keyA := pluginPermissionStorageKey("/bin/test", []string{"a"},
		extensionapi.PermissionBrowserWindowManager)
	keyB := pluginPermissionStorageKey("/bin/test", []string{"a"},
		extensionapi.PermissionStorage)
	assert.NotEqual(t, keyA, keyB)
}

func TestAuthorizerAuthorizeStartCommandComplexBashScriptDecomposes(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{decision: PermissionAllowAlways}
	a := newTestAuthorizerCore(opener, storage)
	ext := testPluginExtension(nil)
	ctx := blueauth.ContextWithClaims(context.Background(),
		blueauth.UserClaims[Extension]{Extra: ext})

	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash",
		Args: []string{"-c",
			"cd /Users/ernestrc/.rune/worktrees/vim && gofmt -l $(find . -name '.go' -not -path './.git/' 2>/dev/null) 2>&1 | head -20"},
		Dir: "/tmp",
	}))
	assert.Equal(t, map[string]string{
		"gofmt *":  pluginPermissionDecisionAllow,
		"find . *": pluginPermissionDecisionAllow,
		"head *":   pluginPermissionDecisionAllow,
	}, storedScopes(t, storage))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/find", Args: []string{".", "-name", "*.go"}, Dir: "/tmp",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/gofmt", Args: []string{"-l", "."}, Dir: "/tmp",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/head", Args: []string{"-20"}, Dir: "/tmp",
	}))

	assert.Equal(t, 1, opener.calls)
}

func testPluginExtension(perms extensionapi.Permissions) Extension {
	return Extension{
		Metadata: extensionapi.Metadata{Permissions: perms},
		Plugin:   true,
		Path:     "/bin/test",
		Args:     []string{"--flag"},
	}
}

func testRegularExtension(perms extensionapi.Permissions) Extension {
	return Extension{Metadata: extensionapi.Metadata{
		DeveloperID:      "dev-id",
		DeveloperEmail:   "dev@example.com",
		DeveloperKey:     "dev-key",
		ExtensionID:      "test-extension",
		ExtensionName:    "Test Extension",
		ExtensionVersion: "v1.2.3",
		Permissions:      perms,
	}}
}

func contextWithPeerProcess(ctx context.Context, process peerprocess.Process) context.Context {
	return peer.NewContext(ctx, &peer.Peer{Addr: PeerProcessAddr{Process: process}})
}

func mustNewAuthorizer(
	t *testing.T, opener PromptOpener,
	storage storageapi.Service, editor text.Editor,
) *Authorizer {
	t.Helper()
	a, err := NewAuthorizer(editor, opener, storage, syncScheduleNextTick, &capturingStatus{},
		testTrustStore(t), newFakeResolver(), Config{})
	require.NoError(t, err)
	return a
}

func mustNewAuthorizerWithStatus(
	t *testing.T, opener PromptOpener,
	storage storageapi.Service, editor text.Editor,
	status *capturingStatus,
) *Authorizer {
	t.Helper()
	a, err := NewAuthorizer(editor, opener, storage, syncScheduleNextTick, status,
		testTrustStore(t), newFakeResolver(), Config{})
	require.NoError(t, err)
	return a
}

// newTextComponent returns the kind of PromptOpener the IDE builds the
// authorizer with.
func newTextComponent(t *testing.T) *text.Component {
	t.Helper()
	cwd, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), cwd)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	cfg := text.DefaultConfig()
	cfg.ScheduleNextTick = syncScheduleNextTick
	comp, err := text.NewComponent(standard.Editor(),
		workspace.NewSchemeWorkspace(cwd, scheme, syncScheduleNextTick), cfg)
	require.NoError(t, err)
	return comp
}

// testTrustStore returns a pkgtrust.Store backed by the embedded keyring,
// which trusts the fingerprint the verified-publisher tests exercise.
func testTrustStore(t *testing.T) *pkgtrust.Store {
	t.Helper()
	return pkgtrust.NewStore()
}

func newTestAuthorizerCore(
	opener PromptOpener, storage storageapi.Service,
) *Authorizer {
	a := &Authorizer{
		prompter: newPermissionPrompter(opener, syncScheduleNextTick, &capturingStatus{}),
		storage:  storage,
		trust:    pkgtrust.NewStore(),
		resolver: newFakeResolver(),
		once:     make(map[string]pluginPermissionOnceDecision),
		pending:  make(map[string]*pendingPrompt),
	}
	return a
}

func TestAuthorizerDecisionChangeFiresOnRuntimeDecision(t *testing.T) {
	t.Parallel()

	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a := newTestAuthorizerCore(opener, storagestub.NewInMemoryService())
	var changed int
	a.onDecisionChange(func() { changed++ })

	err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{
		Extra: testPluginExtension(nil),
	}, workspacerpc.Executor_Signal_FullMethodName)
	require.NoError(t, err)
	require.Equal(t, 1, opener.calls)
	assert.Equal(t, 1, changed, "a runtime permission decision invalidates the cache")
}

func TestAuthorizerDecisionChangeNotifiesAllObservers(t *testing.T) {
	t.Parallel()

	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a := newTestAuthorizerCore(opener, storagestub.NewInMemoryService())
	var first, second int
	a.onDecisionChange(func() { first++ })
	a.onDecisionChange(func() { second++ })

	err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{
		Extra: testPluginExtension(nil),
	}, workspacerpc.Executor_Signal_FullMethodName)
	require.NoError(t, err)
	require.Equal(t, 1, opener.calls)
	assert.Equal(t, 1, first, "the first observer must still fire")
	assert.Equal(t, 1, second, "the second observer must also fire")
}

func TestAuthorizerRegularExtensionStartCommandRequiresExecute(t *testing.T) {
	t.Parallel()

	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a := mustNewAuthorizer(t, opener, storagestub.NewInMemoryService(), texttest.NopEditor())
	ext := testRegularExtension(extensionapi.NewPermissions(extensionapi.PermissionStorage))

	err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{Extra: ext},
		workspacerpc.Executor_StartCommand_FullMethodName)
	require.NoError(t, err)
	assert.Zero(t, opener.calls)
}

func TestAuthorizerPluginStartCommandDefersPromptToCommandAuthorizer(t *testing.T) {
	t.Parallel()

	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a := mustNewAuthorizer(t, opener, storagestub.NewInMemoryService(), texttest.NopEditor())

	err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{
		Extra: testPluginExtension(extensionapi.NewPermissions(extensionapi.PermissionStorage)),
	}, workspacerpc.Executor_StartCommand_FullMethodName)
	require.NoError(t, err)
	assert.Zero(t, opener.calls)
}

func TestAuthorizerPluginSignalStillPromptsForExecute(t *testing.T) {
	t.Parallel()

	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a := mustNewAuthorizer(t, opener, storagestub.NewInMemoryService(), texttest.NopEditor())

	err := a.Authorize(context.Background(), blueauth.UserClaims[Extension]{
		Extra: testPluginExtension(extensionapi.NewPermissions(extensionapi.PermissionStorage)),
	}, workspacerpc.Executor_Signal_FullMethodName)
	require.NoError(t, err)
	require.Equal(t, 1, opener.calls)
	// Signal maps to PermissionExecute; the prompt renders the matching
	// action text.
	assert.Contains(t, opener.messages[0],
		PermissionActionText(extensionapi.PermissionExecute))
}

func TestAuthorizerAuthorizeStartCommandPromptsWithCommandDetails(t *testing.T) {
	t.Parallel()

	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a := newTestAuthorizerCore(opener, storagestub.NewInMemoryService())
	ext := testPluginExtension(nil)
	ctx := blueauth.ContextWithClaims(context.Background(), blueauth.UserClaims[Extension]{Extra: ext})

	err := a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/grep",
		Args: []string{"foo"},
		Dir:  "/tmp/workspace",
	})
	require.NoError(t, err)
	require.Equal(t, 1, opener.calls)
	msg := opener.messages[0]
	// The command-specific prompt includes the plugin program and the
	// command it wants to run (path, args, working directory).
	assert.Contains(t, msg, ext.Path)
	assert.Contains(t, msg, fmt.Sprintf("%v", ext.Args))
	assert.Contains(t, msg, "/bin/grep")
	assert.Contains(t, msg, "[foo]")
	assert.Contains(t, msg, "/tmp/workspace")
	assert.Contains(t, msg, "\n\nRuns `/bin/grep`.")
	assert.Contains(t, msg, "\n\nChoosing **Always** or **Never** lets you pick which "+
		"commands and subcommands the decision covers.")
}

const moveTestsScript = `M=/tmp/movetests/movetests; D=internal/workspace/vtescheme; set -e
$M -src $D/vte_scheme_test.go -dst $D/vte_scheme_e2e_test.go -names TestServerSchemeOwnsTerminals,TestServerChroot,TestClientSchemesShareTheMachineServer,TestLoopbackOwnsTerminalsInProcess
$M -src $D/vteclient_test.go -dst $D/vteclient_e2e_test.go -names TestClientVTERendersCanonicalStream,TestClientVTEReplicasConverge,TestClientVTEConvergesAfterResize,TestClientVTEReportsTitleToItsOwnUI,TestClientVTEIsNotASecondResponder,TestClientVTEDeliversOSCToEveryClient
$M -src $D/vtelifecycle_test.go -dst $D/vtelifecycle_e2e_test.go -names TestVTELifecycleOverRPC
$M -src $D/vteregistry_test.go -dst $D/vteregistry_e2e_test.go -names TestVTERegistryLifecycle,TestVTERegistryResizeAuthority,TestVTERegistryDetachRestoresTheHeirsSize
$M -src $D/vteserver_test.go -dst $D/vteserver_e2e_test.go -names TestVTEServerAttachStreamsOutput,TestVTEServerFansOutToEveryClient,TestVTEServerResizeFollowsFocus,TestVTEServerDetachLeavesEmulatorRunning,TestVTEServerDropsSlowClient,TestVTEServerSurvivesDegenerateResize,TestVTEServerClampsCreateDimensions,TestVTEServerRejectsWrongDirectionFrames,TestVTEServerRejectsSecondHello,TestVTEServerDetachKeepsTheEmulator,TestVTEServerHandlesEmptyInput
$M -src $D/vteserver_unit_test.go -dst $D/vteserver_e2e_test.go -names TestAttachmentDelegatesToItsEmulator
/Users/ernestrc/.rune/bin/goimports -w $D/*_test.go
go vet ./$D/ && go vet -tags e2e ./$D/ && echo vet-ok; sed -n '17p' $D/test/persistence_test.go`

func TestAuthorizerAuthorizeOpaqueScriptPromptsForExactApproval(t *testing.T) {
	t.Parallel()

	const width, height = 120, 40
	opener := newTextComponent(t)
	comp := opener.Browser()
	ticks := make(chan func())
	status := &capturingStatus{}
	a := newTestAuthorizerCore(opener, storagestub.NewInMemoryService())
	a.prompter = newPermissionPrompter(opener, func(fn func()) bool {
		ticks <- fn
		return true
	}, status)
	ctx := blueauth.ContextWithClaims(context.Background(), blueauth.UserClaims[Extension]{
		Extra: testRegularExtension(extensionapi.NewPermissions(extensionapi.PermissionExecute)),
	})
	authorize := func(cmd workspaceapi.Cmd) <-chan error {
		done := make(chan error, 1)
		go debug.CapturePanicReport(func() { done <- a.AuthorizeCommand(ctx, cmd) })
		return done
	}
	// Scheduled callbacks run on the test goroutine, as they would on the event
	// loop, so the component is only ever touched from one goroutine.
	openPrompt := func() {
		t.Helper()
		select {
		case fn := <-ticks:
			fn()
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the prompt")
		}
	}
	awaitAuthorization := func(done <-chan error) error {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case fn := <-ticks:
				fn()
			case err := <-done:
				return err
			case <-timer.C:
				t.Fatal("timed out waiting for authorization")
			}
		}
	}
	cmd := workspaceapi.Cmd{
		Path: "bash",
		Args: []string{"-c", moveTestsScript},
		Dir:  "/Users/ernestrc/.rune/worktrees/rune-7bf2/RUNE-321-2",
	}
	closed := `
┌──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┐
│                                                                                                                      │
├──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
└──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘`[1:]

	done := authorize(cmd)
	openPrompt()
	handlertest.RunHandlerSequence(t, comp, width, height, []handlertest.SequenceTestCase{
		{Expected: `
┌──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┐
│                                                                                                                      │
├──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                                                      │
│█●████████████████████████████████████████████████████████████████████████████████████████████████████████████████████│
││                                                                                                                    ││
││  Extension Test Extension by dev-id wants to run bash with args:                                                   ││
││                                                                                                                    ││
││  -c                                                                                                                ││
││  M=/tmp/movetests/movetests; D=internal/workspace/vtescheme; set -e                                                ││
││  $M -src $D/vte_scheme_test.go -dst $D/vte_scheme_e2e_test.go -names TestServerSchemeOwnsTerminals,TestServerChro  ││
││  ot,TestClientSchemesShareTheMachineServer,TestLoopbackOwnsTerminalsInProcess                                      ││
││  $M -src $D/vteclient_test.go -dst $D/vteclient_e2e_test.go -names TestClientVTERendersCanonicalStream,TestClient  ││
││  VTEReplicasConverge,TestClientVTEConvergesAfterResize,TestClientVTEReportsTitleToItsOwnUI,TestClientVTEIsNotASec  ││
││  ondResponder,TestClientVTEDeliversOSCToEveryClient                                                                ││
││  $M -src $D/vtelifecycle_test.go -dst $D/vtelifecycle_e2e_test.go -names TestVTELifecycleOverRPC                   ││
││  $M -src $D/vteregistry_test.go -dst $D/vteregistry_e2e_test.go -names TestVTERegistryLifecycle,TestVTERegistryRe  ││
││  sizeAuthority,TestVTERegistryDetachRestoresTheHeirsSize                                                           ││
││  $M -src $D/vteserver_test.go -dst $D/vteserver_e2e_test.go -names TestVTEServerAttachStreamsOutput,TestVTEServer  ││
││  FansOutToEveryClient,TestVTEServerResizeFollowsFocus,TestVTEServerDetachLeavesEmulatorRunning,TestVTEServerDrops  ││
││  SlowClient,TestVTEServerSurvivesDegenerateResize,TestVTEServerClampsCreateDimensions,TestVTEServerRejectsWrongDi  ││
││  rectionFrames,TestVTEServerRejectsSecondHello,TestVTEServerDetachKeepsTheEmulator,TestVTEServerHandlesEmptyInput  ││
││  $M -src $D/vteserver_unit_test.go -dst $D/vteserver_e2e_test.go -names TestAttachmentDelegatesToItsEmulator       ││
││  /Users/ernestrc/.rune/bin/goimports -w $D/*_test.go                                                               ││
││  go vet ./$D/ && go vet -tags e2e ./$D/ && echo vet-ok; sed -n '17p' $D/test/persistence_test.go                   ││
││                                                                                                                    ││
││  in /Users/ernestrc/.rune/worktrees/rune-7bf2/RUNE-321-2                                                           ││
││                                                                                                                    ││
││  Runs /usr/bin/bash, $M (known only when it runs), /Users/ernestrc/.rune/bin/goimports, /usr/bin/go, echo (shell   ││
││  builtin), /usr/bin/sed.                                                                                           ││
││                                                                                                                    ││
││  ⚠  sed runs arbitrary code.                                                                                       ││
││                                                                                                                    ││
││  Choosing Always or Never lets you pick which commands and subcommands the decision covers.                        ││
││                                                                                                                    ││
││                                                                                                                    ││
││                  Yes                      Always                     No                     Never                  ││
│└────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘│
│                                                                                                                      │
└──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘`[1:]},
		{InputSequence: "A", Expected: closed},
	})
	// Always opens the scope picker, after the prompt's status is
	// withdrawn, where the whole command is picked since $M is not known
	// until it runs.
	openPrompt()
	openPrompt()
	handlertest.RunHandlerSequence(t, comp, width, height, []handlertest.SequenceTestCase{
		{Expected: `
┌──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┐
│                                                                                                                      │
├──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│█●███████████████████████████████████████████████████ Always allow ███████████████████████████████████████████████████│
││                                                                                                                    ││
││ From now on, extension Test Extension may run the checked commands without asking you. * stands for any further    ││
││ arguments.                                                                                                         ││
││                                                                                                                    ││
││ 󰄲  /Users/ernestrc/.rune/bin/goimports *                                                                           ││
││ └─ 󰄲  /Users/ernestrc/.rune/bin/goimports -w *                                                                     ││
││ 󰄮  go *                                                                                                            ││
││ └─ 󰄲  go vet *                                                                                                     ││
││    └─ 󰄲  go vet -tags *                                                                                            ││
││       └─ 󰄲  go vet -tags e2e *                                                                                     ││
││ 󰄮  echo *  (shell builtin)                                                                                         ││
││ └─ 󰄲  echo vet-ok *  (shell builtin)                                                                               ││
││    └─ 󰄲  echo vet-ok  (shell builtin)                                                                              ││
││ 󰄮  sed *  ⚠  runs arbitrary code                                                                                   ││
││ └─ 󰄮  sed -n *  ⚠  runs arbitrary code                                                                             ││
││                                                                                                                    ││
││ Run authorizer list in the console to see what each extension may run, and authorizer revoke <id> to revoke a      ││
││ decision.                                                                                                          ││
││                                                                                                                    ││
││ <space> toggles · <enter> confirms · <esc> cancels                                                                 ││
│└────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘│
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
└──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘`[1:]},
		{InputSequence: "<enter>", Expected: closed},
	})
	require.NoError(t, awaitAuthorization(done))
	blocked := status.blocked()
	require.Len(t, blocked, 2)
	assert.Equal(t, "User authorization required for bash", blocked[0].Msg)
	assert.Equal(t, "User authorization required: pick the commands extension Test Extension may run",
		blocked[1].Msg)

	select {
	case err := <-authorize(cmd):
		require.NoError(t, err)
	case <-ticks:
		t.Fatal("Always must cover the identical command")
	}

	cmd.Dir = "/tmp"
	done = authorize(cmd)
	openPrompt()
	handlertest.RunHandlerSequence(t, comp, width, height, []handlertest.SequenceTestCase{
		{InputSequence: "N", Expected: closed},
	})
	require.ErrorIs(t, awaitAuthorization(done), blueauth.ErrForbidden)
}

func TestAuthorizerAuthorizeStartCommandRegularExtensionRequiresExecute(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		perms      extensionapi.Permissions
		wantErrIs  error
		wantPrompt int
	}{
		{name: "with execute", perms: extensionapi.NewPermissions(
			extensionapi.PermissionExecute), wantPrompt: 1},
		{name: "without execute", perms: extensionapi.NewPermissions(
			extensionapi.PermissionStorage), wantErrIs: blueauth.ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opener := &stubPromptOpener{decision: PermissionAllowOnce}
			a := newTestAuthorizerCore(opener, storagestub.NewInMemoryService())
			ext := testRegularExtension(tc.perms)
			ctx := blueauth.ContextWithClaims(context.Background(), blueauth.UserClaims[Extension]{Extra: ext})

			err := a.AuthorizeCommand(ctx, workspaceapi.Cmd{Path: "/bin/grep"})
			if tc.wantErrIs != nil {
				assert.ErrorIs(t, err, tc.wantErrIs)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tc.wantPrompt, opener.calls)
			if tc.wantPrompt > 0 {
				msg := opener.messages[0]
				// Command prompts for regular extensions identify the
				// extension by name and developer id and show the command.
				assert.Contains(t, msg, ext.ExtensionName)
				assert.Contains(t, msg, ext.DeveloperID)
				assert.Contains(t, msg, "/bin/grep")
			}
		})
	}
}

func TestAuthorizerAuthorizeStartCommandDecisions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		decision   PermissionDecision
		wantErrIs  error
		wantStored map[string]string
	}{
		{name: "allow once", decision: PermissionAllowOnce, wantStored: map[string]string{}},
		{name: "deny once", decision: PermissionDenyOnce, wantErrIs: blueauth.ErrForbidden,
			wantStored: map[string]string{}},
		{name: "allow always", decision: PermissionAllowAlways,
			wantStored: map[string]string{"/bin/grep foo *": pluginPermissionDecisionAllow}},
		{name: "deny always", decision: PermissionDenyAlways, wantErrIs: blueauth.ErrForbidden,
			wantStored: map[string]string{"/bin/grep foo *": pluginPermissionDecisionDeny}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			storage := storagestub.NewInMemoryService()
			opener := &stubPromptOpener{decision: tc.decision}
			a := newTestAuthorizerCore(opener, storage)
			ext := testPluginExtension(nil)
			ctx := blueauth.ContextWithClaims(context.Background(), blueauth.UserClaims[Extension]{Extra: ext})
			cmd := workspaceapi.Cmd{Path: "/bin/grep", Args: []string{"foo"}, Dir: "/tmp"}

			err := a.AuthorizeCommand(ctx, cmd)
			if tc.wantErrIs != nil {
				assert.ErrorIs(t, err, tc.wantErrIs)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tc.wantStored, storedScopes(t, storage))
			for _, stored := range storedCommandDecisions(t, storage) {
				assert.Equal(t, []executableRef{{Name: "/bin/grep", Path: "/bin/grep"}},
					stored.Executables)
				assert.Equal(t, cmd.Path, stored.Command.Path)
				assert.Equal(t, cmd.Args, stored.Command.Args)
				assert.Equal(t, cmd.Dir, stored.Command.Dir)
			}
			// Only approving a command records the executables it runs.
			var exe storedExecutable
			err = storage.Get(context.Background(), executableStorageKey("", "/bin/grep"), &exe)
			if tc.decision == PermissionAllowAlways {
				require.NoError(t, err)
				assert.Equal(t, sha256Hex("/bin/grep"), exe.SHA256)
			} else {
				assert.ErrorIs(t, err, storageapi.ErrNotFound)
			}
		})
	}
}

func TestAuthorizerAuthorizeStartCommandPersistedDecisionAppliesToSameCommandWithDifferentArgs(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{
		decision: PermissionAllowAlways,
		picks:    []string{"/bin/grep *"},
	}
	a := newTestAuthorizerCore(opener, storage)
	ext := testPluginExtension(nil)
	ctx := blueauth.ContextWithClaims(context.Background(), blueauth.UserClaims[Extension]{Extra: ext})

	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/grep", Args: []string{"foo"}, Dir: "/tmp",
	}))
	require.Equal(t, [][]string{{
		"[ ] /bin/grep *",
		"└─ [x] /bin/grep foo *",
		"   └─ [ ] /bin/grep foo",
	}}, opener.offered)
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/grep", Args: []string{"foo"}, Dir: "/tmp",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/grep", Args: []string{"bar"}, Dir: "/tmp",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/rm", Args: []string{"-rf", "foo"}, Dir: "/tmp",
	}))

	assert.Equal(t, 2, opener.calls)
}

func TestAuthorizerScopePickerNestsScopesUnderTheScopesThatCoverThem(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{
		decision: PermissionAllowAlways,
		picks:    []string{"git *", "git commit *"},
	}
	a := newTestAuthorizerCore(opener, storage)
	ctx := blueauth.ContextWithClaims(context.Background(),
		blueauth.UserClaims[Extension]{Extra: testPluginExtension(nil)})

	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash", Args: []string{"-c", "git status && git commit -m fix"}, Dir: "/tmp",
	}))
	require.Equal(t, [][]string{{
		"[ ] git *",
		"├─ [x] git status *",
		"│  └─ [ ] git status",
		"└─ [x] git commit *",
		"   └─ [ ] git commit -m *",
		"      └─ [ ] git commit -m fix",
	}}, opener.offered)
	// git * covers git commit *, which needs no approval of its own.
	assert.Equal(t, map[string]string{"git *": pluginPermissionDecisionAllow},
		storedScopes(t, storage))
}

func TestAuthorizerAuthorizeStartCommandRegularExtensionPersistedDecisionAppliesToSameCommandWithDifferentArgs(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{
		decision: PermissionAllowAlways,
		picks:    []string{"/bin/grep *"},
	}
	a := newTestAuthorizerCore(opener, storage)
	ext := testRegularExtension(extensionapi.NewPermissions(extensionapi.PermissionExecute))
	ctx := blueauth.ContextWithClaims(context.Background(), blueauth.UserClaims[Extension]{Extra: ext})

	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/grep", Args: []string{"foo"}, Dir: "/tmp",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/grep", Args: []string{"foo"}, Dir: "/tmp",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/grep", Args: []string{"bar"}, Dir: "/tmp",
	}))

	assert.Equal(t, 1, opener.calls)
}

func TestAuthorizerAuthorizeStartCommandPersistedDecisionNormalizesShellWrapper(t *testing.T) {
	t.Parallel()

	for _, wrapped := range []workspaceapi.Cmd{
		{Path: "/bin/sh", Args: []string{"-lc", "cd /tmp/workspace && grep foo file.txt"}, Dir: "/tmp"},
		{Path: "/bin/bash", Args: []string{"-c", "grep foo file.txt"}, Dir: "/tmp"},
	} {
		t.Run(wrapped.Path, func(t *testing.T) {
			t.Parallel()

			storage := storagestub.NewInMemoryService()
			opener := &stubPromptOpener{
				decision: PermissionAllowAlways,
				picks:    []string{"grep *"},
			}
			a := newTestAuthorizerCore(opener, storage)
			ext := testPluginExtension(nil)
			ctx := blueauth.ContextWithClaims(context.Background(),
				blueauth.UserClaims[Extension]{Extra: ext})

			require.NoError(t, a.AuthorizeCommand(ctx, wrapped))
			require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
				Path: "/usr/bin/grep", Args: []string{"bar", "other.txt"}, Dir: "/tmp/workspace",
			}))
			require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
				Path: "/bin/rm", Args: []string{"-rf", "other.txt"}, Dir: "/tmp/workspace",
			}))

			assert.Equal(t, 2, opener.calls)
		})
	}
}

func TestAuthorizerAuthorizeStartCommandPersistedDecisionBroadensSequenceShellScripts(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{
		decision: PermissionAllowAlways,
		picks:    []string{"echo *  (shell builtin)", "grep *"},
	}
	a := newTestAuthorizerCore(opener, storage)
	ext := testPluginExtension(nil)
	ctx := blueauth.ContextWithClaims(context.Background(), blueauth.UserClaims[Extension]{Extra: ext})

	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash",
		Args: []string{"-c", "echo foo; grep bar file.txt"},
		Dir:  "/tmp",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash",
		Args: []string{"-c", "echo baz; grep qux other.txt"},
		Dir:  "/tmp",
	}))

	assert.Equal(t, 1, opener.calls)
}

func TestAuthorizerAuthorizeStartCommandPersistedDecisionBroadensCompoundShellScripts(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{
		decision: PermissionAllowAlways,
		picks:    []string{"make *", "grep *", "rm *"},
	}
	a := newTestAuthorizerCore(opener, storage)
	ext := testPluginExtension(nil)
	ctx := blueauth.ContextWithClaims(context.Background(), blueauth.UserClaims[Extension]{Extra: ext})

	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash",
		Args: []string{
			"-c",
			`make test && grep "blabla" file && bash -c "cd /tmp/abc && rm -rf ."`,
		},
		Dir: "/tmp",
	}))
	// Each picked command runs silently afterwards, as long as it is the
	// same file.
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/rm", Args: []string{"-rf", "foo.txt"}, Dir: "/tmp",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "grep", Args: []string{"bar", "other.txt"}, Dir: "/tmp",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/make", Args: []string{"build"}, Dir: "/tmp",
	}))
	assert.Equal(t, 1, opener.calls)

	opener.decision = PermissionDenyOnce
	require.ErrorIs(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/rm", Args: []string{"-rf", "foo.txt"}, Dir: "/tmp",
	}), blueauth.ErrForbidden)
	assert.Equal(t, 2, opener.calls)
}

func TestAuthorizerAuthorizeStartCommandPersistedDecisionDoesNotBroadenBeyondEffectiveSet(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{decision: PermissionAllowAlways}
	a := newTestAuthorizerCore(opener, storage)
	ext := testPluginExtension(nil)
	ctx := blueauth.ContextWithClaims(context.Background(), blueauth.UserClaims[Extension]{Extra: ext})

	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash",
		Args: []string{"-c", "make test && grep bla file"},
		Dir:  "/tmp",
	}))
	// curl is not in the approved set, so it prompts again.
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/curl", Args: []string{"https://x"}, Dir: "/tmp",
	}))

	assert.Equal(t, 2, opener.calls)
}

func TestAuthorizerAuthorizeStartCommandPartialApprovalStillPrompts(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{
		decision: PermissionAllowAlways,
		picks:    []string{"git *", "tail *"},
	}
	a := newTestAuthorizerCore(opener, storage)
	ctx := blueauth.ContextWithClaims(context.Background(),
		blueauth.UserClaims[Extension]{Extra: testPluginExtension(nil)})
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash", Args: []string{"-c", "git log | tail -n 5"}, Dir: "/tmp",
	}))
	require.Equal(t, 1, opener.calls)

	// git and bash are approved already, head is not, so the prompt asks
	// about head alone.
	opener.picks = nil
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash", Args: []string{"-c", "git log | head -n 5"}, Dir: "/tmp",
	}))
	require.Equal(t, 2, opener.calls)
	assert.Contains(t, opener.messages[1], "\n\nRuns `/usr/bin/head`.\n\n")
	require.Len(t, opener.offered, 2)
	assert.Equal(t, []string{
		"[x] head *",
		"└─ [ ] head -n *",
		"   └─ [ ] head -n 5",
	}, opener.offered[1])

	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash", Args: []string{"-c", "git status | head -n 1"}, Dir: "/tmp",
	}))
	assert.Equal(t, 2, opener.calls)

	// When what is left runs arbitrary code, the prompt warns of it too.
	opener.decision = PermissionAllowOnce
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash", Args: []string{"-c", "git log | sed -n 1p"}, Dir: "/tmp",
	}))
	require.Equal(t, 3, opener.calls)
	assert.Contains(t, opener.messages[2],
		"\n\nRuns `/usr/bin/sed`.\n\n⚠️ `sed` runs arbitrary code.\n\n")
}

func TestAuthorizerAlwaysApprovesTheProgramsOfACommandAllowedOtherwise(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{
		decision: PermissionAllowAlways,
		picks:    []string{"git *"},
	}
	a := newTestAuthorizerCore(opener, storage)
	ctx := blueauth.ContextWithClaims(context.Background(),
		blueauth.UserClaims[Extension]{Extra: testPluginExtension(nil)})
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{Path: "git", Args: []string{"status"}}))

	// git * covers what timeout runs, so Always only approves timeout.
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "timeout", Args: []string{"5", "git", "status"},
	}))
	require.Equal(t, 2, opener.calls)
	assert.Equal(t, []string{PromptOptionYes, PromptOptionYesAlways, PromptOptionNo},
		opener.options[1])
	assert.Contains(t, opener.messages[1], "\n\nRuns `/usr/bin/timeout`.\n\n"+
		"Choosing **Always** approves `/usr/bin/timeout` from now on.")
	assert.Len(t, opener.offered, 1, "there is nothing to pick")
	assert.Equal(t, map[string]string{"git *": pluginPermissionDecisionAllow},
		storedScopes(t, storage))

	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "timeout", Args: []string{"9", "git", "log"},
	}))
	assert.Equal(t, 2, opener.calls)
}

func TestAuthorizerAsksEveryTimeWhenNoDecisionForGoodCanCoverTheCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cmd  workspaceapi.Cmd
		want string
	}{
		{name: "programs that cannot be told",
			cmd:  workspaceapi.Cmd{Path: "/bin/bash", Args: []string{"-c", "$CMD args"}},
			want: "\n\nRune asks every time because it runs no program a scope can describe."},
		{name: "program in a directory not known until it runs",
			cmd:  workspaceapi.Cmd{Path: "bash", Args: []string{"-c", "cd sub && ./build --fast"}},
			want: "`./build` (the script changes directory, so Rune asks every time)."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			storage := storagestub.NewInMemoryService()
			opener := &stubPromptOpener{decision: PermissionAllowOnce}
			a := newTestAuthorizerCore(opener, storage)
			ctx := blueauth.ContextWithClaims(context.Background(),
				blueauth.UserClaims[Extension]{Extra: testPluginExtension(nil)})

			require.NoError(t, a.AuthorizeCommand(ctx, tc.cmd))
			assert.Equal(t, [][]string{{PromptOptionYes, PromptOptionNo}}, opener.options)
			assert.Contains(t, opener.messages[0], tc.want)
			assert.NotContains(t, opener.messages[0], "Always")
			assert.Empty(t, storedCommandDecisions(t, storage))
		})
	}
}

func TestAuthorizerScopesContainerCommandsAsArbitraryCode(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{decision: PermissionAllowAlways}
	a := newTestAuthorizerCore(opener, storage)
	ctx := blueauth.ContextWithClaims(context.Background(),
		blueauth.UserClaims[Extension]{Extra: testPluginExtension(nil)})

	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash", Args: []string{"-c", "docker run --rm alpine"},
	}))
	assert.Contains(t, opener.messages[0], "\n\nRuns `/bin/bash`, `/usr/bin/docker`.\n\n"+
		"⚠️ `docker` runs arbitrary code.\n\n")
	assert.Equal(t, [][]string{{
		"[ ] docker *" + scopeWarning,
		"└─ [ ] docker run *" + scopeWarning,
		"   └─ [ ] docker run --rm *" + scopeWarning,
		"      └─ [x] docker run --rm alpine",
	}}, opener.offered)
	assert.Equal(t, map[string]string{"docker run --rm alpine": pluginPermissionDecisionAllow},
		storedScopes(t, storage))
}

func TestAuthorizerAuthorizeStartCommandOnceDecisionIncludesPeerAndCommand(t *testing.T) {
	t.Parallel()

	opener := &stubPromptOpener{decision: PermissionAllowOnce}
	a := newTestAuthorizerCore(opener, storagestub.NewInMemoryService())
	ext := testPluginExtension(nil)
	ctx := contextWithPeerProcess(context.Background(), peerprocess.Process{
		PID:  123,
		UID:  501,
		Exe:  "/usr/local/bin/plugin-cli",
		Argv: []string{"plugin-cli", "run"},
	})
	ctx = blueauth.ContextWithClaims(ctx, blueauth.UserClaims[Extension]{Extra: ext})

	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/grep", Args: []string{"foo"}, Dir: "/tmp",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/grep", Args: []string{"foo"}, Dir: "/tmp",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/grep", Args: []string{"bar"}, Dir: "/tmp",
	}))

	assert.Equal(t, 2, opener.calls)
	require.Len(t, a.once, 2)
	for _, decision := range a.once {
		assert.Equal(t, "/usr/local/bin/plugin-cli", decision.Path)
		assert.Equal(t, extensionapi.PermissionExecute, decision.Permission)
		assert.Equal(t, "/bin/grep", decision.Command.Path)
	}
}

// blockingPromptOpener blocks every Prompt call on a trigger channel
// until the test calls release(). It records the PromptHandler of the
// first prompt it observes so the test can simulate the user approving
// the (single) floating prompt that the browser deduplicates by
// message.
type blockingPromptOpener struct {
	decision PermissionDecision

	mu       sync.Mutex
	calls    int
	firstHdl handler.PromptHandler
	firstOpt int
	options  []string
	waiters  []chan struct{}
	ready    chan struct{}
}

func newBlockingPromptOpener(decision PermissionDecision) *blockingPromptOpener {
	return &blockingPromptOpener{
		decision: decision,
		ready:    make(chan struct{}),
	}
}

func (p *blockingPromptOpener) Prompt(
	_ string, options []string, _ []term.KeyComb,
	promptHandler handler.PromptHandler,
) browser.Window {
	want := optionForDecision(p.decision)
	gate := make(chan struct{})
	p.mu.Lock()
	p.calls++
	if p.firstHdl == nil {
		p.firstHdl = promptHandler
		p.options = options
		for i, opt := range options {
			if opt == want {
				p.firstOpt = i
				break
			}
		}
	}
	first := p.calls == 1
	p.waiters = append(p.waiters, gate)
	p.mu.Unlock()
	if first {
		close(p.ready)
	}
	<-gate
	return browsertest.NopWindow()
}

func (p *blockingPromptOpener) Floating(
	browser.Floating, browserapi.FloatingConfig,
) (browser.Window, error) {
	panic("blocking prompts answer Yes, so open no scope picker")
}

// release unblocks every Prompt call made so far and fires the stored
// PromptHandler's OnSelect exactly once with the matching option index.
// This simulates the user approving a single floating prompt that the
// browser's deduplicate-by-message rule leaves as the only attached
// handler.
func (p *blockingPromptOpener) release() {
	p.mu.Lock()
	waiters := p.waiters
	p.waiters = nil
	hdl := p.firstHdl
	optIdx := p.firstOpt
	opts := p.options
	p.mu.Unlock()
	if hdl != nil {
		hdl.OnSelect(optIdx, opts[optIdx])
	}
	for _, w := range waiters {
		close(w)
	}
}

func (p *blockingPromptOpener) waitReady(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-p.ready:
	case <-time.After(d):
		t.Fatalf("timed out waiting for first prompt to arrive")
	}
}

func (p *blockingPromptOpener) promptCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestAuthorizerCoalescesConcurrentPrompts(t *testing.T) {
	t.Parallel()

	opener := newBlockingPromptOpener(PermissionAllowOnce)
	a := newTestAuthorizerCore(opener, storagestub.NewInMemoryService())
	ext := testPluginExtension(nil)
	ctx := contextWithPeerProcess(context.Background(), peerprocess.Process{
		PID: 123, UID: 501, Exe: "/bin/plugin", Argv: []string{"plugin"},
	})
	ctx = blueauth.ContextWithClaims(ctx, blueauth.UserClaims[Extension]{Extra: ext})

	const concurrency = 8
	errs := make(chan error, concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			errs <- a.Authorize(ctx, blueauth.UserClaims[Extension]{Extra: ext},
				"/browser.EventPublisher/Publish")
		}()
	}

	// Wait for the first Authorize call to reach the prompt opener;
	// give peers a moment so they also reach coalescedPrompt.
	opener.waitReady(t, 5*time.Second)
	time.Sleep(50 * time.Millisecond)

	opener.release()

	for i := 0; i < concurrency; i++ {
		select {
		case err := <-errs:
			require.NoError(t, err, "Authorize #%d", i)
		case <-time.After(5 * time.Second):
			t.Fatalf("Authorize #%d did not return in time", i)
		}
	}

	// Exactly one prompt should have been opened across all N
	// concurrent Authorize calls; the rest must have coalesced.
	assert.Equal(t, 1, opener.promptCalls(),
		"concurrent Authorize calls for the same onceKey must coalesce on a single prompt")
}

func TestAuthorizerPersistedScopeDoesNotCoverOtherExecutables(t *testing.T) {
	t.Parallel()

	opener := &stubPromptOpener{
		decision: PermissionAllowAlways,
		picks:    []string{"git *"},
	}
	a := newTestAuthorizerCore(opener, storagestub.NewInMemoryService())
	ctx := blueauth.ContextWithClaims(context.Background(),
		blueauth.UserClaims[Extension]{Extra: testPluginExtension(nil)})
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "git", Args: []string{"status"}, Dir: "/ws",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/git", Args: []string{"push"}, Dir: "/ws",
	}))
	require.Equal(t, 1, opener.calls)

	cases := []struct {
		name string
		cmd  workspaceapi.Cmd
		want string
	}{
		{name: "relative to the directory",
			cmd:  workspaceapi.Cmd{Path: "./git", Args: []string{"status"}, Dir: "/ws"},
			want: "Runs `/ws/git`."},
		{name: "elsewhere",
			cmd:  workspaceapi.Cmd{Path: "/tmp/evil/git", Args: []string{"status"}},
			want: "Runs `/tmp/evil/git`."},
		{name: "in a PATH the command sets",
			cmd: workspaceapi.Cmd{Path: "bash", Args: []string{"-c", "git status"},
				Env: []string{"PATH=/home/u/.local/bin:/usr/bin"}},
			want: "`/home/u/.local/bin/git`"},
		{name: "with a preloaded library",
			cmd: workspaceapi.Cmd{Path: "git", Args: []string{"status"},
				Env: []string{"LD_PRELOAD=/tmp/x.so"}},
			want: "Rune asks every time because it sets LD_PRELOAD."},
		{name: "under a wrapper never approved",
			cmd:  workspaceapi.Cmd{Path: "nice", Args: []string{"git", "status"}},
			want: "Runs `/usr/bin/nice`.\n\n"},
	}
	for _, tc := range cases {
		opener.decision = PermissionDenyOnce
		calls := opener.calls
		err := a.AuthorizeCommand(ctx, tc.cmd)
		assert.ErrorIs(t, err, blueauth.ErrForbidden, tc.name)
		require.Equal(t, calls+1, opener.calls, tc.name)
		assert.Contains(t, opener.messages[len(opener.messages)-1], tc.want, tc.name)
	}
}

func TestAuthorizerPersistedScopeDoesNotCoverShadowingExecutable(t *testing.T) {
	t.Parallel()

	r := newFakeResolver("/home/u/go/bin/git")
	r.hostPATH = []string{"/home/u/go/bin", "/usr/bin"}
	opener := &stubPromptOpener{
		decision: PermissionAllowAlways,
		picks:    []string{"git *"},
	}
	a := newTestAuthorizerCore(opener, storagestub.NewInMemoryService())
	a.resolver = r
	ctx := blueauth.ContextWithClaims(context.Background(),
		blueauth.UserClaims[Extension]{Extra: testPluginExtension(nil)})
	cmd := workspaceapi.Cmd{Path: "bash", Args: []string{"-c", "git status"}}
	require.NoError(t, a.AuthorizeCommand(ctx, cmd))
	require.NoError(t, a.AuthorizeCommand(ctx, cmd))
	require.Equal(t, 1, opener.calls)

	// A git dropped earlier in the PATH is another program.
	r.setMissing("/home/u/go/bin/git", false)
	opener.decision = PermissionDenyOnce
	require.ErrorIs(t, a.AuthorizeCommand(ctx, cmd), blueauth.ErrForbidden)
	require.Equal(t, 2, opener.calls)
	assert.Contains(t, opener.messages[1], "`/home/u/go/bin/git`")
}

func TestAuthorizerTransparentWrappersScopeTheCommandTheyRun(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{decision: PermissionAllowAlways}
	a := newTestAuthorizerCore(opener, storage)
	ctx := blueauth.ContextWithClaims(context.Background(),
		blueauth.UserClaims[Extension]{Extra: testPluginExtension(nil)})
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "env", Args: []string{"FOO=1", "make"},
	}))
	assert.Equal(t, map[string]string{"make *": pluginPermissionDecisionAllow},
		storedScopes(t, storage))

	opener.decision = PermissionDenyOnce
	require.ErrorIs(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "env", Args: []string{"rm", "-rf", "/"},
	}), blueauth.ErrForbidden)
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "env", Args: []string{"BAR=2", "make", "test"},
	}))
	assert.Equal(t, 2, opener.calls)
}

func TestAuthorizerRenewsChangedExecutable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		option string
		// wantErr is the error the commands that run the changed
		// executable get.
		wantErr error
		// wantPrompts are the prompts after a second command runs the
		// changed executable.
		wantPrompts int
	}{
		{option: PromptOptionRenew, wantPrompts: 2},
		{option: PromptOptionOnce, wantPrompts: 3},
		{option: PromptOptionNo, wantErr: blueauth.ErrForbidden, wantPrompts: 3},
	}
	for _, tc := range cases {
		t.Run(strings.TrimSpace(tc.option), func(t *testing.T) {
			t.Parallel()

			r := newFakeResolver()
			storage := storagestub.NewInMemoryService()
			opener := &stubPromptOpener{
				decision: PermissionAllowAlways,
				picks:    []string{"git *", "git status *"},
			}
			a := newTestAuthorizerCore(opener, storage)
			a.resolver = r
			ext := testRegularExtension(extensionapi.NewPermissions(extensionapi.PermissionExecute))
			ctx := blueauth.ContextWithClaims(context.Background(),
				blueauth.UserClaims[Extension]{Extra: ext})
			require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
				Path: "git", Args: []string{"status"},
			}))

			r.setContent("/usr/bin/git", "rebuilt")
			opener.renewal = tc.option
			push := workspaceapi.Cmd{Path: "git", Args: []string{"push"}}
			err := a.AuthorizeCommand(ctx, push)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 2, opener.calls)
			assert.Equal(t, "**`/usr/bin/git`** has changed since you approved `git *` for "+
				"extension Test Extension.\n\n"+
				"sha256 `"+fingerprintPreview(sha256Hex("/usr/bin/git"))+"` → `"+
				fingerprintPreview(sha256Hex("rebuilt"))+"`, modified 2026-10-25 14:02.\n\n"+
				"Renew authorization?", opener.messages[1])

			// Renewing covers every approval of the executable at once.
			err = a.AuthorizeCommand(ctx, workspaceapi.Cmd{Path: "git", Args: []string{"status"}})
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantPrompts, opener.calls)

			var exe storedExecutable
			require.NoError(t, storage.Get(context.Background(),
				executableStorageKey("", "/usr/bin/git"), &exe))
			if tc.option == PromptOptionRenew {
				assert.Equal(t, sha256Hex("rebuilt"), exe.SHA256)
			} else {
				assert.Equal(t, sha256Hex("/usr/bin/git"), exe.SHA256)
			}
		})
	}
}

func TestAuthorizerScopePickerCancelDeniesAndEmptyPickAllowsOnce(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		opener  *stubPromptOpener
		wantErr error
	}{
		{name: "cancel", wantErr: blueauth.ErrForbidden, opener: &stubPromptOpener{
			decision: PermissionAllowAlways,
			cancel:   true,
		}},
		{name: "nothing picked", opener: &stubPromptOpener{
			decision: PermissionAllowAlways,
			picks:    []string{},
		}},
		{name: "nothing picked for never", wantErr: blueauth.ErrForbidden,
			opener: &stubPromptOpener{
				decision: PermissionDenyAlways,
				picks:    []string{},
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			storage := storagestub.NewInMemoryService()
			a := newTestAuthorizerCore(tc.opener, storage)
			ext := testRegularExtension(extensionapi.NewPermissions(extensionapi.PermissionExecute))
			ctx := blueauth.ContextWithClaims(context.Background(),
				blueauth.UserClaims[Extension]{Extra: ext})
			cmd := workspaceapi.Cmd{Path: "git", Args: []string{"status"}}
			for range 2 {
				err := a.AuthorizeCommand(ctx, cmd)
				if tc.wantErr != nil {
					assert.ErrorIs(t, err, tc.wantErr)
				} else {
					assert.NoError(t, err)
				}
			}
			// The decision holds for a moment only.
			assert.Equal(t, 1, tc.opener.calls)
			assert.Len(t, tc.opener.offered, 1)
			assert.Empty(t, storedCommandDecisions(t, storage))
		})
	}
}

func TestAuthorizerScopePickerThatCannotOpenSavesNothing(t *testing.T) {
	t.Parallel()

	errNoWindow := errors.New("no window")
	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{decision: PermissionAllowAlways, floatingErr: errNoWindow}
	a := newTestAuthorizerCore(opener, storage)
	ctx := blueauth.ContextWithClaims(context.Background(), blueauth.UserClaims[Extension]{
		Extra: testRegularExtension(extensionapi.NewPermissions(extensionapi.PermissionExecute)),
	})

	err := a.AuthorizeCommand(ctx, workspaceapi.Cmd{Path: "git", Args: []string{"status"}})

	assert.ErrorIs(t, err, errNoWindow)
	assert.Empty(t, storedCommandDecisions(t, storage))
}

func TestAuthorizerNeverDeniesPickedScopesOverAllowedOnes(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{
		decision: PermissionDenyAlways,
		picks:    []string{"git push *"},
	}
	a := newTestAuthorizerCore(opener, storage)
	ctx := blueauth.ContextWithClaims(context.Background(),
		blueauth.UserClaims[Extension]{Extra: testPluginExtension(nil)})
	require.ErrorIs(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "git", Args: []string{"push", "origin"},
	}), blueauth.ErrForbidden)

	opener.decision = PermissionAllowAlways
	opener.picks = []string{"git *"}
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "git", Args: []string{"status"},
	}))
	assert.Equal(t, map[string]string{
		"git *":      pluginPermissionDecisionAllow,
		"git push *": pluginPermissionDecisionDeny,
	}, storedScopes(t, storage))

	// git * allows git push, but the deny of git push * wins.
	calls := opener.calls
	require.ErrorIs(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "git", Args: []string{"push", "-f"},
	}), blueauth.ErrForbidden)
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "git", Args: []string{"log"},
	}))
	assert.Equal(t, calls, opener.calls)
}

type capturingNotifications struct {
	browserapi.Notifications
	messages []string
}

func (n *capturingNotifications) Notify(
	_ browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	n.messages = append(n.messages, fmt.Sprintf(msg, args...))
	return "", nil
}

func TestNewAuthorizerForgetsLegacyCommandDecisions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	storage := storagestub.NewInMemoryService()
	prefix := pluginPermissionStorageKey("/bin/test", nil, extensionapi.PermissionExecute)
	legacy := prefix + ":legacy"
	require.NoError(t, storage.Set(ctx, legacy, storedPermissionDecision{
		Key: legacy, Decision: pluginPermissionDecisionAllow,
		Command: pluginPermissionCommandDetail{Path: "/bin/bash", Args: []string{"-c", "git status"}},
	}))
	program := pluginPermissionProgramStorageKey("/bin/test", extensionapi.PermissionLSP)
	require.NoError(t, storage.Set(ctx, program, storedPermissionDecision{
		Key: program, Decision: pluginPermissionDecisionAllow,
	}))
	scoped := commandScopeKey(prefix, "scoped")
	require.NoError(t, storage.Set(ctx, scoped, storedPermissionDecision{
		Key: scoped, Decision: pluginPermissionDecisionAllow,
		Command: pluginPermissionCommandDetail{Path: "git"}, Scope: "git *",
	}))

	notes := &capturingNotifications{}
	newAuthorizer := func() {
		_, err := NewAuthorizer(texttest.NopEditor(), nil, storage, syncScheduleNextTick,
			&capturingStatus{}, testTrustStore(t), newFakeResolver(),
			Config{Notifications: notes})
		require.NoError(t, err)
	}
	newAuthorizer()

	var stored storedPermissionDecision
	assert.ErrorIs(t, storage.Get(ctx, legacy, &stored), storageapi.ErrNotFound)
	assert.NoError(t, storage.Get(ctx, program, &stored))
	assert.NoError(t, storage.Get(ctx, scoped, &stored))
	require.Len(t, notes.messages, 1)
	assert.Contains(t, notes.messages[0], "Rune forgot 1 command approvals saved by an older version")

	newAuthorizer()
	assert.Len(t, notes.messages, 1)
}

func TestAuthorizerNeverPersistsApprovalsOfProgramsNotFound(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cmd  workspaceapi.Cmd
		want string
	}{
		{name: "relative to a directory the script changes to",
			cmd:  workspaceapi.Cmd{Path: "bash", Args: []string{"-c", "cd sub && make && ./build --fast"}},
			want: "`./build` (the script changes directory, so Rune asks every time)"},
		{name: "not on the PATH yet",
			cmd:  workspaceapi.Cmd{Path: "bash", Args: []string{"-c", "make install && tool run"}},
			want: "`tool` (not found, so Rune asks every time)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := newFakeResolver("/usr/bin/tool", "/bin/tool")
			storage := storagestub.NewInMemoryService()
			opener := &stubPromptOpener{
				decision: PermissionAllowAlways,
			}
			a := newTestAuthorizerCore(opener, storage)
			a.resolver = r
			ext := testPluginExtension(nil)
			ctx := blueauth.ContextWithClaims(context.Background(),
				blueauth.UserClaims[Extension]{Extra: ext})

			require.NoError(t, a.AuthorizeCommand(ctx, tc.cmd))
			assert.Contains(t, opener.messages[0], tc.want)
			for _, rows := range opener.offered {
				for _, row := range rows {
					assert.NotContains(t, row, "build", "programs not found are not offered")
					assert.NotContains(t, row, "tool")
					assert.NotContains(t, row, "this exact command")
				}
			}
			// What was found is allowed now, so only Yes and No are left
			// for what was not.
			opener.decision = PermissionAllowOnce
			require.NoError(t, a.AuthorizeCommand(ctx, tc.cmd))
			assert.Equal(t, 2, opener.calls)
			assert.Equal(t, []string{PromptOptionYes, PromptOptionNo}, opener.options[1])
		})
	}
}

func TestAuthorizerAuthorizeCommandBuildsOnPastDecisions(t *testing.T) {
	t.Parallel()

	built := time.Date(2026, 10, 25, 14, 2, 0, 0, time.Local)
	rebuilt := built.Add(19 * time.Hour)
	content := func(p string) string { return "#!/bin/sh\n# " + p + "\n" }
	const rebuiltGit = "#!/bin/sh\n# git, rebuilt with a fix\n"
	host := diskHost{t: t, root: t.TempDir()}
	for _, p := range []string{
		"/bin/bash", "/bin/sh", "/usr/bin/git", "/usr/bin/tail", "/usr/bin/head",
		"/usr/bin/sed", "/usr/bin/find", "/usr/bin/xargs", "/usr/bin/rm", "/usr/bin/make",
		"/usr/bin/timeout", "/usr/bin/grep", "/usr/bin/docker", "/home/u/bin/git",
		"/ws/build", "/ws/sub/build",
	} {
		host.write(p, content(p), built)
	}
	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{}
	a, err := NewAuthorizer(texttest.NopEditor(), opener, storage, syncScheduleNextTick,
		&capturingStatus{}, testTrustStore(t), NewWorkspaceResolver(WorkspaceResolverConfig{
			FS: host, Root: "/ws", Home: "/home/u", HostPATH: staticHostPATH("/usr/bin", "/bin"),
		}), Config{})
	require.NoError(t, err)
	agent := testRegularExtension(extensionapi.NewPermissions(extensionapi.PermissionExecute))
	other := agent
	other.ExtensionID, other.ExtensionName = "other-extension", "Other Extension"

	run := func(name string, args ...string) workspaceapi.Cmd {
		return workspaceapi.Cmd{Path: name, Args: args}
	}
	bash := func(script string) workspaceapi.Cmd { return run("bash", "-c", script) }
	const pick = "\n\nChoosing **Always** or **Never** lets you pick which commands and " +
		"subcommands the decision covers."
	scopes := []string{PromptOptionYes, PromptOptionYesAlways, PromptOptionNo, PromptOptionNoNever}
	programs := []string{PromptOptionYes, PromptOptionYesAlways, PromptOptionNo}
	once := []string{PromptOptionYes, PromptOptionNo}
	renewal := []string{PromptOptionRenew, PromptOptionOnce, PromptOptionNo}
	allow, deny := pluginPermissionDecisionAllow, pluginPermissionDecisionDeny

	steps := []struct {
		name string
		// ext runs the command, or the agent when nil.
		ext   *Extension
		setup func()
		cmd   workspaceapi.Cmd
		// The user's answers. Prompts nobody expects are answered No.
		decision PermissionDecision
		picks    []string
		renew    string
		// wantOptions are the options of the prompt, or nil when the user
		// is not asked.
		wantOptions []string
		// wantNote is the prompt without its first paragraph, which shows
		// the command, or the whole prompt of a renewal.
		wantNote    string
		wantOffered []string
		wantErr     error
		// wantSaved are the decisions the step saves, by scope.
		wantSaved map[string]string
		// wantApproved are the executables whose fingerprints the step
		// saves.
		wantApproved []string
	}{
		{name: "Always on a first command lets the user pick what it covers",
			cmd: run("git", "status"), decision: PermissionAllowAlways, picks: []string{"git *"},
			wantOptions: scopes, wantNote: "Runs `/usr/bin/git`." + pick,
			wantOffered: []string{"[ ] git *", "└─ [x] git status *", "   └─ [ ] git status"},
			wantSaved:   map[string]string{"git *": allow}, wantApproved: []string{"/usr/bin/git"}},
		{name: "a scope covers other arguments, and the same file by its path",
			cmd: run("/usr/bin/git", "push", "origin", "main")},
		{name: "a pipeline asks only about the programs not approved yet",
			cmd: bash("git log | tail -n 5"), decision: PermissionAllowAlways, picks: []string{"tail *"},
			wantOptions: scopes, wantNote: "Runs `/bin/bash`, `/usr/bin/tail`." + pick,
			wantOffered:  []string{"[x] tail *", "└─ [ ] tail -n *", "   └─ [ ] tail -n 5"},
			wantSaved:    map[string]string{"tail *": allow},
			wantApproved: []string{"/bin/bash", "/usr/bin/tail"}},
		{name: "what was approved is neither asked about nor offered again",
			cmd: bash("git diff | head -n 5"), decision: PermissionAllowAlways,
			wantOptions: scopes, wantNote: "Runs `/usr/bin/head`." + pick,
			wantOffered: []string{"[x] head *", "└─ [ ] head -n *", "   └─ [ ] head -n 5"},
			wantSaved:   map[string]string{"head *": allow}, wantApproved: []string{"/usr/bin/head"}},
		{name: "a script whose programs are all approved runs without asking",
			cmd: bash("git status | head -n 1 && tail -n 1 log")},
		{name: "Always approves a shell never approved when its programs are",
			cmd: run("sh", "-c", "git log | tail -n 2"), decision: PermissionAllowAlways,
			wantOptions:  programs,
			wantNote:     "Runs `/bin/sh`.\n\nChoosing **Always** approves `/bin/sh` from now on.",
			wantApproved: []string{"/bin/sh"}},
		{name: "the shell runs without asking from then on",
			cmd: run("sh", "-c", "git status | head -n 3")},
		{name: "a wrapper is approved by its file alone",
			cmd: run("timeout", "5", "git", "status"), decision: PermissionAllowAlways,
			wantOptions: programs,
			wantNote: "Runs `/usr/bin/timeout`.\n\nChoosing **Always** approves " +
				"`/usr/bin/timeout` from now on.",
			wantApproved: []string{"/usr/bin/timeout"}},
		{name: "and then wraps any command approved",
			cmd: run("timeout", "60", "git", "push")},
		{name: "a program that shadows an approved one is another program",
			cmd: workspaceapi.Cmd{Path: "bash", Args: []string{"-c", "git status"},
				Env: []string{"PATH=/home/u/bin:/usr/bin"}},
			wantOptions: scopes, wantNote: "Runs `/home/u/bin/git`." + pick,
			wantErr: blueauth.ErrForbidden},
		{name: "a rebuilt executable asks to renew the approvals that run it",
			setup: func() { host.write("/usr/bin/git", rebuiltGit, rebuilt) },
			cmd:   run("git", "log"), renew: PromptOptionRenew, wantOptions: renewal,
			wantNote: "**`/usr/bin/git`** has changed since you approved `git *` for extension " +
				"Test Extension.\n\nsha256 `" + fingerprintPreview(sha256Hex(content("/usr/bin/git"))) +
				"` → `" + fingerprintPreview(sha256Hex(rebuiltGit)) + "`, modified " +
				rebuilt.Format("2006-01-02 15:04") + ".\n\nRenew authorization?",
			wantApproved: []string{"/usr/bin/git"}},
		{name: "renewing holds for every approval that runs the file",
			cmd: bash("git log | tail -n 9")},
		{name: "Never denies the scopes picked",
			cmd: run("rm", "-rf", "build"), decision: PermissionDenyAlways, picks: []string{"rm *"},
			wantOptions: scopes, wantNote: "Runs `/usr/bin/rm`." + pick,
			wantOffered: []string{"[x] rm *", "└─ [ ] rm -rf *", "   └─ [ ] rm -rf build"},
			wantErr:     blueauth.ErrForbidden, wantSaved: map[string]string{"rm *": deny}},
		{name: "a denied program forbids every command that runs it without asking",
			cmd: bash("git status && rm -f log"), wantErr: blueauth.ErrForbidden},
		{name: "find is approved like any program, since what -exec runs is asked about apart",
			cmd: run("find", ".", "-exec", "head", "{}", "+"), decision: PermissionAllowAlways,
			picks: []string{"find *"}, wantOptions: scopes, wantNote: "Runs `/usr/bin/find`." + pick,
			wantOffered: []string{"[ ] find *", "└─ [x] find . *", "   └─ [ ] find . -exec *",
				"      └─ [ ] find . -exec head *", "         └─ [ ] find . -exec head '{}' *",
				"            └─ [ ] find . -exec head '{}' +"},
			wantSaved: map[string]string{"find *": allow}, wantApproved: []string{"/usr/bin/find"}},
		{name: "find * does not cover a command -exec runs that was denied",
			cmd:     run("find", ".", "-name", "*.o", "-exec", "rm", "{}", ";"),
			wantErr: blueauth.ErrForbidden},
		{name: "nor one never approved",
			cmd:         run("find", ".", "-name", "*.go", "-exec", "grep", "-l", "TODO", "{}", "+"),
			wantOptions: scopes, wantNote: "Runs `/usr/bin/grep`." + pick,
			wantErr: blueauth.ErrForbidden},
		{name: "but runs commands approved without asking",
			cmd: run("find", ".", "-name", "*.go", "-exec", "head", "-n", "1", "{}", "+")},
		{name: "a shell builtin is approved like a program",
			cmd: run("sh", "-c", "echo ok"), decision: PermissionAllowAlways,
			picks:       []string{"echo *  (shell builtin)"},
			wantOptions: scopes, wantNote: "Runs `echo` (shell builtin)." + pick,
			wantOffered: []string{"[ ] echo *  (shell builtin)", "└─ [x] echo ok *  (shell builtin)",
				"   └─ [ ] echo ok  (shell builtin)"},
			wantSaved: map[string]string{"echo *  (shell builtin)": allow}},
		{name: "find passing the file it found to a script runs without asking",
			cmd: run("find", ".", "-name", "*.txt", "-exec", "sh", "-c", `echo "$1"`, "sh", "{}", ";")},
		{name: "find putting the file it found in a script is asked about every time",
			cmd:      run("find", ".", "-name", "*.txt", "-exec", "sh", "-c", "echo {}", ";"),
			decision: PermissionAllowOnce, wantOptions: once,
			wantNote: "Rune asks every time because find puts the names of the files it finds " +
				"in the command it runs, where they can run as code."},
		{name: "a program in the workspace is approved by its file",
			cmd: run("./build", "--fast"), decision: PermissionAllowAlways, picks: []string{"./build *"},
			wantOptions: scopes, wantNote: "Runs `/ws/build`." + pick,
			wantOffered: []string{"[x] ./build *", "└─ [ ] ./build --fast *", "   └─ [ ] ./build --fast"},
			wantSaved:   map[string]string{"./build *": allow}, wantApproved: []string{"/ws/build"}},
		{name: "so the same name in another directory is another program",
			cmd:         workspaceapi.Cmd{Path: "./build", Args: []string{"--fast"}, Dir: "/ws/sub"},
			wantOptions: scopes, wantNote: "Runs `/ws/sub/build`." + pick,
			wantErr: blueauth.ErrForbidden},
		{name: "find -execdir runs a relative name in each directory it finds",
			cmd:      run("find", ".", "-name", "Makefile", "-execdir", "./build", ";"),
			decision: PermissionAllowOnce, wantOptions: once,
			wantNote: "Runs `./build` (find runs it in the directory of each file it finds, " +
				"so Rune asks every time)."},
		{name: "a script that changes directory does too",
			cmd: bash("cd sub && ./build"), decision: PermissionAllowOnce, wantOptions: once,
			wantNote: "Runs `./build` (the script changes directory, so Rune asks every time)."},
		{name: "but not a name looked up in an absolute PATH",
			cmd: bash("cd sub && head -n 1 x")},
		{name: "xargs is approved by its file alone",
			cmd: bash("git ls-files | xargs head -n 1"), decision: PermissionAllowAlways,
			wantOptions: programs,
			wantNote: "Runs `/usr/bin/xargs`.\n\nChoosing **Always** approves " +
				"`/usr/bin/xargs` from now on.",
			wantApproved: []string{"/usr/bin/xargs"}},
		{name: "xargs putting what it reads in a script is asked about every time",
			cmd:      bash(`git ls-files | xargs -I{} sh -c "echo {}"`),
			decision: PermissionAllowOnce, wantOptions: once,
			wantNote: "Rune asks every time because xargs puts the lines it reads in the " +
				"command it runs, where they can run as code."},
		{name: "a program that runs arbitrary code is warned of and approved exactly",
			cmd: bash("git log | sed -n 1p"), decision: PermissionAllowAlways,
			wantOptions: scopes,
			wantNote:    "Runs `/usr/bin/sed`.\n\n⚠️ `sed` runs arbitrary code." + pick,
			wantOffered: []string{"[ ] sed *" + scopeWarning, "└─ [ ] sed -n *" + scopeWarning,
				"   └─ [x] sed -n 1p"},
			wantSaved: map[string]string{"sed -n 1p": allow}, wantApproved: []string{"/usr/bin/sed"}},
		{name: "so other code asks again",
			cmd: bash("git log | sed -n 2p"), decision: PermissionAllowOnce, wantOptions: scopes,
			wantNote: "Runs `/usr/bin/sed`.\n\n⚠️ `sed` runs arbitrary code." + pick},
		{name: "a container runs arbitrary code",
			cmd: bash("docker run --rm alpine"), decision: PermissionAllowAlways,
			wantOptions: scopes,
			wantNote:    "Runs `/usr/bin/docker`.\n\n⚠️ `docker` runs arbitrary code." + pick,
			wantOffered: []string{"[ ] docker *" + scopeWarning, "└─ [ ] docker run *" + scopeWarning,
				"   └─ [ ] docker run --rm *" + scopeWarning, "      └─ [x] docker run --rm alpine"},
			wantSaved:    map[string]string{"docker run --rm alpine": allow},
			wantApproved: []string{"/usr/bin/docker"}},
		{name: "a script whose programs cannot be told is asked about every time",
			cmd: bash("$CMD --version"), decision: PermissionAllowOnce, wantOptions: once,
			wantNote: "Rune asks every time because it runs no program a scope can describe."},
		{name: "so is a command that sets what its programs load",
			cmd: workspaceapi.Cmd{Path: "git", Args: []string{"status"},
				Env: []string{"LD_PRELOAD=/tmp/x.so"}},
			wantOptions: once, wantNote: "Rune asks every time because it sets LD_PRELOAD.",
			wantErr: blueauth.ErrForbidden},
		{name: "a program not found is never approved",
			cmd: bash("make && tool run"), decision: PermissionAllowAlways, picks: []string{"make *"},
			wantOptions: scopes,
			wantNote:    "Runs `/usr/bin/make`, `tool` (not found, so Rune asks every time)." + pick,
			wantOffered: []string{"[x] make *", "└─ [ ] make"},
			wantSaved:   map[string]string{"make *": allow}, wantApproved: []string{"/usr/bin/make"}},
		{name: "so it is asked about every time",
			cmd: bash("make && tool run"), decision: PermissionAllowOnce, wantOptions: once,
			wantNote: "Runs `tool` (not found, so Rune asks every time)."},
		{name: "another extension is asked about what this one may run",
			ext: &other, cmd: run("git", "status"),
			wantOptions: scopes, wantNote: "Runs `/usr/bin/git`." + pick,
			wantErr: blueauth.ErrForbidden},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			if step.setup != nil {
				step.setup()
			}
			ext := agent
			if step.ext != nil {
				ext = *step.ext
			}
			calls, pickers := opener.calls, len(opener.offered)
			decisions, fingerprints := storedScopes(t, storage), storedFingerprints(t, storage)
			opener.decision = cmp.Or(step.decision, PermissionDenyOnce)
			opener.picks, opener.renewal = step.picks, step.renew
			ctx := blueauth.ContextWithClaims(context.Background(),
				blueauth.UserClaims[Extension]{Extra: ext})

			err := a.AuthorizeCommand(ctx, step.cmd)

			if step.wantErr != nil {
				assert.ErrorIs(t, err, step.wantErr)
			} else {
				assert.NoError(t, err)
			}
			if step.wantOptions == nil {
				assert.Equal(t, calls, opener.calls, "the user is not asked")
			} else if assert.Equal(t, calls+1, opener.calls, "the user is asked once") {
				assert.Equal(t, step.wantOptions, opener.options[calls])
				note := opener.messages[calls]
				if !slices.Contains(step.wantOptions, PromptOptionRenew) {
					_, note, _ = strings.Cut(note, "\n\n")
				}
				assert.Equal(t, step.wantNote, note)
			}
			var offered []string
			if len(opener.offered) > pickers {
				offered = opener.offered[pickers]
			}
			assert.Equal(t, step.wantOffered, offered)
			assert.Equal(t, step.wantSaved, changedEntries(decisions, storedScopes(t, storage)))
			assert.Equal(t, step.wantApproved, slices.Sorted(maps.Keys(
				changedEntries(fingerprints, storedFingerprints(t, storage)))))
		})
	}
}

// diskHost is a workspace host whose file system is a directory on the
// disk, so that the resolver the IDE uses finds and fingerprints its
// executables: /usr/bin/git is <root>/usr/bin/git.
type diskHost struct {
	t    *testing.T
	root string
}

func (h diskHost) Stat(name string) (fs.FileInfo, error) { return os.Stat(h.file(name)) }

func (h diskHost) Open(name string) (workspaceapi.File, error) { return os.Open(h.file(name)) }

func (h diskHost) file(name string) string {
	return filepath.Join(h.root, filepath.FromSlash(name))
}

// write writes the executable p, modified at mtime so that the prompts
// that show it read the same on every run.
func (h diskHost) write(p, content string, mtime time.Time) {
	h.t.Helper()
	writeExecutable(h.t, h.file(p), content, 0o755)
	require.NoError(h.t, os.Chtimes(h.file(p), mtime, mtime))
}

// storedFingerprints maps the executables in storage to the digests the
// user approved.
func storedFingerprints(t *testing.T, storage storageapi.Service) map[string]string {
	t.Helper()
	it, err := storage.List(context.Background(), nil)
	require.NoError(t, err)
	defer it.Close()
	ret := map[string]string{}
	for it.HasNext() {
		var stored storedExecutable
		require.NoError(t, it.NextTo(&stored))
		if strings.HasPrefix(stored.Key, executableStoragePrefix) {
			ret[stored.Path] = stored.SHA256
		}
	}
	return ret
}

// changedEntries returns the entries of after that before lacks or holds
// another value for, or nil when there are none.
func changedEntries(before, after map[string]string) map[string]string {
	var ret map[string]string
	for k, v := range after {
		if old, ok := before[k]; ok && old == v {
			continue
		}
		if ret == nil {
			ret = map[string]string{}
		}
		ret[k] = v
	}
	return ret
}
