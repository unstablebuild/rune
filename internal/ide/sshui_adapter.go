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

package ide

import (
	"context"
	"fmt"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/inputbox"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/workspace/workspacessh"
)

type promptResult struct {
	text string
	err  error
}

type promptChoiceOutcome struct {
	index    int
	selected bool
}

type promptChoiceCompletion struct {
	once    sync.Once
	outcome chan promptChoiceOutcome
}

func newPromptChoiceCompletion() *promptChoiceCompletion {
	return &promptChoiceCompletion{outcome: make(chan promptChoiceOutcome, 1)}
}

func (c *promptChoiceCompletion) selectIndex(index int) {
	c.complete(promptChoiceOutcome{index: index, selected: true})
}

func (c *promptChoiceCompletion) dismiss() {
	c.complete(promptChoiceOutcome{})
}

func (c *promptChoiceCompletion) complete(outcome promptChoiceOutcome) {
	c.once.Do(func() {
		c.outcome <- outcome
	})
}

func (c *promptChoiceCompletion) wait(ctx context.Context) (int, error) {
	select {
	case outcome := <-c.outcome:
		return promptChoiceResult(outcome)
	default:
	}

	select {
	case outcome := <-c.outcome:
		return promptChoiceResult(outcome)
	case <-ctx.Done():
		select {
		case outcome := <-c.outcome:
			return promptChoiceResult(outcome)
		default:
			return -1, ctx.Err()
		}
	}
}

func promptChoiceResult(outcome promptChoiceOutcome) (int, error) {
	if outcome.selected {
		return outcome.index, nil
	}
	return -1, context.Canceled
}

// workspaceWindowManagerUI is the IDE-side implementation of
// workspacessh.UI. It schedules prompts onto the IDE event loop and renders
// them in the currently focused workspace browser.
type workspaceWindowManagerUI struct {
	ide *IDE
}

func newWorkspaceWindowManagerUI(i *IDE) *workspaceWindowManagerUI {
	return &workspaceWindowManagerUI{ide: i}
}

var _ workspacessh.UI = (*workspaceWindowManagerUI)(nil)

func (u *workspaceWindowManagerUI) PromptSecret(ctx context.Context, label string) (string, error) {
	return u.promptInput(ctx, label, "", true)
}

func (u *workspaceWindowManagerUI) PromptText(ctx context.Context, label, defaultValue string) (string, error) {
	return u.promptInput(ctx, label, defaultValue, false)
}

func (u *workspaceWindowManagerUI) PromptChoice(
	ctx context.Context, message string, options []string,
) (int, error) {
	if len(options) == 0 {
		return -1, fmt.Errorf("PromptChoice: at least one option is required")
	}
	completion := newPromptChoiceCompletion()
	scheduled := u.ide.scheduleFn(func() {
		_ = u.ide.Prompt(message, options, nil, handler.FuncPromptHandler(
			func(i int, _ string) {
				completion.selectIndex(i)
			},
			func() error {
				// Selection precedes the prompt lifecycle's trailing close, so
				// only the first completion can determine the caller's result.
				completion.dismiss()
				return nil
			},
		))
	})
	if !scheduled {
		return -1, context.Canceled
	}
	return completion.wait(ctx)
}

func (u *workspaceWindowManagerUI) Notify(level workspacessh.NotificationLevel, msg string) {
	notifications := u.ide.Notifications()
	if notifications == nil {
		return
	}
	apiLevel := browserapi.LevelInfo
	switch level {
	case workspacessh.NotificationWarning:
		apiLevel = browserapi.LevelWarn
	case workspacessh.NotificationError:
		apiLevel = browserapi.LevelError
	}
	// SSH auth callbacks (Password, KeyboardInteractive, gatherSigners)
	// run on the workspace-build goroutine; hop onto the event loop so
	// notis.inFocus reads workspaceManagerHandler.focus on the same
	// goroutine that mutates it.
	u.ide.scheduleFn(func() {
		_, _ = notifications.Notify(apiLevel, "%s", msg)
	})
}

// promptInput schedules a single-line floating input on the event loop and
// blocks until the user submits or dismisses it.
func (u *workspaceWindowManagerUI) promptInput(
	ctx context.Context, label, defaultValue string, redact bool,
) (string, error) {
	ch := make(chan promptResult, 1)
	scheduled := u.ide.scheduleFn(func() {
		b := u.ide.workspaceHandler.focusBrowser()
		opts := []inputbox.Option{
			inputbox.WithPrompt(label),
			inputbox.WithCtrlCAborts(),
		}
		if redact {
			opts = append(opts, inputbox.WithRedact(true))
		}
		if defaultValue != "" {
			opts = append(opts, inputbox.WithText(defaultValue))
		}
		ib := inputbox.New(opts...)
		fh := &sshInputFloating{
			ib:     ib,
			label:  label,
			result: ch,
		}
		win, err := b.Floating(fh, browserapi.FloatingConfig{
			Alignment: component.AlignmentCentered,
		})
		if err != nil {
			ch <- promptResult{err: fmt.Errorf("open floating prompt: %w", err)}
			return
		}
		fh.win = win
	})
	if !scheduled {
		return "", context.Canceled
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		return r.text, r.err
	}
}

// sshInputFloating wraps inputbox.Handler to implement browserapi.Floating
// for a single-shot prompt. Submitting sends the text down result; Esc
// sends context.Canceled.
type sshInputFloating struct {
	ib     *inputbox.Handler
	label  string
	result chan promptResult
	win    browserapi.Window
	sent   bool
}

var _ browserapi.Floating = (*sshInputFloating)(nil)

func (s *sshInputFloating) Handle(ev term.Event) (bool, bool) {
	isEsc := ev.Type == term.EventKey && ev.Key == term.KeyEsc
	exit, handled := s.ib.Handle(ev)
	if !exit {
		return false, handled
	}
	if !s.sent {
		s.sent = true
		if isEsc {
			s.result <- promptResult{err: context.Canceled}
		} else {
			s.result <- promptResult{text: s.ib.Text()}
		}
	}
	return true, true
}

func (s *sshInputFloating) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return s.ib.Cursor()
}

func (s *sshInputFloating) Selection() (string, bool) {
	return s.ib.Selection()
}

func (s *sshInputFloating) Resize(width, height int) {
	s.ib.Resize(width, height)
}

func (s *sshInputFloating) Draw(w term.Writer) {
	s.ib.Draw(w)
}

func (s *sshInputFloating) Dimensions() (int, int) {
	width := len(s.label) + 32
	if width < 40 {
		width = 40
	}
	return width, 1
}

func (s *sshInputFloating) Close() error {
	if !s.sent {
		s.sent = true
		s.result <- promptResult{err: context.Canceled}
	}
	return nil
}
