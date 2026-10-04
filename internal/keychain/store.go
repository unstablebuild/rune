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

package keychain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
)

// Service is the service Store files its items under in the OS
// credential store.
const Service = "Rune"

// Storage is the document storage a Store falls back to. Get must
// return an error wrapping storageapi.ErrNotFound for a missing ID.
type Storage interface {
	Get(ctx context.Context, ID string, doc any) error
	Set(ctx context.Context, ID string, doc any) error
	Delete(ctx context.Context, ID string) error
}

// Store keeps JSON-encodable documents in a Keyring, and in a fallback
// Storage while the keyring cannot be used: a Linux server with no
// Secret Service, a macOS Keychain locked to an SSH session, or a
// document over the platform's item size limit. After the first keyring
// failure, the Store reads and writes only the fallback for the rest of
// its life.
//
// A document is never read from the keyring while the fallback holds a
// copy of it. The fallback only ever holds a copy written before the
// keyring was first used, or one written after the keyring failed, so
// its copy is never the older of the two. A copy found there moves to
// the keyring the first time the keyring accepts it.
type Store struct {
	keyring  Keyring
	scope    string
	fallback Storage
	failed   atomic.Bool
}

// NewStore returns a Store that keeps documents in keyring, under
// accounts prefixed with scope, and in fallback when keyring fails.
// scope keeps installations that do not share a fallback, such as two
// data directories, from reading each other's items.
func NewStore(keyring Keyring, scope string, fallback Storage) *Store {
	if keyring == nil || fallback == nil {
		panic("keychain.NewStore: keyring and fallback must not be nil")
	}
	return &Store{keyring: keyring, scope: scope, fallback: fallback}
}

// Get decodes the document stored under ID into doc. It returns an
// error wrapping storageapi.ErrNotFound when neither the keyring nor
// the fallback holds one.
func (s *Store) Get(ctx context.Context, ID string, doc any) error {
	err := s.fallback.Get(ctx, ID, doc)
	if err == nil {
		s.migrate(ctx, ID, doc)
		return nil
	}
	// Unless the fallback is known not to hold a copy, the keyring's
	// may be the stale one.
	if !errors.Is(err, storageapi.ErrNotFound) || s.failed.Load() {
		return err
	}
	secret, err := s.keyring.Get(ctx, Service, s.account(ID))
	if errors.Is(err, ErrNotFound) {
		return storageapi.ErrNotFound
	}
	if err != nil {
		s.fail(ctx, err)
		return fmt.Errorf("keychain: get %s: %w", ID, err)
	}
	if err := json.Unmarshal([]byte(secret), doc); err != nil {
		return fmt.Errorf("keychain: decode %s: %w", ID, err)
	}
	return nil
}

// Set stores doc under ID, replacing any previous document.
func (s *Store) Set(ctx context.Context, ID string, doc any) error {
	if !s.failed.Load() && s.setKeyring(ctx, ID, doc) == nil {
		err := s.fallback.Delete(ctx, ID)
		if err == nil || errors.Is(err, storageapi.ErrNotFound) {
			return nil
		}
		// The fallback copy shadows the keyring's, so it must not be
		// left behind older than it.
	}
	return s.fallback.Set(ctx, ID, doc)
}

// Delete removes the document stored under ID from both the keyring
// and the fallback. Removing a missing document is not an error.
//
// The keyring is tried even after it has failed: a copy left behind
// there would sign a signed-out user back in as soon as no fallback
// copy shadows it.
func (s *Store) Delete(ctx context.Context, ID string) error {
	var errs []error
	err := s.fallback.Delete(ctx, ID)
	if err != nil && !errors.Is(err, storageapi.ErrNotFound) {
		errs = append(errs, err)
	}
	err = s.keyring.Delete(ctx, Service, s.account(ID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		s.fail(ctx, err)
		errs = append(errs, fmt.Errorf("keychain: delete %s: %w", ID, err))
	}
	return errors.Join(errs...)
}

// migrate moves a document found in the fallback into the keyring. If
// the fallback copy cannot be removed afterwards, the two copies are
// identical and the fallback one goes on shadowing the keyring's.
func (s *Store) migrate(ctx context.Context, ID string, doc any) {
	if s.failed.Load() || s.setKeyring(ctx, ID, doc) != nil {
		return
	}
	_ = s.fallback.Delete(ctx, ID)
}

func (s *Store) setKeyring(ctx context.Context, ID string, doc any) error {
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("keychain: encode %s: %w", ID, err)
	}
	if err := s.keyring.Set(ctx, Service, s.account(ID), string(data)); err != nil {
		s.fail(ctx, err)
		return err
	}
	return nil
}

// fail stops writes from going to the keyring. A call cut short by its
// caller says nothing about the keyring, so it does not count.
func (s *Store) fail(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	if s.failed.CompareAndSwap(false, true) {
		log.WithField(logging.KeyClass, "keychain.Store").
			Infof("cannot use the OS keychain, keeping secrets in the "+
				"data directory instead: %v", err)
	}
}

func (s *Store) account(ID string) string {
	return s.scope + "/" + ID
}
