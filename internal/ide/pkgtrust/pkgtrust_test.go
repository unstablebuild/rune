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

package pkgtrust

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/release"
)

const embeddedFingerprint = "D3F9E65DE72888CC03D45CF5064D4ABCFA6D9338"

func TestVerifyBundle(t *testing.T) {
	entity, err := openpgp.NewEntity("Test Publisher", "", "test@example.com", nil)
	require.NoError(t, err)
	store := NewStoreWithKeyring(openpgp.EntityList{entity})

	bundle := signedTestBundle(t, entity, []byte("bundle contents"))
	provenance, err := store.VerifyBundle(bundle, bytes.NewReader([]byte("bundle contents")))
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%016X", entity.PrimaryKey.KeyId), provenance.KeyID)
	require.NotEmpty(t, provenance.Fingerprint)
	require.Equal(t, "Test Publisher <test@example.com>", provenance.PrimaryIdentity)

	t.Run("rejects unknown signing key", func(t *testing.T) {
		bundle.Metadata[MetadataSigningKeyID] = "0123456789ABCDEF"
		_, err := store.VerifyBundle(bundle, bytes.NewReader([]byte("bundle contents")))
		require.Error(t, err)
	})

	t.Run("rejects tampered bundle", func(t *testing.T) {
		bundle = signedTestBundle(t, entity, []byte("bundle contents"))
		_, err := store.VerifyBundle(bundle, bytes.NewReader([]byte("tampered contents")))
		require.Error(t, err)
	})
}

func TestVerifyEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "extension")
	require.NoError(t, os.WriteFile(path, []byte("trusted"), 0755))
	info, err := os.Lstat(path)
	require.NoError(t, err)
	sum := sha256.Sum256([]byte("trusted"))
	entries := []Entry{{
		Path:   "extension",
		SHA256: hex.EncodeToString(sum[:]),
		Mode:   info.Mode(),
	}}

	require.NoError(t, VerifyEntries(dir, entries))
	require.NoError(t, os.WriteFile(path, []byte("tampered"), 0755))
	require.Error(t, VerifyEntries(dir, entries))
	require.NoError(t, os.Remove(path))
	require.Error(t, VerifyEntries(dir, entries))
}

func TestKeyringFetchReplaces(t *testing.T) {
	entity, err := openpgp.NewEntity("Rotated Publisher", "", "rotated@example.com", nil)
	require.NoError(t, err)
	fingerprint := strings.ToUpper(hex.EncodeToString(entity.PrimaryKey.Fingerprint))
	armored := armoredPublicKey(t, entity)

	store := NewStore(t.TempDir(), func() ([]byte, error) { return armored, nil })

	bundle := signedTestBundle(t, entity, []byte("bundle contents"))
	provenance, err := store.VerifyBundle(bundle, bytes.NewReader([]byte("bundle contents")))
	require.NoError(t, err)
	require.Equal(t, fingerprint, provenance.Fingerprint)

	// replace semantics: the fetched ring revokes embedded trust anchors
	require.True(t, store.IsTrustedFingerprint(fingerprint))
	require.False(t, store.IsTrustedFingerprint(embeddedFingerprint))
}

func TestKeyringFetchFailureKeepsEmbedded(t *testing.T) {
	store := NewStore(t.TempDir(), func() ([]byte, error) {
		return nil, fmt.Errorf("network down")
	})
	// VerifyBundle waits for the fetch to finish (here it fails), then
	// falls back to the embedded ring.
	require.True(t, store.IsTrustedFingerprint(embeddedFingerprint))
}

func TestKeyringFetchMalformedKeepsEmbedded(t *testing.T) {
	store := NewStore(t.TempDir(), func() ([]byte, error) { return []byte("junk"), nil })
	store.awaitFetch()
	require.True(t, store.IsTrustedFingerprint(embeddedFingerprint))
}

func TestKeyringCache(t *testing.T) {
	dataDir := t.TempDir()
	entity, err := openpgp.NewEntity("Cached Publisher", "", "cached@example.com", nil)
	require.NoError(t, err)
	fingerprint := strings.ToUpper(hex.EncodeToString(entity.PrimaryKey.Fingerprint))
	armored := armoredPublicKey(t, entity)

	store := NewStore(dataDir, func() ([]byte, error) { return armored, nil })
	store.awaitFetch()
	require.True(t, store.IsTrustedFingerprint(fingerprint))
	_, err = os.Stat(filepath.Join(dataDir, "trusted_keys.asc"))
	require.NoError(t, err)

	// simulate a restart: a new store over the same dataDir loads the
	// cached ring, which replaces the embedded one, even without a fetch.
	restarted := NewStore(dataDir, nil)
	require.True(t, restarted.IsTrustedFingerprint(fingerprint))
	require.False(t, restarted.IsTrustedFingerprint(embeddedFingerprint))

	t.Run("missing cache file keeps embedded ring", func(t *testing.T) {
		fresh := NewStore(t.TempDir(), nil)
		require.True(t, fresh.IsTrustedFingerprint(embeddedFingerprint))
	})

	t.Run("corrupt cache file falls back to embedded ring", func(t *testing.T) {
		corruptDir := t.TempDir()
		require.NoError(t, os.WriteFile(
			filepath.Join(corruptDir, "trusted_keys.asc"), []byte("junk"), 0644))
		fresh := NewStore(corruptDir, nil)
		require.True(t, fresh.IsTrustedFingerprint(embeddedFingerprint))
	})
}

func armoredPublicKey(t *testing.T, entities ...*openpgp.Entity) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	require.NoError(t, err)
	for _, entity := range entities {
		require.NoError(t, entity.Serialize(w))
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func signedTestBundle(t *testing.T, entity *openpgp.Entity, contents []byte) release.Bundle {
	t.Helper()
	var signature bytes.Buffer
	require.NoError(t, openpgp.ArmoredDetachSign(&signature, entity, bytes.NewReader(contents), nil))
	return release.Bundle{Metadata: map[string]string{
		MetadataSigningKeyID: fmt.Sprintf("%016X", entity.PrimaryKey.KeyId),
		MetadataSignature:    signature.String(),
	}}
}
