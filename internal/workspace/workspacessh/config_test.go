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

package workspacessh

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
)

func TestSkipPreflightDefaultsFalse(t *testing.T) {
	cfg, err := fromConfig(config.MapConfig(map[string]any{}))
	require.NoError(t, err)
	assert.False(t, cfg.skipPreflight)
}

func TestSkipPreflightExplicitTrue(t *testing.T) {
	cfg, err := fromConfig(config.MapConfig(map[string]any{
		"skip_preflight": true,
	}))
	require.NoError(t, err)
	assert.True(t, cfg.skipPreflight)
}

func TestSkipPreflightExplicitFalse(t *testing.T) {
	cfg, err := fromConfig(config.MapConfig(map[string]any{
		"skip_preflight": false,
	}))
	require.NoError(t, err)
	assert.False(t, cfg.skipPreflight)
}

func TestSkipPreflightWrongTypeErrors(t *testing.T) {
	_, err := fromConfig(config.MapConfig(map[string]any{
		"skip_preflight": "yes",
	}))
	require.Error(t, err)
}

func TestProvisionPackagesDefaultsTrue(t *testing.T) {
	cfg, err := fromConfig(config.MapConfig(map[string]any{}))
	require.NoError(t, err)
	assert.True(t, cfg.provisionPackages,
		"remote package provisioning must be on by default")
}

func TestProvisionPackagesExplicitFalse(t *testing.T) {
	cfg, err := fromConfig(config.MapConfig(map[string]any{
		"provision_packages": false,
	}))
	require.NoError(t, err)
	assert.False(t, cfg.provisionPackages)
}

func TestProvisionPackagesExplicitTrue(t *testing.T) {
	cfg, err := fromConfig(config.MapConfig(map[string]any{
		"provision_packages": true,
	}))
	require.NoError(t, err)
	assert.True(t, cfg.provisionPackages)
}

func TestProvisionPackagesWrongTypeErrors(t *testing.T) {
	_, err := fromConfig(config.MapConfig(map[string]any{
		"provision_packages": "yes",
	}))
	require.Error(t, err)
}

func TestStrictHostKeyCheckingDefaultsTrue(t *testing.T) {
	cfg, err := fromConfig(config.MapConfig(map[string]any{}))
	require.NoError(t, err)
	assert.True(t, cfg.strictHostKeyChecking,
		"strict host key checking must be on by default")
}

func TestStrictHostKeyCheckingExplicitTrue(t *testing.T) {
	cfg, err := fromConfig(config.MapConfig(map[string]any{
		"strict_host_key_checking": true,
	}))
	require.NoError(t, err)
	assert.True(t, cfg.strictHostKeyChecking)
}

func TestStrictHostKeyCheckingExplicitFalse(t *testing.T) {
	cfg, err := fromConfig(config.MapConfig(map[string]any{
		"strict_host_key_checking": false,
	}))
	require.NoError(t, err)
	assert.False(t, cfg.strictHostKeyChecking)
}

func TestStrictHostKeyCheckingWrongTypeErrors(t *testing.T) {
	_, err := fromConfig(config.MapConfig(map[string]any{
		"strict_host_key_checking": "yes",
	}))
	require.Error(t, err)
}

func TestPrivateKeysExpandEnv(t *testing.T) {
	t.Setenv("RUNE_TEST_SSH_KEYS", "/keys")
	cfg, err := fromConfig(config.MapConfig(map[string]any{
		"private_keys": []any{"$RUNE_TEST_SSH_KEYS/id_ed25519", "~/.ssh/id_rsa"},
	}))
	require.NoError(t, err)
	assert.Equal(t, []string{"/keys/id_ed25519", "~/.ssh/id_rsa"}, cfg.privateKeys)
}
