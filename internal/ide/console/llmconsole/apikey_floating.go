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

package llmconsole

import (
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/handler/inputbox"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// keyInputFloating wraps an inputbox.Handler as a single-shot floating
// prompt. onDone fires exactly once with the entered text (or aborted=true
// when dismissed via Esc/Ctrl-C).
type keyInputFloating struct {
	ib     *inputbox.Handler
	label  string
	win    browserapi.Window
	onDone func(key string, aborted bool)
	done   bool
}

var _ browserapi.Floating = (*keyInputFloating)(nil)

func (k *keyInputFloating) Handle(ev term.Event) (bool, bool) {
	isEsc := ev.Type == term.EventKey && ev.Key == term.KeyEsc
	exit, handled := k.ib.Handle(ev)
	if !exit {
		return false, handled
	}
	k.finish(isEsc)
	return true, true
}

func (k *keyInputFloating) finish(aborted bool) {
	if k.done {
		return
	}
	k.done = true
	text := k.ib.Text()
	if k.onDone != nil {
		k.onDone(text, aborted)
	}
}

func (k *keyInputFloating) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return k.ib.Cursor()
}

func (k *keyInputFloating) Selection() (string, bool) { return k.ib.Selection() }

func (k *keyInputFloating) Resize(width, height int) { k.ib.Resize(width, height) }

func (k *keyInputFloating) Draw(w term.Writer) { k.ib.Draw(w) }

func (k *keyInputFloating) Dimensions() (int, int) {
	return k.ib.Dimensions()
}

func (k *keyInputFloating) Close() error {
	k.finish(true)
	return nil
}
