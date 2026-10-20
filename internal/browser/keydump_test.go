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

package browser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
)

type keydumpClipboard struct{ copies int }

func (c *keydumpClipboard) Copy(string, clipboard.Data) error {
	c.copies++
	return nil
}

func (c *keydumpClipboard) Paste(string) (clipboard.Data, error) {
	return clipboard.Data{}, nil
}

type keydumpNotifications struct{}

func (keydumpNotifications) Notify(
	browserapi.NotificationLevel, string, ...any,
) (string, error) {
	return "", nil
}

func (keydumpNotifications) NotifyOnce(
	browserapi.NotificationLevel, string, ...any,
) (string, error) {
	return "", nil
}

func (keydumpNotifications) UpdateNotificationProgress(
	string, string, int64, int64,
) error {
	return nil
}

func TestKeydumpHeldDragCopiesOnlyOnPress(t *testing.T) {
	clip := &keydumpClipboard{}
	h := Keydump(clip, keydumpNotifications{})

	// A key event becomes one row; the list must be sized before
	// ElementAt can resolve a cell to it.
	_, handled := h.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
	require.True(t, handled)
	h.(*keydump).list.Resize(20, 5)

	press := term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 0, MouseY: 0}
	_, handled = h.Handle(press)
	require.True(t, handled)
	require.Equal(t, 1, clip.copies)

	// Repeats of the held button at the same cell are a drag, not new
	// clicks: they must not copy or notify again.
	_, _ = h.Handle(press)
	_, _ = h.Handle(press)
	assert.Equal(t, 1, clip.copies, "held-button repeats must not re-copy")

	_, _ = h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease})
	_, handled = h.Handle(press)
	assert.True(t, handled)
	assert.Equal(t, 2, clip.copies, "the next press copies again")
}
