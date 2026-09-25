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
	"fmt"

	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/vi"
	"unstable.build/rune/internal/workspace"
)

func newIntegrationTestCase(t *testing.T, content string) (
	*cell.Buffer, workspace.FlusherCloser, workspaceapi.URI, func(),
) {
	tempDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)

	workspaceURI, err := workspaceapi.CurrentUserHostURI(tempDir)
	require.NoError(t, err)

	mu := new(sync.Mutex)
	sched, drainSched := newTestScheduler(t, mu)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.FileScheme, workspace.NewFileScheme))
	w, err := manager.AddWorkspace(context.Background(), workspaceURI)
	require.NoError(t, err)

	file, err := os.CreateTemp(tempDir, "workspace_int_test")
	require.NoError(t, err)

	_, err = file.Write([]byte(content))
	require.NoError(t, err)

	uri, err := workspaceapi.CurrentUserHostURI(file.Name())
	require.NoError(t, err)

	swapURI, err := workspaceapi.CurrentUserHostURI(tempDir)
	require.NoError(t, err)

	buffer := cell.NewBuffer()
	fc, err := w.Load(uri, buffer, swapURI, false)
	require.NoError(t, err)
	drainSched()

	return buffer, fc, uri, func() {
		manager.Close()
		file.Close()
		os.Remove(file.Name())
		fc.Close()
	}
}

func newViIntegrationTestCase(
	t *testing.T, content string, width, height int,
) (*cell.Buffer, tui.Handler, func()) {
	buf, _, uri, clean := newIntegrationTestCase(t, content)
	defer clean()

	vi := vi.NewWithIndent(buf, uri, text.IndentRuneTab, 0)
	vi.Resize(width, height)
	return buf, vi, clean
}

func TestLastEOLUndoFileIntegration(t *testing.T) {
	buf, _, _, cleanup := newIntegrationTestCase(t, "a\n")
	defer cleanup()

	initialString := buf.String()
	initialCells := buf.RawCells()
	assert.Equal(t, "a", initialString)
	assert.Equal(t,
		[][]term.Cell{{{Ch: 'a', Bytes: 1, Width: 1}}}, initialCells)

	at := term.Coordinates{Y: 1}
	buf.Edit(context.Background(), at, at, "\n")
	newString := buf.String()
	newCells := buf.RawCells()
	assert.Equal(t, "a\n", newString)
	assert.Equal(t,
		[][]term.Cell{{{Ch: 'a', Bytes: 1, Width: 1}}, {}}, newCells)

	ok, _ := buf.Undo()
	assert.True(t, ok)
	newString2 := buf.String()
	newCells2 := buf.RawCells()
	assert.Equal(t, initialString, newString2)
	assert.Equal(t, initialCells, newCells2)

	buf.Edit(context.Background(), at, at, "\n")
	newString = buf.String()
	newCells = buf.RawCells()
	assert.Equal(t, "a\n", newString)
	assert.Equal(t,
		[][]term.Cell{{{Ch: 'a', Bytes: 1, Width: 1}}, {}}, newCells)
}

func TestViIntegration(t *testing.T) {
	t.Run("last EOL", func(t *testing.T) {
		t.Parallel()

		for _, content := range []string{"hello", "hello\n"} {
			t.Run(fmt.Sprintf("insert word below last line: %q", content), func(t *testing.T) {
				t.Parallel()

				buf, vi, cleanup := newViIntegrationTestCase(t, content, 4, 4)
				defer cleanup()

				for _, ch := range "Goworld" {
					vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
				}
				vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
				assert.Equal(t, "hello\nworld", buf.String())

				// undo
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'u'})
				require.True(t, handled)
				require.Equal(t, "hello", buf.String())

				for _, ch := range "Goworld" {
					vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
				}
				vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
				assert.Equal(t, "hello\nworld", buf.String())
			})

			t.Run(fmt.Sprintf("insert a newline last line: %q", content), func(t *testing.T) {
				t.Parallel()

				buf, vi, clean := newViIntegrationTestCase(t, content, 4, 4)
				defer clean()

				for _, ch := range "Go\n" {
					vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
				}
				vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
				assert.Equal(t, "hello\n\n", buf.String())

				// undo
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'u'})
				require.True(t, handled)
				require.Equal(t, "hello", buf.String())

				for _, ch := range "Go\n" {
					vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
				}
				vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
				assert.Equal(t, "hello\n\n", buf.String())
			})
		}
	})

	t.Run("line select paste on last EOL", func(t *testing.T) {
		t.Parallel()

		buf, vi, clean := newViIntegrationTestCase(t, "a\nb\nc\nd\n", 4, 4)
		defer clean()

		for _, ch := range "Gkyyp" {
			_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			require.True(t, handled)
		}

		require.Equal(t, "a\nb\nc\nc\nd", buf.String())
	})

	t.Run("line select copy last line after", func(t *testing.T) {
		t.Parallel()

		buf, vi, clean := newViIntegrationTestCase(t, "a\nb\nc\nd\n", 4, 4)
		defer clean()

		for _, ch := range "Gyyggp" {
			_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			require.True(t, handled)
		}

		require.Equal(t, "a\nd\nb\nc\nd", buf.String())
	})

	t.Run("paste line after last line", func(t *testing.T) {
		t.Parallel()

		buf, vi, clean := newViIntegrationTestCase(t, "a\nb\nc\nd\n", 2, 2)
		defer clean()

		for _, ch := range "VjyGp" {
			_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			require.True(t, handled)
		}

		require.Equal(t, "a\nb\nc\nd\na\nb", buf.String())
	})

	t.Run("delete last empty line", func(t *testing.T) {
		t.Parallel()

		buf, vi, clean := newViIntegrationTestCase(t, "a\nb\nc\nd\n\n", 4, 4)
		defer clean()

		for _, ch := range "Gdd" {
			_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			require.True(t, handled)
		}

		require.Equal(t, "a\nb\nc\nd", buf.String())
	})

	t.Run("delete last empty line and second to last", func(t *testing.T) {
		t.Parallel()

		buf, vi, clean := newViIntegrationTestCase(t, "a\nb\nc\nd\n\n", 4, 4)
		defer clean()

		for _, ch := range "Gdddd" {
			_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			require.True(t, handled)
		}

		require.Equal(t, "a\nb\nc", buf.String())
	})

	t.Run("delete any empty line", func(t *testing.T) {
		t.Parallel()

		buf, vi, clean := newViIntegrationTestCase(t, "a\n\nc", 4, 4)
		defer clean()

		for _, ch := range "jdd" {
			_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			require.True(t, handled)
		}

		require.Equal(t, "a\nc", buf.String())
	})

	t.Run("delete only newline in visual mode", func(t *testing.T) {
		t.Parallel()

		buf, vi, clean := newViIntegrationTestCase(t, "a\n\nc", 4, 4)
		defer clean()

		for _, ch := range "jvkld" {
			_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			require.True(t, handled)
		}

		require.Equal(t, "a\nc", buf.String())
	})
}
