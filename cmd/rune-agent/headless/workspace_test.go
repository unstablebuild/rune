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

package headless

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnectRequiresPluginEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name    string
		socket  string
		datadir string
	}{
		{name: "no socket", datadir: "/tmp/data"},
		{name: "no datadir", socket: "unix:///tmp/rune.sock"},
		{name: "neither"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RUNE_SOCKET", tc.socket)
			t.Setenv("RUNE_DATADIR", tc.datadir)

			w, err := Connect(ExtensionID)

			assert.Nil(t, w)
			assert.ErrorIs(t, err, ErrNotInRune)
			assert.Contains(t, err.Error(),
				"must be invoked from a Rune plugin command (! or !!)")
		})
	}
}

func TestConnectDecodesCertificate(t *testing.T) {
	t.Setenv("RUNE_SOCKET", "unix:///tmp/rune.sock")
	t.Setenv("RUNE_DATADIR", t.TempDir())
	t.Setenv("RUNE_TOKEN", "sekret")
	t.Setenv("RUNE_CERT", base64.StdEncoding.EncodeToString(selfSignedPEM(t)))

	w, err := Connect(ExtensionID)

	require.NoError(t, err)
	require.NotNil(t, w)
}

func TestConnectRejectsMalformedCertificate(t *testing.T) {
	t.Setenv("RUNE_SOCKET", "unix:///tmp/rune.sock")
	t.Setenv("RUNE_DATADIR", t.TempDir())
	t.Setenv("RUNE_CERT", "not base64!!")

	w, err := Connect(ExtensionID)

	assert.Nil(t, w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode RUNE_CERT")
}

func selfSignedPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "rune-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
