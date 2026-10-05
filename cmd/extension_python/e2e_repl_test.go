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

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

func TestE2E_Scenarios_REPL(t *testing.T) {
	findUV(t)

	for _, name := range allScenarios {
		t.Run(name, func(t *testing.T) {
			env := runExtensionOnScenario(t, name)
			handler := &pyHandler{
				exec:   newDirExecutor(env.dir),
				notify: newFakeNotifications(),
				cwd:    env.dir,
			}

			t.Run("pip list shows installed dependencies", func(t *testing.T) {
				out := strings.ToLower(runREPL(t, handler, "pip", "list"))
				assert.Contains(t, out, "numpy",
					"the synced env should expose the numpy dependency")
				assert.Contains(t, out, "requests",
					"the synced env should expose the requests dependency")
			})

			t.Run("run executes against synced env", func(t *testing.T) {
				out := runREPL(t, handler, "run", "python", "-c",
					"import numpy; print(int(numpy.arange(5).sum()))")
				assert.Contains(t, out, "10")
			})

			t.Run("pip show resolves a compiled dependency", func(t *testing.T) {
				out := strings.ToLower(runREPL(t, handler, "pip", "show", "numpy"))
				assert.Contains(t, out, "name: numpy")
			})
		})
	}
}

// runREPL dispatches a `python` subcommand through the real handler,
// verifying the full public path produces exactly one responsive
// result, then returns the raw uv output the handler captured for
// content assertions. Driving HandleCommand exercises the routing and
// argument expansion; runUVCapture reads back the same output without
// having to render the markdown component to a terminal.
func runREPL(t *testing.T, h *pyHandler, args ...string) string {
	t.Helper()
	it, err := h.HandleCommand(
		context.Background(),
		repl.Command{Name: pyCommandName, Args: args},
		repl.NopProgressWriter(),
	)
	require.NoError(t, err)
	results, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.Len(t, results, 1)

	prefix := uvRoutes[args[0]]
	expanded := append(append([]string{}, prefix...), args[1:]...)
	out, err := h.runUVCapture(context.Background(), expanded...)
	require.NoError(t, err)
	return out
}
