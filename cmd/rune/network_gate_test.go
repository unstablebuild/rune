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

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"unstable.build/rune/auth"
	"unstable.build/rune/cmd/rune/ide/apiclient"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/runenet"
)

type stubNetworkAPI struct {
	user       auth.RPCUser
	signedIn   bool
	statusErr  error
	controlURL string
	authKey    string
	credErr    error
	credCalls  int
	machines   []apiclient.Machine
	machineErr error
	removed    []string
	removeErr  error
}

func (s *stubNetworkAPI) AccountStatus(context.Context) (auth.RPCUser, bool, error) {
	return s.user, s.signedIn, s.statusErr
}

func (s *stubNetworkAPI) NetworkCredentials(context.Context) (string, string, error) {
	s.credCalls++
	return s.controlURL, s.authKey, s.credErr
}

func (s *stubNetworkAPI) NetworkMachines(
	context.Context,
) ([]apiclient.Machine, error) {
	return s.machines, s.machineErr
}

func (s *stubNetworkAPI) NetworkMachineRemove(_ context.Context, id string) error {
	if s.removeErr != nil {
		return s.removeErr
	}
	s.removed = append(s.removed, id)
	return nil
}

func TestNetworkGateCheck(t *testing.T) {
	tsuite := []struct {
		name string
		api  stubNetworkAPI
		want error
	}{
		{"signed out", stubNetworkAPI{}, runenet.ErrNotAuthenticated},
		{
			"undecodable token",
			stubNetworkAPI{statusErr: errors.New("bad jwt")},
			runenet.ErrNotAuthenticated,
		},
		{
			// The free plan carries the network too; how many
			// machines it covers is the server's call at mint time.
			"never subscribed",
			stubNetworkAPI{user: auth.RPCUser{Role: auth.RoleUser}, signedIn: true},
			nil,
		},
		{
			"paid",
			stubNetworkAPI{user: auth.RPCUser{Role: auth.RolePaid}, signedIn: true},
			nil,
		},
		{
			"one-off purchase",
			stubNetworkAPI{user: auth.RPCUser{Role: auth.RoleOneOff}, signedIn: true},
			nil,
		},
		{
			"admin",
			stubNetworkAPI{user: auth.RPCUser{Role: auth.RoleAdmin}, signedIn: true},
			nil,
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			api := tcase.api
			gate := newNetworkGate(&api)
			err := gate.check(context.Background())
			if tcase.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.True(t, errors.Is(err, tcase.want), "got %v", err)
		})
	}
}

func TestNetworkGateCredentials(t *testing.T) {
	t.Run("mints credentials for an entitled account", func(t *testing.T) {
		api := &stubNetworkAPI{
			user:       auth.RPCUser{Role: auth.RolePaid},
			signedIn:   true,
			controlURL: "https://control.example.com",
			authKey:    "tskey-1",
		}
		controlURL, authKey, err := newNetworkGate(api).
			credentials(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "https://control.example.com", controlURL)
		assert.Equal(t, "tskey-1", authKey)
	})

	t.Run("does not ask for credentials while signed out", func(t *testing.T) {
		api := &stubNetworkAPI{
			user:     auth.RPCUser{Role: auth.RoleUser},
			signedIn: false,
		}
		_, _, err := newNetworkGate(api).credentials(context.Background())
		assert.True(t, errors.Is(err, runenet.ErrNotAuthenticated), "got %v", err)
		assert.Zero(t, api.credCalls)
	})

	t.Run("maps the API entitlement errors", func(t *testing.T) {
		tsuite := []struct {
			name string
			err  error
			want error
		}{
			{"expired session", auth.ErrNotAuthenticated, runenet.ErrNotAuthenticated},
			{
				"plan lapsed server-side",
				apiclient.ErrSubscriptionRequired,
				runenet.ErrSubscriptionRequired,
			},
			{
				"machine allowance spent",
				apiclient.ErrMachineLimit,
				runenet.ErrMachineLimit,
			},
		}
		for _, tcase := range tsuite {
			t.Run(tcase.name, func(t *testing.T) {
				api := &stubNetworkAPI{
					user:     auth.RPCUser{Role: auth.RoleUser},
					signedIn: true,
					credErr:  tcase.err,
				}
				_, _, err := newNetworkGate(api).credentials(context.Background())
				assert.True(t, errors.Is(err, tcase.want), "got %v", err)
			})
		}
	})
}

// The user names machines, the account server works in ids, so the
// gate is what turns one into the other.
func TestNetworkGateRemove(t *testing.T) {
	t.Run("removes the machine with that name", func(t *testing.T) {
		api := &stubNetworkAPI{
			user:     auth.RPCUser{Role: auth.RoleUser},
			signedIn: true,
			machines: []apiclient.Machine{
				{ID: "1", Hostname: "laptop"},
				{ID: "2", Hostname: "desktop"},
			},
		}
		require.NoError(t, newNetworkGate(api).
			remove(context.Background(), "desktop"))
		assert.Equal(t, []string{"2"}, api.removed)
	})

	t.Run("reports a name no machine answers to", func(t *testing.T) {
		api := &stubNetworkAPI{
			user:     auth.RPCUser{Role: auth.RoleUser},
			signedIn: true,
			machines: []apiclient.Machine{{ID: "1", Hostname: "laptop"}},
		}
		err := newNetworkGate(api).remove(context.Background(), "workstation")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "workstation")
		assert.Empty(t, api.removed)
	})

	t.Run("needs a signed-in account", func(t *testing.T) {
		api := &stubNetworkAPI{}
		err := newNetworkGate(api).remove(context.Background(), "laptop")
		assert.True(t, errors.Is(err, runenet.ErrNotAuthenticated), "got %v", err)
	})
}

func TestNetworkPromptFor(t *testing.T) {
	message, options, bindings, ok := networkPromptFor(runenet.ErrNotAuthenticated)
	require.True(t, ok)
	assert.Equal(t, networkSignedOutMessage, message)
	assert.Equal(t,
		[]string{networkOptSignIn, networkOptSignUp, networkOptCancel}, options)
	assert.Len(t, bindings, len(options))

	message, options, _, ok = networkPromptFor(runenet.ErrSubscriptionRequired)
	require.True(t, ok)
	assert.Equal(t, networkUpgradeMessage, message)
	assert.Equal(t, []string{networkOptUpgrade, networkOptCancel}, options)

	message, options, _, ok = networkPromptFor(runenet.ErrMachineLimit)
	require.True(t, ok)
	assert.Equal(t, networkMachineLimitMessage, message)
	assert.Equal(t, []string{networkOptUpgrade, networkOptCancel}, options)

	// There is nothing to buy and nothing to sign into: the machine is
	// off the network because the account asked for it to be.
	message, options, bindings, ok = networkPromptFor(runenet.ErrMachineRemoved)
	require.True(t, ok)
	assert.Equal(t, networkMachineRemovedMessage, message)
	assert.Equal(t, []string{networkOptOK}, options)
	assert.Len(t, bindings, len(options))

	_, _, _, ok = networkPromptFor(errors.New("network unreachable"))
	assert.False(t, ok)
}

// The account's machine list is keyed by the coordination server's
// durable id, so that is what says whether this machine is still one
// of the account's.
func TestMachineRemoved(t *testing.T) {
	machines := []apiclient.Machine{
		{ID: "1", Hostname: "laptop"},
		{ID: "2", Hostname: "desktop"},
	}
	tsuite := []struct {
		name      string
		machineID string
		machines  []apiclient.Machine
		want      bool
	}{
		{"still listed", "1", machines, false},
		{"no longer listed", "3", machines, true},
		{"account has no machines left", "1", nil, true},
		{"never registered", "", machines, false},
		// Two machines of one account may answer to the same name;
		// only the id says which one this is.
		{"same name, different machine", "3",
			[]apiclient.Machine{{ID: "1", Hostname: "laptop"}}, true},
	}
	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			assert.Equal(t, tcase.want,
				machineRemoved(tcase.machineID, tcase.machines))
		})
	}
}

// A logout is either a node key that reached its lifetime, which is
// re-minted silently, or a machine the account removed, which the user
// has to be told about. Only the account's machine list tells them
// apart, so an unreachable account server claims neither.
func TestNetworkHandleNeedsLogin(t *testing.T) {
	setup := func(t *testing.T, api *stubNetworkAPI) (
		*network, *networkPrompter, *int,
	) {
		t.Helper()
		net := newNetwork(config.NopConfig(), t.TempDir(), "", newNetworkGate(api))
		t.Cleanup(func() { _ = net.Close() })
		prompts := 0
		return net, &networkPrompter{
			scheduleNextTick: func(func()) bool { prompts++; return true },
		}, &prompts
	}

	t.Run("prompts when the account no longer lists it", func(t *testing.T) {
		api := &stubNetworkAPI{
			user:     auth.RPCUser{Role: auth.RoleUser},
			signedIn: true,
			machines: []apiclient.Machine{{ID: "1", Hostname: "laptop"}},
		}
		net, prompter, prompts := setup(t, api)
		net.handleNeedsLogin(context.Background(), prompter, "9")
		assert.Equal(t, 1, *prompts)
		assert.Zero(t, api.credCalls)
	})

	t.Run("re-registers silently when it is still listed", func(t *testing.T) {
		api := &stubNetworkAPI{
			user:     auth.RPCUser{Role: auth.RoleUser},
			signedIn: true,
			machines: []apiclient.Machine{{ID: "1", Hostname: "laptop"}},
			credErr:  errors.New("credentials endpoint unreachable"),
		}
		net, prompter, prompts := setup(t, api)
		net.handleNeedsLogin(context.Background(), prompter, "1")
		assert.Zero(t, *prompts)
		assert.Equal(t, 1, api.credCalls)
	})

	t.Run("says nothing when the account cannot be reached", func(t *testing.T) {
		api := &stubNetworkAPI{
			user:       auth.RPCUser{Role: auth.RoleUser},
			signedIn:   true,
			machineErr: errors.New("account server unreachable"),
		}
		net, prompter, prompts := setup(t, api)
		net.handleNeedsLogin(context.Background(), prompter, "1")
		assert.Zero(t, *prompts)
		assert.Zero(t, api.credCalls)
	})
}

// `network remove` completes machine names, which runs on a keystroke.
// Machines prompts a signed-out user to sign in; MachineNames must not,
// or pressing Tab would open a modal mid-keystroke.
func TestGatedNetworkMachineNames(t *testing.T) {
	newGated := func(t *testing.T, api *stubNetworkAPI) (gatedNetwork, *int) {
		t.Helper()
		net := newNetwork(config.NopConfig(), t.TempDir(), "", newNetworkGate(api))
		t.Cleanup(func() { _ = net.Close() })
		prompts := 0
		return gatedNetwork{n: net, prompter: &networkPrompter{
			scheduleNextTick: func(func()) bool { prompts++; return true },
		}}, &prompts
	}

	t.Run("offers the account's machines", func(t *testing.T) {
		gated, prompts := newGated(t, &stubNetworkAPI{
			user:     auth.RPCUser{Role: auth.RoleUser},
			signedIn: true,
			machines: []apiclient.Machine{
				{ID: "1", Hostname: "laptop"},
				{ID: "2", Hostname: "desktop"},
			},
		})
		names, err := gated.MachineNames(context.Background())
		require.NoError(t, err)
		assert.Equal(t, []string{"laptop", "desktop"}, names)
		assert.Zero(t, *prompts)
	})

	t.Run("stays silent while signed out", func(t *testing.T) {
		gated, prompts := newGated(t, &stubNetworkAPI{})
		_, err := gated.MachineNames(context.Background())
		assert.True(t, errors.Is(err, runenet.ErrNotAuthenticated), "got %v", err)
		assert.Zero(t, *prompts)
	})

	// The same account, asked the same question by the `machines`
	// subcommand, is prompted: that one the user asked for.
	t.Run("Machines still prompts", func(t *testing.T) {
		gated, prompts := newGated(t, &stubNetworkAPI{})
		_, err := gated.Machines(context.Background())
		require.Error(t, err)
		assert.Equal(t, 1, *prompts)
	})
}

// The gate, prompter, and network take only mandatory dependencies. A
// nil is a wiring bug that must crash at construction, never a state
// the code quietly tolerates.
func TestNetworkNilDependenciesPanic(t *testing.T) {
	assert.Panics(t, func() { newNetworkGate(nil) })
	assert.Panics(t, func() {
		newNetwork(config.NopConfig(), t.TempDir(), "", nil)
	})
	assert.Panics(t, func() {
		newNetworkPrompter(nil, func(func()) bool { return true })
	})
	assert.Panics(t, func() { newNetworkPrompter(&ide.IDE{}, nil) })
}

// The stack is fully assembled whatever the configuration says —
// auto_join only decides whether boot joins — so `network up` can
// join later without restarting Rune.
func TestNewNetworkAlwaysAssembles(t *testing.T) {
	tsuite := []struct {
		name string
		cfg  map[string]any
	}{
		{"no network section", map[string]any{}},
		{"auto_join off", map[string]any{
			"network": map[string]any{"auto_join": false},
		}},
		{"malformed section falls back to defaults", map[string]any{
			"network": map[string]any{"port": "not-a-number"},
		}},
	}
	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			net := newNetwork(config.MapConfig(tcase.cfg), t.TempDir(), "",
				newNetworkGate(&stubNetworkAPI{}))
			t.Cleanup(func() { _ = net.Close() })
			require.NotNil(t, net.node)
			require.NotNil(t, net.gate)
			assert.False(t, net.autoJoin)
		})
	}
}
