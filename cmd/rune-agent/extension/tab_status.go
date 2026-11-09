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
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
)

// Titles the chat's root status record carries through a turn.
const (
	statusTitleSending    = "Sending"
	statusTitleReasoning  = "Reasoning"
	statusTitleWriting    = "Writing"
	statusTitleTools      = "Running tools"
	statusTitleCompacting = "Compacting conversation"
	statusTitleRateLimit  = "Rate limited, retrying"
)

// Messages the chat's root status record carries when a turn ends
// without a reply to summarize.
const (
	statusMsgCompleted = "Turn completed"
	statusMsgTruncated = "Turn stopped after reaching the token limit"
	statusMsgFailed    = "Turn failed"
)

// Limits on what a status carries, within the host's own.
const (
	statusTitleMaxRunes = 80
	statusMsgMaxRunes   = 120
	statusTitleMaxBytes = 192
	statusMsgMaxBytes   = 2048
)

// tabStatusReporter reports what a chat is doing as the status of its
// tab: one root record for the turn and one record per sub-agent
// running in it. A chat without a tab falls back to notifications for
// what the user must be told. Methods may be called from any
// goroutine, but never while holding the chat's UI mutex: the host
// serves status reports under the lock it also draws the chat with.
type tabStatusReporter struct {
	wm   browserapi.WindowManager
	uri  workspaceapi.URI
	noti browserapi.Notifications

	mu       sync.Mutex
	title    string
	progress int
	prompts  []promptStatus
	promptID uint64
	children map[string]string
	last     map[string]browserapi.TabStatus
}

type promptStatus struct {
	seq   uint64
	kind  agent.PromptKind
	title string
}

// newTabStatusReporter returns a reporter for the chat behind the tab
// at uri, or one that only notifies when uri is zero.
func newTabStatusReporter(
	wm browserapi.WindowManager, uri workspaceapi.URI, noti browserapi.Notifications,
) *tabStatusReporter {
	return &tabStatusReporter{
		wm:       wm,
		uri:      uri,
		noti:     noti,
		progress: -1,
		children: make(map[string]string),
		last:     make(map[string]browserapi.TabStatus),
	}
}

// hasTab reports whether the chat has a tab to report on.
func (r *tabStatusReporter) hasTab() bool {
	return r.uri != (workspaceapi.URI{})
}

// beginTurn reports the chat working on a new turn, dropping whatever
// the previous one left behind.
func (r *tabStatusReporter) beginTurn() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.title = statusTitleSending
	r.progress = -1
	r.prompts = nil
	clear(r.children)
	r.reportLocked(browserapi.TabStatus{State: browserapi.TabClear, Progress: -1})
	r.publishRootLocked("")
}

// setTitle reports what the turn is doing now.
func (r *tabStatusReporter) setTitle(title string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.title = clipStatusText(title, statusTitleMaxRunes, statusTitleMaxBytes)
	r.publishRootLocked("")
}

// setToolCall reports the tool the turn is running, summarized as
// "name: summary".
func (r *tabStatusReporter) setToolCall(name, summary string) {
	r.setTitle(toolCallTitle(name, summary))
}

// notify reports what the turn is doing now along with a message the
// user is told about.
func (r *tabStatusReporter) notify(title, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.title = clipStatusText(title, statusTitleMaxRunes, statusTitleMaxBytes)
	r.publishRootLocked(clipStatusText(msg, statusMsgMaxRunes, statusMsgMaxBytes))
	if !r.hasTab() {
		_, _ = r.noti.Notify(browserapi.LevelInfo, "%s", msg)
	}
}

// setProgress reports how many of the turn's tasks are done, or no
// progress at all when there are none.
func (r *tabStatusReporter) setProgress(done, total int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if total <= 0 {
		r.progress = -1
	} else {
		r.progress = min(100, max(0, done*100/total))
	}
	r.publishRootLocked("")
}

// blocked reports the chat waiting on the user for the prompt titled
// title until the returned function is called. Prompts nest: the chat
// shows the latest one pending, and goes back to working once none is.
// Releases after turn cleanup or an earlier release have no effect.
func (r *tabStatusReporter) blocked(kind agent.PromptKind, title string) (release func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.promptID++
	seq := r.promptID
	r.prompts = append(r.prompts, promptStatus{
		seq: seq, kind: kind, title: clipStatusText(title, statusTitleMaxRunes, statusTitleMaxBytes),
	})
	r.publishRootLocked(clipStatusText(title, statusMsgMaxRunes, statusMsgMaxBytes))
	if !r.hasTab() {
		_, _ = r.noti.Notify(browserapi.LevelWarn, "Input required: %s", title)
	}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for i, p := range r.prompts {
			if p.seq == seq {
				r.prompts = append(r.prompts[:i], r.prompts[i+1:]...)
				r.publishRootLocked("")
				return
			}
		}
	}
}

// done reports the turn finished, summarized by msg.
func (r *tabStatusReporter) done(msg string) {
	r.endTurn(browserapi.TabDone, browserapi.LevelSuccess, msg)
}

// failed reports the turn failed for the reason in msg.
func (r *tabStatusReporter) failed(msg string) {
	r.endTurn(browserapi.TabError, browserapi.LevelError, msg)
}

// truncated reports the turn stopped at the token limit, which the
// user must act on.
func (r *tabStatusReporter) truncated() {
	r.endTurn(browserapi.TabError, browserapi.LevelWarn, statusMsgTruncated)
}

// canceled withdraws every record: the user stopped the turn, so there
// is nothing to tell them.
func (r *tabStatusReporter) canceled() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prompts = nil
	clear(r.children)
	r.reportLocked(browserapi.TabStatus{State: browserapi.TabClear, Progress: -1})
}

func (r *tabStatusReporter) endTurn(
	state browserapi.TabState, level browserapi.NotificationLevel, msg string,
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prompts = nil
	clear(r.children)
	r.reportLocked(browserapi.TabStatus{State: browserapi.TabClear, Progress: -1})
	r.reportLocked(browserapi.TabStatus{
		State: state, Progress: -1, Message: clipStatusText(msg, statusMsgMaxRunes, statusMsgMaxBytes),
	})
	if !r.hasTab() {
		_, _ = r.noti.Notify(level, "%s", msg)
	}
}

// childStart reports a sub-agent running under the turn, identified by
// the tool call that spawned it and labelled by title.
func (r *tabStatusReporter) childStart(toolCallID, title string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := childStatusID(toolCallID)
	if id == "" {
		return
	}
	r.children[id] = clipStatusText(title, statusTitleMaxRunes, statusTitleMaxBytes)
	r.reportLocked(browserapi.TabStatus{
		ID: id, State: browserapi.TabWorking, Progress: -1, Title: r.children[id],
	})
}

// childToolCall reports the tool a sub-agent is running.
func (r *tabStatusReporter) childToolCall(toolCallID, name, summary string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := childStatusID(toolCallID)
	if _, ok := r.children[id]; !ok {
		return
	}
	r.reportLocked(browserapi.TabStatus{
		ID: id, State: browserapi.TabWorking, Progress: -1,
		Title: clipStatusText(toolCallTitle(name, summary), statusTitleMaxRunes, statusTitleMaxBytes),
	})
}

// childEnd withdraws a sub-agent's record once it returned.
func (r *tabStatusReporter) childEnd(toolCallID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := childStatusID(toolCallID)
	if _, ok := r.children[id]; !ok {
		return
	}
	delete(r.children, id)
	r.reportLocked(browserapi.TabStatus{ID: id, State: browserapi.TabClear, Progress: -1})
}

// publishRootLocked reports the root record from the reporter's state:
// blocked on the latest pending prompt, or else working on title.
func (r *tabStatusReporter) publishRootLocked(msg string) {
	if n := len(r.prompts); n > 0 {
		p := r.prompts[n-1]
		kind := browserapi.TabKindQuestion
		if p.kind == agent.PromptKindPermission {
			kind = browserapi.TabKindPermission
		}
		r.reportLocked(browserapi.TabStatus{
			State: browserapi.TabBlocked, Kind: kind, Progress: r.progress,
			Title: p.title, Message: msg,
		})
		return
	}
	r.reportLocked(browserapi.TabStatus{
		State: browserapi.TabWorking, Progress: r.progress, Title: r.title, Message: msg,
	})
}

// reportLocked sends status to the host unless it repeats the last
// report for its record. Failures are logged and otherwise ignored: a
// lost indicator must never cost the turn.
func (r *tabStatusReporter) reportLocked(status browserapi.TabStatus) {
	if !r.hasTab() {
		return
	}
	if status.State == browserapi.TabClear {
		if status.ID == "" {
			clear(r.last)
		} else {
			delete(r.last, status.ID)
		}
	} else {
		if last, ok := r.last[status.ID]; ok && last == status {
			return
		}
		r.last[status.ID] = status
	}
	if err := r.wm.SetTabStatus(r.uri, status); err != nil {
		slog.Debug("set chat tab status",
			"uri", r.uri.String(), "id", status.ID, "state", status.State, "error", err)
	}
}

// childStatusID returns the status record id of the sub-agent spawned
// by the given tool call, or "" when the id cannot address a record.
func childStatusID(toolCallID string) string {
	var sb strings.Builder
	for _, c := range toolCallID {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '_', c == '.', c == '+', c == '-':
			sb.WriteRune(c)
		default:
			sb.WriteByte('-')
		}
		if sb.Len() >= 32 {
			break
		}
	}
	if sb.Len() == 0 {
		return ""
	}
	return "agent/" + sb.String()
}

func toolCallTitle(name, summary string) string {
	if summary == "" {
		return name
	}
	return fmt.Sprintf("%s: %s", name, summary)
}

// clipStatusText returns valid UTF-8 without controls, flattened to one line
// within both limits. Truncation reserves a rune and three bytes for an
// ellipsis; maxRunes must be positive and maxBytes at least three.
func clipStatusText(text string, maxRunes, maxBytes int) string {
	text = strings.Join(strings.Fields(text), " ")
	text = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) {
			return -1
		}
		return c
	}, text)
	if utf8.RuneCountInString(text) <= maxRunes && len(text) <= maxBytes {
		return text
	}
	end, count := 0, 0
	for i, c := range text {
		next := i + utf8.RuneLen(c)
		if count >= maxRunes-1 || next > maxBytes-len("…") {
			break
		}
		end = next
		count++
	}
	return strings.TrimRightFunc(text[:end], unicode.IsSpace) + "…"
}

// replySummary returns the first line of a reply that says something,
// stripped of markdown decoration, or "" for a reply that says nothing.
func replySummary(reply string) string {
	inFence := false
	for line := range strings.SplitSeq(reply, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence || strings.HasPrefix(line, "|") {
			continue
		}
		line = strings.TrimLeft(line, "#>*-+ \t")
		if n := strings.IndexFunc(line, func(c rune) bool { return c < '0' || c > '9' }); n > 0 &&
			strings.HasPrefix(line[n:], ". ") {
			line = strings.TrimSpace(line[n+1:])
		}
		line = strings.NewReplacer("**", "", "__", "", "`", "").Replace(line)
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return line
	}
	return ""
}
