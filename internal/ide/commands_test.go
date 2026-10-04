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

package ide

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"unstable.build/rune/internal/text/texttest"
)

func TestCommands(t *testing.T) {
	t.Run("all commands have a handler defined", func(t *testing.T) {
		for cmd, handler := range exCommands {
			assert.NotNil(t, handler.handler, cmd)
		}
	})
	t.Run("all debug commands have a handler defined", func(t *testing.T) {
		for cmd, handler := range exDebugCommands {
			assert.NotNil(t, handler.handler, cmd)
		}
	})
	t.Run("debug commands do not collide with normal commands", func(t *testing.T) {
		for name := range exDebugCommands {
			_, ok := exCommands[name]
			assert.False(t, ok,
				"debug command %q must not also be in exCommands", name)
		}
	})
}

func TestDebugCommandsSubscriptionGating(t *testing.T) {
	t.Run("disabled hides debug commands", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		win, _ := b.ex.Browser().Focus()
		// newExForTesting calls subscribeCommands() with the
		// default ex.debugCommands == false.
		for _, name := range []string{"panic", "crash", "datarace", "heapdump", "pprof"} {
			handled, err := b.ex.comp.DispatchCommand(context.Background(),
				textapi.Command{
					Name:   name,
					Window: win,
				})
			require.NoError(t, err)
			assert.False(t, handled,
				"%s must not be subscribed when debugCommands is false", name)
		}
	})

	t.Run("enabled subscribes debug commands", func(t *testing.T) {
		// Compare re-subscription error counts: enabling
		// debugCommands must surface exactly len(exDebugCommands)
		// extra "command already registered" errors on a second
		// subscribe pass. Probing through DispatchCommand is not an
		// option because the panic/crash handlers crash the process.
		off := newExForTesting(t, texttest.NopEditor())
		offErrCount := countSubscribeErrors(t, off.ex.subscribeCommands())

		on := newExForTesting(t, texttest.NopEditor())
		on.ex.debugCommands = true
		// First pass with debug on registers the gated commands.
		// newExForTesting already called subscribeCommands() with
		// debug off, so this second pass collides on exCommands
		// but installs panic and crash for the first time.
		_ = on.ex.subscribeCommands()
		// Third pass collides on exCommands AND exDebugCommands.
		onErrCount := countSubscribeErrors(t, on.ex.subscribeCommands())

		assert.Equal(t, offErrCount+len(exDebugCommands), onErrCount,
			"re-subscription must surface one error per debug command "+
				"when debugCommands is enabled")
	})
}

// countSubscribeErrors returns the number of "command already
// registered" errors wrapped by err (the multierror returned by
// subscribeCommands).
func countSubscribeErrors(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	return strings.Count(err.Error(), "command already registered")
}

func TestHeapdumpWritesFileAtRequestedPath(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor())
	out := filepath.Join(t.TempDir(), "heap.dump")

	err := b.ex.heapdump(context.Background(), out)
	require.NoError(t, err)

	info, err := os.Stat(out)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0),
		"heap dump file should be non-empty")
}

func TestHeapdumpDefaultsToTempFile(t *testing.T) {
	// Direct the default temp dir to a per-test location so we can
	// scan for the new file without interfering with concurrent
	// tests. os.CreateTemp("", ...) honors TMPDIR on macOS/Linux.
	t.Setenv("TMPDIR", t.TempDir())

	b := newExForTesting(t, texttest.NopEditor())
	err := b.ex.heapdump(context.Background())
	require.NoError(t, err)

	entries, err := os.ReadDir(os.TempDir())
	require.NoError(t, err)
	var matches []os.DirEntry
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "rune-heap-") &&
			strings.HasSuffix(e.Name(), ".dump") {
			matches = append(matches, e)
		}
	}
	require.Len(t, matches, 1, "expected exactly one rune-heap-*.dump file")
	info, err := matches[0].Info()
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))
}
