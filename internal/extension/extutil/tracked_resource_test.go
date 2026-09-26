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

package extutil

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/standard"
	"unstable.build/rune/internal/workspace"
)

func TestResourceTrackerIntegration(t *testing.T) {
	suite := []struct {
		description string
		evs         []textapi.EventType
	}{
		{"complete event set", ResourceTrackerEventsComplete()},
		{"content changes only", ResourceTrackerEventsContent()},
		{"flushed content changes only", ResourceTrackerEventsFlushOnly()},
		{"content changes with scroll", append(ResourceTrackerEventsContent(), textapi.EventTypeScroll)},
	}
	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			tabspaces := 4
			// The key events below are the macOS chords.
			simpleEd := standard.Editor(standard.WithHostMetaChords(true))

			cfg := text.DefaultConfig()

			cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
			cfg.Tabspaces = tabspaces

			cwd := makeURI(t, "memory:///")

			scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), cwd)
			require.NoError(t, err)
			loader := workspace.NewSchemeWorkspace(cwd, scheme, inlineSchedule)

			ed, err := text.NewComponent(simpleEd, loader, cfg)
			require.NoError(t, err)

			tracker := NewResourceTracker(tabspaces, false)
			require.NoError(t, ed.SubscribeEvents(test.evs, tracker))

			ed.Resize(8, 7)

			res1 := makeURI(t, "memory:///1")

			// sut
			var bh browserapi.Handler
			var edh text.Handler
			t.Run("Edit on editor is tracked by tracker", func(t *testing.T) {
				bh, err = ed.OpenFileTab(res1, false)
				require.NoError(t, err)

				res, ok := tracker.Resource(res1)
				require.True(t, ok)

				assert.Equal(t, false, res.Scroll.Wrap)
				assert.Equal(t, tabspaces, res.Scroll.Tabspaces())

				edh, err = ed.Editor(res1)
				require.NoError(t, err)

				assert.Equal(t, "memory:///1", res.URI().String())
				assert.Equal(t, "", res.Scroll.Buffer().String())
				// not in focus yet, so propagated scroll width, height is 0
				assert.Equal(t, 0, res.Scroll.Width())
				assert.Equal(t, 0, res.Scroll.SizeHeight())
			})

			t.Run("switching focus to content propagates width, height", func(t *testing.T) {
				win, err := ed.Focus()
				require.NoError(t, err)
				require.NoError(t, win.SetContent(bh))

				res, ok := tracker.Resource(res1)
				require.True(t, ok)

				assert.Equal(t, 6, res.Scroll.Width())
				assert.Equal(t, 3, res.Scroll.SizeHeight())
			})

			t.Run("Focus returns last resource in focus", func(t *testing.T) {
				res, ok := tracker.Focus()
				require.True(t, ok)

				assert.Equal(t, res1, res.URI())
			})

			if setHasType(textapi.EventTypeEdit, test.evs) {
				t.Run("updates to buffer are replicated to resource", func(t *testing.T) {
					res, ok := tracker.Resource(res1)
					require.True(t, ok)

					edh.CellEditor().
						Edit(context.Background(), term.Coordinates{}, term.Coordinates{},
							"abcdefghi\n1234\nXXXX\nX\nX\nX\nX\nX\nX")

					assert.Equal(t, "abcdefghi\n1234\nXXXX\nX\nX\nX\nX\nX\nX",
						res.Scroll.Buffer().String())

					winpos, ok := res.WindowCoordinates(term.Coordinates{})
					require.True(t, ok)
					assert.NotPanics(t, func() {
						assert.Equal(t, term.Coordinates{}, res.ContentCoordinates(term.Coordinates{}))
						assert.Equal(t, term.Coordinates{}, winpos)
					})
				})
			}

			if setHasType(textapi.EventTypeScroll, test.evs) &&
				setHasType(textapi.EventTypeEdit, test.evs) {
				t.Run("scroll position is replicated to resource", func(t *testing.T) {
					_, handled := bh.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Key: term.KeyArrowUp})
					require.True(t, handled)
					for handled {
						_, handled = bh.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
					}

					res, ok := tracker.Resource(res1)
					require.True(t, ok)

					winPos, ok := res.WindowCoordinates(term.Coordinates{})
					require.True(t, ok)
					assert.Equal(t, term.Coordinates{Y: 6}, res.Offset())
					assert.Equal(t, term.Coordinates{Y: 6}, res.ContentCoordinates(term.Coordinates{}))
					assert.Equal(t, term.Coordinates{Y: -6}, winPos)
					assert.NotPanics(t, func() {
						_ = res.Cursor()
					})
				})
			}

			if setHasType(textapi.EventTypeCursor, test.evs) &&
				setHasType(textapi.EventTypeScroll, test.evs) &&
				setHasType(textapi.EventTypeEdit, test.evs) {
				t.Run("cursor position is replicated to resource", func(t *testing.T) {
					res, ok := tracker.Resource(res1)
					require.True(t, ok)

					cur := edh.CursorAtScroll()
					require.NoError(t, err)
					assert.Equal(t, term.Coordinates{Y: 8, X: 1}, cur)
					assert.Equal(t, term.Coordinates{Y: 8, X: 1}, res.Cursor())

					edh.SetCursorAtScroll(term.Coordinates{Y: 2})
					assert.Equal(t, term.Coordinates{Y: 2}, res.Cursor())
				})

			}

			if setHasType(textapi.EventTypeHidden, test.evs) {
				t.Run("marking lines hidden/visible is replicated", func(t *testing.T) {
					// reset
					handled := true
					for handled {
						_, handled = bh.Handle(term.Event{
							Type: term.EventKey, Key: term.KeyArrowUp})
					}

					// mark line as hidden
					for range 3 {
						bh.Handle(term.Event{Type: term.EventKey, Mod: term.ModShift, Key: term.KeyArrowDown})
					}
					bh.Handle(term.Event{
						Type: term.EventKey,
						Ch:   'h',
						Mod:  term.ModCtrlAlt,
					})

					res, ok := tracker.Resource(res1)
					require.True(t, ok)

					winPos := res.ContentCoordinates(term.Coordinates{Y: 1})
					assert.Equal(t, term.Coordinates{Y: 4}, winPos)

					// mark line as visible
					bh.Handle(term.Event{
						Type: term.EventKey,
						Ch:   'v',
						Mod:  term.ModCtrlAlt,
					})
					winPos = res.ContentCoordinates(term.Coordinates{Y: 1})
					assert.Equal(t, term.Coordinates{Y: 1}, winPos)
				})
			}
		})
	}
}

func makeURI(t *testing.T, uriStr string) workspaceapi.URI {
	uri, err := workspaceapi.ParseURI(uriStr)
	require.NoError(t, err)
	return uri
}

func setHasType(t textapi.EventType, evs []textapi.EventType) bool {
	return slices.Contains(evs, t)
}

// inlineSchedule is a synchronous workspace.ScheduleNextTick stub
// that runs fn on the calling goroutine. Test-only: production code
// must use the host event-loop scheduler so reload's buffer
// mutations do not run on a worker goroutine.
func inlineSchedule(fn func()) bool {
	fn()
	return true
}
