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

package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfigServing(t *testing.T) {
	logger := log.New()
	logger.SetOutput(io.Discard)
	logger.SetLevel(log.TraceLevel)
	url, err := url.Parse("http://localhost:3001")
	require.NoError(t, err)

	for _, method := range []string{"POST", "OPTIONS", "PUT", "DELETE"} {
		t.Run(fmt.Sprintf("%s other than GET fails", method), func(t *testing.T) {
			req := httptest.NewRequest("POST", url.JoinPath(ServeConfigPath).String(), nil)

			w := httptest.NewRecorder()
			sut, err := ServeNativeConfig(logger, url)
			require.NoError(t, err)
			sut.ServeHTTP(w, req)

			resp := w.Result()
			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		})
	}

	t.Run("GET", func(t *testing.T) {
		req := httptest.NewRequest("GET", url.JoinPath(ServeConfigPath).String(), nil)

		w := httptest.NewRecorder()
		sut, err := ServeNativeConfig(logger, url)
		require.NoError(t, err)
		sut.ServeHTTP(w, req)

		resp := w.Result()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var actualCfg Config
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&actualCfg))
		assert.Equal(t, DefaultNativeConfig(url), actualCfg)
	})
}

func TestDefaultConfigSignupURL(t *testing.T) {
	// SignupURL defaults to the staging signup page; production builds
	// override it via -ldflags -X to point at https://rune.build/signup
	// (see the ox-api repo's docker-build-gcp-prod).
	const staging = "https://unstable-build-blue-dev.web.app/signup"
	assert.Equal(t, staging, DefaultM2MConfig().SignupURL)

	api, err := url.Parse("https://api.unstable.build")
	require.NoError(t, err)
	assert.Equal(t, staging, DefaultNativeConfig(api).SignupURL)
}

// TestDefaultNativeConfigPackageKeyring guards the contract that the
// config served to Rune clients advertises the package-signing
// keyring so clients can rotate trust anchors without a new binary.
func TestDefaultNativeConfigPackageKeyring(t *testing.T) {
	api, err := url.Parse("https://api.rune.build")
	require.NoError(t, err)
	keyring := DefaultNativeConfig(api).PackageKeyringArmored
	assert.Contains(t, keyring, "BEGIN PGP PUBLIC KEY BLOCK")
	// the M2M shape is ox-api's backend Auth0 client config; it is not
	// served to Rune clients and must not carry the keyring payload.
	assert.Empty(t, DefaultM2MConfig().PackageKeyringArmored)
}

// TestDefaultNativeConfigTokenURL guards against a regression where
// DefaultNativeConfig advertised a TokenURL on the wrong host: the
// returned TokenURL must be derived from the api URL passed in (the
// public URL ox-api advertises via -A), not from the dev default
// baked into the binary. Otherwise prod ox-api would tell clients to
// redeem tokens at api.unstable.build, which 404s in production.
func TestDefaultNativeConfigTokenURL(t *testing.T) {
	cases := []struct {
		name string
		api  string
		want string
	}{
		{
			name: "prod public URL produces prod token endpoint",
			api:  "https://api.rune.build",
			want: "https://api.rune.build" + ServeTokenPath,
		},
		{
			name: "dev public URL produces dev token endpoint",
			api:  "https://api.unstable.build",
			want: "https://api.unstable.build" + ServeTokenPath,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, err := url.Parse(tc.api)
			require.NoError(t, err)
			assert.Equal(t, tc.want, DefaultNativeConfig(api).Endpoint.TokenURL)
		})
	}
}

// TestDefaultNativeConfigDeviceAuthURL guards the wire contract that
// lets rune --headless sign in by code: the served config carries the
// device authorization endpoint, and a config from an older server
// without it still validates so old and new binaries interoperate.
func TestDefaultNativeConfigDeviceAuthURL(t *testing.T) {
	api, err := url.Parse("https://api.rune.build")
	require.NoError(t, err)

	t.Run("round-trips through json", func(t *testing.T) {
		cfg := DefaultNativeConfig(api)
		assert.Equal(t, DeviceAuthURL, cfg.Endpoint.DeviceAuthURL)

		data, err := json.Marshal(cfg)
		require.NoError(t, err)
		var decoded Config
		require.NoError(t, json.Unmarshal(data, &decoded))
		assert.Equal(t, DeviceAuthURL, decoded.Endpoint.DeviceAuthURL)
	})

	t.Run("fetch accepts a config without it", func(t *testing.T) {
		cfg := DefaultNativeConfig(api)
		cfg.Endpoint.DeviceAuthURL = ""
		srv := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				require.NoError(t, json.NewEncoder(w).Encode(cfg))
			}))
		defer srv.Close()

		srvURL, err := url.Parse(srv.URL)
		require.NoError(t, err)
		fetched, err := FetchConfig(srvURL)
		require.NoError(t, err)
		assert.Empty(t, fetched.Endpoint.DeviceAuthURL)
	})
}
