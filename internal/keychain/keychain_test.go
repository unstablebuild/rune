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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// The go-keyring mock swaps a package-level provider and is not safe
// for concurrent use, so this test must not run in parallel.
func TestSystemRoundTrip(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { keyring.MockInitWithError(errors.New("unset")) })
	kr := System()

	_, err := kr.Get(t.Context(), Service, "user")
	assert.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, kr.Set(t.Context(), Service, "user", "secret"))
	got, err := kr.Get(t.Context(), Service, "user")
	require.NoError(t, err)
	assert.Equal(t, "secret", got)

	require.NoError(t, kr.Delete(t.Context(), Service, "user"))
	assert.ErrorIs(t, kr.Delete(t.Context(), Service, "user"), ErrNotFound)
}

func TestCallAbandonsUnresponsiveStore(t *testing.T) {
	tests := []struct {
		name    string
		ctx     func(t *testing.T) context.Context
		timeout time.Duration
	}{
		{
			name:    "timeout",
			ctx:     func(t *testing.T) context.Context { return t.Context() },
			timeout: 10 * time.Millisecond,
		},
		{
			name: "caller cancels",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				return ctx
			},
			timeout: time.Hour,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			release := make(chan struct{})
			defer close(release)

			_, err := call(tt.ctx(t), tt.timeout, func() (string, error) {
				<-release
				return "late", nil
			})

			require.Error(t, err)
		})
	}
}
