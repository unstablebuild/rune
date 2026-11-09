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
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/auth"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/ide/ideauthorizer"
)

func TestProtocolGrantorGatesRunButKeepsRequestedPermissions(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	verifyKeys, err := keys.Verify(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, verifyKeys)
	meta := extensionapi.Metadata{
		DeveloperID:    "dev-id",
		DeveloperEmail: "dev@example.com",
		DeveloperKey:   "dev-key",
		ExtensionID:    "ext-id",
		ExtensionName:  "Test Extension",
		Permissions: extensionapi.NewPermissions(
			extensionapi.PermissionLSP,
			extensionapi.PermissionStorage,
		),
	}
	p := newProtocol(context.Background(), extension.GrantAll(),
		"ext-id", "/tmp/rune.sock", "/tmp/rune-data", "/tmp/rune-install",
		[]byte("cert"),
		false, config.MapConfig(map[string]any{}), keys, nil, "")
	encoded, err := json.Marshal(meta)
	require.NoError(t, err)

	_, err = p.Write(encoded)
	require.NoError(t, err)
	var cfg extensionapi.Config
	data, err := io.ReadAll(p)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &cfg))
	require.NotNil(t, cfg.Token)
	assert.Equal(t, "/tmp/rune-install", cfg.InstallDir,
		"handshake carries the install dir distinct from the data dir")
	assert.Equal(t, "/tmp/rune-data", cfg.DataDir)
	claims, err := auth.VerifyToken[ideauthorizer.Extension](verifyKeys[0], cfg.Token.AccessToken)
	require.NoError(t, err)

	assert.False(t, claims.Extra.Plugin)
	assert.Equal(t, meta.Permissions, claims.Extra.Permissions)
}

func TestProtocolCloseEndsReadWithoutHandshake(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	p := newProtocol(context.Background(), extension.GrantAll(),
		"ext-id", "/tmp/rune.sock", "/tmp/rune-data", "/tmp/rune-install",
		[]byte("cert"), false, config.MapConfig(map[string]any{}), keys, nil, "")
	read := make(chan error, 1)
	go func() {
		_, err := p.Read(make([]byte, 1))
		read <- err
	}()
	select {
	case err := <-read:
		t.Fatalf("read returned before the extension answered: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	require.NoError(t, p.Close())
	select {
	case err := <-read:
		assert.ErrorIs(t, err, io.EOF)
	case <-time.After(time.Second):
		t.Fatal("closing the protocol must end its read")
	}
	require.NoError(t, p.Close())
}

func TestProtocolCarriesVerifiedPublisherOnlyWhenDeveloperKeyMatches(t *testing.T) {
	t.Parallel()

	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	verifyKeys, err := keys.Verify(context.Background())
	require.NoError(t, err)
	meta := extensionapi.Metadata{
		DeveloperID: "dev-id", DeveloperEmail: "dev@example.com",
		DeveloperKey: "064D4ABCFA6D9338", ExtensionID: "ext-id",
		ExtensionName: "Test Extension", Permissions: extensionapi.NewPermissions(extensionapi.PermissionStorage),
	}
	trustedFingerprint := "D3F9E65DE72888CC03D45CF5064D4ABCFA6D9338"
	p := newProtocol(context.Background(), extension.GrantAll(),
		"ext-id", "/tmp/rune.sock", "/tmp/rune-data", "/tmp/rune-install",
		[]byte("cert"), false, config.MapConfig(map[string]any{}), keys, nil, trustedFingerprint)
	encoded, err := json.Marshal(meta)
	require.NoError(t, err)
	_, err = p.Write(encoded)
	require.NoError(t, err)
	data, err := io.ReadAll(p)
	require.NoError(t, err)
	var cfg extensionapi.Config
	require.NoError(t, json.Unmarshal(data, &cfg))
	claims, err := auth.VerifyToken[ideauthorizer.Extension](verifyKeys[0], cfg.Token.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, trustedFingerprint, claims.Extra.VerifiedPublisher)

	meta.DeveloperKey = "different"
	p = newProtocol(context.Background(), extension.GrantAll(),
		"ext-id", "/tmp/rune.sock", "/tmp/rune-data", "/tmp/rune-install",
		[]byte("cert"), false, config.MapConfig(map[string]any{}), keys, nil, trustedFingerprint)
	encoded, err = json.Marshal(meta)
	require.NoError(t, err)
	_, err = p.Write(encoded)
	require.NoError(t, err)
	data, err = io.ReadAll(p)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &cfg))
	claims, err = auth.VerifyToken[ideauthorizer.Extension](verifyKeys[0], cfg.Token.AccessToken)
	require.NoError(t, err)
	assert.Empty(t, claims.Extra.VerifiedPublisher)
}
