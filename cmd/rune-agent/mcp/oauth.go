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
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
	runeauth "unstable.build/rune/auth"
)

// CIMDURL is the client ID metadata document Rune signs in with at
// authorization servers that accept one. It is always the production
// API server's: authorization servers fetch it from the public
// internet, whichever API server this build talks to.
var CIMDURL = runeauth.MCPClientID(&url.URL{Scheme: "https", Host: "api.rune.build"})

// challenge is a 401 or 403 response from a remote server.
type challenge struct {
	status int
	url    string
	header http.Header
}

// bearerParam returns a parameter of the Bearer challenge.
func (c *challenge) bearerParam(name string) string {
	challenges, err := oauthex.ParseWWWAuthenticate(c.header.Values("WWW-Authenticate"))
	if err != nil {
		return ""
	}
	for _, ch := range challenges {
		if ch.Scheme == "bearer" {
			return ch.Params[name]
		}
	}
	return ""
}

// authState is the authentication of one remote server: its stored
// sign-in, the last challenge it answered with, and the headers its
// headersHelper produced.
type authState struct {
	serverURL string
	cfg       *OAuthConfig
	creds     *CredentialStore
	// client fetches metadata and tokens. It is a plain client over an
	// *http.Transport so the SDK keeps its SSRF guard on discovery.
	client *http.Client

	mu         sync.Mutex
	doc        *tokenDoc
	ts         oauth2.TokenSource
	challenge  *challenge
	lastStatus int
	needsAuth  bool
	// refreshTried is set once a rejected token was refreshed, so a
	// server that keeps rejecting fresh tokens needs sign-in instead.
	refreshTried bool
	helper       map[string]string
}

func newAuthState(
	ctx context.Context, serverURL string, cfg *OAuthConfig, creds *CredentialStore,
) *authState {
	a := &authState{
		serverURL: serverURL,
		cfg:       cfg,
		creds:     creds,
		client:    &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()},
	}
	if creds == nil {
		return a
	}
	doc, err := creds.LoadToken(ctx, serverURL)
	if err != nil {
		slog.Warn("mcp: load stored sign-in", "url", serverURL, "error", err)
		return a
	}
	if doc != nil {
		a.doc = doc
		a.ts = a.tokenSource(a.refreshConfig(doc), doc.token())
	}
	return a
}

// refreshConfig returns the client configuration to refresh doc's
// token with. A configured client's secret is never stored, so it is
// taken from the configuration.
func (a *authState) refreshConfig(doc *tokenDoc) *oauth2.Config {
	cfg := doc.oauthConfig()
	if cfg.ClientSecret == "" && a.cfg != nil && a.cfg.ClientID == doc.ClientID {
		cfg.ClientSecret = a.cfg.ClientSecret
	}
	return cfg
}

// tokenSource returns a source refreshing tok through cfg, which stores
// every new token. Refreshes outlive any one request, so they must not
// use a request's context.
func (a *authState) tokenSource(cfg *oauth2.Config, tok *oauth2.Token) oauth2.TokenSource {
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, a.client)
	return &savingTokenSource{a: a, src: cfg.TokenSource(ctx, tok), last: tok.AccessToken}
}

type savingTokenSource struct {
	a    *authState
	src  oauth2.TokenSource
	mu   sync.Mutex
	last string
}

func (s *savingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := s.src.Token()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	changed := tok.AccessToken != s.last
	s.last = tok.AccessToken
	s.mu.Unlock()
	if changed {
		s.a.saveToken(tok)
	}
	return tok, nil
}

func (a *authState) saveToken(tok *oauth2.Token) {
	a.mu.Lock()
	if a.doc == nil {
		a.mu.Unlock()
		return
	}
	a.doc.setToken(tok)
	doc := *a.doc
	a.mu.Unlock()
	if a.creds == nil {
		return
	}
	if err := a.creds.SaveToken(context.Background(), a.serverURL, &doc); err != nil {
		slog.Warn("mcp: store refreshed token", "url", a.serverURL, "error", err)
	}
}

// bearer returns the access token to send, or empty when signed out. A
// refresh token the authorization server no longer accepts signs the
// server out instead of failing the request, so the server answers
// with the challenge sign-in needs.
func (a *authState) bearer() (string, error) {
	a.mu.Lock()
	ts := a.ts
	a.mu.Unlock()
	if ts == nil {
		return "", nil
	}
	tok, err := ts.Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.ErrorCode == "invalid_grant" {
			a.mu.Lock()
			a.ts = nil
			a.needsAuth = true
			a.mu.Unlock()
			return "", nil
		}
		return "", err
	}
	return tok.AccessToken, nil
}

// record notes the status of a response from the server's origin.
func (a *authState) record(reqURL string, resp *http.Response) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastStatus = resp.StatusCode
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		a.challenge = &challenge{status: resp.StatusCode, url: reqURL, header: resp.Header.Clone()}
	}
}

// beginConnect forgets the outcome of the previous connection.
func (a *authState) beginConnect() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.challenge = nil
	a.lastStatus = 0
	a.needsAuth = false
}

// connected records that the server accepted the current credentials.
func (a *authState) connected() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refreshTried = false
}

// status returns the last response status from the server, and the
// challenge recorded since the connection began.
func (a *authState) status() (int, *challenge, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastStatus, a.challenge, a.needsAuth
}

func (a *authState) setHelperHeaders(h map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.helper = h
}

func (a *authState) helperHeaders() map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.helper
}

// previousRedirectPort returns the loopback port the last sign-in
// redirected to, or zero. Signing in on it again lets a client
// registered for that redirect URI be reused.
func (a *authState) previousRedirectPort() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.doc == nil {
		return 0
	}
	u, err := url.Parse(a.doc.RedirectURL)
	if err != nil {
		return 0
	}
	port, _ := strconv.Atoi(u.Port())
	return port
}

// forceRefresh makes the next request refresh the access token, after
// the server rejected it. It reports false when there is no refresh
// token, or when a refreshed token was rejected already.
func (a *authState) forceRefresh() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.doc == nil || a.doc.RefreshToken == "" || a.ts == nil || a.refreshTried {
		return false
	}
	a.refreshTried = true
	expired := a.doc.token()
	expired.AccessToken = ""
	a.ts = a.tokenSource(a.refreshConfig(a.doc), expired)
	return true
}

// Login signs in to the server through the browser: it discovers the
// authorization server from the recorded challenge, registers a client,
// has open show the authorization URL and waits for cb to receive the
// redirect.
func (a *authState) Login(
	ctx context.Context, cb *callbackServer, open func(string) error,
) error {
	a.mu.Lock()
	ch := a.challenge
	previous := a.doc
	a.mu.Unlock()
	if ch == nil {
		return errors.New("the server did not ask for sign-in")
	}
	challenges, err := oauthex.ParseWWWAuthenticate(ch.header.Values("WWW-Authenticate"))
	if err != nil {
		return fmt.Errorf("parse WWW-Authenticate: %w", err)
	}

	prm := a.protectedResource(ctx, challenges, ch.url)
	issuer := prm.AuthorizationServers[0]
	asm, err := a.authServerMetadata(ctx, issuer)
	if err != nil {
		return err
	}
	redirectURL := cb.RedirectURL()
	client, err := a.register(ctx, asm, redirectURL)
	if err != nil {
		return err
	}

	var granted []string
	if previous != nil && previous.Issuer == asm.Issuer {
		granted = previous.Scopes
	}
	cfg := &oauth2.Config{
		ClientID:     client.id,
		ClientSecret: client.secret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   asm.AuthorizationEndpoint,
			TokenURL:  asm.TokenEndpoint,
			AuthStyle: client.style,
		},
		RedirectURL: redirectURL,
		Scopes:      a.scopes(challenges, prm, asm, granted),
	}
	verifier := oauth2.GenerateVerifier()
	authURL := cfg.AuthCodeURL(cb.state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("resource", prm.Resource))
	if err := open(authURL); err != nil {
		slog.Warn("mcp: open authorization URL", "error", err)
	}

	res, err := cb.wait(ctx)
	if err != nil {
		return err
	}
	if err := validateIssuer(res.iss, asm); err != nil {
		return err
	}
	exchangeCtx := context.WithValue(ctx, oauth2.HTTPClient, a.client)
	tok, err := cfg.Exchange(exchangeCtx, res.code,
		oauth2.VerifierOption(verifier),
		oauth2.SetAuthURLParam("resource", prm.Resource))
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}

	doc := &tokenDoc{
		Issuer:       asm.Issuer,
		ClientID:     cfg.ClientID,
		ClientSecret: client.storedSecret,
		AuthURL:      cfg.Endpoint.AuthURL,
		TokenURL:     cfg.Endpoint.TokenURL,
		RedirectURL:  redirectURL,
		AuthStyle:    int(cfg.Endpoint.AuthStyle),
		Scopes:       cfg.Scopes,
	}
	doc.setToken(tok)
	if a.creds != nil {
		if err := a.creds.SaveToken(ctx, a.serverURL, doc); err != nil {
			slog.Warn("mcp: store sign-in", "url", a.serverURL, "error", err)
		}
	}
	a.mu.Lock()
	a.doc = doc
	a.ts = a.tokenSource(a.refreshConfig(doc), tok)
	a.challenge = nil
	a.needsAuth = false
	a.refreshTried = false
	a.mu.Unlock()
	return nil
}

// protectedResource discovers the server's protected resource metadata,
// falling back to treating the server's origin as its authorization
// server, as servers predating the metadata do.
func (a *authState) protectedResource(
	ctx context.Context, challenges []oauthex.Challenge, resourceURL string,
) *oauthex.ProtectedResourceMetadata {
	type candidate struct{ metadata, resource string }
	var candidates []candidate
	for _, c := range challenges {
		if u := c.Params["resource_metadata"]; u != "" {
			candidates = append(candidates, candidate{u, resourceURL})
			break
		}
	}
	ru, err := url.Parse(resourceURL)
	if err == nil {
		mu := *ru
		mu.RawQuery, mu.Fragment = "", ""
		mu.Path = "/.well-known/oauth-protected-resource/" + strings.TrimLeft(ru.Path, "/")
		candidates = append(candidates, candidate{mu.String(), resourceURL})
		mu.Path = "/.well-known/oauth-protected-resource"
		root := *ru
		root.Path, root.RawQuery, root.Fragment = "", "", ""
		candidates = append(candidates, candidate{mu.String(), root.String()})
	}
	for _, c := range candidates {
		prm, err := oauthex.GetProtectedResourceMetadata(ctx, c.metadata, c.resource, a.client)
		if err != nil || prm == nil || len(prm.AuthorizationServers) == 0 {
			continue
		}
		return prm
	}
	origin := resourceURL
	if ru != nil {
		o := url.URL{Scheme: ru.Scheme, Host: ru.Host}
		origin = o.String()
	}
	return &oauthex.ProtectedResourceMetadata{
		Resource:             resourceURL,
		AuthorizationServers: []string{origin},
	}
}

func (a *authState) authServerMetadata(
	ctx context.Context, issuer string,
) (*oauthex.AuthServerMeta, error) {
	var asm *oauthex.AuthServerMeta
	var err error
	if a.cfg != nil && a.cfg.AuthServerMetadataURL != "" {
		asm, err = oauthex.GetAuthServerMeta(ctx, a.cfg.AuthServerMetadataURL, issuer, a.client)
	} else {
		asm, err = auth.GetAuthServerMetadata(ctx, issuer, a.client)
	}
	if err != nil {
		return nil, fmt.Errorf("authorization server metadata: %w", err)
	}
	if asm == nil {
		// Servers predating metadata discovery serve fixed endpoints.
		base := strings.TrimSuffix(issuer, "/")
		asm = &oauthex.AuthServerMeta{
			Issuer:                issuer,
			AuthorizationEndpoint: base + "/authorize",
			TokenEndpoint:         base + "/token",
			RegistrationEndpoint:  base + "/register",
		}
	}
	return asm, nil
}

type registeredClient struct {
	id     string
	secret string
	// storedSecret is the secret that may be persisted: a dynamically
	// registered client's, never a configured one.
	storedSecret string
	style        oauth2.AuthStyle
}

// register picks the client to sign in as: the configured client, then
// Rune's client ID metadata document, then a client registered with
// this issuer before, then a new dynamic registration.
func (a *authState) register(
	ctx context.Context, asm *oauthex.AuthServerMeta, redirectURL string,
) (registeredClient, error) {
	if a.cfg != nil && a.cfg.ClientID != "" {
		c := registeredClient{id: a.cfg.ClientID, secret: a.cfg.ClientSecret, style: oauth2.AuthStyleInParams}
		if c.secret != "" {
			c.style = selectAuthStyle(asm.TokenEndpointAuthMethodsSupported)
		}
		return c, nil
	}
	if asm.ClientIDMetadataDocumentSupported {
		return registeredClient{id: CIMDURL, style: oauth2.AuthStyleInParams}, nil
	}
	if a.creds != nil {
		stored, err := a.creds.LoadClient(ctx, asm.Issuer, redirectURL)
		if err != nil {
			slog.Warn("mcp: load registered client", "issuer", asm.Issuer, "error", err)
		}
		if stored != nil {
			return clientFromSecret(stored.ClientID, stored.ClientSecret), nil
		}
	}
	if asm.RegistrationEndpoint == "" {
		return registeredClient{}, errors.New(
			"authorization server supports no automatic client registration; set oauth.clientId")
	}
	resp, err := oauthex.RegisterClient(ctx, asm.RegistrationEndpoint, &oauthex.ClientRegistrationMetadata{
		RedirectURIs:            []string{redirectURL},
		ClientName:              "Rune",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		ApplicationType:         "native",
		SoftwareID:              "build.rune.agent",
	}, a.client)
	if err != nil {
		return registeredClient{}, fmt.Errorf("register client: %w", err)
	}
	if a.creds != nil {
		err := a.creds.SaveClient(ctx, asm.Issuer, redirectURL, &clientDoc{
			ClientID:     resp.ClientID,
			ClientSecret: resp.ClientSecret,
			RegisteredAt: time.Now(),
		})
		if err != nil {
			slog.Warn("mcp: store registered client", "issuer", asm.Issuer, "error", err)
		}
	}
	return clientFromSecret(resp.ClientID, resp.ClientSecret), nil
}

func clientFromSecret(id, secret string) registeredClient {
	c := registeredClient{id: id, secret: secret, storedSecret: secret, style: oauth2.AuthStyleInParams}
	if secret != "" {
		c.style = oauth2.AuthStyleAutoDetect
	}
	return c
}

func selectAuthStyle(supported []string) oauth2.AuthStyle {
	switch {
	case slices.Contains(supported, "client_secret_post"):
		return oauth2.AuthStyleInParams
	case slices.Contains(supported, "client_secret_basic"):
		return oauth2.AuthStyleInHeader
	default:
		return oauth2.AuthStyleAutoDetect
	}
}

// scopes returns the scopes to request: the configured ones when set,
// otherwise those the challenge names, or the server advertises, kept
// together with the ones already granted so a step-up loses nothing.
func (a *authState) scopes(
	challenges []oauthex.Challenge, prm *oauthex.ProtectedResourceMetadata,
	asm *oauthex.AuthServerMeta, granted []string,
) []string {
	var scopes []string
	if a.cfg != nil && a.cfg.Scopes != "" {
		scopes = strings.Fields(a.cfg.Scopes)
	} else {
		for _, c := range challenges {
			if c.Scheme == "bearer" && c.Params["scope"] != "" {
				scopes = strings.Fields(c.Params["scope"])
				break
			}
		}
		if len(scopes) == 0 {
			scopes = prm.ScopesSupported
		}
		set := make(map[string]bool)
		for _, s := range granted {
			set[s] = true
		}
		for _, s := range scopes {
			set[s] = true
		}
		scopes = slices.Sorted(maps.Keys(set))
	}
	if slices.Contains(asm.ScopesSupported, "offline_access") && !slices.Contains(scopes, "offline_access") {
		scopes = append(scopes, "offline_access")
	}
	return scopes
}

// validateIssuer checks the authorization response's issuer (RFC 9207),
// which defends against a mix-up between authorization servers.
func validateIssuer(iss string, asm *oauthex.AuthServerMeta) error {
	if iss == "" {
		if asm.AuthorizationResponseIssParameterSupported {
			return errors.New("the authorization response does not name its issuer")
		}
		return nil
	}
	if strings.TrimSuffix(iss, "/") != strings.TrimSuffix(asm.Issuer, "/") {
		return fmt.Errorf("the authorization response comes from issuer %q, want %q", iss, asm.Issuer)
	}
	return nil
}

// Logout revokes the stored tokens where the authorization server
// supports it, and forgets them.
func (a *authState) Logout(ctx context.Context) error {
	a.mu.Lock()
	doc := a.doc
	a.doc = nil
	a.ts = nil
	a.mu.Unlock()
	if doc != nil {
		a.revoke(ctx, doc)
	}
	if a.creds == nil {
		return nil
	}
	return a.creds.DeleteToken(ctx, a.serverURL)
}

// revoke asks the authorization server to revoke the refresh and access
// tokens (RFC 7009). It is best effort: the tokens are forgotten either
// way.
func (a *authState) revoke(ctx context.Context, doc *tokenDoc) {
	asm, err := a.authServerMetadata(ctx, doc.Issuer)
	if err != nil || asm.RevocationEndpoint == "" {
		return
	}
	secret := a.refreshConfig(doc).ClientSecret
	for _, t := range []struct{ token, hint string }{
		{doc.RefreshToken, "refresh_token"},
		{doc.AccessToken, "access_token"},
	} {
		if t.token == "" {
			continue
		}
		form := url.Values{"token": {t.token}, "token_type_hint": {t.hint}}
		if secret == "" {
			form.Set("client_id", doc.ClientID)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, asm.RevocationEndpoint,
			strings.NewReader(form.Encode()))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if secret != "" {
			req.SetBasicAuth(url.QueryEscape(doc.ClientID), url.QueryEscape(secret))
		}
		resp, err := a.client.Do(req)
		if err != nil {
			slog.Warn("mcp: revoke token", "error", err)
			return
		}
		_ = resp.Body.Close()
	}
}
