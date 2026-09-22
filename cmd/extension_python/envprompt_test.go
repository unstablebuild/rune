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

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/extension/langext"
)

func key(k term.Key) term.Event { return term.Event{Type: term.EventKey, Key: k} }

func ch(r rune) term.Event { return term.Event{Type: term.EventKey, Ch: r} }

// TestEnvPromptKeys drives the prompt the way the window manager does:
// events until one reports exit, then Close. Selecting an option and
// dismissing both reach the channel, so only the first send is kept.
func TestEnvPromptKeys(t *testing.T) {
	root := langext.Root{Dir: "/repo", URI: "file:///repo"}
	cases := []struct {
		name   string
		events []term.Event
		want   envAnswer
	}{
		{
			name:   "enter takes the managed default",
			events: []term.Event{key(term.KeyEnter)},
			want:   envAnswer{managed: true, answered: true},
		},
		{
			name:   "right then enter opts out",
			events: []term.Event{key(term.KeyArrowRight), key(term.KeyEnter)},
			want:   envAnswer{managed: false, answered: true},
		},
		{
			name: "right then left returns to managed",
			events: []term.Event{
				key(term.KeyArrowRight), key(term.KeyArrowLeft), key(term.KeyEnter),
			},
			want: envAnswer{managed: true, answered: true},
		},
		{
			name:   "r binding selects managed",
			events: []term.Event{ch('r')},
			want:   envAnswer{managed: true, answered: true},
		},
		{
			name:   "i binding opts out",
			events: []term.Event{ch('i')},
			want:   envAnswer{managed: false, answered: true},
		},
		{
			name:   "esc dismisses without answering",
			events: []term.Event{key(term.KeyEsc)},
			want:   envAnswer{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answers := make(chan envAnswer, 1)
			p := newEnvPrompt(root, answers)
			p.Resize(60, 12)

			var exited bool
			for _, ev := range tc.events {
				exit, handled := p.Handle(ev)
				assert.True(t, handled)
				if exit {
					exited = true
					break
				}
			}
			require.True(t, exited, "prompt must exit")
			require.NoError(t, p.Close())
			assert.Equal(t, tc.want, <-answers)
		})
	}
}

func TestEnvPromptIgnoresNonKeyEvents(t *testing.T) {
	answers := make(chan envAnswer, 1)
	p := newEnvPrompt(langext.Root{Dir: "/repo", URI: "file:///repo"}, answers)
	exit, handled := p.Handle(term.Event{Type: term.EventResize})
	assert.False(t, exit)
	assert.False(t, handled)
	assert.Empty(t, answers)
}

func TestEnvPromptCloseReportsDismissal(t *testing.T) {
	answers := make(chan envAnswer, 1)
	p := newEnvPrompt(langext.Root{Dir: "/repo", URI: "file:///repo"}, answers)
	require.NoError(t, p.Close())
	assert.Equal(t, envAnswer{}, <-answers)
}

func TestAskManageEnvironmentWithoutWindowManager(t *testing.T) {
	managed, answered := askManageEnvironment(
		context.Background(), nil, langext.Root{Dir: "/repo", URI: "file:///repo"})
	assert.False(t, managed)
	assert.False(t, answered)
}
