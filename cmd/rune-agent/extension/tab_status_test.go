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

package extension

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/internal/browser"
)

type capturedNote struct {
	level browserapi.NotificationLevel
	msg   string
}

type capturingNotifications struct {
	mu    sync.Mutex
	notes []capturedNote
}

func (c *capturingNotifications) Notify(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notes = append(c.notes, capturedNote{level: level, msg: fmt.Sprintf(msg, args...)})
	return "", nil
}

func (c *capturingNotifications) NotifyOnce(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	return c.Notify(level, msg, args...)
}

func (c *capturingNotifications) UpdateNotificationProgress(string, string, int64, int64) error {
	return nil
}

func (c *capturingNotifications) captured() []capturedNote {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedNote(nil), c.notes...)
}

func newTestTabStatusReporter(t *testing.T) (*tabStatusReporter, *recordingWindowManager, *capturingNotifications) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("rune-agent://model/rolling-fox")
	require.NoError(t, err)
	wm := &recordingWindowManager{}
	noti := &capturingNotifications{}
	return newTabStatusReporter(wm, uri, noti), wm, noti
}

func TestTabStatusReporterTurn(t *testing.T) {
	r, wm, noti := newTestTabStatusReporter(t)
	r.beginTurn()
	r.setTitle(statusTitleReasoning)
	r.setToolCall("bash", "go test ./...")
	r.setToolCall("bash", "go test ./...")
	r.setProgress(1, 4)
	r.setProgress(1, 4)
	r.done("Fixed the parser bug.")

	assert.Equal(t, []browserapi.TabStatus{
		{State: browserapi.TabClear, Progress: -1},
		{State: browserapi.TabWorking, Progress: -1, Title: statusTitleSending},
		{State: browserapi.TabWorking, Progress: -1, Title: statusTitleReasoning},
		{State: browserapi.TabWorking, Progress: -1, Title: "bash: go test ./..."},
		{State: browserapi.TabWorking, Progress: 25, Title: "bash: go test ./..."},
		{State: browserapi.TabClear, Progress: -1},
		{State: browserapi.TabDone, Progress: -1, Message: "Fixed the parser bug."},
	}, wm.tabStatuses(), "repeated reports are sent once")
	assert.Empty(t, noti.captured(), "a chat with a tab is not notified directly")

	t.Run("a new turn starts over", func(t *testing.T) {
		wm.reset()
		r.beginTurn()
		assert.Equal(t, []browserapi.TabStatus{
			{State: browserapi.TabClear, Progress: -1},
			{State: browserapi.TabWorking, Progress: -1, Title: statusTitleSending},
		}, wm.tabStatuses())
	})

	t.Run("failed", func(t *testing.T) {
		wm.reset()
		r.failed("create completion: boom")
		assert.Equal(t, []browserapi.TabStatus{
			{State: browserapi.TabClear, Progress: -1},
			{State: browserapi.TabError, Progress: -1, Message: "create completion: boom"},
		}, wm.tabStatuses())
	})

	t.Run("truncated", func(t *testing.T) {
		wm.reset()
		r.beginTurn()
		r.truncated()
		assert.Equal(t, browserapi.TabStatus{
			State: browserapi.TabError, Progress: -1, Message: statusMsgTruncated,
		}, wm.tabStatuses()[len(wm.tabStatuses())-1])
	})

	t.Run("canceled withdraws everything", func(t *testing.T) {
		wm.reset()
		r.beginTurn()
		r.canceled()
		assert.Equal(t, browserapi.TabStatus{State: browserapi.TabClear, Progress: -1},
			wm.tabStatuses()[len(wm.tabStatuses())-1])
		r.setTitle(statusTitleReasoning)
		assert.Equal(t, browserapi.TabStatus{State: browserapi.TabWorking, Progress: -1, Title: statusTitleReasoning},
			wm.tabStatuses()[len(wm.tabStatuses())-1], "the clear forgot the last report")
	})

	t.Run("notify carries a message once", func(t *testing.T) {
		wm.reset()
		r.beginTurn()
		r.notify(statusTitleRateLimit, "Rate limit reached, retrying in 5s")
		r.notify(statusTitleRateLimit, "Rate limit reached, retrying in 5s")
		statuses := wm.tabStatuses()
		assert.Equal(t, browserapi.TabStatus{
			State: browserapi.TabWorking, Progress: -1, Title: statusTitleRateLimit,
			Message: "Rate limit reached, retrying in 5s",
		}, statuses[len(statuses)-1])
		assert.Len(t, statuses, 3)
		assert.Empty(t, noti.captured())
	})

	t.Run("a failing host loses the indicator only", func(t *testing.T) {
		wm.reset()
		wm.statusErr = errors.New("tab gone")
		r.setTitle("still fine")
		assert.Len(t, wm.tabStatuses(), 1)
	})
}

func TestTabStatusReporterBlocked(t *testing.T) {
	r, wm, noti := newTestTabStatusReporter(t)
	r.beginTurn()
	r.setProgress(2, 4)
	wm.reset()

	releaseFirst := r.blocked(agent.PromptKindPermission, "Allow grep via shell?")
	releaseSecond := r.blocked(agent.PromptKindQuestion, "Which database?")
	assert.Equal(t, []browserapi.TabStatus{
		{State: browserapi.TabBlocked, Kind: browserapi.TabKindPermission, Progress: 50,
			Title: "Allow grep via shell?", Message: "Allow grep via shell?"},
		{State: browserapi.TabBlocked, Kind: browserapi.TabKindQuestion, Progress: 50,
			Title: "Which database?", Message: "Which database?"},
	}, wm.tabStatuses())

	wm.reset()
	releaseFirst()
	assert.Equal(t, []browserapi.TabStatus{
		{State: browserapi.TabBlocked, Kind: browserapi.TabKindQuestion, Progress: 50, Title: "Which database?"},
	}, wm.tabStatuses(), "the latest prompt stays shown without repeating its message")

	wm.reset()
	r.setTitle("bash: ls")
	assert.Empty(t, wm.tabStatuses(), "a title change while blocked is held back")
	releaseSecond()
	assert.Equal(t, []browserapi.TabStatus{
		{State: browserapi.TabWorking, Progress: 50, Title: "bash: ls"},
	}, wm.tabStatuses(), "working resumes with the latest title")
	assert.Empty(t, noti.captured())

	t.Run("releasing twice is harmless", func(t *testing.T) {
		wm.reset()
		releaseSecond()
		assert.Empty(t, wm.tabStatuses())
	})
}

func TestChatTabStatusStaleRelease(t *testing.T) {
	for _, tc := range []struct {
		name       string
		transition func(*tabStatusReporter)
	}{
		{"canceled", (*tabStatusReporter).canceled},
		{"done", func(r *tabStatusReporter) { r.done("Finished") }},
		{"failed", func(r *tabStatusReporter) { r.failed("Failed") }},
		{"new turn", (*tabStatusReporter).beginTurn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, wm, _ := newTestTabStatusReporter(t)
			r.beginTurn()
			release := r.blocked(agent.PromptKindQuestion, "Old question")
			tc.transition(r)
			wm.reset()
			release()
			release()
			assert.Empty(t, wm.tabStatuses(), "stale releases must not publish status")
		})
	}
}

func TestChatTabStatusReleasePreservesNewPrompt(t *testing.T) {
	for _, newTurn := range []bool{false, true} {
		t.Run(fmt.Sprintf("newTurn=%t", newTurn), func(t *testing.T) {
			r, wm, _ := newTestTabStatusReporter(t)
			r.beginTurn()
			releaseOld := r.blocked(agent.PromptKindQuestion, "Old question")
			if newTurn {
				r.beginTurn()
			} else {
				releaseOld()
			}
			releaseNew := r.blocked(agent.PromptKindPermission, "New permission")
			wm.reset()
			releaseOld()
			releaseOld()
			assert.Empty(t, wm.tabStatuses(), "old releases must not remove a new prompt")
			require.Len(t, r.prompts, 1)
			assert.Equal(t, "New permission", r.prompts[0].title)
			releaseNew()
			assert.Equal(t, []browserapi.TabStatus{
				{State: browserapi.TabWorking, Progress: -1, Title: statusTitleSending},
			}, wm.tabStatuses())
			wm.reset()
			releaseNew()
			assert.Empty(t, wm.tabStatuses())
		})
	}
}

func TestTabStatusReporterChildren(t *testing.T) {
	r, wm, _ := newTestTabStatusReporter(t)
	r.beginTurn()
	wm.reset()

	r.childStart("call_abc", "explore the parser")
	r.childToolCall("call_abc", "search_content", "func Parse")
	r.childToolCall("call_unknown", "bash", "ls")
	r.childStart("call_def", "run the tests")
	r.childEnd("call_abc")
	r.childEnd("call_abc")
	assert.Equal(t, []browserapi.TabStatus{
		{ID: "agent/call_abc", State: browserapi.TabWorking, Progress: -1, Title: "explore the parser"},
		{ID: "agent/call_abc", State: browserapi.TabWorking, Progress: -1, Title: "search_content: func Parse"},
		{ID: "agent/call_def", State: browserapi.TabWorking, Progress: -1, Title: "run the tests"},
		{ID: "agent/call_abc", State: browserapi.TabClear, Progress: -1},
	}, wm.tabStatuses(), "unknown and finished children are ignored")

	t.Run("the turn's end withdraws the rest", func(t *testing.T) {
		wm.reset()
		r.done("ok")
		assert.Equal(t, []browserapi.TabStatus{
			{State: browserapi.TabClear, Progress: -1},
			{State: browserapi.TabDone, Progress: -1, Message: "ok"},
		}, wm.tabStatuses())
		r.childToolCall("call_def", "bash", "ls")
		assert.Len(t, wm.tabStatuses(), 2)
	})

	t.Run("ids fit the protocol", func(t *testing.T) {
		assert.Equal(t, "agent/call_abc", childStatusID("call_abc"))
		assert.Equal(t, "agent/toolu-01A", childStatusID("toolu/01A"))
		assert.Equal(t, "agent/"+strings.Repeat("a", 32), childStatusID(strings.Repeat("a", 40)))
		assert.Equal(t, "", childStatusID(""))
	})
}

func TestTabStatusReporterWithoutTab(t *testing.T) {
	noti := &capturingNotifications{}
	wm := &recordingWindowManager{}
	r := newTabStatusReporter(wm, workspaceapi.URI{}, noti)
	r.beginTurn()
	r.setTitle(statusTitleReasoning)
	r.setProgress(1, 2)
	r.childStart("call_abc", "explore")
	release := r.blocked(agent.PromptKindQuestion, "Which one?")
	release()
	r.notify(statusTitleRateLimit, "Rate limited")
	r.done("Turn completed")
	r.failed("Turn failed: boom")
	r.truncated()
	r.canceled()
	assert.Empty(t, wm.tabStatuses(), "nothing is reported without a tab")
	assert.Equal(t, []capturedNote{
		{level: browserapi.LevelWarn, msg: "Input required: Which one?"},
		{level: browserapi.LevelInfo, msg: "Rate limited"},
		{level: browserapi.LevelSuccess, msg: "Turn completed"},
		{level: browserapi.LevelError, msg: "Turn failed: boom"},
		{level: browserapi.LevelWarn, msg: statusMsgTruncated},
	}, noti.captured())
}

func TestTabStatusReporterTextLimits(t *testing.T) {
	for _, tc := range []struct{ name, text, title, message string }{
		{"CJK over bytes", strings.Repeat("界", 70), strings.Repeat("界", 63) + "…", strings.Repeat("界", 70)},
		{"CJK exact bytes", strings.Repeat("界", 64), strings.Repeat("界", 64), strings.Repeat("界", 64)},
		{"mixed over bytes", "abc" + strings.Repeat("界", 64), "abc" + strings.Repeat("界", 62) + "…", "abc" + strings.Repeat("界", 64)},
		{"emoji exact bytes", strings.Repeat("😀", 48), strings.Repeat("😀", 48), strings.Repeat("😀", 48)},
		{"emoji over bytes", strings.Repeat("😀", 49), strings.Repeat("😀", 47) + "…", strings.Repeat("😀", 49)},
		{"ASCII exact runes", strings.Repeat("a", 80), strings.Repeat("a", 80), strings.Repeat("a", 80)},
		{"ASCII over runes", strings.Repeat("a", 81), strings.Repeat("a", 79) + "…", strings.Repeat("a", 81)},
		{"message exact runes", strings.Repeat("😀", 120), strings.Repeat("😀", 47) + "…", strings.Repeat("😀", 120)},
		{"message over runes", strings.Repeat("😀", 121), strings.Repeat("😀", 47) + "…", strings.Repeat("😀", 119) + "…"},
		{"whitespace", "  a\n\tb\r\n c  ", "a b c", "a b c"},
		{"controls", "a\x00b\x1bc\x7fd\u0080e", "abcde", "abcde"},
		{"invalid UTF8", "a\xffb", "a�b", "a�b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, publish := range []struct {
				name string
				run  func(*tabStatusReporter)
			}{
				{"blocked", func(r *tabStatusReporter) { r.blocked(agent.PromptKindQuestion, tc.text) }},
				{"notify", func(r *tabStatusReporter) { r.notify(tc.text, tc.text) }},
				{"title", func(r *tabStatusReporter) { r.setTitle(tc.text) }},
				{"tool", func(r *tabStatusReporter) { r.setToolCall(tc.text, "") }},
				{"child", func(r *tabStatusReporter) { r.childStart("call", tc.text) }},
				{"child tool", func(r *tabStatusReporter) {
					r.childStart("call", "Starting")
					r.childToolCall("call", tc.text, "")
				}},
				{"done", func(r *tabStatusReporter) { r.done(tc.text) }},
				{"failed", func(r *tabStatusReporter) { r.failed(tc.text) }},
			} {
				t.Run(publish.name, func(t *testing.T) {
					r, wm, _ := newTestTabStatusReporter(t)
					r.beginTurn()
					wm.reset()
					publish.run(r)
					statuses := wm.tabStatuses()
					require.NotEmpty(t, statuses)
					for _, status := range statuses {
						_, err := browser.ReportFromTabStatus(status, "rune-agent")
						assert.NoError(t, err, "captured status must be accepted by the host")
						assert.True(t, utf8.ValidString(status.Title))
						assert.True(t, utf8.ValidString(status.Message))
						assert.LessOrEqual(t, len(status.Title), 192)
						assert.LessOrEqual(t, len(status.Message), 2048)
					}
					last := statuses[len(statuses)-1]
					if publish.name != "done" && publish.name != "failed" {
						assert.Equal(t, tc.title, last.Title)
					}
					if publish.name == "blocked" || publish.name == "notify" || publish.name == "done" || publish.name == "failed" {
						assert.Equal(t, tc.message, last.Message)
					}
				})
			}
		})
	}
}

func TestClipStatusText(t *testing.T) {
	assert.Equal(t, "", clipStatusText("", 10, 192))
	assert.Equal(t, "a b c", clipStatusText("  a\n\tb   c  ", 10, 192))
	assert.Equal(t, "0123456789", clipStatusText("0123456789", 10, 192))
	assert.Equal(t, "012345678…", clipStatusText("0123456789a", 10, 192))
	assert.Equal(t, "構築中構築中構築中…", clipStatusText(strings.Repeat("構築中", 5), 10, 192))
	assert.Equal(t, "hello…", clipStatusText("hello world and more", 7, 192))
}

func TestReplySummary(t *testing.T) {
	for _, tc := range []struct{ reply, want string }{
		{"", ""},
		{"\n\n   \n", ""},
		{"Fixed the parser bug.\n\nDetails follow.", "Fixed the parser bug."},
		{"# Summary\n\nAll tests pass.", "Summary"},
		{"**Done.** Two files changed.", "Done. Two files changed."},
		{"- first item\n- second", "first item"},
		{"1. first step\n2. second", "first step"},
		{"2024 was a good year.", "2024 was a good year."},
		{"```go\nfunc main() {}\n```\nThe code above.", "The code above."},
		{"| a | b |\n|---|---|\nAfter the table.", "After the table."},
		{"> quoted\nreply", "quoted"},
		{"`make test` passed", "make test passed"},
	} {
		assert.Equal(t, tc.want, replySummary(tc.reply), "%q", tc.reply)
	}
}
