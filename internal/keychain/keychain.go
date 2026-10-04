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

// Package keychain keeps secrets in the operating system's credential
// store, so they are encrypted at rest and stay out of the data
// directory and everything that copies it: backups, synced dotfiles,
// container volumes and machine images.
package keychain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zalando/go-keyring"
	"unstable.build/rune/internal/debug"
)

// ErrNotFound is returned by a Keyring that holds no secret for the
// requested service and user.
var ErrNotFound = errors.New("secret not found in keychain")

// Keyring stores secrets, each identified by a service and a user.
type Keyring interface {
	Get(ctx context.Context, service, user string) (string, error)
	Set(ctx context.Context, service, user, secret string) error
	Delete(ctx context.Context, service, user string) error
}

// systemTimeout bounds a single call into the OS credential store. A
// Secret Service waiting on an unlock prompt nobody is there to answer
// would otherwise block its caller forever.
const systemTimeout = time.Second

// System returns the operating system's credential store: the login
// Keychain on macOS, the Secret Service on Linux and the BSDs, and the
// Credential Manager on Windows. A call fails rather than blocks when
// the store does not answer within a second or ctx ends first.
func System() Keyring {
	return systemKeyring{}
}

type systemKeyring struct{}

func (systemKeyring) Get(ctx context.Context, service, user string) (string, error) {
	return call(ctx, systemTimeout, func() (string, error) {
		return keyring.Get(service, user)
	})
}

func (systemKeyring) Set(ctx context.Context, service, user, secret string) error {
	_, err := call(ctx, systemTimeout, func() (struct{}, error) {
		return struct{}{}, keyring.Set(service, user, secret)
	})
	return err
}

func (systemKeyring) Delete(ctx context.Context, service, user string) error {
	_, err := call(ctx, systemTimeout, func() (struct{}, error) {
		return struct{}{}, keyring.Delete(service, user)
	})
	return err
}

// call runs fn off the caller's goroutine so it can be abandoned: the
// credential store APIs take no context. An abandoned fn finishes, or
// stays blocked, on its own.
func call[T any](
	ctx context.Context, timeout time.Duration, fn func() (T, error),
) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go debug.CapturePanicReport(func() {
		value, err := fn()
		done <- result{value: value, err: err}
	})

	select {
	case r := <-done:
		if errors.Is(r.err, keyring.ErrNotFound) {
			r.err = ErrNotFound
		}
		return r.value, r.err
	case <-ctx.Done():
		var zero T
		return zero, fmt.Errorf("keychain did not answer: %w", ctx.Err())
	}
}
