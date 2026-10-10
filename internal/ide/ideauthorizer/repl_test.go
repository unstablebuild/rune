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
		syncScheduleNextTick, nil, pkgtrust.NewStore(t.TempDir(), nil), Config{})
	require.NoError(t, err)

	require.Equal(t, 1, editor.calls)
	assert.Equal(t, authorizerREPLCommand, editor.manual.Name)
	require.Len(t, editor.manual.Commands, 2)
	assert.Equal(t, authorizerREPLCommandList, editor.manual.Commands[0].Name)
	assert.Equal(t, authorizerREPLCommandRevoke, editor.manual.Commands[1].Name)
	assert.NotNil(t, editor.handler)
}

func TestAuthorizerRequiresEditor(t *testing.T) {
	t.Parallel()

	_, err := NewAuthorizer(nil, nil, storagestub.NewInMemoryService(),
		syncScheduleNextTick, nil, pkgtrust.NewStore(t.TempDir(), nil), Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "editor is required")
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
		extensionapi.PermissionBrowserWindowManager, nil, pluginPermissionDecisionAllow))
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
	assert.Equal(t, []string{authorizerREPLCommandList, authorizerREPLCommandRevoke}, items)

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
		extensionapi.PermissionBrowserWindowManager, nil, pluginPermissionDecisionAllow))
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
		extensionapi.PermissionBrowserWindowManager, nil, pluginPermissionDecisionDeny))
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
		extensionapi.PermissionBrowserWindowManager, nil, pluginPermissionDecisionDeny))
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
	authorizer := newTestAuthorizerCore(nil, storage)

	identity := pluginPermissionIdentity{Path: ext.Path, Args: ext.Args}
	grep := pluginPermissionCommandDetail{
		Path: "/usr/bin/grep", Args: []string{"foo"}, Dir: "/tmp",
	}
	rm := pluginPermissionCommandDetail{
		Path: "/bin/rm", Args: []string{"-rf", "foo"}, Dir: "/tmp",
	}
	grepKeys := pluginPermissionCommandStorageKeys(ext.Path, ext.Args,
		extensionapi.PermissionExecute, grep)
	rmKeys := pluginPermissionCommandStorageKeys(ext.Path, ext.Args,
		extensionapi.PermissionExecute, rm)
	require.Len(t, grepKeys, 1)
	require.Len(t, rmKeys, 1)
	require.NotEqual(t, grepKeys[0], rmKeys[0])

	require.NoError(t, authorizer.setStoredDecision(context.Background(),
		grepKeys[0], identity, extensionapi.PermissionExecute, &grep,
		pluginPermissionDecisionAllow))
	require.NoError(t, authorizer.setStoredDecision(context.Background(),
		rmKeys[0], identity, extensionapi.PermissionExecute, &rm,
		pluginPermissionDecisionAllow))

	entries, err := authorizer.storedDecisions(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.NotEqual(t, entries[0].ID(), entries[1].ID(),
		"command-scoped decisions for the same plugin/permission must have distinct IDs")
	assert.NotEqual(t, entries[0].Display(), entries[1].Display(),
		"command-scoped decisions for the same plugin/permission must have distinct displays")

	// Locate the grep entry by its key and revoke it by ID.
	var grepEntry pluginPermissionStoredDecisionEntry
	for _, entry := range entries {
		if entry.Key == grepKeys[0] {
			grepEntry = entry
			break
		}
	}
	require.NotEmpty(t, grepEntry.Key)

	_, err = authorizerREPLHandler{authorizer: authorizer}.HandleCommand(
		context.Background(), repl.Command{
			Name: authorizerREPLCommand,
			Args: []string{authorizerREPLCommandRevoke, grepEntry.ID()},
		}, repl.NopProgressWriter())
	require.NoError(t, err)

	var stored storedPermissionDecision
	err = storage.Get(context.Background(), grepKeys[0], &stored)
	assert.ErrorIs(t, err, storageapi.ErrNotFound,
		"revoking by ID must delete exactly the grep decision")
	require.NoError(t, storage.Get(context.Background(), rmKeys[0], &stored),
		"revoking by ID must leave the rm decision intact")
	assert.Equal(t, pluginPermissionDecisionAllow, stored.Decision)
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
