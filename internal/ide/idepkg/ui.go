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

package idepkg

import (
	"context"
	"sync"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// UI is where a user sees the notifications of the package installs
// and uses made on their behalf, and answers the questions they ask.
type UI interface {
	browserapi.Notifications
	// PromptConfig asks the user whether to apply the change to their
	// config that p describes, and calls answer once with the decision.
	// Dismissing the prompt denies the change. answer is never called
	// when the user can no longer be reached.
	PromptConfig(p ConfigPrompt, answer func(approved bool))
}

// ConfigPrompt asks the user to approve a change a package makes to
// their config.
type ConfigPrompt struct {
	// Message is markdown.
	Message string
	// Options are the answers the user picks from. Options[0] approves
	// the change; every other option denies it.
	Options []PromptOption
	// MaxWidth bounds the width of the prompt in cells; 0 sizes it to
	// the message.
	MaxWidth int
}

// PromptOption is an answer to a ConfigPrompt.
type PromptOption struct {
	Label string
	// Key picks the option; 0 binds no key.
	Key rune
}

type uiKey struct{}

// WithUI returns a copy of ctx that has the Manager calls it is passed
// to notify and ask ui instead of the Manager's own UI, including when
// the user answers a prompt after the call returned. A host uses it to
// install packages for a user on another machine.
func WithUI(ctx context.Context, ui UI) context.Context {
	return context.WithValue(ctx, uiKey{}, ui)
}

func (m *Manager) ui(ctx context.Context) UI {
	if ui, ok := ctx.Value(uiKey{}).(UI); ok {
		return ui
	}
	return managerUI{Notifications: m.n, m: m}
}

// managerUI is the UI of the user of the Manager's own window manager.
type managerUI struct {
	browserapi.Notifications
	m *Manager
}

func (u managerUI) PromptConfig(p ConfigPrompt, answer func(approved bool)) {
	u.m.PromptConfig(p, answer)
}

// PromptConfig shows p in a floating window of the Manager's own
// window manager. See UI.PromptConfig.
func (m *Manager) PromptConfig(p ConfigPrompt, answer func(approved bool)) {
	var once sync.Once
	reply := func(approved bool) {
		once.Do(func() { answer(approved) })
	}
	newMessage := markdownOrFallback(m.parser, m.scheduleNextTick)
	if p.MaxWidth > 0 {
		newMessage = boundedFloatingMessage(newMessage, p.MaxWidth)
	}
	options := make([]string, len(p.Options))
	bindings := make([]term.KeyComb, 0, len(p.Options))
	for i, opt := range p.Options {
		options[i] = "    " + opt.Label + "    "
		if opt.Key != 0 {
			bindings = append(bindings, term.KeyComb{Ch: opt.Key})
		}
	}
	if len(bindings) != len(options) {
		bindings = nil
	}
	prompt := handler.NewPrompt(handler.PromptConfig{
		HighlightAttr: term.Attributes{
			Attrs: term.AttrBold,
			Bg:    term.ColorRed,
		},
		OptionAttr: term.Attributes{
			Attrs: term.AttrBold,
			Bg:    term.ColorGray,
		},
		OptionBindings: bindings,
		PromptConfig: component.PromptConfig{
			Message:    p.Message,
			Options:    options,
			NewMessage: newMessage,
		},
		PromptHandler: handler.FuncPromptHandler(
			func(idx int, _ string) { reply(idx == 0) },
			func() error {
				reply(false)
				return nil
			}),
	})

	ok := m.scheduleNextTick(func() {
		_, err := m.wm.Floating(prompt, browserapi.FloatingConfig{
			Alignment: component.AlignmentCentered,
		})
		if err != nil {
			_, _ = m.n.Notify(browserapi.LevelError, "show config prompt: %s", err)
			reply(false)
		}
	})
	if !ok {
		m.log(log.ErrorLevel, "idepkg config prompt: could not schedule")
		reply(false)
	}
}
