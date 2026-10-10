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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/text/texttest"
)

// replayPlugin records the calls asyncPlugin replays after the swap
// so the tests can assert both delivery and ordering.
type replayPlugin struct {
	*testVte
	ops           []string
	events        []term.Event
	focus         []bool
	width, height int
}

func (r *replayPlugin) Resize(width, height int) {
	r.ops = append(r.ops, "resize")
	r.width, r.height = width, height
}

func (r *replayPlugin) OnFocusChange(inFocus bool) {
	r.ops = append(r.ops, "focus")
	r.focus = append(r.focus, inFocus)
}

func (r *replayPlugin) Handle(ev term.Event) (bool, bool) {
	r.ops = append(r.ops, "key")
	r.events = append(r.events, ev)
	return false, true
}

func drawPluginToString(t *testing.T, ap *asyncPlugin, width, height int) string {
	t.Helper()
	w := term.NewStringWriter(width, height)
	ap.Draw(w)
	require.NoError(t, w.Flush())
	return w.String()
}

func TestAsyncPlugin(t *testing.T) {
	t.Run("returns immediately and draws the loading animation while the factory blocks", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		defer b.Close()

		release := make(chan struct{})
		tv := newTestVte()
		ap := newAsyncPlugin(b.ex, "sleep 20", 100, func() (pluginHandler, error) {
			<-release
			return tv, nil
		})

		assert.Equal(t, "sleep 20", ap.Title())
		width, height := ap.Dimensions()
		assert.Equal(t, 80, width,
			"placeholder must mirror the plugin interactive width")
		assert.Equal(t, 45, height)

		ap.Resize(10, 3)
		out := drawPluginToString(t, ap, 10, 3)
		assert.NotEmpty(t, strings.TrimSpace(out),
			"pre-ready Draw must render an animation frame")

		close(release)
		b.waitAsyncVTELoads()
		b.flushScheduled()

		require.NotNil(t, ap.real)
		require.NoError(t, ap.Close())
	})

	t.Run("replays queued operations in order after completion", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		defer b.Close()

		release := make(chan struct{})
		rp := &replayPlugin{testVte: newTestVte()}
		ap := newAsyncPlugin(b.ex, "run", 100, func() (pluginHandler, error) {
			<-release
			return rp, nil
		})

		ap.Resize(42, 17)
		ap.OnFocusChange(false)
		ap.OnFocusChange(true)
		exit, handled := ap.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
		assert.False(t, exit)
		assert.False(t, handled,
			"pre-ready keys stay unhandled so global bindings keep working")
		_, handled = ap.Handle(term.Event{Type: term.EventMouse})
		assert.False(t, handled, "mouse events are dropped pre-ready")

		close(release)
		b.waitAsyncVTELoads()
		b.flushScheduled()

		require.NotNil(t, ap.real)
		assert.Equal(t, []string{"resize", "focus", "focus", "key"}, rp.ops)
		assert.Equal(t, 42, rp.width)
		assert.Equal(t, 17, rp.height)
		assert.Equal(t, []bool{false, true}, rp.focus,
			"every pre-ready focus transition must replay in order")
		require.Len(t, rp.events, 1)
		assert.Equal(t, 'l', rp.events[0].Ch)
		require.NoError(t, ap.Close())
	})

	t.Run("esc and ctrl-c abandon the pending window", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		defer b.Close()

		release := make(chan struct{})
		tv := newTestVte()
		ap := newAsyncPlugin(b.ex, "run", 100, func() (pluginHandler, error) {
			<-release
			return tv, nil
		})

		exit, handled := ap.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
		assert.True(t, exit)
		assert.True(t, handled)
		exit, _ = ap.Handle(term.Event{
			Type: term.EventKey, Ch: 'c', Mod: term.ModCtrl,
		})
		assert.True(t, exit)

		require.NoError(t, ap.Close())
		require.NoError(t, ap.Close(), "Close must be idempotent")

		close(release)
		b.waitAsyncVTELoads()
		b.flushScheduled()

		assert.True(t, tv.calledClose,
			"abandoned handler must be released on completion")
		assert.Nil(t, ap.real, "nothing may be installed after Close")
	})

	t.Run("factory error renders and notifies", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		defer b.Close()
		rec := &recordingNotifications{inner: b.ex.notifications}
		b.ex.notifications = rec

		ap := newAsyncPlugin(b.ex, "deploy", 100, func() (pluginHandler, error) {
			return nil, errors.New("stalled transport")
		})
		ap.Resize(40, 3)

		b.waitAsyncVTELoads()
		b.flushScheduled()

		notes := rec.snapshot()
		require.NotEmpty(t, notes)
		assert.Equal(t, browserapi.LevelError, notes[0].level)
		assert.Contains(t, notes[0].msg, "stalled transport")
		out := drawPluginToString(t, ap, 40, 3)
		assert.Contains(t, out, "deploy")
		assert.Contains(t, out, "stalled transport")
		require.NoError(t, ap.Close())
	})
}

func TestExecutePluginDoesNotBlockOnSlowSpawn(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor())
	defer b.Close()
	b.Resize(100, 40)

	release := make(chan struct{})
	tv := newTestVte()
	b.ex.newPluginHandler = func(_ int, args ...string) (pluginHandler, error) {
		<-release
		return tv, nil
	}

	done := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		done <- b.ex.executePlugin(context.Background(), "sleep", "20")
	})
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("executePlugin blocked on a slow terminal spawn")
	}
	assert.Equal(t, 1, b.comp.Browser().FloatingWindows(),
		"the floating window must open while the spawn is in flight")

	close(release)
	b.waitAsyncVTELoads()
	b.flushScheduled()

	require.Len(t, tv.onFocusChange, 2,
		"queued focus transitions must replay onto the real handler")
	assert.False(t, tv.onFocusChange[0])
	assert.True(t, tv.onFocusChange[1])
	assert.Equal(t, 1, b.comp.Browser().FloatingWindows())
}
