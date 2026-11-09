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

package sounds

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var discard = BackendFunc(func(Sound) {})

func TestRegistryResolve(t *testing.T) {
	t.Parallel()
	r := NewRegistry(discard)
	require.NoError(t, r.Set(map[string]string{
		"ding":    "/sounds/ding.wav",
		"chime":   "ding",
		Warning:   "chime",
		Error:     "",
		Info:      Success,
		"quiet":   Error,
		"helpful": Info,
	}))

	tests := []struct {
		name string
		want Sound
		ok   bool
	}{
		{name: Success, want: Sound{Name: Success, BuiltIn: Success}, ok: true},
		{name: "ding", want: Sound{Name: "ding", Path: "/sounds/ding.wav"}, ok: true},
		{name: "chime", want: Sound{Name: "chime", Path: "/sounds/ding.wav"}, ok: true},
		{name: Warning, want: Sound{Name: Warning, Path: "/sounds/ding.wav", BuiltIn: Warning},
			ok: true},
		{name: Info, want: Sound{Name: Info, BuiltIn: Success}, ok: true},
		{name: "helpful", want: Sound{Name: "helpful", BuiltIn: Success}, ok: true},
		{name: Error},
		{name: "quiet"},
		{name: "unknown"},
	}
	for _, test := range tests {
		got, ok := r.resolve(test.name)
		assert.Equal(t, test.ok, ok, test.name)
		assert.Equal(t, test.want, got, test.name)
	}
}

func TestRegistrySetDropsInvalidEntries(t *testing.T) {
	t.Parallel()
	r := NewRegistry(discard)
	err := r.Set(map[string]string{
		"ok":        "/a.wav",
		"":          "/b.wav",
		"a/b":       "/c.wav",
		"typo":      "warnign",
		"loop-a":    "loop-b",
		"loop-b":    "loop-a",
		"via-typo":  "typo",
		"via-valid": "ok",
	})
	require.Error(t, err)
	for _, msg := range []string{
		`sound name "" must not be empty`,
		`sound name "a/b" must not be empty`,
		`sound "typo": refers to unknown sound "warnign"`,
		`sound "loop-a": refers back to itself`,
		`sound "loop-b": refers back to itself`,
		`sound "via-typo": refers to unknown sound "warnign"`,
	} {
		assert.ErrorContains(t, err, msg)
	}
	assert.Equal(t, []string{Error, Info, "ok", Success, "via-valid", Warning}, r.Names())
}

func TestRegistryNamesIncludeSilencedSounds(t *testing.T) {
	t.Parallel()
	r := NewRegistry(discard)
	require.NoError(t, r.Set(map[string]string{"muted": ""}))
	assert.Equal(t, []string{Error, Info, "muted", Success, Warning}, r.Names())
}

func TestRegistrySetReplaces(t *testing.T) {
	t.Parallel()
	r := NewRegistry(discard)
	require.NoError(t, r.Set(map[string]string{"ding": "/ding.wav", Error: ""}))
	require.NoError(t, r.Set(nil))
	assert.NotContains(t, r.Names(), "ding")
	_, ok := r.resolve(Error)
	assert.True(t, ok, "the reserved sound is audible again")
}

func TestRegistryPlay(t *testing.T) {
	t.Parallel()
	var played []Sound
	r := NewRegistry(BackendFunc(func(s Sound) { played = append(played, s) }))
	require.NoError(t, r.Set(map[string]string{Info: "", "ding": "/ding.wav"}))

	r.Play(Warning)
	r.Play(Info)
	r.Play("unknown")
	r.Play("ding")
	assert.Equal(t, []Sound{
		{Name: Warning, BuiltIn: Warning},
		{Name: "ding", Path: "/ding.wav"},
	}, played)
}
