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

package llmconsole

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/llm/llamacpp"
)

// localHandler implements the `models local` subtree.
type localHandler struct {
	registry *llamacpp.Registry
}

func newLocalHandler(reg *llamacpp.Registry) *localHandler {
	return &localHandler{registry: reg}
}

// HandleCommand satisfies repl.CommandHandler for the local subtree.
// cmd.Args is the remaining args after the `local` prefix has been
// stripped by the parent dispatcher.
func (h *localHandler) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(cmd.Args) == 0 {
		return nil, errors.New("usage: models local <list|download|delete> [args]")
	}
	switch cmd.Args[0] {
	case "list":
		if len(cmd.Args) != 1 {
			return nil, errors.New("usage: models local list")
		}
		return h.listModels(), nil
	case "download":
		return h.handleDownload(ctx, cmd.Args[1:], pw)
	case "delete":
		return h.handleDelete(ctx, cmd.Args[1:])
	default:
		return nil, fmt.Errorf("unknown local subcommand: %s", cmd.Args[0])
	}
}

// Complete satisfies repl.CommandHandler for the local subtree.
func (h *localHandler) Complete(
	_ context.Context, _ string, args []string,
) (iterator.Iterator[string], error) {
	subs := []string{"list", "download", "delete"}
	switch len(args) {
	case 0:
		return iterator.FromSlice(subs), nil
	case 1:
		return iterator.FromSlice(filterNames(subs, args[0])), nil
	case 2:
		if args[0] == "delete" {
			return h.completeDelete(args[1]), nil
		}
	}
	return iterator.FromSlice[string](nil), nil
}

func (h *localHandler) listModels() iterator.Iterator[component.Responsive] {
	refs, err := h.registry.CachedReferences()
	if err != nil {
		return markdownOutput(fmt.Sprintf("local list: %v", err))
	}
	if len(refs) == 0 {
		return markdownOutput("*(no local models downloaded)*")
	}
	var b strings.Builder
	b.WriteString("## Local Models\n\n")
	for _, ref := range refs {
		fmt.Fprintf(&b, "- **%s**\n", ref.String())
	}
	return markdownOutput(b.String())
}

func (h *localHandler) handleDelete(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) != 1 {
		return nil, errors.New("usage: models local delete <reference>")
	}
	ref, err := llamacpp.ParseReference(args[0])
	if err != nil {
		return nil, fmt.Errorf("parse reference: %w", err)
	}
	if err := h.registry.Delete(ctx, ref); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s is not present in the local cache", ref.String())
		}
		return nil, fmt.Errorf("local delete %s: %w", ref.String(), err)
	}
	return markdownOutput(fmt.Sprintf("Deleted `%s` from the local cache", ref.String())), nil
}

func (h *localHandler) completeDelete(prefix string) iterator.Iterator[string] {
	refs, err := h.registry.CachedReferences()
	if err != nil {
		return iterator.FromSlice[string](nil)
	}
	var out []string
	for _, ref := range refs {
		name := ref.String()
		if strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	return iterator.FromSlice(out)
}
