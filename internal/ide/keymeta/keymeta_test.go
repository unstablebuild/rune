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

package keymeta

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler"
)

func TestParseRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		spec string
		want Meta
		name string
	}{
		{"<super>", Super, "Super"},
		{"<alt>", Alt, "Alt"},
		{"<ctrl-super>", CtrlSuper, "Ctrl+Super"},
		{"<alt-super>", AltSuper, "Alt+Super"},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			got, err := Parse(tc.spec)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.spec, got.String())
			assert.Equal(t, tc.name, got.Name())
		})
	}
}

func TestParseRejects(t *testing.T) {
	for _, spec := range []string{
		"", "super", "<meta>", "<Super>", "<ctrl>", "<super-ctrl>", " <alt>",
	} {
		t.Run(spec, func(t *testing.T) {
			_, err := Parse(spec)
			assert.Error(t, err)
		})
	}
}

func TestZeroValueIsSuper(t *testing.T) {
	var m Meta
	assert.Equal(t, Super, m)
	assert.Equal(t, "<super>", m.String())
	assert.Equal(t, "<super>", Meta(200).String(), "out of range reads as Super")
}

func TestOptions(t *testing.T) {
	for _, tc := range []struct {
		goos, mode string
		want       []Meta
	}{
		{"darwin", "vim", []Meta{Super}},
		{"darwin", "emacs", []Meta{Super}},
		{"windows", "vim", []Meta{Super}},
		{"linux", "vim", []Meta{Super, Alt}},
		{"linux", "helix", []Meta{Super, Alt}},
		{"linux", "standard", []Meta{Super, Alt}},
		{"linux", "emacs", []Meta{Super, CtrlSuper, AltSuper}},
	} {
		t.Run(tc.goos+"/"+tc.mode, func(t *testing.T) {
			assert.Equal(t, tc.want, Options(tc.goos, tc.mode))
		})
	}
}

func TestApply(t *testing.T) {
	const (
		shift = term.ModShift
		ctrl  = term.ModCtrl
		alt   = term.ModAlt
		meta  = term.ModMeta
	)
	for _, tc := range []struct {
		meta Meta
		in   term.Modifier
		want term.Modifier
	}{
		{Super, meta, meta},
		{Super, meta | shift | ctrl | alt, meta | shift | ctrl | alt},
		{Alt, 0, 0},
		{Alt, ctrl, ctrl},
		{Alt, meta, alt},
		{Alt, meta | shift, alt | shift},
		{Alt, meta | ctrl, alt | ctrl},
		{Alt, meta | alt, alt},
		{CtrlSuper, meta, ctrl | meta},
		{CtrlSuper, meta | shift, ctrl | meta | shift},
		{CtrlSuper, meta | ctrl, ctrl | meta},
		{CtrlSuper, meta | alt, ctrl | meta | alt},
		{CtrlSuper, alt | shift, alt | shift},
		{AltSuper, meta, alt | meta},
		{AltSuper, meta | ctrl | shift, alt | meta | ctrl | shift},
		{AltSuper, meta | alt, alt | meta},
		{AltSuper, ctrl, ctrl},
	} {
		in := term.KeyComb{Ch: 'x', Mod: tc.in}
		got := tc.meta.Apply(in)
		assert.Equal(t, term.KeyComb{Ch: 'x', Mod: tc.want}, got,
			"%s applied to mod %d", tc.meta, tc.in)
	}
}

func TestApplyCollapse(t *testing.T) {
	parse := func(s string) term.KeyComb {
		k, err := term.ParseKey(s)
		require.NoError(t, err)
		return k
	}
	assert.Equal(t,
		CtrlSuper.Apply(parse("<meta-x>")), CtrlSuper.Apply(parse("<ctrl-meta-x>")))
	assert.Equal(t,
		AltSuper.Apply(parse("<meta-x>")), AltSuper.Apply(parse("<alt-meta-x>")))
	assert.Equal(t, parse("<alt-f>"), Alt.Apply(parse("<meta-f>")))
	assert.Equal(t, parse("<ctrl-meta-enter>"), CtrlSuper.Apply(parse("<meta-enter>")))
}

func TestApplySequence(t *testing.T) {
	first, err := term.ParseKey("<meta-x>")
	require.NoError(t, err)
	last, err := term.ParseKey("o")
	require.NoError(t, err)
	got := Alt.ApplySequence(handler.Sequence{First: first, Last: last})
	assert.Equal(t, term.ModAlt, got.First.Mod)
	assert.Equal(t, last, got.Last)
	single := Alt.ApplySequence(handler.Sequence{First: first})
	assert.Equal(t, term.KeyComb{}, single.Last)
}
