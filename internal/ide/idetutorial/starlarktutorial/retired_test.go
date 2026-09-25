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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/ide/idetutorial"
)

// TestTutorialsForOlderRunesLoad asserts a tutorial written against a
// DSL this Rune no longer has still parses. Failing here would fail
// the whole config decode and take every other tutorial with it.
func TestTutorialsForOlderRunesLoad(t *testing.T) {
	t.Parallel()
	sources := map[string]string{
		"retired builtin": `
def run():
    floating_window(title="Hi", text="there")
tutorial(entry=run)
`,
		"retired argument": `
def run():
    wait_command(command="edit", on_error="run edit")
tutorial(entry=run)
`,
		"retired alignment": `
def run():
    wait_event(event="open", alignment="top", offset=0.2)
tutorial(entry=run)
`,
	}
	for name, src := range sources {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			notis := &fakeNotis{}
			tut := newRetiredTutorial(t, src, notis)

			tut.Reset()
			assert.True(t, tut.Finished(),
				"an unsupported lesson must not arm a step")
			assert.False(t, tut.Completed())
			assert.Empty(t, tut.ActiveKind())

			require.Eventually(t, func() bool {
				return notis.containsSubstring("not supported by this version of Rune")
			}, time.Second, 5*time.Millisecond,
				"got %v", notis.renderedCalls())
			assert.True(t, notis.containsLevel(browserapi.LevelError),
				"the refusal must read as an error")
		})
	}
}

// TestSupportedTutorialsStillRun guards the retirement check against
// false positives: a lesson that only names a retired word as its own
// variable or keyword is not an old tutorial.
func TestSupportedTutorialsStillRun(t *testing.T) {
	t.Parallel()
	src := `
def helper(offset = 1):
    return offset

def run():
    markdown = "not the retired builtin"
    helper(offset = 2)
    notify(message = markdown)
tutorial(entry=run)
`
	notis := &fakeNotis{}
	tut := newRetiredTutorial(t, src, notis)
	tut.Reset()
	waitFinished(t, tut, time.Second)
	assert.True(t, tut.Completed())
	assert.True(t, notis.containsSubstring("not the retired builtin"),
		"got %v", notis.renderedCalls())
}

func newRetiredTutorial(t *testing.T, src string, notis *fakeNotis) *Tutorial {
	t.Helper()
	tut, err := New(
		"legacy", src,
		idetutorial.PromptStyle{}, nil, notis, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil, nil, nil,
	)
	require.NoError(t, err, "a tutorial for an older Rune must still load")
	require.NotNil(t, tut)
	tut.Resize(40, 20)
	return tut
}
