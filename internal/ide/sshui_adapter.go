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
	"errors"
	"fmt"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/workspace/workspacessh"
)

type promptChoiceOutcome struct {
	index    int
	answers  []string
	selected bool
	err      error
}

func (o promptChoiceOutcome) error() error {
	if o.err != nil {
		return o.err
	}
	if !o.selected {
		return context.Canceled
	}
	return nil
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

func (c *promptChoiceCompletion) answer(index int, answers []string) {
	c.complete(promptChoiceOutcome{index: index, answers: answers, selected: true})
}

func (c *promptChoiceCompletion) fail(err error) {
	c.complete(promptChoiceOutcome{err: err})
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
	outcome, err := c.result(ctx)
	if err != nil {
		return -1, err
	}
	return outcome.index, nil
}

func (c *promptChoiceCompletion) waitAnswers(ctx context.Context) ([]string, error) {
	outcome, err := c.result(ctx)
	if err != nil {
		return nil, err
	}
	return outcome.answers, nil
}

func (c *promptChoiceCompletion) result(ctx context.Context) (promptChoiceOutcome, error) {
	select {
	case outcome := <-c.outcome:
		return outcome, outcome.error()
	default:
	}

	select {
	case outcome := <-c.outcome:
		return outcome, outcome.error()
	case <-ctx.Done():
		select {
		case outcome := <-c.outcome:
			return outcome, outcome.error()
		default:
			return promptChoiceOutcome{}, ctx.Err()
		}
	}
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

func (u *workspaceWindowManagerUI) PromptFields(
	ctx context.Context, message string, fields []browserapi.Field,
) ([]string, error) {
	completion := newPromptChoiceCompletion()
	// win and closed are only touched on the event loop.
	var win browser.Window
	closed := false
	scheduled := u.ide.scheduleFn(func() {
		w, opened, err := u.ide.workspaceHandler.focusBrowser().OpenPrompt(browserapi.PromptConfig{
			Message: message,
			Options: []string{"OK"},
			Fields:  fields,
		}, handler.FuncAnswerHandler(
			func(i int, _ string, answers []string) {
				completion.answer(i, answers)
			},
			func() error {
				closed = true
				completion.dismiss()
				return nil
			},
		))
		switch {
		case err != nil:
			completion.fail(fmt.Errorf("open prompt: %w", err))
		case !opened:
			// Its handler would never be told, leaving the caller waiting.
			completion.fail(errors.New("open prompt: the same prompt is already open"))
		default:
			win = w
		}
	})
	if !scheduled {
		return nil, context.Canceled
	}
	answers, err := completion.waitAnswers(ctx)
	if err != nil && errors.Is(err, ctx.Err()) {
		u.ide.scheduleFn(func() {
			if win != nil && !closed {
				_ = win.Close()
			}
		})
	}
	return answers, err
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
