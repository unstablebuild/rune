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
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"golang.org/x/oauth2"
)

// A sourcer returning ErrUnavailable is an expected condition (e.g. the
// telemetry refresh-only sourcer while logged out); it must not be
// logged at error level even though getToken wraps the error.
func TestTokenUnavailableNotLoggedAsError(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	svc := storagestub.NewInMemoryService()
	sourcer := &testSourcer{retErr: ErrUnavailable}
	source := NewCachedTokenSource(sourcer, svc)

	_, err := source.Token()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnavailable)

	for _, entry := range hook.AllEntries() {
		assert.Greater(t, entry.Level, log.ErrorLevel,
			"expected no error-level log, got: %s", entry.Message)
	}
}

func TestCachedTokenToken(t *testing.T) {
	t.Run("uses sourcer if no token is cached", func(t *testing.T) {
		svc := storagestub.NewInMemoryService()
		sourcer, token := goodSourcer()
		source := NewCachedTokenSource(sourcer, svc)
		actualToken, err := source.Token()
		require.NoError(t, err)
		assertEqualToken(t, token, actualToken)
	})

	t.Run("reuses token from cache if called again, within same session", func(t *testing.T) {
		svc := storagestub.NewInMemoryService()
		sourcer, token := goodSourcer()
		source := NewCachedTokenSource(sourcer, svc)

		for i := 0; i < 2; i++ {
			actualToken, err := source.Token()
			require.NoError(t, err)
			assertEqualToken(t, token, actualToken)
		}

		assert.Equal(t, int32(1), sourcer.called.Load())
	})

	t.Run("reuses token from cache if called again, accross sessions", func(t *testing.T) {
		svc := storagestub.NewInMemoryService()

		for i := 0; i < 2; i++ {
			sourcer, token := goodSourcer()
			source := NewCachedTokenSource(sourcer, svc)

			actualToken, err := source.Token()
			require.NoError(t, err)

			if i == 0 {
				assert.Equal(t, int32(1), sourcer.called.Load())
				assertEqualToken(t, token, actualToken)
			} else {
				assert.Equal(t, int32(0), sourcer.called.Load())
			}
		}

	})

	t.Run("acquires new token if token from cache is expired", func(t *testing.T) {
		svc := storagestub.NewInMemoryService()

		for i := 0; i < 2; i++ {
			sourcer, token := goodSourcerWithExpiry(0)
			source := NewCachedTokenSource(sourcer, svc)

			actualToken, err := source.Token()
			require.NoError(t, err)

			// new token every session
			assert.Equal(t, int32(1), sourcer.called.Load())
			assertEqualToken(t, token, actualToken)
		}

	})

	// use-case: two concurrent CachedTokenSource using the same underlying storage
	t.Run("before acquiring new token, it checks storage if token from inmemory cache is expired", func(t *testing.T) {
		svc := storagestub.NewInMemoryService()

		badSourcer, expiredToken := goodSourcerWithExpiry(-1)
		source := NewCachedTokenSource(badSourcer, svc)

		actualToken, err := source.Token()
		require.NoError(t, err)

		assert.Equal(t, int32(1), badSourcer.called.Load())
		assertEqualToken(t, expiredToken, actualToken)

		goodSourcer, storageToken := goodSourcerWithExpiry(30 * time.Minute)
		// force store token in svc
		actualToken, err = NewCachedTokenSource(goodSourcer, svc).Token()
		require.NoError(t, err)

		assert.Equal(t, int32(1), goodSourcer.called.Load())
		assertEqualToken(t, storageToken, actualToken)

		actualToken, err = source.Token()
		require.NoError(t, err)

		assert.Equal(t, int32(1), badSourcer.called.Load())
		assertEqualToken(t, storageToken, actualToken)
	})

	t.Run("is goroutine safe", func(t *testing.T) {
		svc := storagestub.NewInMemoryService()
		// token Valid returns false if we are within 10 seconds of
		// expiry, and this cannot be changed.
		sourcer, _ := goodSourcerWithExpiry(1 * time.Minute)
		source := NewCachedTokenSource(sourcer, svc)

		const n = 1000
		var wg sync.WaitGroup

		wg.Add(n)
		for i := 0; i < n; i++ {
			go func() {
				defer wg.Done()
				_, _ = source.Token()
			}()
		}
		wg.Wait()

		assert.Equal(t, int32(1), sourcer.called.Load())
	})

	t.Run("ignores document service errors, within same session", func(t *testing.T) {
		svc := failingDocumentService{}
		sourcer, token := goodSourcer()
		source := NewCachedTokenSource(sourcer, svc)

		for i := 0; i < 2; i++ {
			actualToken, err := source.Token()
			require.NoError(t, err)
			assertEqualToken(t, token, actualToken)
		}

		assert.Equal(t, int32(1), sourcer.called.Load())
	})

	t.Run("ignores document service errors, accross sessions", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			sourcer, token := goodSourcer()
			source := NewCachedTokenSource(sourcer, failingDocumentService{})

			actualToken, err := source.Token()
			require.NoError(t, err)

			// new token every session
			assert.Equal(t, int32(1), sourcer.called.Load())
			assertEqualToken(t, token, actualToken)
		}
	})

	t.Run("bubbles up Sourcer errors", func(t *testing.T) {
		svc := storagestub.NewInMemoryService()
		sourcer, _ := badSourcer()
		source := NewCachedTokenSource(sourcer, svc)
		_, actualErr := source.Token()
		require.EqualError(t, actualErr, "token source: boom")
	})

	t.Run("bubbles up oauth2.TokenSource errors", func(t *testing.T) {
		svc := storagestub.NewInMemoryService()
		sourcer, _ := badSourceSourcer()
		source := NewCachedTokenSource(sourcer, svc)
		_, actualErr := source.Token()
		require.EqualError(t, actualErr, "acquire token: boom")
	})

	t.Run("preserves cached token on transient refresh error", func(t *testing.T) {
		svc := storagestub.NewInMemoryService()
		cachedToken := &oauth2.Token{
			AccessToken:  "1234",
			RefreshToken: "refresh-1234",
			Expiry:       time.Now().Add(-24 * time.Hour),
		}
		err := svc.Set(context.Background(), tokenDocumentID, newStoredToken(cachedToken))
		require.NoError(t, err)

		transient := &url.Error{Op: "Post", URL: "https://example/token",
			Err: errors.New("i/o timeout")}
		sourcer := &testSourcer{retSource: &testSource{retErr: transient}}
		source := NewCachedTokenSource(sourcer, svc)

		_, err = source.Token()
		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrNotAuthenticated),
			"transient errors must not surface as ErrNotAuthenticated")

		// storage entry is not deleted
		var stored storedToken
		require.NoError(t, svc.Get(context.Background(), tokenDocumentID, &stored))
		assert.Equal(t, "refresh-1234", stored.RefreshToken)
	})

	t.Run("purges cached token and returns ErrNotAuthenticated when refresh token is rejected", func(t *testing.T) {
		svc := storagestub.NewInMemoryService()
		cachedToken := &oauth2.Token{
			AccessToken:  "1234",
			RefreshToken: "refresh-1234",
			Expiry:       time.Now().Add(-24 * time.Hour),
		}
		err := svc.Set(context.Background(), tokenDocumentID, newStoredToken(cachedToken))
		require.NoError(t, err)

		rejected := &oauth2.RetrieveError{
			Response:  &http.Response{StatusCode: http.StatusBadRequest},
			ErrorCode: "invalid_grant",
		}
		sourcer := &testSourcer{retSource: &testSource{retErr: rejected}}
		source := NewCachedTokenSource(sourcer, svc)

		_, err = source.Token()
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrNotAuthenticated))

		// storage entry is purged
		var stored storedToken
		err = svc.Get(context.Background(), tokenDocumentID, &stored)
		require.Error(t, err)
	})

	t.Run("propagates ErrNotAuthenticated from sourcer when no cached token", func(t *testing.T) {
		svc := storagestub.NewInMemoryService()
		sourcer := FuncTokenSourcer(
			func(_ context.Context, token *oauth2.Token) (oauth2.TokenSource, error) {
				assert.Nil(t, token)
				return nil, ErrNotAuthenticated
			})
		source := NewCachedTokenSource(sourcer, svc)
		_, err := source.Token()
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrNotAuthenticated))
	})
}

// TestCachedTokenConcurrentAccess reproduces the data race where the
// oauth2 reuse token source mutates the cached *oauth2.Token in place
// (as oauth2.reuseTokenSource.Token does on refresh) while another
// goroutine reads the same token via Token/Cached and calls Valid.
func TestCachedTokenConcurrentAccess(t *testing.T) {
	svc := storagestub.NewInMemoryService()
	mutating := &mutatingSource{token: &oauth2.Token{
		AccessToken:  uuid.New().String(),
		TokenType:    "bearer",
		RefreshToken: "refresh",
		Expiry:       time.Now().Add(time.Hour),
	}}
	source := NewCachedTokenSource(&testSourcer{retSource: mutating}, svc)

	const goroutines = 8
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				mutating.expire()
				_, _ = source.Token()
			}
		}
	}()

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if tok, err := source.Token(); err == nil && tok != nil {
						_ = tok.Valid()
					}
					if tok := source.Cached(context.Background()); tok != nil {
						_ = tok.Valid()
					}
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

type mutatingSource struct {
	mu    sync.Mutex
	token *oauth2.Token
}

func (m *mutatingSource) expire() {
	m.mu.Lock()
	m.token.Expiry = time.Now().Add(-time.Second)
	m.mu.Unlock()
}

func (m *mutatingSource) Token() (*oauth2.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token.Expiry = time.Now().Add(time.Hour)
	// Return a copy so concurrent expire() writes do not race the
	// caller cloning the returned token.
	tok := *m.token
	return &tok, nil
}

// TestConcurrentTokenAndPurge drives Token() and Purge() concurrently,
// mirroring how CachedTokenSource is shared as gRPC PerRPCCredentials
// (Token per RPC) while a logout calls Purge. It exercises the cancel
// bookkeeping shared by both paths under the race detector.
func TestConcurrentTokenAndPurge(t *testing.T) {
	svc := storagestub.NewInMemoryService()
	sourcer, _ := goodSourcer()
	source := NewCachedTokenSource(sourcer, svc)

	const goroutines = 8
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = source.Token()
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = source.Purge()
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestCachedTokenCached(t *testing.T) {
	t.Run("returns nil when nothing persisted and no in-memory token", func(t *testing.T) {
		svc := storagestub.NewInMemoryService()
		source := NewCachedTokenSource(nil, svc)

		got := source.Cached(context.Background())
		assert.Nil(t, got)
	})

	t.Run("lazily hydrates from storage without invoking the sourcer", func(t *testing.T) {
		svc := storagestub.NewInMemoryService()
		stored := &oauth2.Token{
			AccessToken:  "1234",
			TokenType:    "bearer",
			RefreshToken: "refresh-1234",
			Expiry:       time.Now().Add(30 * time.Minute),
		}
		stored = stored.WithExtra(map[string]any{"extra": map[string]any{
			"Email":   "myEmail",
			"Account": "myAccount",
			"ID":      "myID",
			"Role":    RolePaid,
		}})
		require.NoError(t, svc.Set(context.Background(),
			tokenDocumentID, newStoredToken(stored)))

		sourcer, _ := goodSourcer()
		source := NewCachedTokenSource(sourcer, svc)

		got := source.Cached(context.Background())
		require.NotNil(t, got)
		assertEqualToken(t, stored, got)
		assert.Equal(t, int32(0), sourcer.called.Load(),
			"Cached must not invoke the TokenSourcer")
	})

	t.Run("returns expired persisted token without refreshing", func(t *testing.T) {
		svc := storagestub.NewInMemoryService()
		expired := &oauth2.Token{
			AccessToken:  "1234",
			RefreshToken: "refresh-1234",
			Expiry:       time.Now().Add(-24 * time.Hour),
		}
		expired = expired.WithExtra(map[string]any{"extra": map[string]any{
			"Role": RolePaid,
		}})
		require.NoError(t, svc.Set(context.Background(),
			tokenDocumentID, newStoredToken(expired)))

		sourcer, _ := goodSourcer()
		source := NewCachedTokenSource(sourcer, svc)

		got := source.Cached(context.Background())
		require.NotNil(t, got)
		assert.WithinDuration(t, expired.Expiry.Truncate(time.Second),
			got.Expiry.Truncate(time.Second), 0)
		assert.Equal(t, int32(0), sourcer.called.Load())
	})
}

// TestGetTokenDoesNotBlockPurge asserts that logout (Purge) returns
// promptly while an interactive login is in flight, instead of freezing
// behind it.
func TestGetTokenDoesNotBlockPurge(t *testing.T) {
	sourcer := newBlockingSourcer()
	source := NewCachedTokenSource(sourcer, storagestub.NewInMemoryService())

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = source.Token()
	}()
	<-sourcer.started

	purged := make(chan error, 1)
	go func() { purged <- source.Purge() }()

	select {
	case err := <-purged:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		close(sourcer.release)
		t.Fatal("Purge blocked behind in-flight interactive token acquisition")
	}

	close(sourcer.release)
	<-done
}

// TestPurgeCancelsInFlightLogin asserts that logout cancels an in-flight
// login and that the cancelled login does not resurrect the cleared
// token.
func TestPurgeCancelsInFlightLogin(t *testing.T) {
	sourcer := newBlockingSourcer()
	source := NewCachedTokenSource(sourcer, storagestub.NewInMemoryService())

	loginErr := make(chan error, 1)
	go func() {
		_, err := source.Token()
		loginErr <- err
	}()
	<-sourcer.started

	require.NoError(t, source.Purge())

	select {
	case err := <-loginErr:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		close(sourcer.release)
		t.Fatal("login was not cancelled by logout")
	}

	assert.Nil(t, source.Cached(context.Background()))
}

// TestLoginRespectsContextCancellation asserts that cancelling the ctx
// passed to TokenCtx (e.g. a shell ctrl-c on the login command) aborts
// the in-flight browser flow.
func TestLoginRespectsContextCancellation(t *testing.T) {
	sourcer := newBlockingSourcer()
	source := NewCachedTokenSource(sourcer, storagestub.NewInMemoryService())

	ctx, cancel := context.WithCancel(context.Background())
	loginErr := make(chan error, 1)
	go func() {
		_, err := source.TokenCtx(ctx)
		loginErr <- err
	}()
	<-sourcer.started
	cancel()

	select {
	case err := <-loginErr:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		close(sourcer.release)
		t.Fatal("login did not observe ctx cancellation")
	}
}

func TestStorageCancellationReleasesCacheLock(t *testing.T) {
	source := NewCachedTokenSource(nil, successfulGetService{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := source.TokenCtx(ctx)
	require.ErrorIs(t, err, context.Canceled)

	done := make(chan struct{})
	go func() {
		_ = source.Purge()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cache lock remained held after cancellation")
	}
}

// TestSecondLoginSupersedesFirst asserts that cancelling the first
// login's ctx (as the caller does when a new login starts) aborts the
// in-flight first login and lets a second login proceed to completion.
func TestSecondLoginSupersedesFirst(t *testing.T) {
	sourcer := newBlockingSourcer()
	source := NewCachedTokenSource(sourcer, storagestub.NewInMemoryService())

	ctx1, cancel1 := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := source.TokenCtx(ctx1)
		first <- err
	}()
	<-sourcer.started

	// The caller supersedes the first login by cancelling its ctx, then
	// starts a second one.
	cancel1()
	select {
	case err := <-first:
		require.Error(t, err, "first login should be superseded")
	case <-time.After(2 * time.Second):
		close(sourcer.release)
		t.Fatal("first login was not superseded")
	}

	second := make(chan error, 1)
	go func() {
		_, err := source.TokenCtx(context.Background())
		second <- err
	}()
	<-sourcer.started

	close(sourcer.release)
	require.NoError(t, <-second)
}

func assertEqualToken(t *testing.T, expected, actual *oauth2.Token) {
	assert.Equal(t, expected.AccessToken, actual.AccessToken)
	assert.Equal(t, expected.TokenType, actual.TokenType)
	assert.Equal(t, expected.RefreshToken, actual.RefreshToken)
	assert.WithinDuration(t, expected.Expiry.Truncate(time.Second),
		actual.Expiry.Truncate(time.Second), 0)
	assert.Equal(t, expected.Extra("extra"), actual.Extra("extra"))
}

func badSourcer() (*testSourcer, error) {
	err := errors.New("boom")
	return &testSourcer{retErr: err}, err
}

func badSourceSourcer() (*testSourcer, error) {
	err := errors.New("boom")
	return &testSourcer{retSource: &testSource{retErr: err}}, err
}

func goodSourcer() (*testSourcer, *oauth2.Token) {
	return goodSourcerWithExpiry(1 * time.Hour)
}

func goodSourcerWithExpiry(expiry time.Duration) (*testSourcer, *oauth2.Token) {
	goodSource, token := goodSourceWithExpiry(expiry)
	return &testSourcer{retSource: goodSource}, token
}

func goodSourceWithExpiry(expiry time.Duration) (*testSource, *oauth2.Token) {
	token := &oauth2.Token{
		AccessToken:  uuid.New().String(),
		TokenType:    "bearer",
		RefreshToken: "1235",
		Expiry:       time.Now().Add(expiry),
	}
	token = token.WithExtra(map[string]any{"extra": map[string]any{
		"Email":   "myEmail",
		"Account": "myAccount",
		"ID":      "myID",
		"Role":    RoleAdmin,
	}})
	return &testSource{retToken: token}, token
}

type testSourcer struct {
	called    atomic.Int32
	retSource oauth2.TokenSource
	retErr    error
}

func (t *testSourcer) TokenSource(context.Context, *oauth2.Token) (oauth2.TokenSource, error) {
	t.called.Add(1)
	return t.retSource, t.retErr
}

type testSource struct {
	called   atomic.Int32
	retToken *oauth2.Token
	retErr   error
}

func (t *testSource) Token() (*oauth2.Token, error) {
	t.called.Add(1)
	return t.retToken, t.retErr
}

// blockingSourcer simulates an interactive browser OAuth flow: its
// source captures the acquisition ctx, signals on started each time a
// flow begins blocking, and returns when release is closed or the ctx
// is cancelled. started is buffered so sequential flows can each signal
// without a reader present.
type blockingSourcer struct {
	started chan struct{}
	release chan struct{}
	token   *oauth2.Token
}

func newBlockingSourcer() *blockingSourcer {
	_, token := goodSourcer()
	return &blockingSourcer{
		started: make(chan struct{}, 8),
		release: make(chan struct{}),
		token:   token,
	}
}

func (s *blockingSourcer) TokenSource(ctx context.Context, _ *oauth2.Token) (oauth2.TokenSource, error) {
	return blockingSource{parent: s, ctx: ctx}, nil
}

type blockingSource struct {
	parent *blockingSourcer
	ctx    context.Context
}

func (s blockingSource) Token() (*oauth2.Token, error) {
	s.parent.started <- struct{}{}
	select {
	case <-s.parent.release:
		return s.parent.token, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

type failingDocumentService struct {
}

type successfulGetService struct {
	failingDocumentService
}

func (successfulGetService) Get(context.Context, string, interface{}) error {
	return nil
}

func (failingDocumentService) Create(ctx context.Context, ID string, doc interface{}) error {
	return errors.New("oopsie")
}
func (failingDocumentService) Set(ctx context.Context, ID string, doc interface{}) error {
	return errors.New("oopsie")
}
func (failingDocumentService) Update(ctx context.Context, ID string,
	updates []storageapi.Update, precond ...storageapi.Precondition) error {
	return errors.New("oopsie")
}
func (failingDocumentService) Get(ctx context.Context, ID string, doc interface{}) error {
	return errors.New("oopsie")
}
func (failingDocumentService) Delete(ctx context.Context, ID string) error {
	return errors.New("oopsie")
}
func (failingDocumentService) List(ctx context.Context, filters []storageapi.Filter) (storageapi.Iterator, error) {
	return nil, errors.New("oopsie")
}

func (failingDocumentService) Partition(name string) (storageapi.Service, error) {
	return failingDocumentService{}, errors.New("oopsie")
}

func (failingDocumentService) Close() error {
	return nil
}
