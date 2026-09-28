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

package loginshell

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/auth"
	"unstable.build/rune/cmd/rune/ide/apiclient"
)

type fakeClient struct {
	loginURL  string
	loginErr  error
	logoutErr error
	logoutHit bool
	account   auth.RPCUser
	hasToken  bool
	statusErr error
}

func (c *fakeClient) Login(context.Context) apiclient.LoginSession {
	urlCh := make(chan *url.URL, 1)
	done := make(chan error, 1)
	if c.loginURL != "" {
		u, _ := url.Parse(c.loginURL)
		urlCh <- u
	}
	close(urlCh)
	done <- c.loginErr
	close(done)
	return apiclient.LoginSession{URL: urlCh, Done: done}
}

func (c *fakeClient) Logout(context.Context) error {
	c.logoutHit = true
	return c.logoutErr
}

func (c *fakeClient) AccountStatus(context.Context) (auth.RPCUser, bool, error) {
	return c.account, c.hasToken, c.statusErr
}

func TestLoginStreamsURLThenSuccess(t *testing.T) {
	t.Parallel()

	_, h := Login(&fakeClient{
		loginURL: "https://auth.example.com/oauth?code=abc",
		hasToken: true,
	})
	out := runCommand(t, h)

	require.Len(t, out, 2)
	assert.Contains(t, out[0], "auth.example.com/oauth")
	assert.Contains(t, out[1], "Login successful")
}

func TestLoginRendersPaidAccountStatus(t *testing.T) {
	t.Parallel()

	_, h := Login(&fakeClient{
		loginURL: "https://auth.example.com/oauth",
		hasToken: true,
		account: auth.RPCUser{
			Email:    "user@example.com",
			Role:     auth.RolePaid,
			PlanEnds: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		},
	})
	out := runCommand(t, h)

	require.Len(t, out, 2)
	assert.Contains(t, out[1], "Login successful")
	assert.Contains(t, out[1], "user@example.com")
	assert.Contains(t, out[1], "Plan: Rune Pro")
	assert.Contains(t, out[1], "2026-07-01")
}

func TestLoginRendersFreeAccountStatus(t *testing.T) {
	t.Parallel()

	_, h := Login(&fakeClient{
		loginURL: "https://auth.example.com/oauth",
		hasToken: true,
		account:  auth.RPCUser{Email: "free@example.com", Role: auth.RoleUser},
	})
	out := runCommand(t, h)

	require.Len(t, out, 2)
	assert.Contains(t, out[1], "free@example.com")
	assert.Contains(t, out[1],
		"Upgrade to Rune Pro to access Rune networking features and premium support")
}

func TestPlanLabel(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		role auth.Role
		want string
	}{
		{"never subscribed", auth.RoleUser, "Rune"},
		{"basic", auth.RoleBasic, "Rune"},
		{"paid", auth.RolePaid, "Rune Pro"},
		{"one-off", auth.RoleOneOff, "One-off"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, planLabel(tt.role))
		})
	}
}

func TestLoginReportsFailure(t *testing.T) {
	t.Parallel()

	_, h := Login(&fakeClient{
		loginURL: "https://auth.example.com/oauth",
		loginErr: errors.New("boom"),
	})
	out := runCommand(t, h)

	require.Len(t, out, 2)
	assert.Contains(t, out[0], "auth.example.com/oauth")
	assert.Contains(t, out[1], "Login did not complete")
	assert.Contains(t, out[1], "boom")
}

func TestLoginAccountStatusFailures(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		client  *fakeClient
		wantErr string
	}{
		{
			name: "unreadable token after login",
			client: &fakeClient{
				loginURL:  "https://auth.example.com/oauth",
				statusErr: errors.New("jwt: decode payload"),
			},
			wantErr: "jwt: decode payload",
		},
		{
			name:    "login that stores no token",
			client:  &fakeClient{loginURL: "https://auth.example.com/oauth"},
			wantErr: "no account token",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, h := Login(tt.client)
			it, err := h.HandleCommand(context.Background(),
				repl.Command{}, repl.NopProgressWriter())
			require.NoError(t, err)
			items, err := sdkiterator.ToSlice(context.Background(), it)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			for _, s := range responsiveStrings(t, items) {
				assert.NotContains(t, s, "Login successful")
			}
		})
	}
}

func TestLogoutReportsSuccess(t *testing.T) {
	t.Parallel()

	c := &fakeClient{}
	_, h := Logout(c)
	out := runCommand(t, h)

	assert.True(t, c.logoutHit)
	require.Len(t, out, 1)
	assert.Contains(t, out[0], "Logged out")
}

func TestLogoutPropagatesError(t *testing.T) {
	t.Parallel()

	_, h := Logout(&fakeClient{logoutErr: errors.New("nope")})
	_, err := h.HandleCommand(context.Background(), repl.Command{Name: "logout"},
		repl.NopProgressWriter())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")
}

func runCommand(t *testing.T, h textapi.REPLHandler) []string {
	t.Helper()
	it, err := h.HandleCommand(context.Background(),
		repl.Command{}, repl.NopProgressWriter())
	require.NoError(t, err)
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	return responsiveStrings(t, items)
}

func responsiveStrings(t *testing.T, items []component.Responsive) []string {
	t.Helper()
	out := make([]string, 0, len(items))
	for _, item := range items {
		width := 120
		height := item.Height(width)
		if height <= 0 {
			height = 1
		}
		writer := term.NewStringWriter(width, height)
		item.Resize(width, height)
		item.Draw(writer)
		require.NoError(t, writer.Flush())
		out = append(out, strings.TrimSpace(writer.String()))
	}
	return out
}
