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
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/release"
)

// The two independent signers baked into the release ring. Either one
// alone can sign a release, so losing one key does not block shipping.
var embeddedFingerprints = []string{
	"D3F9E65DE72888CC03D45CF5064D4ABCFA6D9338",
	"E885B45B2BD6E53B5C50077A49421B9547B5B830",
}

func TestEmbeddedKeyring(t *testing.T) {
	store := NewStore()
	require.Len(t, store.ring, len(embeddedFingerprints))
	for _, fp := range embeddedFingerprints {
		require.True(t, store.IsTrustedFingerprint(fp), fp)
		require.True(t, store.IsTrustedFingerprint(strings.ToLower(fp)), fp)
	}

	t.Run("ignores a planted keyring on disk", func(t *testing.T) {
		// A v1.3.x data dir may still hold the cache the served keyring
		// used to be persisted to; an attacker who can write there must
		// not be able to add a trust anchor through it.
		attacker, err := openpgp.NewEntity("Attacker", "", "attacker@example.com", nil)
		require.NoError(t, err)
		fingerprint := strings.ToUpper(hex.EncodeToString(attacker.PrimaryKey.Fingerprint))
		dataDir := t.TempDir()
		var planted bytes.Buffer
		require.NoError(t, attacker.Serialize(&planted))
		require.NoError(t, os.WriteFile(
			filepath.Join(dataDir, "trusted_keys.asc"), planted.Bytes(), 0o644))

		store := NewStore()
		require.False(t, store.IsTrustedFingerprint(fingerprint))
		bundle := signedTestBundle(t, attacker, []byte("bundle contents"))
		_, err = store.VerifyBundle(bundle, bytes.NewReader([]byte("bundle contents")))
		require.ErrorContains(t, err, "is not trusted")
	})
}

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

func signedTestBundle(t *testing.T, entity *openpgp.Entity, contents []byte) release.Bundle {
	t.Helper()
	var signature bytes.Buffer
	require.NoError(t, openpgp.ArmoredDetachSign(&signature, entity, bytes.NewReader(contents), nil))
	return release.Bundle{Metadata: map[string]string{
		MetadataSigningKeyID: fmt.Sprintf("%016X", entity.PrimaryKey.KeyId),
		MetadataSignature:    signature.String(),
	}}
}
