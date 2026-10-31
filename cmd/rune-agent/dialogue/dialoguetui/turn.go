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

package dialoguetui

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/markdown"
)

// collapseMode controls how a Turn renders its tool calls.
type collapseMode int

const (
	collapseModeExpanded  collapseMode = iota // full list with output
	collapseModeCollapsed                     // tree with one node per tool call
)

func (m collapseMode) isCollapsed() bool {
	return m == collapseModeCollapsed
}

func (m collapseMode) next() collapseMode {
	if m == collapseModeCollapsed {
		return collapseModeExpanded
	}
	return collapseModeCollapsed
}

// Turn tracks the tool calls for a single LLM response turn.
// It renders tool calls as a tree: top-level calls are direct
// entries, and sub-agent child calls are nested under their
// parent tool call.
//
// Turn satisfies component.Responsive and is inserted as a
// single node in the Component's messages list.
type Turn struct {
	list      component.ResponsiveList
	tools     map[string]*toolNode
	toolOrder []string // insertion-order tracking
	cfg       *ComponentConfig
	mode      collapseMode // draw-time flag
	root      bool         // true for top-level turns (shows expand hint)
	width     int          // from Resize, for VirtualWriter bounds
	height    int          // from Resize
	drawCount int          // incremented on each collapsed draw for spinner

	interrupter     term.Interrupter
	animation       *component.Animation // non-nil when animating collapsed spinner
	animationCtx    context.Context      // passed to Animation.InitWithComponents
	cancelAnimation context.CancelFunc   // cancels animationCtx
}

// toolNode tracks a single tool call in the turn.
type toolNode struct {
	listNode      *component.ListNode
	lastChildNode *component.ListNode // insertion cursor for child tool calls
	name          string
	args          string // stored for collapsed rendering
	summary       string
	output        string
	resultOf      *toolNode // parent invocation for sub-agent result leaves
	hovered       bool
	done          bool
	isError       bool
	dropped       bool            // true when the tool result was dropped from history
	header        *toolCallHeader // non-nil after CompleteToolCall (for dropped-attr updates)
	prompt        bool            // true for ask_user_question (different icon/no animation)
	isMemory      bool            // true for memory recall entries (different icon/no animation)
	isResult      bool            // true for sub-agent result leaf nodes (different icon)
	startTime     time.Time       // when the tool call was announced (for live elapsed display)
	duration      time.Duration   // final execution duration (set on completion)
	childTurn     *Turn           // non-nil for sub-agent tool calls with children
	images        []*markdown.Component
}

// ToolCallDetails is a snapshot of an invocation and its full response.
// Output may be empty even after completion when a tool returns no text.
// A sub-agent result row leaves Args empty so its preview opens on the
// response rather than on a possibly long prompt.
type ToolCallDetails struct {
	Key                         ToolCallKey
	Name, Args, Summary, Output string
	Done                        bool
	IsError                     bool
	MarkdownOutput              bool // response comes from a sub-agent
}

// ToolCallKey identifies the invocation behind a transcript row for the
// lifetime of that row. Unlike tool call IDs, which providers may reuse
// across turns, keys never collide. A sub-agent result row shares the key
// of the invocation whose response it displays; replaying a transcript
// yields new keys.
type ToolCallKey struct{ tn *toolNode }

func (tn *toolNode) details() ToolCallDetails {
	if tn.resultOf != nil {
		return ToolCallDetails{
			Key:  ToolCallKey{tn.resultOf},
			Name: tn.resultOf.name, Output: tn.output,
			Done: true, IsError: tn.isError, MarkdownOutput: true,
		}
	}
	return ToolCallDetails{
		Key:  ToolCallKey{tn},
		Name: tn.name, Args: tn.args, Summary: tn.summary, Output: tn.output,
		Done: tn.done, IsError: tn.isError,
		MarkdownOutput: tn.name == "agent" || (tn.name == "skill" && tn.childTurn != nil),
	}
}

// ToolCallAt returns the tool whose name is drawn at pos, which is
// relative to the turn. Every other cell of a tool entry, such as its
// summary, arguments or output, reports false so it stays selectable.
func (t *Turn) ToolCallAt(pos term.Coordinates) (ToolCallDetails, bool) {
	tn := t.toolNodeAt(pos)
	if tn == nil {
		return ToolCallDetails{}, false
	}
	return tn.details(), true
}

func (t *Turn) toolNodeAt(pos term.Coordinates) *toolNode {
	if pos.X < 0 || pos.X >= t.width || pos.Y < 0 || pos.Y >= t.Height(t.width) {
		return nil
	}
	if t.mode.isCollapsed() {
		return t.collapsedToolAt(pos)
	}
	// Expanded entries (including memory and child tools) live in a flat
	// list; their order can differ from insertion order after replay.
	entries := make(map[component.ListNode]*toolNode)
	t.collectExpandedEntries(entries)
	y := 0
	for node, ok := t.list.Front(); ok; node, ok = node.Next() {
		h := node.Value().(component.Responsive).Height(t.width)
		if pos.Y >= y && pos.Y < y+h {
			// The header is the first row of every entry.
			tn := entries[node]
			if tn == nil || tn.header == nil || pos.Y != y ||
				!labelContains(tn.header.prefix, tn.header.name, pos.X) {
				return nil
			}
			return tn
		}
		y += h
	}
	return nil
}

// labelContains reports whether column x falls on label when label is
// drawn right after prefix.
func labelContains(prefix, label string, x int) bool {
	start := graphemecluster.StringWidth(prefix)
	return x >= start && x < start+graphemecluster.StringWidth(label)
}

// setHovered marks only the hovered tool, including a child tool, so
// moving between turns also clears the previous highlight.
func (t *Turn) setHovered(target *toolNode) (changed bool) {
	for _, id := range t.toolOrder {
		tn := t.tools[id]
		hovered := tn == target
		if tn.hovered != hovered {
			tn.hovered = hovered
			if tn.header != nil {
				tn.header.hovered = hovered
			}
			changed = true
		}
		if tn.childTurn != nil && tn.childTurn.setHovered(target) {
			changed = true
		}
	}
	return
}

func (t *Turn) collectExpandedEntries(entries map[component.ListNode]*toolNode) {
	for _, id := range t.toolOrder {
		tn := t.tools[id]
		if tn.listNode != nil {
			entries[*tn.listNode] = tn
		}
		if tn.childTurn != nil {
			tn.childTurn.collectExpandedEntries(entries)
		}
	}
}

func (t *Turn) collapsedToolAt(pos term.Coordinates) *toolNode {
	return t.collapsedToolAtWidth(pos, t.width)
}

func (t *Turn) collapsedToolAtWidth(pos term.Coordinates, width int) *toolNode {
	y := pos.Y
	for _, id := range t.toolOrder {
		tn := t.tools[id]
		if y == 0 {
			status, _ := t.collapsedStatus(tn, 0)
			if !labelContains(status, tn.name, pos.X-collapsedIndent) {
				return nil
			}
			return tn
		}
		y--
		for _, image := range tn.images {
			h := image.Height(max(0, width-collapsedIndent))
			if y < h {
				return nil
			}
			y -= h
		}
		if tn.childTurn != nil {
			h := tn.childTurn.collapsedHeightAt(max(0, width-collapsedIndent))
			if y < h {
				return tn.childTurn.collapsedToolAtWidth(
					term.Coordinates{X: pos.X - collapsedIndent, Y: y}, max(0, width-collapsedIndent))
			}
			y -= h
		}
	}
	return nil
}

// NewTurn creates a new Turn. The interrupter is used to drive
// the collapsed spinner animation; it may be nil (no animation).
func NewTurn(cfg *ComponentConfig, interrupter term.Interrupter) *Turn {
	t := &Turn{
		tools:       make(map[string]*toolNode),
		cfg:         cfg,
		interrupter: interrupter,
	}
	t.list.Init()
	return t
}

// AddToolCall adds a running tool entry.
func (t *Turn) AddToolCall(id, name, args, summary string) {
	isPrompt := promptToolNames[name]
	node := new(component.ListNode)
	prefix := runningPrefix
	prefixAttr := term.Attributes{} // default for running spinner icon
	cfg := t.cfg.ToolCallStringConfig
	if isPrompt {
		prefix = promptPrefix
		prefixAttr = t.promptAttr()
		cfg = t.cfg.PromptToolCallStringConfig
	}
	entry, hdr := toolCallEntryWithConfig(t.cfg, prefix, prefixAttr, name, args, summary, cfg)
	*node = t.list.PushBack(entry)
	t.tools[id] = &toolNode{
		listNode: node,
		name:     name,
		args:     args,
		summary:  summary,
		prompt:   isPrompt,
		header:   hdr,
	}
	t.toolOrder = append(t.toolOrder, id)
	if !isPrompt {
		t.ensureAnimation()
	}
}

// AddMemoryRecall adds a memory recall parent node with individual
// memories nested as children, mirroring the sub-agent tool tree.
// Memory entries are ephemeral — they appear in the UI but don't
// survive conversation reload.
func (t *Turn) AddMemoryRecall(memories []MemoryRecallEntry, duration time.Duration) {
	if len(memories) == 0 {
		return
	}

	const parentID = "memory-recall"

	// Build parent expanded-mode entry.
	parentEntry := new(component.ResponsiveList)
	parentEntry.Init()
	hdr := &toolCallHeader{
		prefix:     memoryPrefix,
		prefixAttr: t.memoryAttr(),
		name:       "memory recall",
		restAttr:   t.cfg.MemoryIDStringConfig.Attributes,
	}
	parentEntry.PushBack(hdr)

	parentNode := new(component.ListNode)
	*parentNode = t.list.PushBack(parentEntry)

	parentTN := &toolNode{
		listNode: parentNode,
		name:     "memory recall",
		done:     true,
		isMemory: true,
		duration: duration,
		header:   hdr,
	}
	t.tools[parentID] = parentTN
	t.toolOrder = append(t.toolOrder, parentID)

	// Create child turn and insert child entries after the parent
	// (expanded mode uses the flat list; collapsed mode recurses
	// into childTurn).
	parentTN.childTurn = NewTurn(t.cfg, nil)
	insertAfter := parentNode
	for _, mem := range memories {
		// Register in child turn (collapsed rendering).
		parentTN.childTurn.addMemoryChild(mem.ID, mem.Content)

		// Insert in parent list (expanded rendering).
		childEntry := new(component.ResponsiveList)
		childEntry.Init()
		childHdr := &toolCallHeader{
			prefix:     memoryPrefix,
			prefixAttr: t.memoryAttr(),
			name:       mem.ID,
			restAttr:   t.cfg.MemoryIDStringConfig.Attributes,
		}
		childEntry.PushBack(childHdr)
		if content := truncateToolOutput(t.cfg, mem.Content); content != "" {
			result := component.NewResponsiveString(content,
				component.StringResponsiveConfig{
					NoSplitWords: true,
					StringConfig: t.cfg.MemoryContentStringConfig,
				})
			childEntry.PushBack(component.NewSpan(result, t.cfg.ToolResultSpanConfig))
		}
		childNode := new(component.ListNode)
		*childNode = t.list.InsertAfter(childEntry, *insertAfter)
		insertAfter = childNode
		parentTN.lastChildNode = childNode

		// Link expanded-mode list node into child turn.
		parentTN.childTurn.tools[mem.ID].listNode = childNode
		parentTN.childTurn.tools[mem.ID].header = childHdr
		parentTN.childTurn.tools[mem.ID].output = mem.Content
	}
}

// addMemoryChild adds a single memory entry to this turn (used by
// AddMemoryRecall for the child turn that drives collapsed rendering).
func (t *Turn) addMemoryChild(id, content string) {
	node := new(component.ListNode)
	entry := new(component.ResponsiveList)
	entry.Init()
	hdr := &toolCallHeader{
		prefix:     memoryPrefix,
		prefixAttr: t.memoryAttr(),
		name:       id,
		restAttr:   t.cfg.MemoryIDStringConfig.Attributes,
	}
	entry.PushBack(hdr)

	if content != "" {
		truncated := truncateToolOutput(t.cfg, content)
		if truncated != "" {
			result := component.NewResponsiveString(truncated,
				component.StringResponsiveConfig{
					NoSplitWords: true,
					StringConfig: t.cfg.MemoryContentStringConfig,
				})
			entry.PushBack(component.NewSpan(result, t.cfg.ToolResultSpanConfig))
		}
	}

	*node = t.list.PushBack(entry)
	t.tools[id] = &toolNode{
		listNode: node,
		name:     id,
		output:   content,
		done:     true,
		isMemory: true,
		header:   hdr,
	}
	t.toolOrder = append(t.toolOrder, id)
}

// CompleteToolCall replaces a running tool indicator with a
// completed header (check or cross) and truncated output.
func (t *Turn) CompleteToolCall(id, name, args, summary, output string, isError bool) {
	tn, ok := t.tools[id]
	if !ok {
		return
	}
	tn.done = true
	tn.isError = isError
	if name != "" {
		tn.name = name
	}
	if args != "" {
		tn.args = args
	}
	if summary != "" {
		tn.summary = summary
	}
	tn.output = output

	prefix := donePrefix
	prefixAttr := t.successAttr()
	if isError {
		prefix = errorPrefix
		prefixAttr = t.errorAttr()
	}

	// Build a list with the header and optional truncated output.
	entry := new(component.ResponsiveList)
	entry.Init()
	headerEntry, hdr := toolCallEntry(t.cfg, prefix, prefixAttr, tn.name, args, summary)
	entry.PushBack(headerEntry)
	tn.header = hdr
	hdr.hovered = tn.hovered

	truncatedText := truncateToolOutput(t.cfg, output)
	if truncatedText != "" {
		result := component.NewResponsiveString(truncatedText,
			component.StringResponsiveConfig{
				NoSplitWords: true,
				StringConfig: t.cfg.ToolResultStringConfig,
			})
		entry.PushBack(component.NewSpan(result, t.cfg.ToolResultSpanConfig))
	}

	for _, image := range tn.images {
		entry.PushBack(image)
	}
	// Replace the running entry with the completed one.
	tn.listNode.SetValue(entry)

	if !t.hasRunningTools() {
		t.stopAnimation()
	}
}

// AddToolCallImage appends an image to an existing invocation, including child
// invocations. Unknown IDs and empty paths are ignored. An empty caption uses
// the filename. imagePath is a slash-separated path in the filesystem supplied
// by MarkdownConfig, which need not match the local OS.
func (t *Turn) AddToolCallImage(id, imagePath, caption string) {
	if imagePath == "" {
		return
	}
	tn, ok := t.tools[id]
	if !ok {
		for _, parent := range t.tools {
			if parent.childTurn != nil {
				parent.childTurn.AddToolCallImage(id, imagePath, caption)
			}
		}
		return
	}
	cfg := *DefaultMarkdownConfig()
	if t.cfg.MarkdownConfig != nil {
		cfg = *t.cfg.MarkdownConfig
	}
	cfg.Dir = path.Dir(imagePath)
	if caption == "" {
		caption = path.Base(imagePath)
	}
	title := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", " ", "\r", " ").Replace(caption)
	caption = strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "\n", " ", "\r", " ").Replace(caption)
	destination := (&url.URL{Path: path.Base(imagePath)}).String()
	image, err := markdown.NewWithConfig(fmt.Sprintf("![%s](<%s> \"%s\"){rows=12}", caption, destination, title), cfg)
	if err != nil {
		return
	}
	tn.images = append(tn.images, image)
	entry := new(component.ResponsiveList)
	entry.Init()
	entry.PushBack(tn.listNode.Value().(component.Responsive))
	entry.PushBack(image)
	tn.listNode.SetValue(entry)
}

// AddChildToolCall adds a tool call nested under a parent
// tool call. If the parent doesn't exist, it's added as a
// top-level tool call.
func (t *Turn) AddChildToolCall(parentID, id, name, args, summary string) {
	parent, ok := t.tools[parentID]
	if !ok {
		// Parent not found — add as top-level.
		t.AddToolCall(id, name, args, summary)
		return
	}

	if parent.childTurn == nil {
		parent.childTurn = NewTurn(t.cfg, nil)
	}

	parent.childTurn.AddToolCall(id, name, args, summary)

	// Insert child entry after the parent's last inserted node.
	insertAfter := parent.listNode
	if parent.lastChildNode != nil {
		insertAfter = parent.lastChildNode
	}
	childNode := new(component.ListNode)
	entry, hdr := toolCallEntry(t.cfg, runningPrefix, term.Attributes{}, name, args, summary)
	*childNode = t.list.InsertAfter(entry, *insertAfter)
	parent.lastChildNode = childNode

	// Keep a reference so CompleteChildToolCall can update it.
	parent.childTurn.tools[id].listNode = childNode
	parent.childTurn.tools[id].header = hdr
}

// CompleteChildToolCall completes a tool call nested under a
// parent tool call.
func (t *Turn) CompleteChildToolCall(parentID, id, name, args, summary, output string, isError bool) {
	parent, ok := t.tools[parentID]
	if !ok {
		t.CompleteToolCall(id, name, args, summary, output, isError)
		return
	}
	if parent.childTurn == nil {
		return
	}
	parent.childTurn.CompleteToolCall(id, name, args, summary, output, isError)
}

// AddChildResult registers a sub-agent result leaf node under a parent
// tool call for collapsed-mode rendering. In collapsed mode the node is
// shown with a dedicated icon (󰆈 success / 󰅽 error). In expanded mode
// the parent tool call already displays the full output, so no extra
// list entry is inserted.
func (t *Turn) AddChildResult(parentID, output string, isError bool) {
	parent, ok := t.tools[parentID]
	if !ok {
		return
	}

	if parent.childTurn == nil {
		parent.childTurn = NewTurn(t.cfg, nil)
	}

	// Use a synthetic tool ID that won't collide with real tool calls.
	resultID := parentID + ":result"

	summary := truncateFirstLine(output)

	// Register in child turn only — collapsed rendering picks it up.
	// No expanded-mode list entry is created because the parent tool
	// call already shows the full sub-agent output.
	tn := &toolNode{
		name:     summary,
		output:   output,
		resultOf: parent,
		done:     true,
		isError:  isError,
		isResult: true,
	}
	parent.childTurn.tools[resultID] = tn
	parent.childTurn.toolOrder = append(parent.childTurn.toolOrder, resultID)
}

// SetToolStartTime sets the start time for a tool call, used for live
// elapsed display in collapsed mode. It searches recursively into child turns.
func (t *Turn) SetToolStartTime(id string, ts time.Time) {
	if tn, ok := t.tools[id]; ok {
		tn.startTime = ts
		return
	}
	for _, tn := range t.tools {
		if tn.childTurn != nil {
			tn.childTurn.SetToolStartTime(id, ts)
		}
	}
}

// SetToolDuration sets the final execution duration for a tool call.
// It searches recursively into child turns.
func (t *Turn) SetToolDuration(id string, d time.Duration) {
	if tn, ok := t.tools[id]; ok {
		tn.duration = d
		return
	}
	for _, tn := range t.tools {
		if tn.childTurn != nil {
			tn.childTurn.SetToolDuration(id, d)
		}
	}
}

// MarkDropped marks the tool calls with the given IDs as dropped,
// changing their icon color to gray in both collapsed and expanded modes.
// It recurses into child turns.
func (t *Turn) MarkDropped(ids []string) {
	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	t.markDroppedSet(idSet)
}

func (t *Turn) markDroppedSet(idSet map[string]bool) {
	for id, tn := range t.tools {
		if idSet[id] {
			tn.dropped = true
			if tn.header != nil {
				tn.header.prefixAttr = t.droppedAttr()
			}
		}
		if tn.childTurn != nil {
			tn.childTurn.markDroppedSet(idSet)
		}
	}
}

// HasTools returns whether this turn has any tool calls.
func (t *Turn) HasTools() bool {
	return len(t.tools) > 0
}

// SetCollapseMode sets the collapse mode and recurses into all child Turns.
func (t *Turn) SetCollapseMode(mode collapseMode) {
	t.mode = mode
	if mode.isCollapsed() {
		t.ensureAnimation()
	} else {
		t.stopAnimation()
	}
	for _, tn := range t.tools {
		if tn.childTurn != nil {
			tn.childTurn.SetCollapseMode(mode)
		}
	}
}

// completeRunning resolves all running tool calls as canceled,
// recursing into child turns. This is called when the turn ends for
// tool calls whose completion events never arrived (e.g. dropped by a
// full channel, or lost because cancellation stopped the event loop
// before the result was delivered). Their outcome is unknown, so they
// must not render as successful.
func (t *Turn) completeRunning() {
	for id, tn := range t.tools {
		if tn.childTurn != nil {
			tn.childTurn.completeRunning()
		}
		if !tn.done {
			t.CompleteToolCall(id, tn.name, tn.args, tn.summary, "canceled", true)
		}
	}
	t.stopAnimation()
}

// Close releases resources associated with this Turn, including
// any running spinner animation.
func (t *Turn) Close() {
	t.stopAnimation()
	for _, tn := range t.tools {
		for _, image := range tn.images {
			image.Close()
		}
		if tn.childTurn != nil {
			tn.childTurn.Close()
		}
	}
}

func (t *Turn) hasRunningTools() bool {
	for _, tn := range t.tools {
		if !tn.done && !tn.prompt && !tn.isMemory {
			return true
		}
	}
	return false
}

func (t *Turn) ensureAnimation() {
	if t.animation != nil || t.interrupter == nil || !t.mode.isCollapsed() || !t.hasRunningTools() {
		return
	}
	frames := make([]component.WithAttributes, len(spinnerFrames))
	seq := make([]int, len(spinnerFrames))
	for i, ch := range spinnerFrames {
		frames[i] = component.NewStringWithConfig(string(ch), component.StringConfig{})
		seq[i] = i
	}
	t.animationCtx, t.cancelAnimation = context.WithCancel(context.Background())
	t.animation = new(component.Animation)
	t.animation.InitWithComponents(t.animationCtx, t.interrupter, frames, seq, 8)
}

func (t *Turn) stopAnimation() {
	if t.animation == nil {
		return
	}
	// Cancel the context first so any in-flight Interrupt(ctx)
	// call returns immediately, then Close() won't block.
	t.cancelAnimation()
	_ = t.animation.Close()
	t.animation = nil
	t.animationCtx = nil
	t.cancelAnimation = nil
}

// collapsedHeight returns the number of lines needed to render
// the collapsed tree view: one per tool plus recursive children.
func (t *Turn) collapsedHeight() int {
	return t.collapsedHeightAt(t.width)
}

func (t *Turn) collapsedHeightAt(width int) int {
	h := 0
	for _, id := range t.toolOrder {
		h++
		tn := t.tools[id]
		for _, image := range tn.images {
			h += image.Height(max(0, width-collapsedIndent))
		}
		if tn.childTurn != nil {
			h += tn.childTurn.collapsedHeightAt(max(0, width-collapsedIndent))
		}
	}
	return h
}

// Height satisfies component.Responsive.
func (t *Turn) Height(width int) int {
	if t.mode == collapseModeCollapsed {
		h := t.collapsedHeightAt(width)
		if h > 0 {
			if t.root {
				h++ // expand hint line
			}
			h++ // bottom padding
		}
		return h
	}
	return t.list.Height(width)
}

// Resize satisfies tui.Component.
func (t *Turn) Resize(width, height int) {
	t.width = width
	t.height = height
	t.list.Resize(width, height)
}

// Draw satisfies tui.Component.
func (t *Turn) Draw(w term.Writer) {
	if t.mode == collapseModeCollapsed {
		t.drawCollapsedAt(w, t.width)
	} else {
		t.list.Draw(w)
	}
}

var (
	defaultTreeAttr      = term.Attributes{Fg: term.ColorGray}
	defaultSuccessAttr   = term.Attributes{Fg: term.ColorGreen}
	defaultErrorAttr     = term.Attributes{Fg: term.ColorRed}
	defaultToolNameAttr  = term.Attributes{Attrs: term.AttrBold, Fg: term.ColorFuchsia}
	defaultToolArgsAttr  = term.Attributes{Fg: term.ColorGray}
	defaultPromptAttr    = term.Attributes{Fg: term.ColorAqua}
	defaultDroppedAttr   = term.Attributes{Fg: term.ColorGray}
	defaultMemoryAttr    = term.Attributes{Fg: term.ColorPurple}
	defaultResultAttr    = term.Attributes{Fg: term.ColorGreen}
	defaultResultErrAttr = term.Attributes{Fg: term.ColorRed}
)

func (t *Turn) treeAttr() term.Attributes {
	if t.cfg.CollapsedTreeAttr != (term.Attributes{}) {
		return t.cfg.CollapsedTreeAttr
	}
	return defaultTreeAttr
}

func (t *Turn) successAttr() term.Attributes {
	if t.cfg.CollapsedSuccessAttr != (term.Attributes{}) {
		return t.cfg.CollapsedSuccessAttr
	}
	return defaultSuccessAttr
}

func (t *Turn) errorAttr() term.Attributes {
	if t.cfg.CollapsedErrorAttr != (term.Attributes{}) {
		return t.cfg.CollapsedErrorAttr
	}
	return defaultErrorAttr
}

func (t *Turn) toolNameAttr() term.Attributes {
	if t.cfg.CollapsedToolNameAttr != (term.Attributes{}) {
		return t.cfg.CollapsedToolNameAttr
	}
	return defaultToolNameAttr
}

func (t *Turn) toolArgsAttr() term.Attributes {
	if t.cfg.CollapsedToolArgsAttr != (term.Attributes{}) {
		return t.cfg.CollapsedToolArgsAttr
	}
	return defaultToolArgsAttr
}

func (t *Turn) promptAttr() term.Attributes {
	if t.cfg.CollapsedPromptAttr != (term.Attributes{}) {
		return t.cfg.CollapsedPromptAttr
	}
	return defaultPromptAttr
}

func (t *Turn) droppedAttr() term.Attributes {
	if t.cfg.CollapsedDroppedAttr != (term.Attributes{}) {
		return t.cfg.CollapsedDroppedAttr
	}
	return defaultDroppedAttr
}

func (t *Turn) memoryAttr() term.Attributes {
	if t.cfg.CollapsedMemoryAttr != (term.Attributes{}) {
		return t.cfg.CollapsedMemoryAttr
	}
	return defaultMemoryAttr
}

func (t *Turn) memoryIDAttr() term.Attributes {
	return t.cfg.MemoryIDStringConfig.Attributes
}

func (t *Turn) resultAttr() term.Attributes {
	if t.cfg.CollapsedResultAttr != (term.Attributes{}) {
		return t.cfg.CollapsedResultAttr
	}
	return defaultResultAttr
}

func (t *Turn) resultErrorAttr() term.Attributes {
	if t.cfg.CollapsedResultErrorAttr != (term.Attributes{}) {
		return t.cfg.CollapsedResultErrorAttr
	}
	return defaultResultErrAttr
}

// drawCollapsedAt renders the box-drawing tree view within the given width.
func (t *Turn) drawCollapsedAt(w term.Writer, width int) {
	frame := t.drawCount
	t.drawCount++
	tAttr := t.treeAttr()
	nameAttr := t.toolNameAttr()
	argsAttr := t.toolArgsAttr()
	y := 0
	for i, id := range t.toolOrder {
		tn := t.tools[id]
		isLast := i == len(t.toolOrder)-1

		connector := "├─ "
		if isLast {
			connector = "└─ "
		}

		x := writeRuneLineAttr(w, 0, y, connector, width, tAttr)
		status, statusAttr := t.collapsedStatus(tn, frame)
		x = writeRuneLineAttr(w, x, y, status, width, statusAttr)
		labelAttr := nameAttr
		if tn.isMemory {
			labelAttr = t.memoryIDAttr()
		} else if tn.isResult {
			labelAttr = argsAttr
		}
		if tn.hovered {
			labelAttr.Fg = term.ColorWhite
		}
		x = writeRuneLineAttr(w, x, y, tn.name, width, labelAttr)
		if tn.isMemory {
			t.writeCollapsedDuration(w, x, y, tn, width)
		} else if !tn.isResult {
			x = t.writeCollapsedDuration(w, x, y, tn, width)

			if tn.summary != "" {
				writeRuneLineAttr(w, x, y, " "+tn.summary, width, argsAttr)
			} else if args := formatToolArgs(tn.args); args != "" {
				if utf8.RuneCountInString(args) > maxToolArgs {
					args = string([]rune(args)[:maxToolArgs]) + "..."
				}
				writeRuneLineAttr(w, x, y, " "+args, width, argsAttr)
			}
		}
		y++

		for _, image := range tn.images {
			imageWidth := max(0, width-collapsedIndent)
			h := image.Height(imageWidth)
			image.Resize(imageWidth, h)
			image.Draw(&component.VirtualWriter{Writer: w,
				Offset: term.Coordinates{X: collapsedIndent, Y: y}, Width: imageWidth, Height: h})
			y += h
		}

		if tn.childTurn != nil && len(tn.childTurn.toolOrder) > 0 {
			childWidth := max(0, width-collapsedIndent)
			childH := tn.childTurn.collapsedHeightAt(childWidth)
			if !isLast {
				for cy := 0; cy < childH; cy++ {
					w.SetCell(term.Coordinates{X: 0, Y: y + cy}, term.NewCell('│', 1, tAttr))
				}
			}
			childVW := &component.VirtualWriter{
				Writer: w,
				Offset: term.Coordinates{X: collapsedIndent, Y: y},
				Width:  childWidth,
				Height: childH,
			}
			tn.childTurn.drawCollapsedAt(childVW, childWidth)
			y += childH
		}
	}
	if t.root && len(t.toolOrder) > 0 {
		writeRuneLineAttr(w, 0, y, expandHintText, width, t.cfg.ReasoningAnnotationStringConfig.Attributes)
	}
}

// collapsedStatus returns the status icon drawn before a collapsed tool
// node's name. Every spinner frame has the same width.
func (t *Turn) collapsedStatus(tn *toolNode, frame int) (string, term.Attributes) {
	if tn.isMemory {
		return memoryPrefix, t.memoryAttr()
	}
	if !tn.done {
		if tn.prompt {
			return promptPrefix, t.promptAttr()
		}
		return string(spinnerFrames[frame%len(spinnerFrames)]) + " ", term.Attributes{}
	}
	if tn.dropped {
		if tn.isError {
			return errorPrefix, t.droppedAttr()
		}
		return donePrefix, t.droppedAttr()
	}
	if tn.isResult {
		if tn.isError {
			return resultErrPrefix, t.resultErrorAttr()
		}
		return resultPrefix, t.resultAttr()
	}
	if tn.isError {
		return errorPrefix, t.errorAttr()
	}
	return donePrefix, t.successAttr()
}

// writeRuneLineAttr writes s at (x, y) with the given attributes,
// stopping at maxWidth. Returns the new x position.
func writeRuneLineAttr(w term.Writer, x, y int, s string, maxWidth int, attr term.Attributes) int {
	return tcomponent.WriteText(w, x, y, maxWidth, s, attr)
}

// formatDuration formats a duration for display in the tool call tree.
// If precision is positive the duration is truncated to that granularity
// (e.g. time.Second → "1s"). Otherwise durations >= 1s are rounded to
// seconds, >= 1ms to milliseconds, and sub-millisecond are left as-is.
func formatDuration(d time.Duration, precision time.Duration) string {
	if precision > 0 {
		return d.Truncate(precision).String()
	}
	switch {
	case d >= time.Second:
		return d.Round(time.Second).String()
	case d >= time.Millisecond:
		return d.Round(time.Millisecond).String()
	default:
		return d.String()
	}
}

// writeCollapsedDuration writes the duration for a tool node in collapsed
// mode and returns the new x position. For running tools it shows live
// elapsed time; for completed tools it shows the final duration. The color
// matches the status icon.
func (t *Turn) writeCollapsedDuration(w term.Writer, x, y int, tn *toolNode, maxWidth int) int {
	if tn.prompt {
		return x
	}
	var durStr string
	var attr term.Attributes
	prec := t.cfg.DurationPrecision
	if tn.done {
		if tn.duration <= 0 {
			return x
		}
		durStr = formatDuration(tn.duration, prec)
		if tn.dropped {
			attr = t.droppedAttr()
		} else if tn.isError {
			attr = t.errorAttr()
		} else {
			attr = t.successAttr()
		}
	} else {
		if tn.startTime.IsZero() {
			return x
		}
		durStr = formatDuration(time.Since(tn.startTime), prec)
	}
	return writeRuneLineAttr(w, x, y, " "+durStr, maxWidth, attr)
}

const (
	runningPrefix   = "⚙ "
	donePrefix      = "✓ "
	errorPrefix     = "✗ "
	promptPrefix    = "? "
	memoryPrefix    = "󰍛 "
	resultPrefix    = "󰆈 "
	resultErrPrefix = "󰅽 "
	expandHintText  = "Press <ctrl-o> to expand"
	// collapsedIndent is the width of a tree connector, which is also how
	// far each level of child tools is indented.
	collapsedIndent = 3
)

var (
	spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

	promptToolNames = map[string]bool{
		"ask_user_question":  true,
		"request_user_input": true,
		"request_skill":      true,
	}

	taskToolNames = map[string]bool{
		"TaskCreate":  true,
		"TaskUpdate":  true,
		"TaskGet":     true,
		"TaskList":    true,
		"update_plan": true,
	}
)

// IsTaskTool returns true for tool names that are part of the task
// progress protocol. These tools update the progress widget silently
// and should not be rendered as tool call entries.
func IsTaskTool(name string) bool {
	return taskToolNames[name]
}

// truncateFirstLine returns the first line of s, truncated to 60 runes.
// Used for generating short display names from sub-agent result text.
func truncateFirstLine(s string) string {
	const maxLen = 60
	line, _, _ := strings.Cut(s, "\n")
	runes := []rune(line)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "..."
	}
	return line
}

func truncateToolOutput(cfg *ComponentConfig, output string) string {
	maxLines := cfg.ToolResultMaxLines
	if maxLines == 0 {
		maxLines = 5
	}
	const maxChars = 500

	if len(output) > maxChars {
		output = output[:maxChars]
	}

	lines := strings.Split(output, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		return strings.Join(lines, "\n") + "\n..."
	}
	return output
}

// toolCallHeader renders a tool call header with the prefix (icon)
// styled independently from the name+summary text. Only the name is
// highlighted on hover, since it is the only clickable part. It satisfies
// component.Responsive with a fixed height of 1.
type toolCallHeader struct {
	prefix     string
	prefixAttr term.Attributes
	name       string
	rest       string // drawn after name
	restAttr   term.Attributes
	hovered    bool
	width      int
}

func (h *toolCallHeader) Height(int) int  { return 1 }
func (h *toolCallHeader) Resize(w, _ int) { h.width = w }
func (h *toolCallHeader) Draw(w term.Writer) {
	x := writeRuneLineAttr(w, 0, 0, h.prefix, h.width, h.prefixAttr)
	nameAttr := h.restAttr
	if h.hovered {
		nameAttr.Fg = term.ColorWhite
	}
	x = writeRuneLineAttr(w, x, 0, h.name, h.width, nameAttr)
	writeRuneLineAttr(w, x, 0, h.rest, h.width, h.restAttr)
}

// toolCallEntry creates a Responsive entry for a tool call
// header using the default ToolCallStringConfig.
func toolCallEntry(cfg *ComponentConfig, prefix string, prefixAttr term.Attributes, name, args, summary string) (component.Responsive, *toolCallHeader) {
	return toolCallEntryWithConfig(cfg, prefix, prefixAttr, name, args, summary, cfg.ToolCallStringConfig)
}

// toolCallEntryWithConfig creates a Responsive entry for a tool call
// header with a custom string config for the header text.
func toolCallEntryWithConfig(cfg *ComponentConfig, prefix string, prefixAttr term.Attributes, name, args, summary string, headerCfg component.StringConfig) (component.Responsive, *toolCallHeader) {
	list := new(component.ResponsiveList)
	list.Init()

	var rest string
	if summary != "" {
		rest = " " + summary
	}
	header := &toolCallHeader{
		prefix:     prefix,
		prefixAttr: prefixAttr,
		name:       name,
		rest:       rest,
		restAttr:   headerCfg.Attributes,
	}
	list.PushBack(header)

	formatted := formatToolArgs(args)
	if formatted != "" {
		if len(formatted) > maxToolArgs {
			formatted = formatted[:maxToolArgs] + "..."
		}
		argsComp := component.NewResponsiveString(formatted,
			component.StringResponsiveConfig{
				NoSplitWords: true,
				StringConfig: cfg.ToolCallArgsStringConfig,
			})
		list.PushBack(argsComp)
	}

	return component.NewSpan(list, cfg.ToolCallSpanConfig), header
}
