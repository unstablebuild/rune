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
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
)

type countingNotifications struct{ n int }

func (c *countingNotifications) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	c.n++
	return "", nil
}

func (c *countingNotifications) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	c.n++
	return "", nil
}

func (c *countingNotifications) UpdateNotificationProgress(string, string, int64, int64) error {
	return nil
}

func TestKeydumpPointerLeaves(t *testing.T) {
	for _, size := range [][2]int{{60, 15}, {1, 1}} {
		notifications := new(countingNotifications)
		k := Keydump(clipboard.NewInMemory(), notifications)
		for _, ch := range "a long sequence of keys to dump" {
			k.Handle(term.Event{Type: term.EventKey, Ch: ch})
		}
		k.Resize(size[0], size[1])
		k.Handle(term.Event{Type: term.EventMouse, MouseX: -1, MouseY: 0})
		k.Handle(term.Event{Type: term.EventMouse, MouseX: 0, MouseY: size[1]})
		_, selected := k.Selection()
		assert.False(t, selected, "a leave does not copy a row")
		assert.Zero(t, notifications.n)

		handlertest.RunPointerLeaves(t, k, size[0], size[1])
	}
}
