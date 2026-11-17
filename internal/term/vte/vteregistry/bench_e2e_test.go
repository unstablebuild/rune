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

package vteregistry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtebench"
)

// TestVTEBench measures a terminal owned in this process by a Registry
// against one opened directly, which is what the registry must cost
// nothing over.
func TestVTEBench(t *testing.T) {
	loop := vtebench.NewLoop(t)
	scheme := vtebench.LocalScheme(t)

	direct := vtebench.Path{
		Name: "direct",
		Open: func(t *testing.T, cmd []string) *vtebench.VTE {
			host := vtebench.NewHost()
			h, err := vte.NewHandler(host, host, scheme, scheme, host,
				vtebench.Config(loop, cmd))
			require.NoError(t, err)
			loop.Schedule(func() { h.Resize(vtebench.Width, vtebench.Height) })
			return &vtebench.VTE{Comp: h.Component(), Host: host, Close: func() { _ = h.Close() }}
		},
	}

	registryHost := vtebench.NewHost()
	r := New(registryHost, registryHost, scheme, scheme, registryHost, Config{
		VTE: vtebench.Config(loop, nil),
	})
	t.Cleanup(func() { _ = r.Close() })
	registry := vtebench.Path{
		Name: "registry",
		Open: func(t *testing.T, cmd []string) *vtebench.VTE {
			v, err := r.CreateVTE(context.Background(), vte.CreateOptions{
				CommandAndArgs: cmd, Width: vtebench.Width, Height: vtebench.Height,
			})
			require.NoError(t, err)
			a := v.(*attachment)
			loop.Schedule(func() { a.Resize(vtebench.Width, vtebench.Height) })
			return &vtebench.VTE{
				Comp: a.c.h.Component(), Host: registryHost,
				Close: func() { _ = r.DeleteVTE(context.Background(), a.ID()) },
			}
		},
	}

	vtebench.Run(t, []vtebench.Path{direct, registry})
}
