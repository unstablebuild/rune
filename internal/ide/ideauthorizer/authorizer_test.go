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
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	blueauth "github.com/unstablebuild/blue/auth"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi/workspacerpc"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"google.golang.org/grpc/peer"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension/extensionv2/peerprocess"
	"unstable.build/rune/internal/ide/pkgtrust"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
)

const testWindowManagerResource = "/browser.WindowManager/NewWindow"

// stubPromptOpener is a test PromptOpener that always selects the option
// whose label maps to decision. It records each prompt message so tests
// can assert what the user would see.
type stubPromptOpener struct {
	decision PermissionDecision
	calls    int
	messages []string
}

func (s *stubPromptOpener) Prompt(
	message string, options []string,
	_ []term.KeyComb, promptHandler handler.PromptHandler,
) browser.Window {
	s.calls++
	s.messages = append(s.messages, message)
	want := optionForDecision(s.decision)
	for i, opt := range options {
		if opt == want {
			promptHandler.OnSelect(i, opt)
			return browsertest.NopWindow()
		}
	}
	panic("test prompt option was not provided: " + want)
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

// capturingNotifications is a test double for browserapi.Notifications
// that records each emitted notification so tests can assert that a
// warning notification was delivered.
type capturingNotifications struct {
	mu            sync.Mutex
	notifications []capturedNotification
}

type capturedNotification struct {
	Level browserapi.NotificationLevel
	Msg   string
}

func (n *capturingNotifications) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notifications = append(n.notifications, capturedNotification{
		Level: level,
		Msg:   fmt.Sprintf(msg, args...),
	})
	return "", nil
}

func (n *capturingNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n *capturingNotifications) UpdateNotificationProgress(
	string, string, int64, int64,
) error {
	return nil
}

func (n *capturingNotifications) captured() []capturedNotification {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]capturedNotification(nil), n.notifications...)
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
	noti := &capturingNotifications{}
	a := mustNewAuthorizerWithNotifications(t, opener,
		storagestub.NewInMemoryService(), texttest.NopEditor(), noti)
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

	// Creating the prompt emits a single warning notification with the
	// relevant scope so the workspace tab can be highlighted for attention.
	captured := noti.captured()
	require.Len(t, captured, 1)
	assert.Equal(t, browserapi.LevelWarn, captured[0].Level)
	assert.Contains(t, captured[0].Msg, "authorization")
	assert.Contains(t, captured[0].Msg, "Test Extension")

	err = a.Authorize(context.Background(), blueauth.UserClaims[Extension]{Extra: ext},
		testWindowManagerResource)
	require.NoError(t, err)
	assert.Equal(t, 1, opener.calls)
	// The cached once-decision prevents a second prompt and thus a
	// second warning notification.
	assert.Len(t, noti.captured(), 1)
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
	a, err := NewAuthorizer(editor, opener, storage, syncScheduleNextTick, nil,
		testTrustStore(t), Config{
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
				storagestub.NewInMemoryService(), syncScheduleNextTick, nil,
				testTrustStore(t), tc.config)
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
		syncScheduleNextTick, nil, testTrustStore(t), Config{
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
	command := pluginPermissionCommandDetail{Path: cmd.Path, Args: cmd.Args, Dir: cmd.Dir}
	keys := extensionPermissionCommandStorageKeys(ext,
		extensionapi.PermissionExecute, command)
	require.NotEmpty(t, keys)
	var stored storedPermissionDecision
	err = storage.Get(context.Background(), keys[0], &stored)
	assert.ErrorIs(t, err, storageapi.ErrNotFound)
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
		storagestub.NewInMemoryService(), syncScheduleNextTick, nil,
		testTrustStore(t), Config{
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
		storagestub.NewInMemoryService(), syncScheduleNextTick, nil,
		testTrustStore(t), Config{
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
				syncScheduleNextTick, nil, testTrustStore(t), Config{
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
			command := pluginPermissionCommandDetail{
				Path: cmd.Path, Args: cmd.Args, Dir: cmd.Dir,
			}
			keys := extensionPermissionCommandStorageKeys(tc.ext,
				extensionapi.PermissionExecute, command)
			if tc.ext.Plugin {
				keys = pluginPermissionCommandStorageKeys(tc.ext.Path, tc.ext.Args,
					extensionapi.PermissionExecute, command)
			}
			require.NotEmpty(t, keys)
			var stored storedPermissionDecision
			err = storage.Get(context.Background(), keys[0], &stored)
			assert.ErrorIs(t, err, storageapi.ErrNotFound)
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
		syncScheduleNextTick, nil, testTrustStore(t),
		Config{AutoAuthorizeVerifiedPublisher: true})
	require.NoError(t, err)
	assert.ErrorIs(t, a.Authorize(context.Background(), blueauth.UserClaims[Extension]{Extra: ext},
		testWindowManagerResource), blueauth.ErrForbidden)
	assert.Zero(t, opener.calls)

	noToggleOpener := &stubPromptOpener{decision: PermissionAllowOnce}
	withoutToggle, err := NewAuthorizer(texttest.NopEditor(), noToggleOpener,
		storagestub.NewInMemoryService(), syncScheduleNextTick, nil,
		testTrustStore(t), Config{})
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
	noti := &capturingNotifications{}
	a := mustNewAuthorizerWithNotifications(t, opener,
		storagestub.NewInMemoryService(), texttest.NopEditor(), noti)
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

	// Creating the prompt emits a single warning notification.
	captured := noti.captured()
	require.Len(t, captured, 1)
	assert.Equal(t, browserapi.LevelWarn, captured[0].Level)
	assert.Contains(t, captured[0].Msg, "authorization")
	assert.Contains(t, captured[0].Msg, "/bin/test")
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

func TestPluginPermissionCommandStorageKeysIgnoresArgsForSameCommand(t *testing.T) {
	t.Parallel()

	keysA := pluginPermissionCommandStorageKeys("/bin/test", []string{"a"},
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/bin/grep",
			Args: []string{"foo"},
			Dir:  "/tmp/workspace",
		})
	keysB := pluginPermissionCommandStorageKeys("/bin/test", []string{"a"},
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/bin/grep",
			Args: []string{"bar", "file.txt"},
			Dir:  "/other",
		})

	assert.Equal(t, keysA, keysB)
}

func TestPluginPermissionCommandStorageKeysChangeWithEffectiveCommand(t *testing.T) {
	t.Parallel()

	keysA := pluginPermissionCommandStorageKeys("/bin/test", []string{"a"},
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/bin/grep",
			Args: []string{"foo"},
			Dir:  "/tmp/workspace",
		})
	keysB := pluginPermissionCommandStorageKeys("/bin/test", []string{"a"},
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/bin/rm",
			Args: []string{"-rf", "foo"},
			Dir:  "/tmp/workspace",
		})

	assert.NotEqual(t, keysA, keysB)
}

func TestPluginPermissionCommandStorageKeysNormalizeShellWrappedCommand(t *testing.T) {
	t.Parallel()

	keysA := pluginPermissionCommandStorageKeys("/bin/test", []string{"a"},
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/bin/sh",
			Args: []string{"-lc", "cd /tmp/workspace && grep foo file.txt"},
			Dir:  "/tmp",
		})
	keysB := pluginPermissionCommandStorageKeys("/bin/test", []string{"a"},
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/usr/bin/grep",
			Args: []string{"bar", "other.txt"},
			Dir:  "/elsewhere",
		})

	assert.Equal(t, keysA, keysB)
}

func TestPluginPermissionCommandStorageKeysDecomposeCompoundScript(t *testing.T) {
	t.Parallel()

	compound := pluginPermissionCommandStorageKeys("/bin/test", []string{"a"},
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/bin/bash",
			Args: []string{"-c", "grep foo file.txt && rm file.txt"},
			Dir:  "/tmp",
		})
	grepOnly := pluginPermissionCommandStorageKeys("/bin/test", []string{"a"},
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/usr/bin/grep",
			Args: []string{"bar", "other.txt"},
			Dir:  "/tmp",
		})

	assert.Len(t, compound, 2)
	assert.Len(t, grepOnly, 1)
	assert.Contains(t, compound, grepOnly[0])
}

func TestPluginPermissionCommandStorageKeysDecomposePipelineScript(t *testing.T) {
	t.Parallel()

	pipeline := pluginPermissionCommandStorageKeys("/bin/test", []string{"a"},
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/bin/bash",
			Args: []string{"-c", "grep foo file.txt | wc -l"},
			Dir:  "/tmp",
		})
	grepOnly := pluginPermissionCommandStorageKeys("/bin/test", []string{"a"},
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/usr/bin/grep",
			Args: []string{"bar", "other.txt"},
			Dir:  "/tmp",
		})

	assert.Len(t, pipeline, 2)
	assert.Contains(t, pipeline, grepOnly[0])
}

func TestPluginPermissionCommandStorageKeysOpaqueScriptFallsBackToExactMatch(t *testing.T) {
	t.Parallel()

	keysA := pluginPermissionCommandStorageKeys("/bin/test", []string{"a"},
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/bin/bash",
			Args: []string{"-c", "$CMD args"},
			Dir:  "/tmp",
		})
	keysB := pluginPermissionCommandStorageKeys("/bin/test", []string{"a"},
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/bin/bash",
			Args: []string{"-c", "$OTHER x"},
			Dir:  "/tmp",
		})

	assert.Len(t, keysA, 1)
	assert.Len(t, keysB, 1)
	assert.NotEqual(t, keysA, keysB)
}

func TestPluginPermissionCommandStorageKeysDecomposeCmdSubstScript(t *testing.T) {
	t.Parallel()

	complex := pluginPermissionCommandStorageKeys("/bin/test", []string{"a"},
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/bin/bash",
			Args: []string{"-c",
				"cd /Users/ernestrc/.rune/worktrees/vim && gofmt -l $(find . -name '.go' -not -path './.git/' 2>/dev/null) 2>&1 | head -20"},
			Dir: "/tmp",
		})
	findOnly := pluginPermissionCommandStorageKeys("/bin/test", []string{"a"},
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/usr/bin/find",
			Args: []string{".", "-name", "*.go"},
			Dir:  "/tmp",
		})
	gofmtOnly := pluginPermissionCommandStorageKeys("/bin/test", []string{"a"},
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/usr/bin/gofmt",
			Args: []string{"-l", "."},
			Dir:  "/tmp",
		})
	headOnly := pluginPermissionCommandStorageKeys("/bin/test", []string{"a"},
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/usr/bin/head",
			Args: []string{"-20"},
			Dir:  "/tmp",
		})

	assert.Len(t, complex, 3)
	assert.Contains(t, complex, findOnly[0])
	assert.Contains(t, complex, gofmtOnly[0])
	assert.Contains(t, complex, headOnly[0])
}

func TestPluginPermissionEffectiveCommandsDecomposeScriptSubstitutions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  pluginPermissionCommandDetail
		want []string
	}{
		{
			name: "perl script with backtick command substitution",
			cmd: pluginPermissionCommandDetail{
				Path: "/usr/bin/perl",
				Args: []string{"-e", "print `git rev-parse --show-toplevel`;"},
			},
			want: []string{"perl"},
		},
		{
			name: "python stdin script from shell heredoc",
			cmd: pluginPermissionCommandDetail{
				Path: "/bin/bash",
				Args: []string{"-c", "python <<'PY'\nimport json\nprint(json.dumps({'ok': True}))\nPY"},
			},
			want: []string{"python"},
		},
		{
			name: "backtick command substitution in argument",
			cmd: pluginPermissionCommandDetail{
				Path: "/bin/bash",
				Args: []string{"-c", "echo `git rev-parse --show-toplevel`"},
			},
			want: []string{"echo", "git"},
		},
		{
			name: "nested dollar command substitutions in argument",
			cmd: pluginPermissionCommandDetail{
				Path: "/bin/bash",
				Args: []string{"-c", "printf %s $(dirname $(which go))"},
			},
			want: []string{"dirname", "printf", "which"},
		},
		{
			name: "command substitution command name with literal nested command",
			cmd: pluginPermissionCommandDetail{
				Path: "/bin/bash",
				Args: []string{"-c", "$(echo grep) foo file.txt"},
			},
			want: []string{"echo", "grep"},
		},
		{
			name: "backtick command name with literal nested command",
			cmd: pluginPermissionCommandDetail{
				Path: "/bin/bash",
				Args: []string{"-c", "`echo grep` foo file.txt"},
			},
			want: []string{"echo", "grep"},
		},
		{
			name: "complex bash sequence with nested substitutions",
			cmd: pluginPermissionCommandDetail{
				Path: "/bin/bash",
				Args: []string{"-c", "cd /tmp && echo `git rev-parse --show-toplevel` && printf %s $(dirname $(which go)) | wc -c"},
			},
			want: []string{"dirname", "echo", "git", "printf", "wc", "which"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := pluginPermissionEffectiveCommands(tt.cmd)
			require.True(t, ok)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, expectedScopeLabels(tt.want), pluginPermissionApprovalScopeLabels(tt.cmd))
		})
	}
}

func expectedScopeLabels(commands []string) []string {
	labels := make([]string, len(commands))
	for i, command := range commands {
		labels[i] = command + " *"
	}
	return labels
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
	a, err := NewAuthorizer(editor, opener, storage, syncScheduleNextTick, nil,
		testTrustStore(t), Config{})
	require.NoError(t, err)
	return a
}

// mustNewAuthorizerWithNotifications is like mustNewAuthorizer but wires
// a capturing notifications double so tests can assert that prompt
// creation also emits a warning notification.
func mustNewAuthorizerWithNotifications(
	t *testing.T, opener PromptOpener,
	storage storageapi.Service, editor text.Editor,
	noti browserapi.Notifications,
) *Authorizer {
	t.Helper()
	a, err := NewAuthorizer(editor, opener, storage, syncScheduleNextTick, noti,
		testTrustStore(t), Config{})
	require.NoError(t, err)
	return a
}

// testTrustStore returns a pkgtrust.Store backed by the embedded keyring,
// which trusts the fingerprint the verified-publisher tests exercise.
func testTrustStore(t *testing.T) *pkgtrust.Store {
	t.Helper()
	return pkgtrust.NewStore(t.TempDir(), nil)
}

func newTestAuthorizerCore(
	opener PromptOpener, storage storageapi.Service,
) *Authorizer {
	a := &Authorizer{
		prompter: newPermissionPrompter(opener, syncScheduleNextTick, nil),
		storage:  storage,
		trust:    pkgtrust.NewStore("", nil),
		once:     make(map[string]pluginPermissionOnceDecision),
		pending:  make(map[string]*pendingPrompt),
	}
	return a
}

// newTestAuthorizerCoreWithNotifications is like newTestAuthorizerCore but
// wires a capturing notifications double so tests can assert that prompt
// creation also emits a warning notification.
func newTestAuthorizerCoreWithNotifications(
	opener PromptOpener, storage storageapi.Service,
	noti browserapi.Notifications,
) *Authorizer {
	a := &Authorizer{
		prompter: newPermissionPrompter(opener, syncScheduleNextTick, noti),
		storage:  storage,
		trust:    pkgtrust.NewStore("", nil),
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
	assert.Contains(t, msg, "Always")
	assert.Contains(t, msg, "\n\nChoosing **Always** approves **grep** for all future authorization requests, for any argument combination.")
	assert.NotContains(t, msg, "grep ∗")
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
	comp := browser.NewComponent(browser.DefaultConfig())
	ticks := make(chan func())
	noti := &capturingNotifications{}
	a := newTestAuthorizerCore(comp, storagestub.NewInMemoryService())
	a.prompter = newPermissionPrompter(comp, func(fn func()) bool {
		ticks <- fn
		return true
	}, noti)
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
│                                                                                                                      │
│█●███████████████████████████████████████████████████████████████████████████████████████████████████████████████████ │
││                                                                                                                   │ │
││  Extension Test Extension by dev-id wants to run bash with args:                                                  │ │
││                                                                                                                   │ │
││  -c                                                                                                               │ │
││  M=/tmp/movetests/movetests; D=internal/workspace/vtescheme; set -e                                               │ │
││  $M -src $D/vte_scheme_test.go -dst $D/vte_scheme_e2e_test.go -names TestServerSchemeOwnsTerminals,TestServerChr  │ │
││  oot,TestClientSchemesShareTheMachineServer,TestLoopbackOwnsTerminalsInProcess                                    │ │
││  $M -src $D/vteclient_test.go -dst $D/vteclient_e2e_test.go -names TestClientVTERendersCanonicalStream,TestClien  │ │
││  tVTEReplicasConverge,TestClientVTEConvergesAfterResize,TestClientVTEReportsTitleToItsOwnUI,TestClientVTEIsNotAS  │ │
││  econdResponder,TestClientVTEDeliversOSCToEveryClient                                                             │ │
││  $M -src $D/vtelifecycle_test.go -dst $D/vtelifecycle_e2e_test.go -names TestVTELifecycleOverRPC                  │ │
││  $M -src $D/vteregistry_test.go -dst $D/vteregistry_e2e_test.go -names TestVTERegistryLifecycle,TestVTERegistryR  │ │
││  esizeAuthority,TestVTERegistryDetachRestoresTheHeirsSize                                                         │ │
││  $M -src $D/vteserver_test.go -dst $D/vteserver_e2e_test.go -names TestVTEServerAttachStreamsOutput,TestVTEServe  │ │
││  rFansOutToEveryClient,TestVTEServerResizeFollowsFocus,TestVTEServerDetachLeavesEmulatorRunning,TestVTEServerDro  │ │
││  psSlowClient,TestVTEServerSurvivesDegenerateResize,TestVTEServerClampsCreateDimensions,TestVTEServerRejectsWron  │ │
││  gDirectionFrames,TestVTEServerRejectsSecondHello,TestVTEServerDetachKeepsTheEmulator,TestVTEServerHandlesEmptyI  │ │
││  nput                                                                                                             │ │
││  $M -src $D/vteserver_unit_test.go -dst $D/vteserver_e2e_test.go -names TestAttachmentDelegatesToItsEmulator      │ │
││  /Users/ernestrc/.rune/bin/goimports -w $D/*_test.go                                                              │ │
││  go vet ./$D/ && go vet -tags e2e ./$D/ && echo vet-ok; sed -n '17p' $D/test/persistence_test.go                  │ │
││                                                                                                                   │ │
││  in /Users/ernestrc/.rune/worktrees/rune-7bf2/RUNE-321-2                                                          │ │
││                                                                                                                   │ │
││  Choosing Always approves only this exact command. Any change to its arguments or working directory will prompt   │ │
││  again.                                                                                                           │ │
││                                                                                                                   │ │
││                                                                                                                   │ │
││                  Yes                     Always                     No                     Never                  │ │
│└───────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘ │
│                                                                                                                      │
│                                                                                                                      │
│                                                                                                                      │
└──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘`[1:]},
		{InputSequence: "A", Expected: closed},
	})
	require.NoError(t, <-done)
	assert.Equal(t, []capturedNotification{{
		Level: browserapi.LevelWarn,
		Msg:   "User authorization required for bash",
	}}, noti.captured())

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
	require.ErrorIs(t, <-done, blueauth.ErrForbidden)
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
		wantStored string
	}{
		{name: "allow once", decision: PermissionAllowOnce},
		{name: "deny once", decision: PermissionDenyOnce, wantErrIs: blueauth.ErrForbidden},
		{name: "allow always", decision: PermissionAllowAlways,
			wantStored: pluginPermissionDecisionAllow},
		{name: "deny always", decision: PermissionDenyAlways,
			wantErrIs: blueauth.ErrForbidden, wantStored: pluginPermissionDecisionDeny},
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

			command := pluginPermissionCommandDetail{Path: cmd.Path, Args: cmd.Args, Dir: cmd.Dir}
			keys := pluginPermissionCommandStorageKeys(ext.Path, ext.Args,
				extensionapi.PermissionExecute, command)
			require.Len(t, keys, 1)
			var stored storedPermissionDecision
			err = storage.Get(context.Background(), keys[0], &stored)
			if tc.wantStored == "" {
				assert.ErrorIs(t, err, storageapi.ErrNotFound)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantStored, stored.Decision)
				assert.Equal(t, command.Path, stored.Command.Path)
				assert.Equal(t, command.Args, stored.Command.Args)
				assert.Equal(t, command.Dir, stored.Command.Dir)
			}
		})
	}
}

func TestAuthorizerAuthorizeStartCommandPersistedDecisionAppliesToSameCommandWithDifferentArgs(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{decision: PermissionAllowAlways}
	a := newTestAuthorizerCore(opener, storage)
	ext := testPluginExtension(nil)
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
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/rm", Args: []string{"-rf", "foo"}, Dir: "/tmp",
	}))

	assert.Equal(t, 2, opener.calls)
}

func TestAuthorizerAuthorizeStartCommandRegularExtensionPersistedDecisionAppliesToSameCommandWithDifferentArgs(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{decision: PermissionAllowAlways}
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

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{decision: PermissionAllowAlways}
	a := newTestAuthorizerCore(opener, storage)
	ext := testPluginExtension(nil)
	ctx := blueauth.ContextWithClaims(context.Background(), blueauth.UserClaims[Extension]{Extra: ext})

	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/sh",
		Args: []string{"-lc", "cd /tmp/workspace && grep foo file.txt"},
		Dir:  "/tmp",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/grep",
		Args: []string{"bar", "other.txt"},
		Dir:  "/tmp/workspace",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/rm",
		Args: []string{"-rf", "other.txt"},
		Dir:  "/tmp/workspace",
	}))

	assert.Equal(t, 2, opener.calls)
}

func TestAuthorizerAuthorizeStartCommandPersistedDecisionNormalizesPlainShellWrapper(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{decision: PermissionAllowAlways}
	a := newTestAuthorizerCore(opener, storage)
	ext := testPluginExtension(nil)
	ctx := blueauth.ContextWithClaims(context.Background(), blueauth.UserClaims[Extension]{Extra: ext})

	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash",
		Args: []string{"-c", "grep foo file.txt"},
		Dir:  "/tmp",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/grep",
		Args: []string{"bar", "other.txt"},
		Dir:  "/tmp/workspace",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/rm",
		Args: []string{"-rf", "other.txt"},
		Dir:  "/tmp/workspace",
	}))

	assert.Equal(t, 2, opener.calls)
}

func TestAuthorizerAuthorizeStartCommandPersistedDecisionBroadensSequenceShellScripts(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{decision: PermissionAllowAlways}
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
	opener := &stubPromptOpener{decision: PermissionAllowAlways}
	a := newTestAuthorizerCore(opener, storage)
	ext := testPluginExtension(nil)
	ctx := blueauth.ContextWithClaims(context.Background(), blueauth.UserClaims[Extension]{Extra: ext})

	// Approve a compound script containing make, grep, and rm.
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash",
		Args: []string{
			"-c",
			`make test && grep "blabla" file && bash -c "cd /tmp/abc && rm -rf ."`,
		},
		Dir: "/tmp",
	}))
	// Each constituent command runs silently afterwards.
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/rm", Args: []string{"-rf", "foo.txt"}, Dir: "/tmp",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/grep", Args: []string{"bar", "other.txt"}, Dir: "/tmp",
	}))
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/make", Args: []string{"build"}, Dir: "/tmp",
	}))

	assert.Equal(t, 1, opener.calls)
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
	ext := testPluginExtension(nil)

	// Pre-seed approval for grep only.
	grepKeys := pluginPermissionCommandStorageKeys(ext.Path, ext.Args,
		extensionapi.PermissionExecute, pluginPermissionCommandDetail{
			Path: "/usr/bin/grep",
			Args: []string{"x"},
			Dir:  "/tmp",
		})
	require.Len(t, grepKeys, 1)
	require.NoError(t, storage.Set(context.Background(), grepKeys[0],
		storedPermissionDecision{Decision: pluginPermissionDecisionAllow}))

	opener := &stubPromptOpener{decision: PermissionAllowAlways}
	a := newTestAuthorizerCore(opener, storage)
	ctx := blueauth.ContextWithClaims(context.Background(),
		blueauth.UserClaims[Extension]{Extra: ext})

	// Compound script containing grep and rm: grep is already approved,
	// rm is not, so a prompt is shown.
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash",
		Args: []string{"-c", "grep foo file.txt && rm file.txt"},
		Dir:  "/tmp",
	}))
	assert.Equal(t, 1, opener.calls)

	// After "Yes, All" both commands are approved; a subsequent rm runs
	// silently, and grep's approval was not duplicated.
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/rm", Args: []string{"-rf", "foo"}, Dir: "/tmp",
	}))
	assert.Equal(t, 1, opener.calls)
}

func TestAuthorizerAuthorizeStartCommandOpaqueScriptFallsBackToExactMatch(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	opener := &stubPromptOpener{decision: PermissionAllowAlways}
	a := newTestAuthorizerCore(opener, storage)
	ext := testPluginExtension(nil)
	ctx := blueauth.ContextWithClaims(context.Background(), blueauth.UserClaims[Extension]{Extra: ext})

	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash",
		Args: []string{"-c", "$CMD args"},
		Dir:  "/tmp",
	}))
	// Different opaque script prompts again (no broadening).
	require.NoError(t, a.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash",
		Args: []string{"-c", "$OTHER args"},
		Dir:  "/tmp",
	}))

	assert.Equal(t, 2, opener.calls)
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
