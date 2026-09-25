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
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/ide/idetutorial"
	"unstable.build/rune/internal/ide/idetutorial/starlarktutorial"
)

// recordingNotis records every notification with its level, so a test
// can tell a lesson's own success messages apart from the runtime's
// error and stranded-skip reports.
type recordingNotis struct {
	mu   sync.Mutex
	msgs []string
	errs []string
	info []string
}

func (n *recordingNotis) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	rendered := fmt.Sprintf(msg, args...)
	n.mu.Lock()
	n.msgs = append(n.msgs, rendered)
	switch level {
	case browserapi.LevelError, browserapi.LevelWarn:
		n.errs = append(n.errs, rendered)
	case browserapi.LevelInfo:
		n.info = append(n.info, rendered)
	}
	n.mu.Unlock()
	return "", nil
}

func (n *recordingNotis) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n *recordingNotis) UpdateNotificationProgress(_, _ string, _, _ int64) error {
	return nil
}

func (n *recordingNotis) snapshot() (all, errs, info []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.msgs...),
		append([]string(nil), n.errs...),
		append([]string(nil), n.info...)
}

// shippedTutorials names every tutorial the binary embeds, so the skip
// suites stay in step with what ships.
func shippedTutorials() map[string]string {
	return map[string]string{
		"basics":     basicsTutorial,
		"navigation": navigationTutorial,
		"agent":      agentTutorial,
	}
}

// skipEveryStep resets tut and skips whatever step is armed until the
// run goroutine exits. It reports the title of every step it skipped,
// and false when the tutorial is still running after the budget, which
// means a step refused to advance.
func skipEveryStep(t *testing.T, tut *starlarktutorial.Tutorial) ([]string, bool) {
	t.Helper()
	tut.Reset()
	var titles []string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if tut.WaitFinished(time.Millisecond) {
			return titles, true
		}
		if tut.ActiveKind() == "" {
			time.Sleep(time.Millisecond)
			continue
		}
		if title := tut.ActiveTitle(); title != "" &&
			(len(titles) == 0 || titles[len(titles)-1] != title) {
			titles = append(titles, title)
		}
		tut.Skip()
	}
	return titles, false
}

func newShippedTutorial(
	t *testing.T, name, src string, notis browserapi.Notifications,
) *starlarktutorial.Tutorial {
	t.Helper()
	alwaysTrue := func() bool { return true }
	tut, err := starlarktutorial.New(
		name, src,
		idetutorial.PromptStyle{}, nil, notis, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		alwaysTrue, alwaysTrue,
	)
	require.NoError(t, err)
	tut.Resize(24, 20)
	return tut
}

// TestShippedTutorialsSurviveSkippingEveryStep drives every shipped
// lesson with nothing but the tile's Skip button. A lesson that
// indexes a response the skip path cannot produce would raise a
// Starlark error instead of completing.
func TestShippedTutorialsSurviveSkippingEveryStep(t *testing.T) {
	t.Parallel()
	for name, src := range shippedTutorials() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			notis := &recordingNotis{}
			tut := newShippedTutorial(t, name, src, notis)
			_, drained := skipEveryStep(t, tut)
			require.True(t, drained,
				"skipping every step never drained the lesson")
			assert.True(t, tut.Completed(),
				"a fully skipped lesson must reach a normal return")
			_, errs, _ := notis.snapshot()
			assert.Empty(t, errs,
				"skipping must not raise a runtime error")
		})
	}
}

// TestShippedTutorialsSkipDoesNotStrandTheLesson asserts no shipped
// lesson depends on an argument the user would have typed: every
// wait_command a skip could strand must either name its arguments or
// guard the read.
func TestShippedTutorialsSkipDoesNotStrandTheLesson(t *testing.T) {
	t.Parallel()
	for name, src := range shippedTutorials() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			notis := &recordingNotis{}
			tut := newShippedTutorial(t, name, src, notis)
			_, drained := skipEveryStep(t, tut)
			require.True(t, drained)
			_, _, info := notis.snapshot()
			for _, msg := range info {
				assert.NotContains(t, msg, "skipped step",
					"a skip stranded the lesson")
			}
		})
	}
}

// TestShippedTutorialsSkipWalksTheWholeLesson asserts a fully skipped
// lesson still walks its whole body: the last step a lesson arms has
// to show up.
func TestShippedTutorialsSkipWalksTheWholeLesson(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"basics", basicsTutorial, "Your cheatsheet"},
		{"navigation", navigationTutorial, "Ask about a symbol"},
		{"agent", agentTutorial, "One last thing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			notis := &recordingNotis{}
			tut := newShippedTutorial(t, tt.name, tt.src, notis)
			titles, drained := skipEveryStep(t, tut)
			require.True(t, drained)
			assert.Contains(t, titles, tt.want)
		})
	}
}
