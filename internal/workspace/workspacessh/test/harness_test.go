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

//go:build e2e

package workspacetest

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareRuneBinaryOutputRemovesStaleDirectory(t *testing.T) {
	out := filepath.Join(t.TempDir(), "runesvc-linux-arm64")
	require.NoError(t, os.MkdirAll(out, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(out, "runesvc"), []byte("stale"), 0o755))

	require.NoError(t, prepareRuneBinaryOutput(out))
	_, err := os.Lstat(out)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestContainerHelper(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)

	scenarios := []SSHDScenario{
		{Name: "pubkey", PublicKeyFile: "/id_ed25519.pub"},
		{Name: "password", PasswordAccess: true, UserPassword: "hunter2"},
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		ports = make(map[string]string)
	)
	for _, s := range scenarios {
		wg.Add(1)
		go func(s SSHDScenario) {
			defer wg.Done()
			c := StartContainer(t, s)
			mu.Lock()
			ports[s.Name] = c.HostPort
			mu.Unlock()
		}(s)
	}
	wg.Wait()
	assert.Len(t, ports, 2)
	assert.NotEqual(t, ports["pubkey"], ports["password"])
}
