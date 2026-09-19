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
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/blue/logging/trace"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"golang.org/x/oauth2"
)

const (
	defaultStorageTimeout = 2 * time.Second
	tokenDocumentID       = "tokenv2"
)

// NewCachedTokenSource allocates storage for a new CachedTokenSource
// and initializes it with sourcer and storage.
func NewCachedTokenSource(
	sourcer TokenSourcer, storage storageapi.Service,
) *CachedTokenSource {
	ret := &CachedTokenSource{sourcer: sourcer, storage: storage}
	noop := func() {}
	ret.cancel.Store(&noop)
	return ret
}

// CachedTokenSource is a oauth2.TokenSource that stores
// tokens in a document.Service to persist across sessions.
type CachedTokenSource struct {
	sourcer TokenSourcer
	storage storageapi.Service

	mu    sync.RWMutex
	token *oauth2.Token
	// cancel aborts the ctx of the in-flight acquisition. It is atomic so
	// concurrent Token callers (CachedTokenSource is shared as gRPC
	// PerRPCCredentials) and Purge can swap and invoke it lock-free; the
	// constructor seeds a no-op so a load is always callable.
	cancel atomic.Pointer[func()]
}

// Purge purged the underlying storage from any cached data.
func (l *CachedTokenSource) Purge() error {
	ctx, cancel := context.WithTimeout(context.Background(),
		defaultStorageTimeout)
	defer cancel()

	abort := l.cancel.Load()
	(*abort)()

	l.mu.Lock()
	l.token = nil
	l.mu.Unlock()

	return l.storage.Delete(ctx, tokenDocumentID)
}

// Cached returns the cached oauth2 token without triggering a refresh
// or a new oauth2 browser flow. If no token has been loaded into
// memory yet, it lazily hydrates from the storage partition. Returns
// nil when no token has ever been persisted. The returned token may
// be expired; callers that need validity checks should consult
// token.Valid() themselves.
func (l *CachedTokenSource) Cached(ctx context.Context) *oauth2.Token {
	return cloneToken(l.cached(ctx))
}

func (l *CachedTokenSource) cached(ctx context.Context) *oauth2.Token {
	l.mu.RLock()
	token := l.token
	l.mu.RUnlock()
	if token != nil {
		return token
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.token != nil {
		return l.token
	}
	ctx, cancel := context.WithTimeout(ctx, defaultStorageTimeout)
	defer cancel()
	var stored storedToken
	if err := l.storage.Get(ctx, tokenDocumentID, &stored); err != nil {
		return nil
	}
	l.token = stored.oauth2()
	return l.token
}

// Token satisfies oauth2.TokenSource.
func (l *CachedTokenSource) Token() (*oauth2.Token, error) {
	return l.TokenCtx(context.Background())
}

// TokenCtx is the cancellable form of Token. The supplied ctx is
// threaded into the underlying TokenSourcer.TokenSource call so an
// in-flight browser OAuth flow can be aborted by cancelling ctx.
// Token() preserves the oauth2.TokenSource interface by calling
// TokenCtx with a fresh context.Background.
func (l *CachedTokenSource) TokenCtx(ctx context.Context) (*oauth2.Token, error) {
	const callType = "Token"
	traceID, ctx := trace.FromContextOrNew(ctx)

	l.mu.RLock()
	token := l.token
	l.mu.RUnlock()

	fields := []logging.Field{
		{Key: logging.KeyClass, Value: "auth.CachedTokenSource"},
		{Key: "ptr", Value: fmt.Sprintf("%p", l)},
		{Key: "cache-valid", Value: strconv.FormatBool(token != nil && token.Valid())},
	}

	attemptAt := logging.LogAttempt(traceID, callType, fields...)
	ret, err := l.getToken(traceID, ctx, fields, token)
	// do not log expected, non-actionable conditions as errors
	if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNotAuthenticated) {
		logging.LogResultLevel(log.DebugLevel, log.DebugLevel, err, attemptAt,
			traceID, callType, fields...)
	} else {
		logging.LogResult(err, attemptAt, traceID, callType, fields...)
	}

	if ret != nil {
		ret = cloneToken(ret)
	}
	return ret, err
}

func (l *CachedTokenSource) getToken(
	traceID trace.ID, ctx context.Context, fields []logging.Field, token *oauth2.Token,
) (*oauth2.Token, error) {
	log := log.WithField("cached", fmt.Sprintf("%p", token))
	log = log.WithField(logging.KeyTraceID, traceID)
	for _, field := range fields {
		log = log.WithField(field.Key, field.Value)
	}

	if token == nil || !token.Valid() {
		// only allow one goroutine to proceed
		l.mu.Lock()

		// double check that while waiting for lock to be freed, some other goroutine
		// didn't complete the auth flow.
		if l.token == nil || !l.token.Valid() {
			ctx, cancel := context.WithTimeout(ctx, defaultStorageTimeout)
			defer cancel()
			var tokenForStorage storedToken
			err := l.storage.Get(ctx, tokenDocumentID, &tokenForStorage)
			log.Tracef("sourcing from storage: %v", err)
			if err == nil {
				token = tokenForStorage.oauth2()
				if ctx.Err() != nil {
					l.mu.Unlock()
					return nil, ctx.Err()
				}
				l.token = token
			} else {
				// ErrNotFound or other cache errors are ignored
				// signal below that a new token needs to be created
				token = nil
			}
		}

		l.mu.Unlock()
	}

	if token != nil && token.Valid() {
		log.Tracef("using cached in memory or in storage")
		return token, ctx.Err()
	}

	log.Tracef("fetching a new one: using refresh_token %v", token != nil)

	// act as semaphore so only 1 goroutine is acquiring a new token at a time
	l.mu.Lock()
	defer l.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// double check that while waiting for lock to be freed, some other goroutine
	// didn't complete the auth flow.
	if l.token != nil && l.token.Valid() {
		log.Tracef("some other goroutine was able to acquire one just in time")
		return l.token, ctx.Err()
	}

	if l.sourcer == nil {
		return nil, ErrUnavailable
	}

	// Only an interactive login (no token to refresh) is cancelable: a
	// newer login or a logout supersedes it via l.cancel. Concurrent
	// refresh/read calls must not register here, or they would cancel
	// each other and defeat the single-acquirer cache.
	if token == nil {
		var cancel func()
		ctx, cancel = context.WithCancel(ctx)
		prev := l.cancel.Swap(&cancel)
		(*prev)()
		defer cancel()
	}

	// if token is nil, then a new oauth2 flow is started
	// otherwise refresh_token grant is used to refresh token
	// under the hood.
	source, err := l.sourcer.TokenSource(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("token source: %w", err)
	}

	newToken, err := source.Token()
	if err != nil {
		// If we tried to refresh and the server rejected the refresh token,
		// purge the cached token and require the user to log in again.
		// For any other failure (transport, 5xx, context canceled), preserve
		// the cached refresh token so subsequent attempts can succeed once
		// the underlying issue resolves.
		if token != nil && isRefreshTokenRejected(err) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			l.token = nil
			purgeCtx, cancel := context.WithTimeout(ctx, defaultStorageTimeout)
			if delErr := l.storage.Delete(purgeCtx, tokenDocumentID); delErr != nil {
				log.Tracef("purge rejected token: %v", delErr)
			}
			cancel()
			return nil, ErrNotAuthenticated
		}
		return nil, fmt.Errorf("acquire token: %v", err)
	}
	// newToken may be the very pointer an oauth2 reuseTokenSource
	// stores and later writes (it sets Token.expiryDelta in place on
	// the returned token). Caching a copy keeps l.token, which other
	// goroutines read for its claims, from aliasing that memory.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	l.token = cloneToken(newToken)

	storeCtx, cancelStore := context.WithTimeout(ctx, defaultStorageTimeout)
	defer cancelStore()

	// store for next session
	if err := l.storage.Set(storeCtx, tokenDocumentID, newStoredToken(l.token)); err != nil {
		log.Tracef("cached token source: storage set: %v", err)
	}

	if !l.token.Valid() {
		log.Errorf("token returned by source is not valid: %s", l.token.AccessToken)
	}

	return l.token, ctx.Err()
}

// isRefreshTokenRejected reports whether err indicates that the OAuth2
// authorization server explicitly rejected the refresh token (as opposed
// to a transport/network/server error).
func isRefreshTokenRejected(err error) bool {
	var rerr *oauth2.RetrieveError
	if !errors.As(err, &rerr) {
		return false
	}
	if rerr.Response == nil {
		return false
	}
	if rerr.Response.StatusCode < http.StatusBadRequest ||
		rerr.Response.StatusCode >= http.StatusInternalServerError {
		return false
	}
	switch rerr.ErrorCode {
	case "invalid_grant", "invalid_request", "invalid_client", "unauthorized_client":
		return true
	}
	return false
}

// storedToken is a mirror of oauth2.Token, with the Extra field
// defined so it can be persisted.
type storedToken struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	Extra        RPCUser   `json:"extra,omitempty"`
}

func newStoredToken(t *oauth2.Token) (ret storedToken) {
	ret.AccessToken = t.AccessToken
	ret.TokenType = t.TokenType
	ret.RefreshToken = t.RefreshToken
	ret.Expiry = t.Expiry

	extra, _ := t.Extra("extra").(map[string]any)
	ret.Extra.Email, _ = extra["Email"].(string)
	ret.Extra.ID, _ = extra["ID"].(string)
	ret.Extra.Role, _ = extra["Role"].(Role)
	ret.Extra.Account, _ = extra["Account"].(string)
	return ret
}

func (t storedToken) oauth2() *oauth2.Token {
	ret := new(oauth2.Token)
	ret.AccessToken = t.AccessToken
	ret.TokenType = t.TokenType
	ret.RefreshToken = t.RefreshToken
	ret.Expiry = t.Expiry

	// unfortunately to keep the server's http token handler
	// agnostic to extra, it serializes it under the field 'extra',
	// rather than adding all the extra properties as part
	// of the resposne payload.
	all := make(map[string]any)
	extra := make(map[string]any)
	all["extra"] = extra
	extra["Email"] = t.Extra.Email
	extra["ID"] = t.Extra.ID
	extra["Role"] = t.Extra.Role
	extra["Account"] = t.Extra.Account
	ret = ret.WithExtra(all)
	return ret
}

// cloneToken returns a deep copy of t so callers never share the
// *oauth2.Token that an oauth2 reuse source mutates in place on
// refresh. Returns nil for a nil input. The round-trip through
// storedToken reconstructs the Extra payload into an independent map.
func cloneToken(t *oauth2.Token) *oauth2.Token {
	if t == nil {
		return nil
	}
	return newStoredToken(t).oauth2()
}
