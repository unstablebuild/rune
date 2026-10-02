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
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"

	blueiterator "github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/internal/handler/finder"
)

const (
	searchChatsHistoryDocID = "searchchats"
	// audit: dialogues are internal logs present in the store index.
	auditDialogueIDPrefix = "audit:"
)

// searchChatsHistoryKey matches the fuzzy_search line-finder default.
var searchChatsHistoryKey = term.KeyComb{Ch: '\\', Mod: term.ModCtrl}

func (h *aiEditorHandler) handleSearchChats(ctx context.Context, cmd textapi.Command) error {
	h.searchChatsOnce.Do(func() {
		h.searchChats = finder.NewSplitCommandHandler(
			finder.Clients{
				Storage:        h.db,
				ResourceOpener: h.o,
				WindowManager:  h.wm,
				Interrupter:    h.p,
				Notifications:  h.n,
				Editor:         h.textEd,
				FileSystem:     h.fs,
				Executor:       h.exec,
			},
			config.NopConfig(),
			h.newSearchChatsFinder,
		)
	})
	return h.searchChats.HandleCommand(ctx, cmd)
}

func (h *aiEditorHandler) newSearchChatsFinder(
	ctx context.Context, cmd textapi.Command, clients finder.Clients,
	invokeWindow browserapi.Window, c config.Config,
) (finder.RedispatchHandler, error) {
	showAll := slices.Contains(cmd.Args, "--all")
	// Display prefixes contain colons, so a selected line can't be parsed
	// back into a dialogue ID; the producer registers each emitted line.
	targets := new(sync.Map)
	fallback := func(_ workspaceapi.FileSystem, ctx context.Context) (
		blueiterator.Iterator[string], error,
	) {
		return h.newChatSearchIterator(ctx, showAll, targets)
	}
	return finder.NewWithOptions(
		ctx, clients, invokeWindow, c,
		searchChatsHistoryKey, searchChatsHistoryDocID, "",
		fallback, nil,
		finder.Options{
			InitialQuery: strings.Join(filterAllFlag(cmd.Args), " "),
			OnSelect: func(item string) {
				id, ok := targets.Load(item)
				if !ok {
					return
				}
				err := h.handleChat(textapi.Command{
					Name:   commandChat,
					Args:   []string{id.(string)},
					Window: invokeWindow,
				})
				if err == nil {
					return
				}
				if _, nerr := h.n.Notify(
					browserapi.LevelError, "searchchats: %v", err,
				); nerr != nil {
					slog.Error("searchchats notify", "error", nerr)
				}
			},
		},
	)
}

// sortedDialogueHeaders is the shared ordering used by agent completion:
// current workspace, sibling worktrees, legacy, then foreign (showAll only),
// most recently updated first within a tier.
func (h *aiEditorHandler) sortedDialogueHeaders(
	ctx context.Context, showAll bool,
) ([]dialoguemanager.DialogueHeader, error) {
	listIt, err := h.dialogueStore.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("dialogue store list: %w", err)
	}

	all, err := iterator.ToSlice(ctx, listIt)
	if err != nil {
		return nil, fmt.Errorf("dialogue store list collect: %w", err)
	}

	dialogueTier := func(d dialoguemanager.DialogueHeader) int {
		ws, hasWS := d.Workspace()
		if hasWS && ws.Equal(h.cwd) {
			return 0
		}
		if hasWS && h.gitID.worktrees[ws.String()] != "" {
			return 1
		}
		if !hasWS {
			return 2
		}
		return 3
	}
	isForeign := func(d dialoguemanager.DialogueHeader) bool {
		ws, hasWS := d.Workspace()
		return hasWS && !ws.Equal(h.cwd) && h.gitID.worktrees[ws.String()] == ""
	}

	filtered := all[:0]
	for _, d := range all {
		if d.ID == "" || d.SubAgent {
			continue
		}
		if !showAll && isForeign(d) {
			continue
		}
		filtered = append(filtered, d)
	}

	slices.SortStableFunc(filtered, func(a, b dialoguemanager.DialogueHeader) int {
		if ta, tb := dialogueTier(a), dialogueTier(b); ta != tb {
			return ta - tb
		}
		return b.UpdatedAt.Compare(a.UpdatedAt)
	})
	return filtered, nil
}

func (h *aiEditorHandler) dialogueDisplayID(d dialoguemanager.DialogueHeader) string {
	ws, hasWS := d.Workspace()
	switch {
	case !hasWS:
		return "<legacy>:" + d.ID
	case ws.Equal(h.cwd):
		return d.ID
	case h.gitID.worktrees[ws.String()] != "":
		return h.gitID.worktrees[ws.String()] + ":" + d.ID
	default:
		return ws.String() + ":" + d.ID
	}
}

func (h *aiEditorHandler) newChatSearchIterator(
	ctx context.Context, showAll bool, targets *sync.Map,
) (blueiterator.Iterator[string], error) {
	headers, err := h.sortedDialogueHeaders(ctx, showAll)
	if err != nil {
		return nil, err
	}
	headers = slices.DeleteFunc(headers, func(d dialoguemanager.DialogueHeader) bool {
		return strings.HasPrefix(d.ID, auditDialogueIDPrefix)
	})
	return &chatSearchIterator{h: h, headers: headers, targets: targets}, nil
}

// chatSearchIterator decodes each session lazily as iteration reaches it,
// keeping multi-MB scans incremental and cancellable.
type chatSearchIterator struct {
	h       *aiEditorHandler
	headers []dialoguemanager.DialogueHeader
	targets *sync.Map
	hi      int
	msgs    []llmapi.Message
	mi      int
	curID   string
	curDisp string
	err     error
}

func (it *chatSearchIterator) Next(ctx context.Context) (string, bool) {
	for {
		for it.mi < len(it.msgs) {
			idx := it.mi
			it.mi++
			text := searchChatMessageText(it.msgs[idx])
			if text == "" {
				continue
			}
			line := it.curDisp + ":" + strconv.Itoa(idx) + ":" +
				string(it.msgs[idx].Role) + ": " + text
			it.targets.Store(line, it.curID)
			return line, true
		}
		if it.hi >= len(it.headers) {
			return "", false
		}
		if err := ctx.Err(); err != nil {
			it.err = err
			return "", false
		}
		d := it.headers[it.hi]
		it.hi++
		dlg, err := it.h.dialogueStore.Get(ctx, d.ID)
		if err != nil {
			slog.Debug("searchchats: skip unreadable dialogue",
				"id", d.ID, "error", err)
			continue
		}
		it.curID, it.curDisp = d.ID, it.h.dialogueDisplayID(d)
		it.msgs, it.mi = dlg.Messages, 0
	}
}

func (it *chatSearchIterator) Err() error   { return it.err }
func (it *chatSearchIterator) Close() error { return nil }

// Tool-call plumbing carries no Content and is skipped.
func searchChatMessageText(m llmapi.Message) string {
	text := m.Content
	if text == "" {
		var sb strings.Builder
		for _, p := range m.MultiContent {
			if p.Type == llmapi.ContentPartTypeText {
				sb.WriteString(p.Text)
				sb.WriteByte(' ')
			}
		}
		text = sb.String()
	}
	return strings.Join(strings.Fields(text), " ")
}
