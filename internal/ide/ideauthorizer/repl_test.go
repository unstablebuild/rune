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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	blueauth "github.com/unstablebuild/blue/auth"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/extension/extensionv2/peerprocess"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
)

type capturingEditor struct {
	*texttest.TestEditor
	manual  textapi.CommandManual
	handler textapi.REPLHandler
	calls   int
}

func newCapturingEditor() *capturingEditor {
	return &capturingEditor{TestEditor: texttest.NopEditor()}
}

func (e *capturingEditor) RegisterREPLCommand(
	manual textapi.CommandManual, handler textapi.REPLHandler,
) error {
	e.calls++
	e.manual = manual
	e.handler = handler
	return nil
}

func TestAuthorizerRegistersRevokeREPLCommand(t *testing.T) {
	t.Parallel()

	editor := newCapturingEditor()
	_, err := NewAuthorizer(editor, nil, storagestub.NewInMemoryService(),
		syncScheduleNextTick, &capturingStatus{}, pkgtrust.NewStore(), newFakeResolver(), Config{})
	require.NoError(t, err)

	require.Equal(t, 1, editor.calls)
	assert.Equal(t, authorizerREPLCommand, editor.manual.Name)
	require.Len(t, editor.manual.Commands, 3)
	assert.Equal(t, authorizerREPLCommandList, editor.manual.Commands[0].Name)
	assert.Equal(t, authorizerREPLCommandRevoke, editor.manual.Commands[1].Name)
	assert.Equal(t, authorizerREPLCommandExecutables, editor.manual.Commands[2].Name)
	assert.NotNil(t, editor.handler)
}

func TestAuthorizerRequiresEditor(t *testing.T) {
	t.Parallel()

	_, err := NewAuthorizer(nil, nil, storagestub.NewInMemoryService(),
		syncScheduleNextTick, &capturingStatus{}, pkgtrust.NewStore(), newFakeResolver(), Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "editor is required")
}

func TestAuthorizerRequiresResolver(t *testing.T) {
	t.Parallel()

	_, err := NewAuthorizer(newCapturingEditor(), nil, storagestub.NewInMemoryService(),
		syncScheduleNextTick, &capturingStatus{}, pkgtrust.NewStore(), nil, Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "executable resolver is required")
}

func TestAuthorizerRequiresStatus(t *testing.T) {
	t.Parallel()

	_, err := NewAuthorizer(newCapturingEditor(), nil, storagestub.NewInMemoryService(),
		syncScheduleNextTick, nil, pkgtrust.NewStore(), newFakeResolver(), Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status reporter is required")
}

func TestAuthorizerListREPLListsPersistedDecisions(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	ext := testPluginExtension(nil)
	authorizer := newTestAuthorizerCore(nil, storage)
	require.NoError(t, authorizer.setStoredDecision(context.Background(),
		pluginPermissionProgramStorageKey(ext.Path,
			extensionapi.PermissionBrowserWindowManager),
		pluginPermissionIdentity{Path: ext.Path, Args: ext.Args},
		extensionapi.PermissionBrowserWindowManager, pluginPermissionDecisionAllow))
	handler := authorizerREPLHandler{authorizer: authorizer}

	it, err := handler.HandleCommand(context.Background(), repl.Command{
		Name: authorizerREPLCommand,
		Args: []string{authorizerREPLCommandList},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)

	require.Len(t, items, 2)
	out := responsiveStrings(t, items)
	assert.Contains(t, out[0], "Extension permission decisions")
	assert.Contains(t, out[1], "persisted:allow:")
	assert.Contains(t, out[1], "persisted")
	assert.Contains(t, out[1], pluginPermissionDecisionAllow)
	assert.Contains(t, out[1], string(extensionapi.PermissionBrowserWindowManager))
	assert.Contains(t, out[1], ext.Path)
}

func TestAuthorizerListREPLListsTransientOnceDecisions(t *testing.T) {
	t.Parallel()

	prompter := &stubPromptOpener{decision: PermissionAllowOnce}
	authorizer := newTestAuthorizerCore(prompter, storagestub.NewInMemoryService())
	ext := testPluginExtension(nil)
	ctx := contextWithPeerProcess(context.Background(), peerprocess.Process{
		PID:  123,
		UID:  501,
		Exe:  "/Users/test/.rune/bin/runectl",
		Argv: []string{"runectl", "lsp", "hover", "iterator"},
	})
	require.NoError(t, authorizer.authorizePlugin(ctx, ext,
		extensionapi.PermissionLSP, "/semantic.Semantic/Hover"))

	it, err := authorizerREPLHandler{authorizer: authorizer}.HandleCommand(
		context.Background(), repl.Command{
			Name: authorizerREPLCommand,
			Args: []string{authorizerREPLCommandList},
		}, repl.NopProgressWriter())
	require.NoError(t, err)
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)

	require.Len(t, items, 2)
	out := responsiveStrings(t, items)
	assert.Contains(t, out[0], "Extension permission decisions")
	assert.Contains(t, out[1], "transient:allow:")
	assert.Contains(t, out[1], "transient")
	assert.Contains(t, out[1], pluginPermissionDecisionAllow)
	assert.Contains(t, out[1], string(extensionapi.PermissionLSP))
	assert.Contains(t, out[1], "/Users/test/.rune/bin/runectl")
	assert.Contains(t, out[1], "[lsp hover iterator]")
}

func TestAuthorizerListREPLHandlesEmptyDecisions(t *testing.T) {
	t.Parallel()

	handler := authorizerREPLHandler{
		authorizer: newTestAuthorizerCore(nil, storagestub.NewInMemoryService()),
	}
	it, err := handler.HandleCommand(context.Background(), repl.Command{
		Name: authorizerREPLCommand,
		Args: []string{authorizerREPLCommandList},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)

	require.Len(t, items, 1)
	assert.Contains(t, responsiveStrings(t, items)[0], "No extension")
}

func TestAuthorizerREPLCompletesSubcommands(t *testing.T) {
	t.Parallel()

	handler := authorizerREPLHandler{
		authorizer: newTestAuthorizerCore(nil, storagestub.NewInMemoryService()),
	}
	it, err := handler.Complete(context.Background(), authorizerREPLCommand, nil)
	require.NoError(t, err)
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{
		authorizerREPLCommandList, authorizerREPLCommandRevoke, authorizerREPLCommandExecutables,
	}, items)

	it, err = handler.Complete(context.Background(), authorizerREPLCommand, []string{"l"})
	require.NoError(t, err)
	items, err = sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{authorizerREPLCommandList}, items)
}

func TestAuthorizerRevokeREPLCompletesPersistedDecisions(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	ext := testPluginExtension(nil)
	authorizer := newTestAuthorizerCore(nil, storage)
	require.NoError(t, authorizer.setStoredDecision(context.Background(),
		pluginPermissionProgramStorageKey(ext.Path,
			extensionapi.PermissionBrowserWindowManager),
		pluginPermissionIdentity{Path: ext.Path, Args: ext.Args},
		extensionapi.PermissionBrowserWindowManager, pluginPermissionDecisionAllow))
	require.NoError(t, storage.Set(context.Background(), "unrelated",
		storedPermissionDecision{Key: "unrelated", Decision: pluginPermissionDecisionAllow}))
	handler := authorizerREPLHandler{authorizer: authorizer}

	it, err := handler.Complete(context.Background(), authorizerREPLCommand,
		[]string{authorizerREPLCommandRevoke, "allow"})
	require.NoError(t, err)
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)

	require.Len(t, items, 1)
	assert.NotContains(t, items[0], " ")
	assert.True(t, strings.HasPrefix(items[0], "persisted:allow:"))
	assert.Contains(t, items[0], string(extensionapi.PermissionBrowserWindowManager))
}

func TestAuthorizerRevokeREPLDeletesPersistedDecisionByID(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	ext := testPluginExtension(nil)
	authorizer := newTestAuthorizerCore(nil, storage)
	key := pluginPermissionProgramStorageKey(ext.Path,
		extensionapi.PermissionBrowserWindowManager)
	require.NoError(t, authorizer.setStoredDecision(context.Background(), key,
		pluginPermissionIdentity{Path: ext.Path, Args: ext.Args},
		extensionapi.PermissionBrowserWindowManager, pluginPermissionDecisionDeny))
	entries, err := authorizer.storedDecisions(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)

	_, err = authorizerREPLHandler{authorizer: authorizer}.HandleCommand(
		context.Background(), repl.Command{
			Name: authorizerREPLCommand,
			Args: []string{authorizerREPLCommandRevoke, entries[0].ID()},
		}, repl.NopProgressWriter())
	require.NoError(t, err)

	var stored storedPermissionDecision
	err = storage.Get(context.Background(), key, &stored)
	assert.ErrorIs(t, err, storageapi.ErrNotFound)
}

func TestAuthorizerRevokeREPLDeletesPersistedDecision(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	ext := testPluginExtension(nil)
	authorizer := newTestAuthorizerCore(nil, storage)
	key := pluginPermissionProgramStorageKey(ext.Path,
		extensionapi.PermissionBrowserWindowManager)
	require.NoError(t, authorizer.setStoredDecision(context.Background(), key,
		pluginPermissionIdentity{Path: ext.Path, Args: ext.Args},
		extensionapi.PermissionBrowserWindowManager, pluginPermissionDecisionDeny))
	entries, err := authorizer.storedDecisions(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)

	handler := authorizerREPLHandler{authorizer: authorizer}
	_, err = handler.HandleCommand(context.Background(), repl.Command{
		Name: authorizerREPLCommand,
		Args: []string{authorizerREPLCommandRevoke, entries[0].Display()},
	}, repl.NopProgressWriter())
	require.NoError(t, err)

	var stored storedPermissionDecision
	err = storage.Get(context.Background(), key, &stored)
	assert.ErrorIs(t, err, storageapi.ErrNotFound)
}

func TestAuthorizerRevokeREPLResurrectsNeverDecision(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	ext := testPluginExtension(nil)
	authorizer := newTestAuthorizerCore(&stubPromptOpener{
		decision: PermissionDenyAlways,
	}, storage)
	err := authorizer.authorizePlugin(context.Background(), ext,
		extensionapi.PermissionBrowserWindowManager, testWindowManagerResource)
	require.ErrorIs(t, err, blueauth.ErrForbidden)
	err = authorizer.authorizePlugin(context.Background(), ext,
		extensionapi.PermissionBrowserWindowManager, testWindowManagerResource)
	require.ErrorIs(t, err, blueauth.ErrForbidden)

	entries, err := authorizer.storedDecisions(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	_, err = authorizerREPLHandler{authorizer: authorizer}.HandleCommand(
		context.Background(), repl.Command{
			Name: authorizerREPLCommand,
			Args: []string{authorizerREPLCommandRevoke, entries[0].Display()},
		}, repl.NopProgressWriter())
	require.NoError(t, err)

	prompter := &stubPromptOpener{decision: PermissionAllowOnce}
	afterRevoke := newTestAuthorizerCore(prompter, storage)
	err = afterRevoke.authorizePlugin(context.Background(), ext,
		extensionapi.PermissionBrowserWindowManager, testWindowManagerResource)
	require.NoError(t, err)
	assert.Equal(t, 1, prompter.calls)
}

func TestAuthorizerListAndRevokeREPLDistinguishCommandScopedDecisions(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	ext := testPluginExtension(nil)
	opener := &stubPromptOpener{
		decision: PermissionAllowAlways,
		picks:    []string{"grep *", "rm *"},
	}
	authorizer := newTestAuthorizerCore(opener, storage)
	ctx := blueauth.ContextWithClaims(context.Background(),
		blueauth.UserClaims[Extension]{Extra: ext})
	require.NoError(t, authorizer.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash", Args: []string{"-c", "grep foo f && rm -rf foo"}, Dir: "/tmp",
	}))

	entries, err := authorizer.storedDecisions(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.NotEqual(t, entries[0].ID(), entries[1].ID(),
		"command-scoped decisions for the same plugin/permission must have distinct IDs")
	assert.Contains(t, entries[0].ID(), ":grep:")
	assert.Contains(t, entries[1].ID(), ":rm:")

	handler := authorizerREPLHandler{authorizer: authorizer}
	out, err := runAuthorizerREPL(t, handler, authorizerREPLCommandList)
	require.NoError(t, err)
	require.Len(t, out, 3)
	assert.Contains(t, out[1], entries[0].ID())
	assert.Contains(t, out[2], entries[1].ID())
	assert.Contains(t, entries[0].Display(), "cmd grep * ← /usr/bin/grep sha256 "+
		fingerprintPreview(sha256Hex("/usr/bin/grep")))
	assert.Contains(t, entries[1].Display(), "cmd rm * ← /usr/bin/rm sha256 ")

	// A scope label revokes the one decision it names.
	out, err = runAuthorizerREPL(t, handler, authorizerREPLCommandRevoke, "grep", "*")
	require.NoError(t, err)
	assert.Contains(t, out[0], "Revoked")
	entries, err = authorizer.storedDecisions(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "rm *", entries[0].CommandScope)

	opener.decision = PermissionDenyOnce
	require.ErrorIs(t, authorizer.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "grep", Args: []string{"x"}, Dir: "/tmp",
	}), blueauth.ErrForbidden)
	require.NoError(t, authorizer.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "rm", Args: []string{"x"}, Dir: "/tmp",
	}))
	assert.Equal(t, 2, opener.calls)
}

func TestAuthorizerRevokeREPLRejectsAmbiguousScope(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	authorizer := newTestAuthorizerCore(&stubPromptOpener{
		decision: PermissionAllowAlways,
		picks:    []string{"git *"},
	}, storage)
	for _, ext := range []Extension{
		testPluginExtension(nil),
		testRegularExtension(extensionapi.NewPermissions(extensionapi.PermissionExecute)),
	} {
		ctx := blueauth.ContextWithClaims(context.Background(),
			blueauth.UserClaims[Extension]{Extra: ext})
		require.NoError(t, authorizer.AuthorizeCommand(ctx, workspaceapi.Cmd{
			Path: "git", Args: []string{"status"},
		}))
	}

	_, err := runAuthorizerREPL(t, authorizerREPLHandler{authorizer: authorizer},
		authorizerREPLCommandRevoke, "git", "*")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2 decisions cover git *")
	assert.Len(t, storedCommandDecisions(t, storage), 2)
}

func TestAuthorizerExecutablesREPLListsApprovedExecutables(t *testing.T) {
	t.Parallel()

	handler := authorizerREPLHandler{
		authorizer: newTestAuthorizerCore(nil, storagestub.NewInMemoryService()),
	}
	out, err := runAuthorizerREPL(t, handler, authorizerREPLCommandExecutables)
	require.NoError(t, err)
	assert.Equal(t, []string{"No approved executables."}, trimLines(out))

	storage := storagestub.NewInMemoryService()
	authorizer := newTestAuthorizerCore(&stubPromptOpener{
		decision: PermissionAllowAlways,
	}, storage)
	ctx := blueauth.ContextWithClaims(context.Background(),
		blueauth.UserClaims[Extension]{Extra: testPluginExtension(nil)})
	require.NoError(t, authorizer.AuthorizeCommand(ctx, workspaceapi.Cmd{
		Path: "/bin/bash", Args: []string{"-c", "git status"},
	}))

	out, err = runAuthorizerREPL(t, authorizerREPLHandler{authorizer: authorizer},
		authorizerREPLCommandExecutables)
	require.NoError(t, err)
	require.Len(t, out, 3)
	assert.Contains(t, out[0], "Approved executables:")
	assert.Contains(t, out[1], "- /bin/bash sha256 "+fingerprintPreview(sha256Hex("/bin/bash")))
	assert.Contains(t, out[2], "- /usr/bin/git sha256 "+fingerprintPreview(sha256Hex("/usr/bin/git")))
}

func trimLines(lines []string) []string {
	ret := make([]string, len(lines))
	for i, l := range lines {
		ret[i] = strings.TrimSpace(l)
	}
	return ret
}

type fakeWorkspaceConfigTrust struct {
	decisions []WorkspaceConfigDecision
	revoked   []WorkspaceConfigDecision
}

func (f *fakeWorkspaceConfigTrust) WorkspaceConfigDecisions(
	context.Context,
) ([]WorkspaceConfigDecision, error) {
	return f.decisions, nil
}

func (f *fakeWorkspaceConfigTrust) RevokeWorkspaceConfigDecision(
	_ context.Context, d WorkspaceConfigDecision,
) error {
	f.revoked = append(f.revoked, d)
	return nil
}

func newWorkspaceConfigREPLHandler(
	t *testing.T, decisions ...WorkspaceConfigDecision,
) (authorizerREPLHandler, *fakeWorkspaceConfigTrust) {
	t.Helper()
	trust := &fakeWorkspaceConfigTrust{decisions: decisions}
	authorizer := newTestAuthorizerCore(nil, storagestub.NewInMemoryService())
	authorizer.config.WorkspaceConfigs = trust
	return authorizerREPLHandler{authorizer: authorizer}, trust
}

func testWorkspaceConfigDecision(
	t *testing.T, workspace, fingerprint string, trusted, persisted bool,
) WorkspaceConfigDecision {
	t.Helper()
	uri, err := workspaceapi.ParseURI(workspace)
	require.NoError(t, err)
	return WorkspaceConfigDecision{
		Workspace:   uri,
		Fingerprint: fingerprint,
		Trusted:     trusted,
		Persisted:   persisted,
	}
}

func runAuthorizerREPL(
	t *testing.T, handler authorizerREPLHandler, args ...string,
) ([]string, error) {
	t.Helper()
	it, err := handler.HandleCommand(context.Background(), repl.Command{
		Name: authorizerREPLCommand,
		Args: args,
	}, repl.NopProgressWriter())
	if err != nil {
		return nil, err
	}
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	return responsiveStrings(t, items), nil
}

func TestAuthorizerListREPLListsWorkspaceConfigDecisions(t *testing.T) {
	t.Parallel()

	const fingerprint = "0123456789abcdef"
	tests := []struct {
		name      string
		decisions []WorkspaceConfigDecision
		want      []string
	}{
		{
			name: "none",
			want: []string{"No extension permission decisions.", "No workspace config decisions."},
		},
		{
			name: "trusted and ignored",
			decisions: []WorkspaceConfigDecision{
				testWorkspaceConfigDecision(t, "file:///home/u/repo", fingerprint, true, true),
				testWorkspaceConfigDecision(t, "file:///home/u/other", fingerprint, false, false),
			},
			want: []string{
				"No extension permission decisions.",
				"Workspace config decisions:",
				"persisted trust file:///home/u/repo sha256:01234567",
				"transient ignore file:///home/u/other sha256:01234567",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, _ := newWorkspaceConfigREPLHandler(t, tt.decisions...)
			out, err := runAuthorizerREPL(t, handler, authorizerREPLCommandList)
			require.NoError(t, err)
			require.Len(t, out, len(tt.want))
			for i, want := range tt.want {
				assert.Contains(t, out[i], want)
			}
			for i, d := range tt.decisions {
				assert.Contains(t, out[2+i], workspaceConfigDecisionID(d))
			}
		})
	}
}

func TestAuthorizerWorkspaceConfigDecisionIDs(t *testing.T) {
	t.Parallel()

	const fingerprint = "0123456789abcdef"
	decisions := []WorkspaceConfigDecision{
		testWorkspaceConfigDecision(t, "file:///a/repo", fingerprint, true, true),
		testWorkspaceConfigDecision(t, "file:///b/repo", fingerprint, true, true),
		testWorkspaceConfigDecision(t, "file:///a/repo", "fedcba9876543210", true, false),
		testWorkspaceConfigDecision(t, "file:///a/repo", fingerprint, false, false),
	}
	seen := make(map[string]bool)
	for _, d := range decisions {
		id := workspaceConfigDecisionID(d)
		assert.NotContains(t, id, " ")
		assert.Contains(t, id, ":workspace-config:repo:")
		assert.False(t, seen[id], "decision IDs must be unique: %s", id)
		seen[id] = true
	}
}

func TestAuthorizerRevokeREPLRevokesWorkspaceConfigDecision(t *testing.T) {
	t.Parallel()

	trusted := testWorkspaceConfigDecision(t, "file:///home/u/repo", "0123456789abcdef", true, true)
	ignored := testWorkspaceConfigDecision(t, "file:///home/u/other", "fedcba9876543210", false, false)
	tests := []struct {
		name        string
		selected    string
		wantRevoked []WorkspaceConfigDecision
		wantErr     bool
	}{
		{
			name:        "persisted trust by ID",
			selected:    workspaceConfigDecisionID(trusted),
			wantRevoked: []WorkspaceConfigDecision{trusted},
		},
		{
			name:        "transient ignore by display",
			selected:    workspaceConfigDecisionDisplay(ignored),
			wantRevoked: []WorkspaceConfigDecision{ignored},
		},
		{
			name:     "unknown",
			selected: "persisted:trust:workspace-config:repo:00000000",
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, fake := newWorkspaceConfigREPLHandler(t, trusted, ignored)
			out, err := runAuthorizerREPL(t, handler,
				append([]string{authorizerREPLCommandRevoke}, strings.Fields(tt.selected)...)...)
			if tt.wantErr {
				require.Error(t, err)
				assert.Empty(t, fake.revoked)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantRevoked, fake.revoked)
			require.Len(t, out, 1)
			assert.Contains(t, out[0], "Revoked")
			assert.Contains(t, out[0], "asks again the next time the workspace opens")
		})
	}
}

func TestAuthorizerRevokeREPLCompletesWorkspaceConfigDecisions(t *testing.T) {
	t.Parallel()

	trusted := testWorkspaceConfigDecision(t, "file:///home/u/repo", "0123456789abcdef", true, true)
	ignored := testWorkspaceConfigDecision(t, "file:///home/u/other", "fedcba9876543210", false, false)
	tests := []struct {
		prefix string
		want   []string
	}{
		{prefix: "", want: []string{workspaceConfigDecisionID(trusted), workspaceConfigDecisionID(ignored)}},
		{prefix: "persisted", want: []string{workspaceConfigDecisionID(trusted)}},
		{prefix: "ignore", want: []string{workspaceConfigDecisionID(ignored)}},
		{prefix: "workspace-config", want: []string{
			workspaceConfigDecisionID(trusted), workspaceConfigDecisionID(ignored),
		}},
		{prefix: "allow", want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			handler, _ := newWorkspaceConfigREPLHandler(t, trusted, ignored)
			it, err := handler.Complete(context.Background(), authorizerREPLCommand,
				[]string{authorizerREPLCommandRevoke, tt.prefix})
			require.NoError(t, err)
			items, err := sdkiterator.ToSlice(context.Background(), it)
			require.NoError(t, err)
			assert.Equal(t, tt.want, items)
		})
	}
}

func responsiveStrings(t *testing.T, items []component.Responsive) []string {
	t.Helper()
	out := make([]string, 0, len(items))
	for _, item := range items {
		width := 120
		height := item.Height(width)
		if height <= 0 {
			height = 1
		}
		writer := term.NewStringWriter(width, height)
		item.Resize(width, height)
		item.Draw(writer)
		require.NoError(t, writer.Flush())
		out = append(out, writer.String())
	}
	return out
}

var _ text.Editor = (*capturingEditor)(nil)
