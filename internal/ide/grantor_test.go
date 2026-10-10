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
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
)

type fakePromptOpener struct {
	option  string
	close   bool
	calls   int
	message string
	options []string
}

func (f *fakePromptOpener) Prompt(
	message string, options []string,
	bindings []term.KeyComb,
	promptHandler handler.PromptHandler,
) browser.Window {
	f.calls++
	f.message = message
	f.options = append([]string(nil), options...)
	if f.close {
		if err := promptHandler.OnClose(); err != nil {
			panic(err)
		}
		return browsertest.NopWindow()
	}
	for i, opt := range options {
		if opt == f.option {
			promptHandler.OnSelect(i, opt)
			return browsertest.NopWindow()
		}
	}
	panic("test prompt option was not provided")
}

func TestPermissionGrantor(t *testing.T) {
	t.Parallel()

	entity, err := openpgp.NewEntity("Trusted Publisher", "", "trusted@example.com", nil)
	require.NoError(t, err)
	trustedFingerprint := strings.ToUpper(hex.EncodeToString(entity.PrimaryKey.Fingerprint))
	trust := pkgtrust.NewStoreWithKeyring(openpgp.EntityList{entity})

	cases := []struct {
		name              string
		promptOption      string
		promptClose       bool
		storedDecision    string
		verifiedPublisher string
		wantGrant         bool
		wantPrompt        bool
		wantStored        string
	}{
		{
			name:         "allow once grants without persisting",
			promptOption: permissionPromptAllowOnce,
			wantGrant:    true,
			wantPrompt:   true,
		},
		{
			name:         "allow always grants and persists",
			promptOption: permissionPromptAllowAlways,
			wantGrant:    true,
			wantPrompt:   true,
			wantStored:   permissionDecisionAllow,
		},
		{
			name:         "deny once denies without persisting",
			promptOption: permissionPromptDenyOnce,
			wantPrompt:   true,
		},
		{
			name:           "stored allow bypasses prompt",
			promptOption:   permissionPromptDenyOnce,
			storedDecision: permissionDecisionAllow,
			wantGrant:      true,
		},
		{
			name:         "prompt close denies once",
			promptOption: permissionPromptAllowOnce,
			promptClose:  true,
			wantPrompt:   true,
		},
		{
			name:              "trusted publisher bypasses prompt",
			promptOption:      permissionPromptDenyOnce,
			verifiedPublisher: trustedFingerprint,
			wantGrant:         true,
		},
		{
			name:              "untrusted fingerprint prompts",
			promptOption:      permissionPromptAllowOnce,
			verifiedPublisher: strings.Repeat("AB", 20),
			wantGrant:         true,
			wantPrompt:        true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			grantor, prompt, storage, meta := newTestPermissionGrantor(tc.promptOption)
			grantor.trust = trust
			prompt.close = tc.promptClose

			if tc.storedDecision != "" {
				key := permissionStorageKey(meta)
				require.NoError(t, storage.Set(context.Background(), key,
					permissionDecision{Decision: tc.storedDecision}))
			}

			ok, err := grantor.Grant(meta, tc.verifiedPublisher)
			require.NoError(t, err)

			assert.Equal(t, tc.wantGrant, ok)
			if tc.wantPrompt {
				assert.Equal(t, 1, prompt.calls)
				assert.NotContains(t, prompt.options, "   Never   ")
				assert.Contains(t, prompt.message,
					"Allow extension **Test Extension** (v1.2.3) by **dev-id** to run?")
				assert.Contains(t, prompt.message, "It requests permission to:")
				assert.Contains(t, prompt.message, "- access editor buffers and file events")
				assert.Contains(t, prompt.message, "- use persistent storage")
			} else {
				assert.Zero(t, prompt.calls)
			}
			if tc.wantStored != "" {
				assertStoredPermissionDecision(t, storage, meta, tc.wantStored)
			} else if tc.storedDecision == "" {
				assertNoStoredPermissionDecision(t, storage, meta)
			}
		})
	}
}

func newTestPermissionGrantor(
	option string,
) (*permissionGrantor, *fakePromptOpener, storageapi.Service, extensionapi.Metadata) {
	prompt := &fakePromptOpener{option: option}
	storage := storagestub.NewInMemoryService()
	grantor := &permissionGrantor{
		promptOpener: prompt,
		storage:      storage,
		scheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
	}
	return grantor, prompt, storage, testPermissionMetadata()
}

func testPermissionMetadata() extensionapi.Metadata {
	return extensionapi.Metadata{
		DeveloperID:      "dev-id",
		DeveloperEmail:   "dev@example.com",
		DeveloperKey:     "dev-key",
		ExtensionID:      "test-extension",
		ExtensionName:    "Test Extension",
		ExtensionVersion: "v1.2.3",
		Permissions: extensionapi.NewPermissions(
			extensionapi.PermissionEditor,
			extensionapi.PermissionStorage,
		),
	}
}

func assertStoredPermissionDecision(
	t *testing.T, storage storageapi.Service, meta extensionapi.Metadata, expected string,
) {
	t.Helper()

	key := permissionStorageKey(meta)
	var stored permissionDecision
	require.NoError(t, storage.Get(context.Background(), key, &stored))
	assert.Equal(t, expected, stored.Decision)
}

func assertNoStoredPermissionDecision(
	t *testing.T, storage storageapi.Service, meta extensionapi.Metadata,
) {
	t.Helper()

	key := permissionStorageKey(meta)
	var stored permissionDecision
	err := storage.Get(context.Background(), key, &stored)
	assert.ErrorIs(t, err, storageapi.ErrNotFound)
}

var _ ideauthorizer.PromptOpener = (*fakePromptOpener)(nil)
