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

package agentshell

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/inputbox"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
)

// renameInputBox records Esc presses so the close callback can distinguish
// a cancel from an Enter commit.
type renameInputBox struct {
	*inputbox.Handler
	cancelled bool
}

func (b *renameInputBox) Handle(ev term.Event) (exit, handled bool) {
	if ev.Type == term.EventKey && ev.Key == term.KeyEsc && ev.Mod == 0 {
		b.cancelled = true
	}
	return b.Handler.Handle(ev)
}

// isParenID reports whether a token looks like the "(id)" tail of a
// "title (id)" completion candidate.
func isParenID(s string) bool {
	return len(s) > 2 && strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")
}

// resolveChatsID unwraps the dialogue ID argument of a chats subcommand.
// Completion offers titled dialogues as "title (id)"; the candidate may
// arrive split into title words plus a trailing "(id)".
func (s *shell) resolveChatsID(ctx context.Context, args []string) (id string, rest []string) {
	if len(args) == 0 {
		return "", nil
	}
	if id = dialoguemanager.ParseNamedID(args[0]); id != args[0] {
		return id, args[1:]
	}
	if _, err := s.store.Get(ctx, args[0]); err == nil {
		return args[0], args[1:]
	}
	for i := 1; i < len(args); i++ {
		if isParenID(args[i]) {
			return args[i][1 : len(args[i])-1], args[i+1:]
		}
	}
	return args[0], args[1:]
}

// renameConversation sets a conversation's title; an empty title opens a
// popup instead and a cleared title falls back to the generated ID.
func (s *shell) renameConversation(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	id, rest := s.resolveChatsID(ctx, args)
	title := strings.TrimSpace(strings.Join(rest, " "))
	if title == "" {
		return s.renameConversationPopup(ctx, id)
	}
	if err := s.store.SetTitle(ctx, id, title); err != nil {
		if errors.Is(err, storageapi.ErrNotFound) {
			return nil, fmt.Errorf(
				"conversation %q has no saved messages yet; send a message before renaming it", id)
		}
		return nil, fmt.Errorf("rename conversation %q: %w", id, err)
	}
	s.retitleOpenTab(ctx, id)
	return markdownOutput(fmt.Sprintf("Renamed **%s** to **%s**.", id, title)), nil
}

func (s *shell) retitleOpenTab(ctx context.Context, id string) {
	if s.retitleTab != nil {
		s.retitleTab(ctx, id)
	}
}

func (s *shell) renameConversationPopup(
	ctx context.Context, id string,
) (iterator.Iterator[component.Responsive], error) {
	d, err := s.store.Get(ctx, id)
	if errors.Is(err, storageapi.ErrNotFound) {
		return nil, fmt.Errorf(
			"conversation %q has no saved messages yet; send a message before renaming it", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get conversation %q: %w", id, err)
	}

	ib := &renameInputBox{Handler: inputbox.New(
		inputbox.WithText(d.Title),
		inputbox.WithPlaceholderText("conversation title"),
		inputbox.WithCtrlCAborts(),
	)}

	var win browserapi.Window
	bhandler := browserapi.FuncHandler(ib, func() error {
		if !ib.cancelled {
			if title, err := ib.Result(); err == nil {
				title = strings.TrimSpace(title)
				if err := s.store.SetTitle(context.Background(), id, title); err != nil {
					s.notify(browserapi.LevelError, "Rename conversation %s: %v", id, err)
				} else {
					s.retitleOpenTab(context.Background(), id)
					if title == "" {
						s.notify(browserapi.LevelInfo,
							"Cleared the conversation title; %s shows as its ID again.", id)
					} else {
						s.notify(browserapi.LevelSuccess,
							"Renamed conversation %s to %q.", id, title)
					}
				}
			}
		}
		return s.wm.CloseWindow(win)
	})
	floating := browserapi.FuncFloating(bhandler, func() (int, int) {
		w, h := ib.Dimensions()
		if w < 40 {
			w = 40
		}
		return w, h
	})

	win, err = s.wm.Floating(floating, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
		Title:     "Rename conversation",
	})
	if err != nil {
		return nil, fmt.Errorf("open floating window: %w", err)
	}
	return iterator.Empty[component.Responsive](), nil
}
