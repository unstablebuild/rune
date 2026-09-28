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
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/blue/logging/trace"
	"golang.org/x/oauth2"
)

//go:embed package_keyring.asc
var packageKeyringArmored string

const (
	// ServeTokenPath is the path at which oauth2 token redemption is performed.
	ServeTokenPath = "/o/oauth2/token"

	// ServeConfigPath is the path at which oauth2 configuration is served over http.
	ServeConfigPath = "/o/oauth2/config"

	authStyle = oauth2.AuthStyleAutoDetect
)

// APIURL, TokenURL, MgmtTokenURL, AuthURL, DeviceAuthURL, JWKSURL,
// ClientID, and SignupURL are the auth0/signup endpoints baked into the
// binary at build time.
var (
	APIURL = "https://dev-fv7z5qrer6vkxhxf.us.auth0.com/api/v2/"
	// TokenURL is used for the end-user / first-party API oauth2 flow and
	// can live on an Auth0 custom domain (e.g. auth.rune.build).
	TokenURL = "https://dev-fv7z5qrer6vkxhxf.us.auth0.com/oauth/token"
	// MgmtTokenURL is a token url as well, but used for the server-to-server
	// client_credentials grant against the Auth0 Management API and MUST point at the
	// canonical tenant hostname — Auth0 does not register the `<tenant>/api/v2/` audience
	// on custom domains, so requests there 404 at /oauth/token. See
	// ox-api's api/user/auth0_store.go.
	MgmtTokenURL  = "https://dev-fv7z5qrer6vkxhxf.us.auth0.com/oauth/token"
	AuthURL       = "https://dev-fv7z5qrer6vkxhxf.us.auth0.com/authorize"
	DeviceAuthURL = "https://dev-fv7z5qrer6vkxhxf.us.auth0.com/oauth/device/code"
	JWKSURL       = "https://dev-fv7z5qrer6vkxhxf.us.auth0.com/.well-known/jwks.json"
	ClientID      = "AhY5YlLUiEjOXNFmmyw4Nve32Hp0ag22"
	SignupURL     = "https://unstable-build-blue-dev.web.app/signup"
)

// Config represents a full oauth2 configuration for clients and servers to use.
type Config struct {
	APIURL    string
	SignupURL string
	JWKSURL   string
	// PackageKeyringArmored is the armored PGP public keyring that Rune
	// clients trust to verify official package bundle signatures. Serving
	// it here lets clients pick up key rotations and revocations without
	// shipping a new binary.
	PackageKeyringArmored string `json:"package_keyring_armored,omitempty"`
	// MgmtTokenURL is the oauth2 token endpoint used by ox-api's
	// server-to-server client_credentials grant against the Auth0
	// Management API. It is distinct from oauth2.Config.Endpoint.TokenURL
	// (which is the end-user / first-party token endpoint) because
	// the Management API audience is not addressable through Auth0
	// custom domains.
	MgmtTokenURL string
	oauth2.Config
}

// DefaultM2MConfig returns the oauth2 provider config used by ox-api's
// own backend to talk to Auth0 as a machine-to-machine client (the
// client_credentials grant against the Auth0 Management API). It
// carries Management API scopes and the MgmtTokenURL, and is NOT the
// shape advertised to Rune clients via /o/oauth2/config — that is
// DefaultNativeConfig.
func DefaultM2MConfig() Config {
	return Config{
		APIURL:       APIURL,
		JWKSURL:      JWKSURL,
		SignupURL:    SignupURL,
		MgmtTokenURL: MgmtTokenURL,
		Config: oauth2.Config{
			ClientID: ClientID,
			Scopes: []string{
				"read:users", "update:users", "create:users", "delete:users",
				"read:users_app_metadata", "update:users_app_metadata",
			},
			Endpoint: oauth2.Endpoint{
				AuthStyle: authStyle,
				AuthURL:   AuthURL,
				TokenURL:  TokenURL,
			},
		},
	}
}

// DefaultNativeConfig returns the auth configuration for both server and native client.
//
// To deploy a new oauth2 provider, domain or simply a new private key the following
// steps must be done:
//
//  1. Create a new secret via bluectl cli. Be sure to add the certs_url, redeem_url
//     and login_url metadata keys for the secret:
//     - bluectl secret create -d "certs_url=<url>" -d "redeem_url=<url>" -d "login_url=<url>" <client-id>
//  2. Add the secret's secret via:
//     - bluectl secret rotate <client_id> <file_with_secret>
//  3. Update this configuration with the new JWKSURL, ClientID, AuthURL,
//     (TokenURL and Scopes too, if applicable).
//  4. Deploy a new version of the ox-api. All clients will pick up the new configuration
//     via FetchConfig.
func DefaultNativeConfig(api *url.URL) Config {
	tokenURL := api.JoinPath(ServeTokenPath).String()
	return Config{
		APIURL:                APIURL,
		JWKSURL:               JWKSURL,
		SignupURL:             SignupURL,
		MgmtTokenURL:          MgmtTokenURL,
		PackageKeyringArmored: packageKeyringArmored,
		Config: oauth2.Config{
			ClientID: ClientID,
			Scopes: []string{
				"offline_access", /* ensure it returns a refresh token */
				"openid",         /* to ensure it returns an id token */
			},
			Endpoint: oauth2.Endpoint{
				AuthStyle:     oauth2.AuthStyleInParams,
				AuthURL:       AuthURL,
				DeviceAuthURL: DeviceAuthURL,
				TokenURL:      tokenURL,
			},
		},
	}
}

// FetchConfig fetches the config at the given api url on ServeConfigPath and
// returns a valid configuration or an error.
func FetchConfig(api *url.URL) (Config, error) {
	var client http.Client
	client.Timeout = 5 * time.Second
	resp, err := client.Get(api.JoinPath(ServeConfigPath).String())
	if err != nil {
		return Config{}, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Config{}, errors.New("http get returned non 200 status")
	}

	var cfg Config
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshal config json: %v", err)
	}

	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// ServeNativeConfig returns an http.Handler that serves DefaultNativeConfig.
func ServeNativeConfig(logger *log.Logger, api *url.URL) (http.Handler, error) {
	const httpCallType = "ServeConfig"

	cfg := DefaultNativeConfig(api)
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("could not marshal config: %v", err)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID, _ := trace.FromContextOrNew(r.Context())
		fields := []logging.Field{
			{Key: logging.KeyClass, Value: "auth.funcConfigHandler"},
			{Key: "Method", Value: r.Method},
			{Key: "URL", Value: r.URL.String()},
		}
		attemptAt := logging.LogAttempt(traceID, httpCallType, fields...)

		if r.Method != http.MethodGet {
			status := http.StatusBadRequest
			w.WriteHeader(status)
			fields = append(fields, logging.Field{Key: "Status", Value: http.StatusText(status)})
			logging.LogResult(nil, attemptAt, traceID, httpCallType, fields...)
			return
		}
		_, err = w.Write(data)
		logging.LogResult(err, attemptAt, traceID, httpCallType, fields...)
	}), nil
}

func validateConfig(cfg Config) error {
	if cfg.Config.ClientID == "" || cfg.Config.Endpoint.AuthURL == "" ||
		cfg.Config.Endpoint.TokenURL == "" {
		return errors.New("unusable oauth2 config: missing key fields")
	}
	return nil
}
