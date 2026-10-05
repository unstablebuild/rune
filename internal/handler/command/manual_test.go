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

package command

import (
	"strings"
	"testing"

	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestMakeManualComponentHeightAccountsForWrapping(t *testing.T) {
	cfg := testDefaultConfig()
	cfg.NoMarkdown = false
	p := &Prompt{config: cfg}

	longSummary := strings.TrimSpace(strings.Repeat(
		"Create a task that runs the given command in the background. ", 6))
	man := Manual{
		Name:     "tasknew",
		Synopsis: "<command>",
		Summary:  longSummary,
	}

	comp := p.makeManualComponent(man, component.FrameCharSet{}, term.Attributes{})

	const width = 60
	got := comp.Height(width)

	want := wrappedManualHeight(t, man, width)
	if got != want {
		t.Fatalf("manual height %d does not account for wrapping at width %d; "+
			"want wrapped height %d", got, width, want)
	}
}

// wrappedManualHeight builds the same markdown document that
// makeManualComponent renders and reports its true wrapped height at
// the given width.
func wrappedManualHeight(t *testing.T, man Manual, width int) int {
	t.Helper()
	var builder strings.Builder
	if err := writeTemplate(&builder, man, markdownTemplate); err != nil {
		t.Fatalf("write manual template: %v", err)
	}
	cfg := markdown.DefaultConfig()
	cfg.HeaderPrefix = false
	cfg.ParagraphSpacing = 0
	cfg.InlineCode = term.Attributes{
		Fg: term.ColorSilver,
		Bg: term.ColorGray,
	}
	md, err := markdown.NewWithConfig(builder.String(), cfg)
	if err != nil {
		t.Fatalf("build manual markdown: %v", err)
	}
	return md.Height(width)
}

func TestManualDoesNotOverlapCommandList(t *testing.T) {
	cfg := testDefaultConfig()
	cfg.ShowManual = true
	cfg.NoMarkdown = false
	cfg.FrameCharSet = component.FrameCharSetDefault()
	cfg.Sync = true

	cmds := []Manual{
		{Name: "aaa", Summary: "short"},
		{Name: "gaa", Synopsis: "<args>...", Summary: "Run a go command.",
			Commands: []Manual{
				{Name: "build"}, {Name: "test"}, {Name: "run"},
				{Name: "vet"}, {Name: "mod"}, {Name: "get"},
			}},
		{Name: "gab"}, {Name: "gac"}, {Name: "gad"}, {Name: "gae"},
		{Name: "gaf"}, {Name: "gag"}, {Name: "gah"}, {Name: "gai"}, {Name: "gaj"},
	}

	dispatchFn, cleanup := nopDispatch()
	defer cleanup(t)
	completeFn, cleanupComplete := nopComplete()
	defer cleanupComplete(t)

	storage := storagestub.NewInMemoryService()
	b := NewPrompt(
		storage, FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
		term.NopInterrupter(), cmds, cfg,
	)
	defer b.Close()

	const width, height = 40, 30
	h := testCommandHandler{b}
	// Size the prompt while the top match ("aaa") has a short manual.
	h.Resize(width, height)
	_ = handlertest.DrawHandler(h, width, height)
	// Narrow to the "g" commands via Handle only (no intervening
	// Resize); the top match now carries a tall manual.
	h.Handle(term.Event{Type: term.EventKey, Ch: 'g'})

	out := handlertest.DrawHandler(h, width, height)

	sepIdx := -1
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.ContainsRune(line, '─') {
			sepIdx = i
			break
		}
	}
	if sepIdx < 0 {
		t.Fatalf("no separator row found; output:\n%s", out)
	}
	// The separator row divides the list from the manual, so it must
	// contain only the horizontal-rule glyph and padding. A list entry
	// bleeding into it (e.g. "gai────…") means the list overdrew the
	// manual region.
	for _, r := range lines[sepIdx] {
		if r != '─' && r != ' ' {
			t.Fatalf("command list overlaps the separator row %d (%q); output:\n%s",
				sepIdx, strings.TrimRight(lines[sepIdx], " "), out)
		}
	}
}
