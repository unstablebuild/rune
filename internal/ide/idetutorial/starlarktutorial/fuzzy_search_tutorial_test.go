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

package starlarktutorial

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/ide/idetutorial"
)

// fuzzySearchTutorialPath is the shipped tutorial the fuzzy-search
// package registers via its config.star tutorials entry.
const fuzzySearchTutorialPath = "../../../../cmd/extension_fuzzy_search/fuzzy_search.star"

func newFuzzySearchTutorial(t *testing.T) (*Tutorial, *fakeNotis) {
	t.Helper()
	src, err := os.ReadFile(fuzzySearchTutorialPath)
	require.NoError(t, err)
	notis := &fakeNotis{}
	tut, err := New(
		"fuzzy_search", string(src),
		idetutorial.PromptStyle{}, nil, notis, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, tut)
	tut.Resize(bodyW, bodyH)
	return tut, notis
}

// TestFuzzySearchTutorialRegisters asserts the shipped tutorial parses
// and registers under the id the config.star tutorials entry expects.
func TestFuzzySearchTutorialRegisters(t *testing.T) {
	t.Parallel()
	tut, _ := newFuzzySearchTutorial(t)
	require.Equal(t, "fuzzy_search", tut.id)
	require.Equal(t, "Fuzzy Search", tut.title)
}

// TestFuzzySearchTutorialFlow drives the shipped tutorial through its
// full step sequence: the search commands resolve via the command
// observer and the live search step via the open event, while every
// key falls through to the focused finder.
func TestFuzzySearchTutorialFlow(t *testing.T) {
	t.Parallel()
	tut, notis := newFuzzySearchTutorial(t)
	resetAndWait(t, tut, time.Second)

	require.Equal(t, "wait_command", activeKindFor(tut))
	tut.ObserveCommand("searchfile", "searchfile", nil, nil)

	// "Find and open a file": a wait_event step. It resolves only on
	// the file-open event, never from keystrokes, and the keys a user
	// types into the finder are not its business.
	waitNextActive(t, tut, "wait_event", time.Second)
	for _, spec := range []string{"a", "<enter>", "<esc>"} {
		ks, err := term.ParseKeys(spec)
		require.NoError(t, err)
		exit, handled := tut.Handle(term.Event{
			Type: term.EventKey, Key: ks[0].Key, Mod: ks[0].Mod, Ch: ks[0].Ch,
		})
		require.False(t, exit, "%s must never close the tile", spec)
		require.False(t, handled,
			"wait_event must not swallow %s; keys reach the finder", spec)
		require.Equal(t, "wait_event", activeKindFor(tut),
			"%s must not resolve the wait_event step", spec)
	}
	tut.ObserveEvent("open", "file:///workspace/main.go")

	waitNextActive(t, tut, "wait_command", time.Second)
	tut.ObserveCommand("searchtext", "searchtext", nil, nil)

	waitNextActive(t, tut, "wait_command", time.Second)
	tut.ObserveCommand("searchfunc", "searchast", nil, nil)

	waitNextActive(t, tut, "wait_command", time.Second)
	tut.ObserveCommand("searchtype", "searchast", nil, nil)

	waitFinished(t, tut, time.Second)
	assert.True(t, tut.Completed())
	assert.True(t, notis.containsSubstring("Happy searching"),
		"the wrap-up must be delivered as a notification: %v",
		notis.renderedCalls())
}
