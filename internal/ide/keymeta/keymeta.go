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

// Package keymeta defines what the <meta> modifier means in Rune's own key
// bindings (the `gui.meta_key` setting). It only rewrites key specs Rune
// parses for itself; the keys the GUI delivers keep their real modifiers,
// so editors and terminals still receive them unchanged.
package keymeta

import (
	"fmt"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler"
)

// Meta is the physical key, or key pair, that <meta> stands for in Rune's
// key bindings. The zero value is Super, which leaves every spec as written.
type Meta uint8

// The meanings gui.meta_key can give <meta>. Options says which ones an
// editor is offered on each OS.
const (
	// Super is <super>: Super on Linux, Command on macOS.
	Super Meta = iota
	// Alt is <alt>: Alt, or Option on macOS.
	Alt
	// CtrlSuper is <ctrl-super>, so <ctrl-meta-x> and <meta-x> collapse.
	CtrlSuper
	// AltSuper is <alt-super>, so <alt-meta-x> and <meta-x> collapse.
	AltSuper
)

var metas = []struct {
	meta Meta
	spec string
	name string
	mask term.Modifier
}{
	{Super, "<super>", "Super", term.ModMeta},
	{Alt, "<alt>", "Alt", term.ModAlt},
	{CtrlSuper, "<ctrl-super>", "Ctrl+Super", term.ModCtrl | term.ModMeta},
	{AltSuper, "<alt-super>", "Alt+Super", term.ModAlt | term.ModMeta},
}

// Parse reads the `gui.meta_key` spelling of a Meta, as String writes it.
func Parse(s string) (Meta, error) {
	for _, m := range metas {
		if s == m.spec {
			return m.meta, nil
		}
	}
	specs := make([]string, len(metas))
	for i, m := range metas {
		specs[i] = m.spec
	}
	return Super, fmt.Errorf("unknown meta key %q, expected one of %s",
		s, strings.Join(specs, ", "))
}

// String returns the `gui.meta_key` spelling, such as "<alt>".
func (m Meta) String() string { return metas[m.index()].spec }

// Name returns the key names for prose, such as "Ctrl+Super".
func (m Meta) Name() string { return metas[m.index()].name }

// Mask returns the modifiers that replace term.ModMeta.
func (m Meta) Mask() term.Modifier { return metas[m.index()].mask }

// index maps an out-of-range Meta to Super so a corrupt value can never
// panic a key lookup.
func (m Meta) index() int {
	if int(m) >= len(metas) {
		return 0
	}
	return int(m)
}

// Options returns the Metas offered for editorMode on goos, the first
// being the default. Only Linux offers a choice: macOS keeps <meta> on
// Command, which desktops do not grab. On Linux the emacs editor already
// owns Alt as its own Meta, so it is offered Super combined with another
// modifier instead.
func Options(goos, editorMode string) []Meta {
	if goos != "linux" {
		return []Meta{Super}
	}
	if editorMode == "emacs" {
		return []Meta{Super, CtrlSuper, AltSuper}
	}
	return []Meta{Super, Alt}
}

// Apply replaces term.ModMeta in k with m's modifiers and returns k
// unchanged when it carries no meta. Under CtrlSuper and AltSuper, a spec
// that already holds the added modifier collapses onto the meta-only one:
// <ctrl-meta-x> and <meta-x> both become Ctrl+Super+X.
func (m Meta) Apply(k term.KeyComb) term.KeyComb {
	if k.Mod&term.ModMeta == 0 {
		return k
	}
	k.Mod = k.Mod&^term.ModMeta | m.Mask()
	return k
}

// ApplySequence applies m to both keys of seq.
func (m Meta) ApplySequence(seq handler.Sequence) handler.Sequence {
	seq.First = m.Apply(seq.First)
	seq.Last = m.Apply(seq.Last)
	return seq
}
