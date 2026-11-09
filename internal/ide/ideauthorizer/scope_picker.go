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

package ideauthorizer

import (
	"errors"
	"fmt"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
	"github.com/unstablebuild/rune-go-sdk/tui"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/markdown"
)

const (
	scopeWarning  = "  ⚠️ runs arbitrary code"
	scopeWildcard = "`*` stands for any further arguments."
	scopeReminder = "Run `authorizer list` in the console to see what each extension may " +
		"run, and `authorizer revoke <id>` to revoke a decision."
	scopeKeys      = "<space> toggles · <enter> confirms · <esc> cancels"
	scopeChecked   = "\U000F0132"
	scopeUnchecked = "\U000F012E"
	// scopePadding is the horizontal padding around the picker: a cell on
	// each side.
	scopePadding = 2
	// scopeTopPadding is the padding between the title of the window and
	// the header.
	scopeTopPadding = 1
)

// The sections of a scopeBody, from top to bottom. A blank row separates
// the list from the reminder.
const (
	scopeSectionHeader = iota
	scopeSectionList
	scopeSectionReminder
	scopeSectionKeys
	scopeSections
)

var (
	scopeTreeAttr   = term.Attributes{Fg: term.ColorGray}
	scopePickedAttr = term.Attributes{Fg: term.ColorGreen}
	// scopeGrayAttr is the attributes of the boxes of the scopes not
	// picked and of those that a scope picked covers.
	scopeGrayAttr  = term.Attributes{Fg: term.ColorGray}
	scopeFocusAttr = term.Attributes{Fg: term.ColorRed, Attrs: term.AttrBold}
	scopeWarnAttr  = term.Attributes{Fg: term.ColorYellow}
)

// scopePickerRequest asks the user which scopes a decision to always or
// never allow a command covers.
type scopePickerRequest struct {
	// Allow reports a decision to always allow; otherwise it is one to
	// never allow.
	Allow     bool
	Requester string
	Scopes    []commandScope
	// Selected index the scopes picked when the picker opens.
	Selected []int
}

// scopePicker lets the user pick the scopes a decision covers. Each row
// is a scope, which space toggles, nested under the scope it narrows.
// Picking a scope decides for the scopes nested under it as well, so
// they show as picked and cannot be toggled until it is unpicked.
type scopePicker struct {
	req  scopePickerRequest
	list *component.FocusList
	// labels are the labels of the scopes, without their warnings.
	labels []string
	// parents index the scope each scope narrows, or are -1.
	parents []int
	// selected are the scopes the user picked, including those that a
	// scope picked covers.
	selected []bool
	header   *markdown.Component
	reminder *markdown.Component
	view     *component.Span

	done     func(selected []int, ok bool)
	finished bool
}

// newScopePicker returns a picker of the scopes of req that calls done
// once, with the scopes picked or ok false when the user cancelled. The
// scopes picked exclude those that another scope picked covers.
func newScopePicker(
	req scopePickerRequest, done func(selected []int, ok bool),
) (*scopePicker, error) {
	p := &scopePicker{
		req:      req,
		list:     new(component.FocusList),
		parents:  make([]int, len(req.Scopes)),
		selected: make([]bool, len(req.Scopes)),
		done:     done,
	}
	// Rows draw their own focus so that it does not recolor the boxes.
	p.list.InitWithAttr(term.Attributes{}, term.Attributes{})
	header := fmt.Sprintf("From now on, **%s** may not run the checked commands, "+
		"and Rune won't ask you. %s", req.Requester, scopeWildcard)
	if req.Allow {
		header = fmt.Sprintf("From now on, **%s** may run the checked commands "+
			"without asking you. %s", req.Requester, scopeWildcard)
	}
	mdCfg := markdown.DefaultConfig()
	mdCfg.HeaderPrefix = false
	var err error
	if p.header, err = markdown.NewWithConfig(header, mdCfg); err != nil {
		return nil, fmt.Errorf("render scope picker header: %w", err)
	}
	mdCfg.Paragraph = scopeGrayAttr
	if p.reminder, err = markdown.NewWithConfig(scopeReminder, mdCfg); err != nil {
		return nil, fmt.Errorf("render scope picker reminder: %w", err)
	}
	body := &scopeBody{list: p.list}
	body.sections[scopeSectionHeader].C = p.header
	body.sections[scopeSectionList].C = p.list
	body.sections[scopeSectionReminder].C = p.reminder
	body.sections[scopeSectionKeys].C = component.NewResponsiveString(scopeKeys,
		component.StringResponsiveConfig{NoSplitWords: true})
	p.view = component.NewSpan(body, component.SpanConfig{
		PadHorizontal: scopePadding,
		PadVertical:   scopeTopPadding,
		// Bottom alignment puts the vertical padding above the body.
		ContentAlignment: component.AlignmentHorizontallyCentered | component.AlignmentBottom,
	})
	byKey := make(map[string]int, len(req.Scopes))
	labels := make(map[string]bool, len(req.Scopes))
	children := make([][]int, len(req.Scopes))
	var roots []int
	for i, s := range req.Scopes {
		label := s.Label
		if labels[label] {
			label = fmt.Sprintf("%s (%d)", label, i+1)
		}
		labels[label] = true
		p.labels = append(p.labels, label)
		p.parents[i] = -1
		if parent, ok := byKey[s.Parent]; ok && s.Parent != "" {
			p.parents[i] = parent
			children[parent] = append(children[parent], i)
		} else {
			roots = append(roots, i)
		}
		byKey[s.Key] = i
	}
	for _, i := range req.Selected {
		p.selected[i] = true
	}
	var push func(i int, tree, indent string)
	push = func(i int, tree, indent string) {
		p.list.PushBack(&scopeRow{picker: p, scope: i, tree: tree})
		for k, c := range children[i] {
			if k == len(children[i])-1 {
				push(c, indent+"└─ ", indent+"   ")
			} else {
				push(c, indent+"├─ ", indent+"│  ")
			}
		}
	}
	for _, i := range roots {
		push(i, "", "")
	}
	return p, nil
}

func (p *scopePicker) title() string {
	if p.req.Allow {
		return "Always allow"
	}
	return "Never allow"
}

// text returns the label of scope i followed by its warning, if any.
func (p *scopePicker) text(i int) string {
	if p.req.Scopes[i].Warn {
		return p.labels[i] + scopeWarning
	}
	return p.labels[i]
}

// covered reports whether a scope the user picked covers scope i.
func (p *scopePicker) covered(i int) bool {
	for a := p.parents[i]; a >= 0; a = p.parents[a] {
		if p.selected[a] {
			return true
		}
	}
	return false
}

// box returns the check box of scope i and its attributes.
func (p *scopePicker) box(i int) (string, term.Attributes) {
	switch {
	case p.covered(i):
		return scopeChecked, scopeGrayAttr
	case p.selected[i]:
		return scopeChecked, scopePickedAttr
	default:
		return scopeUnchecked, scopeGrayAttr
	}
}

func (p *scopePicker) focused() (*scopeRow, bool) {
	n, ok := p.list.Focus()
	if !ok {
		return nil, false
	}
	return n.Value().(*scopeRow), true
}

func (p *scopePicker) toggle() {
	r, ok := p.focused()
	if !ok || p.covered(r.scope) {
		return
	}
	p.selected[r.scope] = !p.selected[r.scope]
}

func (p *scopePicker) finish(ok bool) {
	if p.finished {
		return
	}
	p.finished = true
	var selected []int
	if ok {
		for i, s := range p.selected {
			if s && !p.covered(i) {
				selected = append(selected, i)
			}
		}
	}
	p.done(selected, ok)
}

// Handle satisfies tui.Handler.
func (p *scopePicker) Handle(ev term.Event) (exit, handled bool) {
	if ev.Type != term.EventKey {
		return false, false
	}
	switch ev.Mod {
	case 0:
		switch {
		case ev.Key == term.KeyEnter:
			p.finish(true)
			return true, true
		case ev.Key == term.KeyEsc:
			p.finish(false)
			return true, true
		case ev.Key == term.KeySpace || ev.Ch == ' ':
			p.toggle()
			return false, true
		case ev.Key == term.KeyArrowDown:
			p.list.FocusDown()
			return false, true
		case ev.Key == term.KeyArrowUp:
			p.list.FocusUp()
			return false, true
		}
	case term.ModCtrl:
		switch ev.Ch {
		case 'c':
			p.finish(false)
			return true, true
		case 'j', 'n':
			p.list.FocusDown()
			return false, true
		case 'k', 'p':
			p.list.FocusUp()
			return false, true
		}
	}
	return false, false
}

// Cursor satisfies tui.Handler.
func (p *scopePicker) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, 0, false
}

// Selection satisfies tui.Handler.
func (p *scopePicker) Selection() (string, bool) {
	r, ok := p.focused()
	if !ok {
		return "", false
	}
	return p.text(r.scope), true
}

// Resize satisfies tui.Component.
func (p *scopePicker) Resize(width, height int) {
	p.view.Resize(width, height)
}

// Draw satisfies tui.Component.
func (p *scopePicker) Draw(w term.Writer) {
	p.view.Draw(w)
}

// Dimensions satisfies component.Floating. See scopeBody.Dimensions.
func (p *scopePicker) Dimensions() (width, height int) {
	return p.view.Dimensions()
}

// Close satisfies browserapi.Handler. Closing the picker before the user
// confirms cancels it.
func (p *scopePicker) Close() error {
	p.finish(false)
	return errors.Join(p.header.Close(), p.reminder.Close())
}

// scopeBody lays out the sections of a scopePicker from top to bottom.
// The text sections wrap at the width the rows and the keys need, and
// the list takes the height the other sections leave.
type scopeBody struct {
	list     *component.FocusList
	sections [scopeSections]component.Virtual[tui.Component]
}

// Resize satisfies tui.Component.
func (b *scopeBody) Resize(width, height int) {
	heights := b.heights(width)
	rows := height - 1
	for i, h := range heights {
		if i != scopeSectionList {
			rows -= h
		}
	}
	heights[scopeSectionList] = rows
	y := 0
	for i := range b.sections {
		h := max(0, min(heights[i], height-y))
		b.sections[i].Resize(width, h)
		b.sections[i].Move(term.Coordinates{Y: y})
		y += h
		if i == scopeSectionList {
			y++
		}
	}
}

// Draw satisfies tui.Component.
func (b *scopeBody) Draw(w term.Writer) {
	for i := range b.sections {
		b.sections[i].Draw(w)
	}
}

// Dimensions satisfies component.Floating with the size that shows every
// row and the keys whole, which the window may not have room for. The
// text is wrapped to that width rather than widening it.
func (b *scopeBody) Dimensions() (width, height int) {
	width = graphemecluster.StringWidth(scopeKeys)
	b.list.Iterate(func(c tui.Component) {
		width = max(width, c.(*scopeRow).width())
	})
	for _, h := range b.heights(width) {
		height += h
	}
	return width, height + 1
}

func (b *scopeBody) heights(width int) (heights [scopeSections]int) {
	for i := range b.sections {
		if i == scopeSectionList {
			heights[i] = b.list.Len()
			continue
		}
		heights[i] = b.sections[i].C.(component.Responsive).Height(width)
	}
	return heights
}

// scopeRow is the row of a scope in a scopePicker: the branch of the tree
// that leads to it, its check box and its label.
type scopeRow struct {
	picker *scopePicker
	scope  int
	tree   string
	cols   int
}

func (r *scopeRow) width() int {
	return graphemecluster.StringWidth(r.tree) + graphemecluster.StringWidth(scopeChecked) + 1 +
		graphemecluster.StringWidth(r.picker.text(r.scope))
}

// Resize satisfies tui.Component.
func (r *scopeRow) Resize(width, _ int) {
	r.cols = width
}

// Draw satisfies tui.Component.
func (r *scopeRow) Draw(w term.Writer) {
	x := tcomponent.WriteText(w, 0, 0, r.cols, r.tree, scopeTreeAttr)
	box, attr := r.picker.box(r.scope)
	x = tcomponent.WriteText(w, x, 0, r.cols, box+" ", attr)
	labelAttr := term.Attributes{}
	if focused, ok := r.picker.focused(); ok && focused == r {
		labelAttr = scopeFocusAttr
	}
	x = tcomponent.WriteText(w, x, 0, r.cols, r.picker.labels[r.scope], labelAttr)
	if r.picker.req.Scopes[r.scope].Warn {
		tcomponent.WriteText(w, x, 0, r.cols, scopeWarning, scopeWarnAttr)
	}
}
