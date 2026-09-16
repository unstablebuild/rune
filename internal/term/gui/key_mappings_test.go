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

package gui

import (
	"testing"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
)

var allTermMods = []struct {
	term   term.Modifier
	ebiten ebiten.KeyModifier
}{
	{0, 0},
	{term.ModCtrl, ebiten.KeyModControl},
	{term.ModShift, ebiten.KeyModShift},
	{term.ModAlt, ebiten.KeyModAlt},
	{term.ModMeta, ebiten.KeyModSuper},
	{term.ModCtrlShift, ebiten.KeyModControl | ebiten.KeyModShift},
	{term.ModCtrlAlt, ebiten.KeyModControl | ebiten.KeyModAlt},
	{term.ModCtrlMeta, ebiten.KeyModControl | ebiten.KeyModSuper},
	{term.ModAltShift, ebiten.KeyModAlt | ebiten.KeyModShift},
	{term.ModShiftMeta, ebiten.KeyModShift | ebiten.KeyModSuper},
	{term.ModAltMeta, ebiten.KeyModAlt | ebiten.KeyModSuper},
	{term.ModCtrlShiftAlt, ebiten.KeyModControl | ebiten.KeyModShift | ebiten.KeyModAlt},
	{term.ModCtrlShiftMeta, ebiten.KeyModControl | ebiten.KeyModShift | ebiten.KeyModSuper},
	{term.ModCtrlAltMeta, ebiten.KeyModControl | ebiten.KeyModAlt | ebiten.KeyModSuper},
	{term.ModAltShiftMeta, ebiten.KeyModAlt | ebiten.KeyModShift | ebiten.KeyModSuper},
}

func expectedComb(key ebiten.Key, m term.Modifier) (term.KeyComb, bool) {
	switch key {
	case ebiten.KeyControl, ebiten.KeyControlLeft, ebiten.KeyControlRight:
		return term.KeyComb{Mod: term.ModCtrl}, m == 0
	case ebiten.KeyShift, ebiten.KeyShiftLeft, ebiten.KeyShiftRight:
		return term.KeyComb{Mod: term.ModShift}, m == 0
	case ebiten.KeyAlt, ebiten.KeyAltLeft, ebiten.KeyAltRight:
		return term.KeyComb{Mod: term.ModAlt}, m == 0
	case ebiten.KeyMeta, ebiten.KeyMetaLeft, ebiten.KeyMetaRight:
		return term.KeyComb{Mod: term.ModMeta}, m == 0
	case ebiten.KeyCapsLock:
		return term.KeyComb{Mod: m, Key: term.KeyCapsLock}, true
	case ebiten.KeyNumLock:
		return term.KeyComb{Mod: m, Key: term.KeyNumLock}, true
	case ebiten.KeyScrollLock:
		return term.KeyComb{Mod: m, Key: term.KeyScrollLock}, true
	case ebiten.KeyContextMenu:
		return term.KeyComb{Mod: m, Key: term.KeyMenu}, true
	}
	if base, shift, ok := keyEnumChars(key); ok {
		ch, cmod := resolveCharKey(base, shift, m)
		return term.KeyComb{Mod: cmod, Ch: ch}, true
	}
	if ev, ok := mapEbitenKey(key, m); ok {
		return ev.KeyComb(), true
	}
	return term.KeyComb{}, false
}

func TestCombToEventCoversEveryInputKey(t *testing.T) {
	for key := ebiten.Key(0); key <= ebiten.KeyMax; key++ {
		for _, m := range allTermMods {
			comb, ok := expectedComb(key, m.term)
			if !ok {
				continue
			}
			got, present := combToEvent[comb]
			if !assert.Truef(t, present,
				"missing combToEvent entry for key=%s mod=%d (comb %+v)",
				key.String(), m.term, comb) {
				continue
			}
			rt, rtok := expectedComb(got.Key, ebitenModToTermMod(got.Mods))
			assert.Truef(t, rtok, "stored event for %+v does not decode", comb)
			assert.Equalf(t, comb, rt,
				"combToEvent[%+v] = %+v decodes to %+v, want round-trip", comb, got, rt)
		}
	}
}

func TestCombToEventEntriesDecodeToTheirKey(t *testing.T) {
	for comb, ev := range combToEvent {
		assert.Zerof(t, ev.Action,
			"combToEvent[%+v] must store a normalized event with zero Action", comb)
		got, ok := expectedComb(ev.Key, ebitenModToTermMod(ev.Mods))
		if !assert.Truef(t, ok, "combToEvent[%+v] event %+v is not decodable", comb, ev) {
			continue
		}
		assert.Equalf(t, comb, got,
			"combToEvent[%+v] stores %+v which decodes to %+v", comb, ev, got)
	}
}

func TestCombToEventExcludesUnhandledKeys(t *testing.T) {
	unhandled := []ebiten.Key{
		ebiten.KeyF13, ebiten.KeyF14, ebiten.KeyF15, ebiten.KeyF16, ebiten.KeyF17,
		ebiten.KeyF18, ebiten.KeyF19, ebiten.KeyF20, ebiten.KeyF21, ebiten.KeyF22,
		ebiten.KeyF23, ebiten.KeyF24, ebiten.KeyPause, ebiten.KeyPrintScreen,
	}
	for _, key := range unhandled {
		for _, m := range allTermMods {
			_, ok := expectedComb(key, m.term)
			assert.Falsef(t, ok, "key %s must not be a remap source", key.String())
		}
	}

	synthetic := map[ebiten.Key]term.Key{
		ebiten.KeyCapsLock:    term.KeyCapsLock,
		ebiten.KeyNumLock:     term.KeyNumLock,
		ebiten.KeyScrollLock:  term.KeyScrollLock,
		ebiten.KeyContextMenu: term.KeyMenu,
	}
	for ek, tk := range synthetic {
		ev, ok := combToEvent[term.KeyComb{Key: tk}]
		if assert.Truef(t, ok, "synthetic key %s missing from combToEvent", ek.String()) {
			assert.Equalf(t, ek, ev.Key, "synthetic key %s maps to wrong event", ek.String())
		}
	}
}
