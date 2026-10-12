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
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// Keydump returns a Floating handler that prints the incoming events as rows.
func Keydump(
	clipboard clipboard.Register, notifications browserapi.Notifications,
) Floating {
	ret := new(keydump)
	ret.list.Init()
	cfg := component.StringResponsiveConfig{
		NoSplitWords: true,
		StringConfig: component.StringConfig{
			Alignment: component.AlignmentLeft,
		},
	}
	ret.clipboard = clipboard
	ret.placeholder = component.NewResponsiveString(
		"To exit press <ctrl-c><ctrl-d>", cfg)
	ret.notifications = notifications
	return ret
}

type keydump struct {
	notifications browserapi.Notifications
	clipboard     clipboard.Register
	placeholder   *component.ResponsiveString
	list          component.ResponsiveList
	selection     string
	exit          bool
	// mouseDown tracks whether the left button is currently held so
	// that drag events (MouseLeft repeated while held) are ignored;
	// only the initial press copies a row.
	mouseDown bool
}

func (k *keydump) Handle(ev term.Event) (exit, handled bool) {
	switch ev.Type {
	case term.EventMouse:
		if ev.Key == term.MouseRelease {
			k.mouseDown = false
			return
		}
		if ev.Key != term.MouseLeft || k.mouseDown {
			return
		}
		k.mouseDown = true
		selection, ok := k.list.ElementAt(term.Coordinates{X: ev.MouseX, Y: ev.MouseY})
		if ok {
			sel := selection.Value().(*component.ResponsiveString)
			k.selection = sel.String()
			handled = true
			err := k.clipboard.Copy(
				clipboard.DefaultRegisterID, clipboard.Data{Text: k.selection})
			if err == nil {
				_, _ = k.notifications.Notify(browserapi.LevelInfo,
					"%q copied to clipboard", k.selection)
			} else {
				_, _ = k.notifications.Notify(browserapi.LevelError,
					"%q copied to clipboard: %v", k.selection, err)
			}
		}
		return
	case term.EventKey:
	default:
		return
	}
	if ev.Type != term.EventKey {
		return
	}
	str := ev.KeyComb().String()
	cfg := component.StringResponsiveConfig{
		NoSplitWords: true,
		StringConfig: component.StringConfig{
			Alignment: component.AlignmentLeft,
		},
	}
	k.list.PushFront(component.NewResponsiveString(str, cfg))
	handled = true
	switch {
	case ev.Ch == 'c' && ev.Mod == term.ModCtrl:
		k.exit = true
	case ev.Ch == 'd' && ev.Mod == term.ModCtrl && k.exit:
		exit = true
	}
	return
}

// Cursor should return the cursor coordinates, style and whether it should be shown at all.
func (k *keydump) Cursor() (c term.Coordinates, s term.CursorStyle, show bool) {
	return
}

// Selection returns the selected text and true if there's currently any, or
// an empty string and false if there's no text selected.
func (k *keydump) Selection() (string, bool) {
	return k.selection, k.selection != ""
}

func (k *keydump) Dimensions() (width int, height int) {
	return 60, 20
}

func (k *keydump) Resize(width, height int) {
	if k.list.Len() == 0 {
		k.placeholder.Resize(width, height)
	}
	k.list.Resize(width, height)
}

// Draw draws this component to the underlying Writer. It returns non-nil error
// if something went wrong in the process of writing or the writer returned
// an error.
func (k keydump) Draw(w term.Writer) {
	if k.list.Len() == 0 {
		k.placeholder.Draw(w)
		return
	}
	k.list.Draw(w)
}

func (k keydump) Close() error {
	return nil
}
