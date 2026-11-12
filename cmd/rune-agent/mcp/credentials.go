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

package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"golang.org/x/oauth2"
	"unstable.build/rune/internal/keychain"
)

// credentialsPartition holds the plaintext copies of MCP credentials
// while the OS keychain cannot be used.
const credentialsPartition = "mcp-auth"

// CredentialStore keeps the OAuth tokens and dynamically registered
// clients of remote MCP servers in the OS keychain, falling back to the
// extension's storage when the keychain is unavailable.
type CredentialStore struct {
	store *keychain.Store
}

// NewCredentialStore returns a CredentialStore whose keychain items
// belong to dataDir, and whose fallback is a partition of storage.
func NewCredentialStore(dataDir string, storage storageapi.Service) *CredentialStore {
	scope := dataDir
	if abs, err := filepath.Abs(dataDir); err == nil {
		scope = abs
	}
	return newCredentialStore(keychain.System(), scope, storage)
}

func newCredentialStore(
	keyring keychain.Keyring, scope string, storage storageapi.Service,
) *CredentialStore {
	return &CredentialStore{store: keychain.NewStore(
		keyring, scope, storageapi.WithPartition(storage, credentialsPartition))}
}

// tokenDoc is the stored sign-in of one server: the token and what is
// needed to refresh it. Storage documents allow flat fields only.
type tokenDoc struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	RedirectURL  string
	AuthStyle    int
	Scopes       []string
	AccessToken  string
	TokenType    string
	RefreshToken string
	Expiry       time.Time
}

func (d *tokenDoc) oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     d.ClientID,
		ClientSecret: d.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   d.AuthURL,
			TokenURL:  d.TokenURL,
			AuthStyle: oauth2.AuthStyle(d.AuthStyle),
		},
		RedirectURL: d.RedirectURL,
		Scopes:      d.Scopes,
	}
}

func (d *tokenDoc) token() *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  d.AccessToken,
		TokenType:    d.TokenType,
		RefreshToken: d.RefreshToken,
		Expiry:       d.Expiry,
	}
}

func (d *tokenDoc) setToken(t *oauth2.Token) {
	d.AccessToken = t.AccessToken
	d.TokenType = t.TokenType
	if t.RefreshToken != "" {
		d.RefreshToken = t.RefreshToken
	}
	d.Expiry = t.Expiry
}

// clientDoc is a client registered dynamically with an authorization
// server.
type clientDoc struct {
	ClientID     string
	ClientSecret string
	RegisteredAt time.Time
}

// LoadToken returns the sign-in stored for serverURL, or nil.
func (s *CredentialStore) LoadToken(ctx context.Context, serverURL string) (*tokenDoc, error) {
	var doc tokenDoc
	if err := s.store.Get(ctx, tokenKey(serverURL), &doc); err != nil {
		if errors.Is(err, storageapi.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &doc, nil
}

// SaveToken stores the sign-in for serverURL.
func (s *CredentialStore) SaveToken(ctx context.Context, serverURL string, doc *tokenDoc) error {
	return s.store.Set(ctx, tokenKey(serverURL), doc)
}

// DeleteToken forgets the sign-in for serverURL.
func (s *CredentialStore) DeleteToken(ctx context.Context, serverURL string) error {
	return s.store.Delete(ctx, tokenKey(serverURL))
}

// LoadClient returns the client registered with issuer for redirectURL,
// or nil. A dynamically registered client is only valid at the
// authorization server that issued it.
func (s *CredentialStore) LoadClient(
	ctx context.Context, issuer, redirectURL string,
) (*clientDoc, error) {
	var doc clientDoc
	if err := s.store.Get(ctx, clientKey(issuer, redirectURL), &doc); err != nil {
		if errors.Is(err, storageapi.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &doc, nil
}

// SaveClient stores a client registered with issuer for redirectURL.
func (s *CredentialStore) SaveClient(
	ctx context.Context, issuer, redirectURL string, doc *clientDoc,
) error {
	return s.store.Set(ctx, clientKey(issuer, redirectURL), doc)
}

// DeleteClient forgets the client registered with issuer for
// redirectURL.
func (s *CredentialStore) DeleteClient(ctx context.Context, issuer, redirectURL string) error {
	return s.store.Delete(ctx, clientKey(issuer, redirectURL))
}

func tokenKey(serverURL string) string {
	return "token/" + hashHex(canonicalServerURL(serverURL))
}

func clientKey(issuer, redirectURL string) string {
	return "client/" + hashHex(issuer+"\x00"+redirectURL)
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// canonicalServerURL normalizes the spellings of one server URL that
// differ only in case, default port, fragment or trailing slash.
func canonicalServerURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" &&
		!(u.Scheme == "https" && port == "443") && !(u.Scheme == "http" && port == "80") {
		u.Host = joinHostPort(host, port)
	} else {
		u.Host = joinHostPort(host, "")
	}
	u.Fragment = ""
	u.RawFragment = ""
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return u.String()
}

func joinHostPort(host, port string) string {
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == "" {
		return host
	}
	return host + ":" + port
}
