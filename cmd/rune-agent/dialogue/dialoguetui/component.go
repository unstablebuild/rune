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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/inputbox"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/glslshader"
	"unstable.build/rune/internal/component/shader/timeshader"
	"unstable.build/rune/internal/debug"
	mdhandler "unstable.build/rune/internal/handler/markdown"
	"unstable.build/rune/internal/text/standard"
)

// reasoningEntry tracks a finalized reasoning node for toggling visibility.
type reasoningEntry struct {
	node *component.ListNode  // position in messages list
	comp component.Responsive // the real reasoning component (for restore)
}

// completionList is the '#' context completion overlay drawn between the
// messages region and the attachment strip.
type completionList interface {
	tui.Component
	// InputHeight reports the rows taken by the list's own search bar,
	// which is clipped away so the compose box shows the query instead.
	InputHeight() int
}

// completionMaxRows caps the completion band so it cannot swallow the
// whole messages region.
const completionMaxRows = 10

const (
	completionRadarFPS      = 30
	completionRadarDuration = 24 * time.Hour
	completionRadarLoop     = 1200 * time.Millisecond
	// Wider than the radar default. The wedge fades to nothing at both
	// edges, so with a sweep this neutral a narrow one barely registers;
	// a broad wedge keeps enough of the frame lit to read as motion.
	completionRadarAngularWidth = 0.7
)

type loadingFrame struct {
	mu            sync.Mutex
	root          tui.Component
	shader        *shader.Component
	width, height int
}

func (f *loadingFrame) Draw(w term.Writer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.shader != nil {
		f.shader.Draw(w)
		return
	}
	f.root.Draw(w)
}

func (f *loadingFrame) Resize(width, height int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.width, f.height = width, height
	if f.shader != nil {
		f.shader.Resize(width, height)
		return
	}
	f.root.Resize(width, height)
}

func (f *loadingFrame) setLoading(
	loading bool, frame *handler.Frame, radarColor term.Color,
	interrupter term.Interrupter,
) {
	f.mu.Lock()
	if loading == (f.shader != nil) {
		f.mu.Unlock()
		return
	}
	var closing *shader.Component
	if loading {
		params := completionRadarParams(frame.FrameCharSet, radarColor)
		frameAttr := frame.Attributes
		if frameAttr.Fg == term.ColorDefault {
			frameAttr.Fg = term.ColorWhite
		}
		inner := glslshader.RadarFrame(params, frameAttr)
		cycles := int(completionRadarDuration / completionRadarLoop)
		f.shader = shader.New(f.root, timeshader.Loop(inner, cycles),
			interrupter, completionRadarFPS, completionRadarDuration)
		f.shader.Resize(f.width, f.height)
	} else {
		closing = f.shader
		f.shader = nil
	}
	f.mu.Unlock()
	if closing != nil {
		_ = closing.Close()
	}
}

// completionRadarParams builds the radar-frame parameters for the compose
// box shader, overriding the built-in sweep color only when a color is
// configured.
func completionRadarParams(
	charSet component.FrameCharSet, radarColor term.Color,
) glslshader.RadarFrameParams {
	params := glslshader.DefaultRadarFrameParams(charSet)
	params.AngularWidth = completionRadarAngularWidth
	if radarColor != term.ColorDefault {
		params.Color = radarColor
	}
	return params
}

// Component implements a dialogue tui.Component.
type Component struct {
	cfg          ComponentConfig
	messages     component.ResponsiveList
	spanMessages component.Span
	box          *handler.Frame
	loadingBox   loadingFrame
	input        Input
	height       int
	width        int

	// layout: the viewport is split vertically between the messages region
	// (top, full width) and the compose box (bottom, horizontally centered
	// according to cfg.InputRowColumns). See relayout.
	msgArea component.Virtual[tui.Component]
	boxArea component.Virtual[tui.Component]
	// attachArea is a single-row strip between the two, shown only while
	// there are pending attachments.
	attachArea  component.Virtual[tui.Component]
	attachTabs  tcomponent.Tabs
	attachments []Attachment
	// nextKey allocates draft-local attachment keys. It only ever
	// advances, so removing a chip never hands its key to a later
	// attachment and re-targets a stale inline link.
	nextKey AttachmentKey
	// completion is the '#' completion overlay, nil when closed. It is
	// drawn directly rather than through a Virtual so its own search bar
	// can be clipped out of the band.
	completion     completionList
	completionRows int
	completionPos  term.Coordinates
	// layoutBoxH and layoutMsgH are the compose box height and the total
	// messages content height observed at the last relayout. A change in
	// either means the regions must be resized again; this makes the layout
	// self-correcting without every content mutator having to opt in.
	layoutBoxH int
	layoutMsgH int

	// streamed reasoning
	reasoningMsg  strings.Builder
	reasoningTail *component.ListNode
	// reusable markdown component/handler for the current reasoning stream
	reasoningTailMd      *markdown.Component
	reasoningTailHandler *mdhandler.Handler
	// streamed receives
	msg         strings.Builder
	tail        *component.ListNode
	tailMd      *markdown.Component // reusable markdown component
	tailHandler *mdhandler.Handler  // reusable handler wrapping tailMd
	// receive hint
	hint *component.ListNode
	// tool call tracking via Turn
	currentTurn *Turn
	turnNode    *component.ListNode

	// reasoning visibility toggle
	reasoningVisible    bool                 // true = reasoning shown (default true)
	reasoningEntries    []reasoningEntry     // finalized reasoning entries
	reasoningStreamComp component.Responsive // comp for the current streaming entry
	reasoningAnnotation *component.ListNode  // annotation node ("ctrl-o to ...")

	// collapse mode for tool turns
	collapseMode       collapseMode
	toggleAnchor       component.ListNode
	toggleAnchorOffset int
	toggleAnchorValid  bool

	// prompt state
	activePrompt     *Selection
	activePromptNode *component.ListNode
	promptBodyNode   *component.ListNode // optional markdown body above prompt
	promptResult     chan<- []string
	savedHintComp    component.Responsive // hint saved during active prompt, restored on dismiss
	// prompt text input state (active when user selects a RequiresInput option)
	promptInput     *inputbox.Handler   // text input box for feedback
	promptInputNode *component.ListNode // node in messages list for the input box
	promptInputMode bool                // true when text input is active
	promptLabel     string              // label of the selected RequiresInput option
	// free-form prompt state (active when prompt has zero options)
	freeInputPrompt bool // true when the active prompt uses the main inputbox for free-form text

	// task progress tracking
	progressNode *component.ListNode
	progress     *PlanProgress

	// queued follow-up messages (displayed while agent is busy)
	queuedNodes []*component.ListNode

	// sentAttachments tracks the attachment chip grids rendered above
	// sent messages, paired with their list node so a mouse position can
	// be resolved back to a chip.
	sentAttachments []sentAttachmentGrid

	interrupter term.Interrupter
	mu          sync.Locker // set by Handler; used by AddCommand for async locking
}

// NewComponent allocates storage for a new Component and initializes it.
func NewComponent(cfg ComponentConfig) *Component {
	ret := new(Component)
	ret.Init(cfg)
	return ret
}

// Init initializes this Component with cfg.
func (c *Component) Init(cfg ComponentConfig) {
	if cfg.InputRowColumns == 0 {
		cfg.InputRowColumns = 10
	}
	if cfg.MarkdownConfig == nil {
		d := markdown.DefaultConfig()
		d.HeaderPrefix = false
		cfg.MarkdownConfig = new(d)
	}
	if cfg.ReasoningMarkdownConfig == nil {
		cfg.ReasoningMarkdownConfig = reasoningMarkdownConfig(
			*cfg.MarkdownConfig, cfg.ReasoningStringConfig)
	}
	if cfg.InputRowColumns > component.MaxCols {
		panic(fmt.Sprintf("InputRowColumns must be > 0 and <= %d", component.MaxCols))
	}
	c.cfg = cfg
	c.reasoningVisible = true
	if cfg.StartCollapsed {
		c.collapseMode = collapseModeCollapsed
	}

	c.messages.Init()
	c.messages.Alignment = component.AlignmentBottom
	c.spanMessages.Init(&c.messages, c.cfg.MessagesRowConfig)
	c.msgArea.C = &c.spanMessages

	c.input = c.newInputBackend(cfg)
	c.box = handler.NewFrame(c.input)
	c.box.SetAttr(cfg.InputBox.FrameAttr)
	if cfg.InputBackgroundColor != term.ColorDefault {
		c.box.Bg = cfg.InputBackgroundColor
	}
	if cfg.InputBox.FrameCharSet != (component.FrameCharSet{}) {
		c.box.FrameCharSet = cfg.InputBox.FrameCharSet
	}
	c.loadingBox.root = c.box
	c.boxArea.C = &c.loadingBox

	c.attachTabs.Init()
	c.attachTabs.SetBorder(false)
	c.attachArea.C = &c.attachTabs
}

func (c *Component) newInputBackend(cfg ComponentConfig) Input {
	editor := cfg.Editor
	modal := cfg.EditorModal
	if editor == nil {
		editor = standard.Editor()
		modal = false
	}
	buf := cell.NewBuffer()
	h, err := editor.Edit(context.Background(),
		dialogueComposeURI, buf, false, false)
	if err != nil {
		slog.Error("dialoguetui: compose editor unavailable, using modeless editor", "err", err)
		buf = cell.NewBuffer()
		h, err = standard.Editor().Edit(context.Background(),
			dialogueComposeURI, buf, false, false)
		if err != nil {
			panic(fmt.Sprintf("dialoguetui: default compose editor unavailable: %v", err))
		}
		modal = false
	}
	if cfg.InputBackgroundColor != term.ColorDefault {
		h.SetDefaultAttributes(term.Attributes{Bg: cfg.InputBackgroundColor})
	}
	if modal && cfg.ModalStartInsert {
		h.Handle(term.Event{Type: term.EventKey, Ch: 'i'})
	}
	return newTextHandlerInput(h, buf, cfg.InlineAttachmentAttr)
}

// Draw satisfies tui.Component.
func (c *Component) Draw(w term.Writer) {
	if c.messagesContentHeight() != c.layoutMsgH {
		c.relayout()
	} else {
		c.layoutIfDirty()
	}

	if c.cfg.BackgroundColor != 0 {
		bg := term.NewCell(0, 0, term.Attributes{Bg: c.cfg.BackgroundColor})
		for y := range c.height {
			for x := range c.width {
				w.SetCell(term.Coordinates{X: x, Y: y}, bg)
			}
		}
	}
	vw := component.VirtualWriter{Writer: w, Height: c.height, Width: c.width}
	c.msgArea.Draw(&vw)
	if c.completion != nil && c.completionRows > 0 {
		cw := component.VirtualWriter{
			Writer: &vw,
			Offset: c.completionPos,
			Width:  c.boxWidth(c.width),
			Height: c.completionRows,
		}
		c.completion.Draw(&cw)
	}
	if len(c.attachments) > 0 {
		c.attachArea.Draw(&vw)
	}
	c.boxArea.Draw(&vw)
}

// SetCompletion installs the '#' completion overlay above the compose box.
func (c *Component) SetCompletion(l completionList) {
	c.completion = l
	c.relayout()
}

// ClearCompletion removes the '#' completion overlay.
func (c *Component) ClearCompletion() {
	c.completion = nil
	c.completionRows = 0
	c.relayout()
}

// CompletionOpen reports whether the '#' completion band is showing.
func (c *Component) CompletionOpen() bool {
	return c.completion != nil
}

func (c *Component) setCompletionLoading(loading bool) {
	c.loadingBox.setLoading(loading, c.box, c.cfg.CompletionRadarColor, c.interrupter)
	_ = c.interrupter.Interrupt(context.Background())
}

// Resize satisfies tui.Component.
func (c *Component) Resize(width, height int) {
	// store height for the messages/compose split computed by relayout
	c.height = height
	// store for calculating hint size upon AddReceiveMessageHint
	c.width = width
	c.relayout()
}

// relayout recomputes the messages/compose split and resizes both regions.
// The compose box is bottom-anchored and takes cfg.InputRowColumns of the
// available MaxCols; the messages region takes the remaining rows.
func (c *Component) relayout() {
	width, height := c.width, c.height
	boxW := c.boxWidth(width)
	boxH := c.boxHeight(boxW)
	c.layoutBoxH = boxH

	if boxH > height {
		boxH = height
	}
	if boxH < 0 {
		boxH = 0
	}
	boxX := int(float64((component.MaxCols-c.cfg.InputRowColumns)/2) *
		float64(width) / float64(component.MaxCols))
	if boxX+boxW > width {
		boxW = width - boxX
	}
	attachH := 0
	if len(c.attachments) > 0 && height-boxH > 0 {
		attachH = 1
	}
	compH := 0
	if c.completion != nil {
		compH = max(0, min(completionMaxRows, height-boxH-attachH-1))
	}
	c.completionRows = compH
	msgH := height - boxH - attachH - compH

	c.msgArea.Move(term.Coordinates{})
	c.msgArea.Resize(width, msgH)
	c.completionPos = term.Coordinates{X: boxX, Y: msgH}
	if c.completion != nil {
		c.completion.Resize(boxW, compH+c.completion.InputHeight())
	}
	c.attachArea.Move(term.Coordinates{X: boxX, Y: msgH + compH})
	c.attachArea.Resize(boxW, attachH)
	c.boxArea.Move(term.Coordinates{X: boxX, Y: msgH + compH + attachH})
	c.boxArea.Resize(boxW, boxH)
	c.layoutMsgH = c.messagesContentHeight()
}

// layoutIfDirty recomputes the layout when the compose box grew or shrank
// since the last relayout. Positions are therefore fresh at query time,
// regardless of whether the host asks for the cursor before or after the draw.
func (c *Component) layoutIfDirty() {
	if c.boxHeight(c.boxWidth(c.width)) != c.layoutBoxH {
		c.relayout()
	}
}

// messagesContentHeight returns the total height the messages currently want
// at the width they were last laid out with. Message components mutate in
// place while content streams in, so this is what tells the layout that the
// rows need to be resized again.
func (c *Component) messagesContentHeight() int {
	return c.messages.Height(c.messages.SizeWidth())
}

// Input returns the compose input employed by this Component.
func (c *Component) Input() Input {
	return c.input
}

// Cursor returns the input.Box cursor.
func (c *Component) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	c.layoutIfDirty()
	return c.box.Cursor()
}

// InputPosition returns the offset of the input component.
func (c *Component) InputPosition() term.Coordinates {
	c.layoutIfDirty()
	return c.boxArea.Position()
}

// MessagesPosition returns the offset of the messages component.
func (c *Component) MessagesPosition() term.Coordinates {
	c.layoutIfDirty()
	return c.spanMessages.ContentOffset()
}

// AddAttachment appends a pending attachment to the strip above the
// compose input. It returns the attachment with its draft key, which
// callers need to link it to a range of the compose text.
func (c *Component) AddAttachment(a Attachment) Attachment {
	if a.Key == 0 {
		c.nextKey++
		a.Key = c.nextKey
	} else if a.Key > c.nextKey {
		c.nextKey = a.Key
	}
	c.attachments = append(c.attachments, a)
	idx := c.attachTabs.Add(a.Icon, a.Name)
	c.attachTabs.SetTabAction(idx, removeAttachmentIcon,
		term.Attributes{Fg: term.ColorRed})
	// The strip is a passive list: nothing in it is selected, so no tab
	// should render with the focused attributes Tabs.Add assigns to the
	// first entry.
	c.attachTabs.ResetFocus()
	c.relayout()
	return a
}

// Attachments returns the pending attachments, oldest first.
func (c *Component) Attachments() []Attachment {
	return c.attachments
}

// UpsertAttachment adds a, replacing any pending attachment carrying the
// same non-empty ID so a virtual attachment can be refreshed in place.
func (c *Component) UpsertAttachment(a Attachment) {
	if a.ID != "" {
		for i, existing := range c.attachments {
			if existing.ID != a.ID {
				continue
			}
			// Preserve the existing key so inline links to the
			// attachment survive the refresh.
			a.Key = existing.Key
			c.attachments[i] = a
			c.attachTabs.SetTabIcon(i, a.Icon)
			c.attachTabs.SetTabName(i, a.Name)
			c.attachTabs.ResetFocus()
			c.relayout()
			return
		}
	}
	c.AddAttachment(a)
}

// Draft returns a snapshot of the composed message: its text, the
// pending attachments and the ranges of the text still linked to them.
func (c *Component) Draft() Draft {
	return Draft{
		Text:        c.input.Text(),
		Attachments: slices.Clone(c.attachments),
		Links:       c.input.Links(),
	}
}

// TakeDraft returns the composed draft and clears the compose input and
// the attachment strip together, so text, chips and links can never be
// submitted out of sync.
func (c *Component) TakeDraft() Draft {
	d := Draft{
		Text:        c.input.Text(),
		Attachments: c.attachments,
		Links:       c.input.Links(),
	}
	c.attachments = nil
	c.attachTabs.RemoveAll()
	c.input.Clear()
	c.relayout()
	return d
}

// RestoreDraft replaces the compose state with d, preserving the
// attachment keys its links refer to.
func (c *Component) RestoreDraft(d Draft) {
	c.attachments = nil
	c.attachTabs.RemoveAll()
	for _, a := range d.Attachments {
		c.AddAttachment(a)
	}
	c.input.SetDraft(d.Text, d.Links)
	c.relayout()
}

// RemoveAttachment drops the attachment at idx. Its inline labels stay
// in the compose text as ordinary words; only the links are dropped.
func (c *Component) RemoveAttachment(idx int) {
	if idx < 0 || idx >= len(c.attachments) {
		return
	}
	key := c.attachments[idx].Key
	c.attachments = append(c.attachments[:idx], c.attachments[idx+1:]...)
	c.attachTabs.Remove(idx)
	c.attachTabs.ResetFocus()
	c.input.Unlink(key)
	c.relayout()
}

// AttachmentsPosition returns the offset of the attachment strip and
// whether the strip is currently displayed.
func (c *Component) AttachmentsPosition() (term.Coordinates, bool) {
	if len(c.attachments) == 0 {
		return term.Coordinates{}, false
	}
	c.layoutIfDirty()
	return c.attachArea.Position(), true
}

// AttachmentAt returns the index of the attachment rendered at pos,
// which is relative to the attachment strip.
func (c *Component) AttachmentAt(pos term.Coordinates) (int, bool) {
	if len(c.attachments) == 0 {
		return -1, false
	}
	idx, ok := c.attachTabs.TabAt(pos)
	if !ok || idx < 0 || idx >= len(c.attachments) {
		return -1, false
	}
	return idx, true
}

// AttachmentRemoveAt returns the index of the attachment whose remove
// affordance is rendered at pos, which is relative to the strip.
func (c *Component) AttachmentRemoveAt(pos term.Coordinates) (int, bool) {
	if len(c.attachments) == 0 {
		return -1, false
	}
	idx, ok := c.attachTabs.TabActionAt(pos)
	if !ok || idx < 0 || idx >= len(c.attachments) {
		return -1, false
	}
	return idx, true
}

// SentAttachmentAt returns the attachment whose chip in a sent message's
// grid covers pos, which is relative to the messages content origin.
func (c *Component) SentAttachmentAt(pos term.Coordinates) (Attachment, bool) {
	for _, e := range c.sentAttachments {
		if idx, ok := e.grid.chipAt(term.CoordinatesDiff(pos, e.origin())); ok {
			return e.grid.attachments[idx], true
		}
	}
	return Attachment{}, false
}

// HoverSentAttachment highlights the sent-message chip at pos, clearing
// any other highlight, and reports whether the rendering changed.
func (c *Component) HoverSentAttachment(pos term.Coordinates) (changed bool) {
	for _, e := range c.sentAttachments {
		idx, ok := e.grid.chipAt(term.CoordinatesDiff(pos, e.origin()))
		if !ok {
			idx = -1
		}
		if e.grid.setHovered(idx) {
			changed = true
		}
	}
	return
}

// InputSubmit submits the contents of the input buffer as a send message,
// and returns it for delivery or returns false if there's no text
// in the input buffer. There is no need to call AddSendMessage
// with the return value.
func (c *Component) InputSubmit() (string, bool) {
	text := c.input.Text()
	if len(text) == 0 {
		return "", false
	}

	c.input.Clear()
	c.AddSendMessage(text)
	return text, true
}

// AddSendMessage adds the following msg as a sent message.
func (c *Component) AddSendMessage(msg string) {
	c.AddSendMessageAttachments(msg, nil)
}

// AddSendMessageAttachments adds msg as a sent message, preceded by a
// grid of chips for the attachments that were sent with it.
func (c *Component) AddSendMessageAttachments(msg string, atts []Attachment) {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	c.clearProgress()
	if len(atts) > 0 {
		grid := newAttachmentGrid(atts, c.cfg.SendMessageStringConfig.Attributes)
		span := component.NewSpan(grid, c.cfg.SendMessageSpanConfig)
		node := c.messages.PushBack(span)
		c.sentAttachments = append(c.sentAttachments,
			sentAttachmentGrid{node: node, span: span, grid: grid})
	}
	var strComp component.Responsive
	strComp = component.NewResponsiveString(msg,
		component.StringResponsiveConfig{
			NoSplitWords: true,
			StringConfig: c.cfg.SendMessageStringConfig,
		})
	strComp = component.NewSpan(strComp, c.cfg.SendMessageSpanConfig)
	c.messages.PushBack(strComp)
	if c.cfg.SendMessageBottomPad > 0 {
		c.messages.PushBack(spacer(c.cfg.SendMessageBottomPad))
	}
	c.moveHintToBack()
}

// queuedPrefix returns the prefix used for queued message rendering.
func (c *Component) queuedPrefix() string {
	if c.cfg.QueuedMessagePrefix != "" {
		return c.cfg.QueuedMessagePrefix
	}
	return "󰄝 "
}

// AddQueuedMessage adds msg to the message list with a pending indicator.
// The node is tracked so it can be removed or promoted later.
func (c *Component) AddQueuedMessage(msg string) {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	var strComp component.Responsive
	strComp = component.NewResponsiveString(c.queuedPrefix()+msg,
		component.StringResponsiveConfig{
			NoSplitWords: true,
			StringConfig: c.cfg.QueuedMessageStringConfig,
		})
	strComp = component.NewSpan(strComp, c.cfg.QueuedMessageSpanConfig)
	node := new(component.ListNode)
	*node = c.messages.PushBack(strComp)
	c.queuedNodes = append(c.queuedNodes, node)
	c.moveHintToBack()
}

// RemoveLastQueuedMessage removes the most recently added queued message
// from the display. Called when the user recalls a queued message for editing.
func (c *Component) RemoveLastQueuedMessage() {
	if len(c.queuedNodes) == 0 {
		return
	}
	node := c.queuedNodes[len(c.queuedNodes)-1]
	c.queuedNodes = c.queuedNodes[:len(c.queuedNodes)-1]
	c.messages.Remove(node)
}

// PromoteFirstQueuedMessage converts the first (oldest) queued message
// from pending to sent by replacing its visual component. Called when
// the agent finishes and the queued message is injected.
func (c *Component) PromoteFirstQueuedMessage() {
	if len(c.queuedNodes) == 0 {
		return
	}
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	c.clearProgress()
	node := c.queuedNodes[0]
	c.queuedNodes = c.queuedNodes[1:]
	// Replace the queued node content with a normal sent message style.
	// We need to extract the original text from the queued prefix.
	// Since we can't easily extract text from the component, we remove
	// the old node and insert a sent-style component in its place.
	c.messages.Remove(node)
}

// QueueLen returns the number of queued follow-up messages.
func (c *Component) QueueLen() int {
	return len(c.queuedNodes)
}

// AddSendMessageMarkdown adds a sent message rendered as markdown.
// Falls back to plain text on parse errors.
func (c *Component) AddSendMessageMarkdown(msg string) {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	c.clearProgress()
	var strComp component.Responsive
	md, err := markdown.NewWithConfig(msg, *c.cfg.MarkdownConfig)
	if err != nil {
		strComp = component.NewResponsiveString(msg,
			component.StringResponsiveConfig{
				NoSplitWords: true,
				StringConfig: c.cfg.SendMessageStringConfig,
			})
	} else {
		strComp = md
	}
	strComp = component.NewSpan(strComp, c.cfg.SendMessageSpanConfig)
	c.messages.PushBack(strComp)
	if c.cfg.SendMessageBottomPad > 0 {
		c.messages.PushBack(spacer(c.cfg.SendMessageBottomPad))
	}
	c.moveHintToBack()
}

// AddReasoningChunk adds a reasoning text chunk. Reasoning is rendered
// as markdown styled with ReasoningMarkdownConfig (typically dim gray),
// falling back to plain text on parse errors. When text streaming begins
// via AddReceiveMessageChunk, any active reasoning stream is finalized
// automatically.
func (c *Component) AddReasoningChunk(chunk string) {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	c.reasoningMsg.WriteString(chunk)

	// Fast path: re-parse in-place on the existing markdown component.
	// Only valid while reasoning is visible; when hidden the tail node
	// holds a NopResponsive and must be re-attached below.
	if c.reasoningVisible && c.reasoningTailMd != nil {
		if err := c.reasoningTailMd.Init(c.reasoningMsg.String()); err == nil {
			c.moveHintToBack()
			return
		}
		// Parse error on previously-working content: fall through to recreate.
	}

	if c.reasoningTail != nil {
		c.messages.Remove(c.reasoningTail)
	} else {
		c.reasoningTail = new(component.ListNode)
	}

	var resp component.Responsive
	md, err := markdown.NewWithConfig(
		c.reasoningMsg.String(), *c.cfg.ReasoningMarkdownConfig)
	if err != nil {
		c.reasoningTailMd = nil
		c.reasoningTailHandler = nil
		resp = component.NewResponsiveString(c.reasoningMsg.String(),
			component.StringResponsiveConfig{
				NoSplitWords: true,
				StringConfig: c.cfg.ReasoningStringConfig,
			})
	} else {
		c.reasoningTailMd = md
		c.reasoningTailHandler = mdhandler.New(md)
		resp = c.reasoningTailHandler
	}
	resp = component.NewSpan(resp, c.cfg.ReasoningSpanConfig)
	c.reasoningStreamComp = resp

	if c.reasoningVisible {
		*c.reasoningTail = c.messages.PushBack(resp)
	} else {
		*c.reasoningTail = c.messages.PushBack(component.NopResponsive())
	}
	c.ensureReasoningAnnotation()
	c.moveHintToBack()
}

// reasoningMarkdownConfig derives a markdown config for reasoning text
// from the base config, restyling every element with the reasoning
// string config's attributes so the whole block reads as dim meta-text
// while still honoring markdown structure.
func reasoningMarkdownConfig(
	base markdown.Config, style component.StringConfig,
) *markdown.Config {
	attr := style.Attributes
	base.H1 = attr
	base.H2 = attr
	base.H3 = attr
	base.H4 = attr
	base.H5 = attr
	base.H6 = attr
	base.Paragraph = attr
	base.Bold = attr
	base.Italic = attr
	base.Strikethrough = attr
	base.CodeBlock = attr
	base.InlineCode = attr
	base.Link = attr
	base.LinkURL = attr
	base.Blockquote = attr
	base.HorizontalRuleAttr = attr
	base.ParagraphSpacing = 0
	base.Parser = nil
	return &base
}

// breakReasoning finalizes an active reasoning stream so subsequent
// content appears as a separate entry.
func (c *Component) breakReasoning() {
	if c.reasoningTail != nil {
		c.reasoningEntries = append(c.reasoningEntries, reasoningEntry{
			node: c.reasoningTail,
			comp: c.reasoningStreamComp,
		})
	}
	c.reasoningTail = nil
	c.reasoningMsg.Reset()
	c.reasoningStreamComp = nil
	c.reasoningTailMd = nil
	c.reasoningTailHandler = nil
}

// ensureReasoningAnnotation adds or replaces the reasoning toggle
// annotation at the back of the messages list.
func (c *Component) ensureReasoningAnnotation() {
	if c.reasoningAnnotation != nil {
		c.messages.Remove(c.reasoningAnnotation)
		c.reasoningAnnotation = nil
	}
	text := "ctrl-o to collapse"
	if c.collapseMode.isCollapsed() {
		text = "ctrl-o to expand"
	}
	var ann component.Responsive
	ann = component.NewResponsiveString(text,
		component.StringResponsiveConfig{
			StringConfig: c.cfg.ReasoningAnnotationStringConfig,
		})
	ann = component.FuncResponsive(ann, func(width int) int { return 1 })
	c.reasoningAnnotation = new(component.ListNode)
	*c.reasoningAnnotation = c.messages.PushBack(ann)
}

// setReasoningVisible sets the visibility of all reasoning entries.
func (c *Component) setReasoningVisible(visible bool) {
	c.reasoningVisible = visible
	for _, e := range c.reasoningEntries {
		if c.reasoningVisible {
			e.node.SetValue(e.comp)
		} else {
			e.node.SetValue(component.NopResponsive())
		}
	}
	if c.reasoningTail != nil {
		if c.reasoningVisible {
			c.reasoningTail.SetValue(c.reasoningStreamComp)
		} else {
			c.reasoningTail.SetValue(component.NopResponsive())
		}
	}
}

// ToggleReasoningVisible toggles the visibility of all reasoning entries.
// It returns the new visibility state.
func (c *Component) ToggleReasoningVisible() bool {
	c.setReasoningVisible(!c.reasoningVisible)
	c.ensureReasoningAnnotation()
	return c.reasoningVisible
}

// ToggleContracted toggles collapse mode:
// collapsed → expanded → collapsed.
// Reasoning is hidden in collapsed mode and visible when expanded.
func (c *Component) ToggleContracted() {
	anchor := component.ListNode{}
	anchorOffset := 0
	shouldPreserve := false
	if c.toggleAnchorValid {
		anchor = c.toggleAnchor
		anchorOffset = c.toggleAnchorOffset
		shouldPreserve = true
		c.toggleAnchorValid = false
	} else if c.messages.CanSeekUp() {
		var hasAnchor bool
		anchor, hasAnchor = c.topVisibleMessage()
		if hasAnchor {
			anchorOffset = c.messagesOffset(anchor)
			shouldPreserve = true
		}
	}
	c.collapseMode = c.collapseMode.next()
	c.setCollapseMode(c.collapseMode)
	c.setReasoningVisible(!c.collapseMode.isCollapsed())
	if c.reasoningAnnotation != nil {
		c.ensureReasoningAnnotation()
	}
	c.refreshMessagesLayout()
	if shouldPreserve {
		c.alignVisibleAnchorTop(anchor, anchorOffset)
		if c.collapseMode.isCollapsed() {
			c.toggleAnchor = anchor
			c.toggleAnchorOffset = anchorOffset
			c.toggleAnchorValid = true
		}
	} else {
		c.toggleAnchorValid = false
	}
}

// setCollapseMode propagates the collapse mode to all Turn entries.
func (c *Component) setCollapseMode(mode collapseMode) {
	node, ok := c.messages.Front()
	for ok {
		if turn, isTurn := node.Value().(*Turn); isTurn {
			turn.SetCollapseMode(mode)
		}
		node, ok = node.Next()
	}
}

// AddReceiveMessage adds the following message as a received message.
func (c *Component) AddReceiveMessage(msg string) {
	c.AddReceiveMessageChunk(msg)
	c.AddReceiveMessageBreak()
}

// AddReceiveMessageBreak breaks a receive message stream
// such that next call to AddReceiveMessageChunk will add
// a new chunk of data as a new message.
func (c *Component) AddReceiveMessageBreak() {
	c.breakReasoning()
	c.breakReceiveMessage()
	// Complete and close all turns, not just the current one,
	// since text breaks may have detached earlier turns.
	node, ok := c.messages.Front()
	for ok {
		if turn, isTurn := node.Value().(*Turn); isTurn {
			turn.completeRunning()
			turn.Close()
		}
		node, ok = node.Next()
	}
	c.currentTurn = nil
	c.turnNode = nil
}

// breakReceiveMessage finalizes any in-progress receive message stream,
// inserting a bottom-padding spacer after the message when configured.
func (c *Component) breakReceiveMessage() {
	if c.tail != nil && c.cfg.ReceiveMessageBottomPad > 0 {
		c.messages.InsertAfter(spacer(c.cfg.ReceiveMessageBottomPad), *c.tail)
	}
	c.tail = nil
	c.tailMd = nil
	c.tailHandler = nil
	c.msg.Reset()
}

// AddReceiveMessageChunk adds the following message chunk
// as a received message. A new message is started
// by calling AddReceiveMessageBreak.
func (c *Component) AddReceiveMessageChunk(chunk string) {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	c.breakReasoning()
	// Detach the current turn so that subsequent tool calls create
	// a new Turn at the current position, preserving chronological
	// order of text and tool calls.
	c.currentTurn = nil
	c.turnNode = nil
	c.msg.WriteString(chunk)

	// Fast path: re-parse in-place on the existing component.
	if c.tailMd != nil {
		if err := c.tailMd.Init(c.msg.String()); err == nil {
			c.moveHintToBack()
			return
		}
		// Parse error on previously-working content: fall through to recreate.
	}

	// First chunk or error: create component, handler, span, list node.
	if c.tail != nil {
		c.messages.Remove(c.tail)
	} else {
		c.tail = new(component.ListNode)
	}

	var resp component.Responsive
	md, err := markdown.NewWithConfig(c.msg.String(), *c.cfg.MarkdownConfig)
	if err != nil {
		c.tailMd = nil
		c.tailHandler = nil
		resp = component.NewResponsiveString(c.msg.String(),
			component.StringResponsiveConfig{
				NoSplitWords: true,
				StringConfig: c.cfg.ReceiveMessageStringConfig,
			})
	} else {
		c.tailMd = md
		c.tailHandler = mdhandler.New(md)
		resp = c.tailHandler
	}
	resp = component.NewSpan(resp, c.cfg.ReceiveMessageSpanConfig)
	*c.tail = c.messages.PushBack(resp)
	c.moveHintToBack()
}

// AddErrorMessage adds an inline error message to the message list.
// It breaks any in-progress text and reasoning streams.
func (c *Component) AddErrorMessage(msg string) {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	c.breakReasoning()
	c.breakReceiveMessage()

	var errComp component.Responsive
	errComp = component.NewResponsiveString("! "+msg,
		component.StringResponsiveConfig{
			NoSplitWords: true,
			StringConfig: c.cfg.ErrorStringConfig,
		})
	errComp = component.NewSpan(errComp, c.cfg.ErrorSpanConfig)
	c.messages.PushBack(errComp)
	c.moveHintToBack()
}

// AddWarningMessage adds an inline warning message to the message list.
// It breaks any in-progress text and reasoning streams. Warnings are
// non-fatal and rendered with WarningStringConfig (typically amber/yellow).
func (c *Component) AddWarningMessage(msg string) {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	c.breakReasoning()

	var warnComp component.Responsive
	warnComp = component.NewResponsiveString(msg,
		component.StringResponsiveConfig{
			NoSplitWords: true,
			StringConfig: c.cfg.WarningStringConfig,
		})
	warnComp = component.NewSpan(warnComp, c.cfg.WarningSpanConfig)
	c.messages.PushBack(warnComp)
	c.moveHintToBack()
}

// AddCommand starts an asynchronous command output drain. It adds an
// animation node to the message list, spawns a goroutine to consume
// items from it, and cleans up when done. The caller must NOT hold mu.
func (c *Component) AddCommand(ctx context.Context, it iterator.Iterator[component.Responsive]) {
	frames := []string{".  ", ".. ", "...", " ..", "  .", "   "}
	seq := []int{0, 1, 2, 3, 4, 5}
	anim := component.NewAnimation(c.interrupter, frames, seq, 8)
	animResp := component.FuncResponsive(anim, func(width int) int { return 1 })

	c.mu.Lock()
	c.breakReasoning()
	c.breakReceiveMessage()
	maxOff, scrolled := c.scrollState()
	animNode := new(component.ListNode)
	*animNode = c.messages.PushBack(animResp)
	c.restoreScroll(maxOff, scrolled)
	c.mu.Unlock()
	_ = c.interrupter.Interrupt(ctx)

	go debug.CapturePanicReport(func() {

		defer func() { _ = it.Close() }()
		defer func() {
			c.mu.Lock()
			c.messages.Remove(animNode)
			if it.Err() != nil && !errors.Is(it.Err(), context.Canceled) {
				c.AddErrorMessage(it.Err().Error())
			}
			c.mu.Unlock()
			_ = anim.Close()
			_ = c.interrupter.Interrupt(ctx)
		}()

		for {
			item, ok := it.Next(ctx)
			if !ok {
				break
			}
			c.mu.Lock()
			maxOff, scrolled := c.scrollState()
			wrapped := component.NewSpan(item, c.cfg.CommandOutputSpanConfig)
			c.messages.PushBack(wrapped)
			c.restoreScroll(maxOff, scrolled)
			c.mu.Unlock()
			_ = c.interrupter.Interrupt(ctx)
		}

	})
}

// AddCommandOutput adds a single command output item to the message list.
func (c *Component) AddCommandOutput(item component.Responsive) {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	wrapped := component.NewSpan(item, c.cfg.CommandOutputSpanConfig)
	c.messages.PushBack(wrapped)
	c.moveHintToBack()
}

func (c *Component) promptAnchorNode() *component.ListNode {
	if c.promptBodyNode != nil {
		return c.promptBodyNode
	}
	if c.activePromptNode != nil {
		return c.activePromptNode
	}
	return nil
}

// AddMemoryRecall adds a memory recall tree node with individual
// memories nested as children. Memory entries are ephemeral — they
// appear in the UI but don't survive conversation reload.
func (c *Component) AddMemoryRecall(memories []MemoryRecallEntry, duration time.Duration) {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	c.ensureCurrentTurn()
	c.currentTurn.AddMemoryRecall(memories, duration)
	c.moveHintToBack()
}

// UpdateTaskProgress upserts a task in the progress checklist.
// The progress node is created on first call and persists across
// message breaks (it is only cleared on conversation reset).
func (c *Component) UpdateTaskProgress(entry ProgressTaskEntry) {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)

	if c.progress == nil {
		c.progress = NewPlanProgress(&c.cfg, c.interrupter)
		c.progressNode = new(component.ListNode)
		*c.progressNode = c.messages.PushBack(c.progress)
	}
	c.progress.UpdateTask(entry)
	c.moveHintToBack()
}

// TaskActiveForm returns the active task text to use in the status hint.
// When the progress checklist is visible, it returns an empty string so
// the hint falls back to the current phase label instead of duplicating
// the task subject already shown in the checklist.
func (c *Component) TaskActiveForm() string {
	if c.progress == nil {
		return ""
	}
	return ""
}

// AddToolCall adds a tool invocation indicator to the message list.
// It breaks any in-progress text stream. The id should be the unique
// tool call identifier from the LLM response.
func (c *Component) AddToolCall(id, name, args, summary string) {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	// break any in-progress streams
	c.breakReasoning()
	c.breakReceiveMessage()

	c.ensureCurrentTurn()
	c.currentTurn.AddToolCall(id, name, args, summary)
	c.moveHintToBack()
}

// CompleteToolCall replaces the running tool indicator with a completed
// header (✓ or ✗) and truncated output. If no prior AddToolCall was
// made for this id (e.g. when replaying stored messages), a new entry
// is created directly in a completed state.
func (c *Component) CompleteToolCall(id, name, args, summary, output string, isError bool) {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	c.ensureCurrentTurn()
	if _, ok := c.currentTurn.tools[id]; !ok {
		// No running entry — add one so CompleteToolCall can update it.
		c.currentTurn.AddToolCall(id, name, args, summary)
	}
	c.currentTurn.CompleteToolCall(id, name, args, summary, output, isError)
	c.moveHintToBack()
}

// AddChildToolCall adds a tool call nested under a parent tool call
// in the current turn.
func (c *Component) AddChildToolCall(parentID, id, name, args, summary string) {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	c.ensureCurrentTurn()
	c.currentTurn.AddChildToolCall(parentID, id, name, args, summary)
	c.moveHintToBack()
}

// CompleteChildToolCall completes a child tool call nested under a
// parent tool call in the current turn.
func (c *Component) CompleteChildToolCall(parentID, id, name, args, summary, output string, isError bool) {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	if c.currentTurn == nil {
		return
	}
	c.currentTurn.CompleteChildToolCall(parentID, id, name, args, summary, output, isError)
	c.moveHintToBack()
}

// AddChildResult adds a sub-agent result leaf node under a parent
// tool call in the current turn.
func (c *Component) AddChildResult(parentID, output string, isError bool) {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	if c.currentTurn == nil {
		return
	}
	c.currentTurn.AddChildResult(parentID, output, isError)
	c.moveHintToBack()
}

// SetToolStartTime sets the start time for a tool call, used for live
// elapsed display in collapsed mode.
func (c *Component) SetToolStartTime(id string, t time.Time) {
	if c.currentTurn == nil {
		return
	}
	c.currentTurn.SetToolStartTime(id, t)
}

// SetToolDuration sets the final execution duration for a completed
// tool call. Call this before CompleteToolCall so the expanded-mode
// header includes the duration.
func (c *Component) SetToolDuration(id string, d time.Duration) {
	if c.currentTurn == nil {
		return
	}
	c.currentTurn.SetToolDuration(id, d)
}

// MarkToolsDropped marks tool calls whose results were dropped from
// conversation history, changing their icons to gray. It walks all
// Turn nodes in the messages list.
func (c *Component) MarkToolsDropped(ids []string) {
	node, ok := c.messages.Front()
	for ok {
		if turn, isTurn := node.Value().(*Turn); isTurn {
			turn.MarkDropped(ids)
		}
		node, ok = node.Next()
	}
}

// SeekDown seeks the history panel down.
func (c *Component) SeekDown() bool {
	c.toggleAnchorValid = false
	return c.messages.SeekDown()
}

// SeekUp seeks the history panel up.
func (c *Component) SeekUp() bool {
	c.toggleAnchorValid = false
	return c.messages.SeekUp()
}

func (c *Component) refreshMessagesLayout() {
	width, height := c.messages.SizeWidth(), c.messages.SizeHeight()
	if width <= 0 || height <= 0 {
		return
	}
	c.messages.Resize(width, height)
}

func (c *Component) topVisibleMessage() (component.ListNode, bool) {
	c.refreshMessagesLayout()
	width, height := c.messages.SizeWidth(), c.messages.SizeHeight()
	for node, ok := c.messages.Front(); ok; node, ok = node.Next() {
		resp, ok := node.Value().(component.Responsive)
		if !ok {
			continue
		}
		top := node.Position().Y
		bottom := top + resp.Height(width)
		if bottom > 0 && top < height {
			return node, true
		}
	}
	return component.ListNode{}, false
}

func (c *Component) nodeTop(target component.ListNode) (int, bool) {
	width := c.messages.SizeWidth()
	top := 0
	for node, ok := c.messages.Front(); ok; node, ok = node.Next() {
		if node == target {
			return top, true
		}
		resp, ok := node.Value().(component.Responsive)
		if !ok {
			continue
		}
		top += resp.Height(width)
	}
	return 0, false
}

func (c *Component) messagesOffset(anchor component.ListNode) int {
	top, ok := c.nodeTop(anchor)
	if !ok {
		return 0
	}
	return anchor.Position().Y - top + c.messages.MaxOffset()
}

func (c *Component) alignVisibleAnchorTop(anchor component.ListNode, offset int) {
	top, ok := c.nodeTop(anchor)
	if !ok {
		return
	}
	currentTop := top - c.messages.MaxOffset() + offset
	if currentTop > 0 {
		for range currentTop {
			if !c.messages.SeekDown() {
				return
			}
		}
		return
	}
	for range -currentTop {
		if !c.messages.SeekUp() {
			return
		}
	}
}

// LinkAt queries for a markdown link at the messages-relative coordinate pos.
// It searches visible message nodes in c.messages, unwrapping any outer spans
// or handlers to find the underlying markdown component, and returns the
// LinkInfo if a link is found.
func (c *Component) LinkAt(pos term.Coordinates) *markdown.LinkInfo {
	width, height := c.messages.SizeWidth(), c.messages.SizeHeight()
	if width <= 0 || height <= 0 {
		return nil
	}
	if pos.X < 0 || pos.X >= width || pos.Y < 0 || pos.Y >= height {
		return nil
	}
	c.refreshMessagesLayout()
	for node, ok := c.messages.Front(); ok; node, ok = node.Next() {
		resp, ok := node.Value().(component.Responsive)
		if !ok {
			continue
		}
		top := node.Position().Y
		bottom := top + resp.Height(width)
		if pos.Y < top || pos.Y >= bottom {
			continue
		}
		md, spanOffset := extractMarkdown(node.Value())
		if md == nil {
			continue
		}
		relX := pos.X - (node.Position().X + spanOffset.X)
		relY := pos.Y - (node.Position().Y + spanOffset.Y)
		if link := md.LinkAt(relX, relY); link != nil && link.URL != "" {
			return link
		}
	}
	return nil
}

func extractMarkdown(c any) (*markdown.Component, term.Coordinates) {
	var offset term.Coordinates
	for c != nil {
		switch v := c.(type) {
		case *component.Span:
			offset = term.CoordinatesSum(offset, v.ContentOffset())
			c = v.Content()
		case *mdhandler.Handler:
			return v.Component(), offset
		case *markdown.Component:
			return v, offset
		default:
			return nil, term.Coordinates{}
		}
	}
	return nil, term.Coordinates{}
}

// scrollState captures the current scroll position for later restoration.
func (c *Component) scrollState() (maxOffset int, scrolledUp bool) {
	return c.messages.MaxOffset(), c.messages.CanSeekUp()
}

// restoreScroll adjusts the scroll offset to keep the viewport stable
// after content has been appended to the messages list. When the user
// is scrolled up and new content increases MaxOffset, the offset is
// incremented by the same delta so the viewport shows the same rows.
func (c *Component) restoreScroll(oldMaxOffset int, wasScrolledUp bool) {
	if !wasScrolledUp {
		return
	}
	for range c.messages.MaxOffset() - oldMaxOffset {
		c.messages.SeekUp()
	}
}

// AddReceiveMessageHint adds a hint in the UI that
// a message is about to be received.
// The hint remains visible while content streams in (moved to the
// back of the list automatically). Remove it with RemoveReceiveMessageHint.
func (c *Component) AddReceiveMessageHint(
	hint tui.Component, config component.SpanConfig,
) {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	if c.hint != nil {
		c.RemoveReceiveMessageHint()
	}
	c.hint = new(component.ListNode)
	hintSpan := component.NewSpan(hint, config)
	resp := component.FuncResponsive(hintSpan, func(width int) int {
		return 1
	})
	*c.hint = c.messages.PushBack(resp)
}

// RemoveReceiveMessageHint idempotently removes a hint
// from the UI previously added via AddReceiveMessageHint.
func (c *Component) RemoveReceiveMessageHint() {
	// Only clear the saved hint when no prompt is active.
	// When a prompt is showing, the hint lives in savedHintComp
	// and will be restored by removePrompt; clearing it here
	// would silently destroy the hint.
	if c.activePrompt == nil {
		c.savedHintComp = nil
	}
	if c.hint == nil {
		return
	}

	node := c.hint
	c.messages.Remove(node)
	c.hint = nil
}

// Height satisfies component.Responsive.
func (c *Component) Height(width int) (height int) {
	// do not use container.Height, as first row (messages) is designed
	// to take the remaining space
	height = c.boxHeight(c.boxWidth(width))
	height += c.spanMessages.Height(width)
	return
}

// Reset resets the messages of this Component.
// This does not reset the InputBox, this
// can be performed, if desired, via Component.Input().Reset().
func (c *Component) Reset() {
	c.RemoveReceiveMessageHint()
	c.resetContent()
}

// ResetPreservingHint resets all messages like Reset but keeps the
// receive-message hint visible. Use this during auto-compaction so
// that the progress/animation hint survives the message replay.
func (c *Component) ResetPreservingHint() {
	var savedHint component.Responsive
	if c.hint != nil {
		savedHint = c.messages.Remove(c.hint)
		c.hint = nil
	}
	c.savedHintComp = nil
	c.resetContent()
	if savedHint != nil {
		c.hint = new(component.ListNode)
		*c.hint = c.messages.PushBack(savedHint)
	}
}

// resetContent clears turns, reasoning, progress, and the message list.
// The hint must be handled by the caller before invoking this method.
func (c *Component) resetContent() {
	c.closeAllTurns()
	c.currentTurn = nil
	c.turnNode = nil
	c.breakReasoning()
	c.reasoningEntries = nil
	c.reasoningStreamComp = nil
	if c.reasoningAnnotation != nil {
		c.messages.Remove(c.reasoningAnnotation)
		c.reasoningAnnotation = nil
	}
	if c.progress != nil {
		c.progress.Close()
		c.progress = nil
		c.progressNode = nil
	}
	c.queuedNodes = nil
	c.sentAttachments = nil
	c.msg.Reset()
	c.tail = nil
	c.tailMd = nil
	c.tailHandler = nil
	c.messages.Reset()
}

// AddPrompt creates a prompt in the messages list.
// When options is non-empty, a Selection widget is shown.
// When options is empty, the question title is rendered as a message and
// the main inputbox is used for free-form text entry (freeInputPrompt mode).
// If body is non-empty it is rendered as markdown above the prompt.
// If a prompt is already active, it is dismissed (nil sent on old channel)
// and the returned channel is the old one; otherwise it is nil.
func (c *Component) AddPrompt(
	title, header, body string,
	options []PromptEventOption,
	multiSelect bool,
	resultCh chan<- []string,
) chan<- []string {
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)
	var oldCh chan<- []string
	if c.activePromptNode != nil || c.freeInputPrompt {
		oldCh = c.promptResult
		c.removePrompt()
	}

	// Save and hide the hint while the prompt is active. It will be
	// restored automatically when the prompt is dismissed or selected.
	if c.hint != nil {
		c.savedHintComp = c.messages.Remove(c.hint)
		c.hint = nil
	}

	// Render optional body as markdown before the selection.
	if body != "" {
		var resp component.Responsive
		md, err := markdown.NewWithConfig(body, *c.cfg.MarkdownConfig)
		if err != nil {
			resp = component.NewResponsiveString(body,
				component.StringResponsiveConfig{
					NoSplitWords: true,
					StringConfig: c.cfg.ReceiveMessageStringConfig,
				})
		} else {
			resp = md
		}
		resp = component.NewSpan(resp, c.cfg.ReceiveMessageSpanConfig)
		c.promptBodyNode = new(component.ListNode)
		*c.promptBodyNode = c.messages.PushBack(resp)
	}

	if len(options) == 0 {
		// Free-form prompt: render the question as a receive message and
		// let the user type into the main inputbox.
		label := title
		if header != "" {
			label = header + ": " + title
		}
		qComp := component.NewSpan(
			component.NewResponsiveString(label,
				component.StringResponsiveConfig{
					NoSplitWords: true,
					StringConfig: c.cfg.ReceiveMessageStringConfig,
				}),
			c.cfg.ReceiveMessageSpanConfig,
		)
		c.activePromptNode = new(component.ListNode)
		*c.activePromptNode = c.messages.PushBack(qComp)
		c.freeInputPrompt = true
		c.promptResult = resultCh
		c.moveHintToBack()
		return oldCh
	}

	selOptions := make([]SelectionOption, len(options))
	for i, o := range options {
		selOptions[i] = SelectionOption(o)
	}
	sel := NewSelection(title, header, selOptions, multiSelect, c.cfg.SelectionConfig)
	c.activePrompt = sel
	c.promptResult = resultCh
	c.activePromptNode = new(component.ListNode)
	*c.activePromptNode = c.messages.PushBack(sel)
	c.moveHintToBack()
	return oldCh
}

// removePrompt removes the active prompt node (and optional body node)
// from the messages list without touching the result channel.
func (c *Component) removePrompt() {
	if c.activePromptNode == nil && c.activePrompt == nil && !c.freeInputPrompt {
		return
	}
	if c.promptBodyNode != nil {
		c.messages.Remove(c.promptBodyNode)
		c.promptBodyNode = nil
	}
	if c.activePromptNode != nil {
		c.messages.Remove(c.activePromptNode)
	}
	c.activePrompt = nil
	c.activePromptNode = nil
	c.promptResult = nil
	c.freeInputPrompt = false

	// Restore the hint that was saved when the prompt was added.
	if c.savedHintComp != nil {
		c.hint = new(component.ListNode)
		*c.hint = c.messages.PushBack(c.savedHintComp)
		c.savedHintComp = nil
	}
}

// HasFreeInputPrompt returns true if a free-form text prompt is active.
func (c *Component) HasFreeInputPrompt() bool {
	return c.freeInputPrompt
}

// PrepareFreeInputSubmit extracts text from the main inputbox, removes the
// prompt, and returns the result channel and typed text.
// Returns (nil, "") if no free-form prompt is active or text is empty.
func (c *Component) PrepareFreeInputSubmit() (chan<- []string, string) {
	if !c.freeInputPrompt {
		return nil, ""
	}
	text := strings.TrimSpace(c.input.Text())
	if text == "" {
		return nil, ""
	}
	c.input.Clear()
	c.AddSendMessage(text)
	ch := c.promptResult
	c.removePrompt()
	return ch, text
}

// HasActivePrompt returns true if a prompt is currently active.
func (c *Component) HasActivePrompt() bool {
	return c.activePrompt != nil
}

// PromptMoveUp moves the prompt cursor up.
func (c *Component) PromptMoveUp() {
	if c.activePrompt != nil {
		c.activePrompt.MoveUp()
	}
}

// PromptMoveDown moves the prompt cursor down.
func (c *Component) PromptMoveDown() {
	if c.activePrompt != nil {
		c.activePrompt.MoveDown()
	}
}

// PromptToggle toggles the current item (multiSelect only).
func (c *Component) PromptToggle() {
	if c.activePrompt != nil {
		c.activePrompt.Toggle()
	}
}

// PreparePromptSelect removes the prompt and returns the result
// channel and selected values. The caller must send vals on ch
// outside the lock. Returns (nil, nil) if no prompt is active.
func (c *Component) PreparePromptSelect() (chan<- []string, []string) {
	if c.activePrompt == nil {
		return nil, nil
	}
	vals := c.activePrompt.Selected()
	ch := c.promptResult
	c.removePrompt()
	return ch, vals
}

// PreparePromptDismiss removes the prompt and returns the result
// channel. The caller must send nil on ch outside the lock.
// Returns nil if no prompt is active.
func (c *Component) PreparePromptDismiss() chan<- []string {
	if c.activePrompt == nil && !c.promptInputMode && !c.freeInputPrompt {
		return nil
	}
	c.removePromptInput()
	ch := c.promptResult
	c.removePrompt()
	return ch
}

// PromptInputMode returns true if the prompt is in text input mode.
func (c *Component) PromptInputMode() bool {
	return c.promptInputMode
}

// PromptInput returns the active prompt input box, or nil.
func (c *Component) PromptInput() *inputbox.Handler {
	return c.promptInput
}

// StartPromptInput transitions from selection mode to text input mode.
// It hides the selection, creates an inputbox, and pushes it to the list.
// Returns false if no prompt is active or the current option does not
// require input.
func (c *Component) StartPromptInput() bool {
	if c.activePrompt == nil || !c.activePrompt.CursorRequiresInput() {
		return false
	}
	maxOff, scrolled := c.scrollState()
	defer c.restoreScroll(maxOff, scrolled)

	c.promptLabel = c.activePrompt.Selected()[0]

	// Remove the selection node from the list (but keep activePrompt
	// reference so HasActivePrompt still returns true for the outer
	// check in the handler — we clear it on dismiss).
	if c.activePromptNode != nil {
		c.messages.Remove(c.activePromptNode)
		c.activePromptNode = nil
	}

	// Create a simple inputbox for typing feedback.
	ib := inputbox.New(
		inputbox.WithPlaceholderText("Type your feedback and press Enter to submit (Ctrl-C to go back)"),
	)
	// Ensure the inputbox has a non-zero width so that Cursor() does not
	// divide by zero before the next Draw/layout pass sizes it properly.
	if c.width > 0 {
		ib.Resize(c.width, 1)
	}
	// Use the same styling as the main input box.
	if c.cfg.InputBox.ContentAttr != (term.Attributes{}) {
		ib.SetAttr(c.cfg.InputBox.ContentAttr)
	}

	c.promptInput = ib
	c.promptInputMode = true
	c.promptInputNode = new(component.ListNode)
	*c.promptInputNode = c.messages.PushBack(ib)
	return true
}

// removePromptInput cleans up the text input node if active.
func (c *Component) removePromptInput() {
	if !c.promptInputMode {
		return
	}
	if c.promptInputNode != nil {
		c.messages.Remove(c.promptInputNode)
		c.promptInputNode = nil
	}
	c.promptInput = nil
	c.promptInputMode = false
	c.promptLabel = ""
}

// PreparePromptInputSubmit removes the prompt input and returns the
// result channel, the selected label, and the typed text.
// Returns (nil, "", "") if not in input mode or text is empty.
func (c *Component) PreparePromptInputSubmit() (chan<- []string, string, string) {
	if !c.promptInputMode || c.promptInput == nil {
		return nil, "", ""
	}
	text := strings.TrimSpace(c.promptInput.Text())
	if text == "" {
		return nil, "", ""
	}
	label := c.promptLabel
	ch := c.promptResult
	c.removePromptInput()
	c.removePrompt()
	return ch, label, text
}

// PromptInputCursor returns the cursor position for the prompt input box
// in coordinates relative to the Component's origin.
func (c *Component) PromptInputCursor() (term.Coordinates, term.CursorStyle, bool) {
	if !c.promptInputMode || c.promptInput == nil || c.promptInputNode == nil {
		return term.Coordinates{}, 0, false
	}
	cursor, style, ok := c.promptInput.Cursor()
	if !ok {
		return cursor, style, false
	}
	// Offset by the node's position in the messages list.
	nodePos := c.promptInputNode.Position()
	cursor = term.CoordinatesSum(cursor, nodePos)
	return cursor, style, true
}

func (c *Component) boxWidth(width int) int {
	return int(float64(width) * float64(c.cfg.InputRowColumns) / float64(component.MaxCols))
}

// minMessagesRows is the number of message rows the compose box must
// always leave visible so a growing input never hides the conversation.
const minMessagesRows = 2

// boxHeight returns the compose box height capped so it never consumes
// the whole viewport. The editor's frame and content can report a height
// taller than the screen for long buffers; without a cap the messages row
// collapses to zero and stale conversation rows show through the input.
// The capped box scrolls its content internally instead.
func (c *Component) boxHeight(width int) int {
	h := c.box.Height(width)
	if c.height <= 0 {
		return h
	}
	maxH := c.height - minMessagesRows
	const minH = 3 // top border + one content row + bottom border
	if maxH < minH {
		maxH = minH
	}
	if h > maxH {
		return maxH
	}
	return h
}

const maxToolArgs = 100

// formatToolArgs converts a JSON arguments string into a human-readable
// key=value representation. Falls back to the raw string on parse error.
func formatToolArgs(jsonArgs string) string {
	if jsonArgs == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonArgs), &m); err != nil {
		return jsonArgs
	}
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		v := m[k]
		if v == nil {
			continue
		}
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k)
		b.WriteByte('=')
		switch v := v.(type) {
		case string:
			b.WriteString(strings.ReplaceAll(v, "\n", " "))
		default:
			data, _ := json.Marshal(v)
			b.Write(data)
		}
	}
	return b.String()
}

func (c *Component) moveHintToBack() {
	c.moveProgressToBack()
	c.moveQueuedToBack()
	if c.hint == nil {
		return
	}
	hintResp := c.messages.Remove(c.hint)
	c.hint = new(component.ListNode)
	*c.hint = c.messages.PushBack(hintResp)
}

func (c *Component) moveQueuedToBack() {
	for i, node := range c.queuedNodes {
		resp := c.messages.Remove(node)
		c.queuedNodes[i] = new(component.ListNode)
		*c.queuedNodes[i] = c.messages.PushBack(resp)
	}
}

func (c *Component) moveProgressToBack() {
	if c.progressNode == nil {
		return
	}
	progressResp := c.messages.Remove(c.progressNode)
	c.progressNode = new(component.ListNode)
	*c.progressNode = c.messages.PushBack(progressResp)
}

// clearProgress removes the task progress checklist from the message
// list. It is called when the user submits a new message so that
// completed tasks from the previous turn do not persist.
func (c *Component) clearProgress() {
	if c.progress == nil {
		return
	}
	c.messages.Remove(c.progressNode)
	c.progress.Close()
	c.progress = nil
	c.progressNode = nil
}

func (c *Component) closeAllTurns() {
	node, ok := c.messages.Front()
	for ok {
		if turn, isTurn := node.Value().(*Turn); isTurn {
			turn.Close()
		}
		node, ok = node.Next()
	}
}

func (c *Component) ensureCurrentTurn() {
	if c.currentTurn != nil {
		return
	}
	c.currentTurn = NewTurn(&c.cfg, c.interrupter)
	c.currentTurn.root = true
	c.currentTurn.mode = c.collapseMode
	c.turnNode = new(component.ListNode)
	if anchor := c.promptAnchorNode(); anchor != nil {
		*c.turnNode = c.messages.InsertBefore(c.currentTurn, *anchor)
	} else {
		*c.turnNode = c.messages.PushBack(c.currentTurn)
	}
}

// spacer returns a Responsive component that occupies height rows
// and draws nothing. It is used to separate messages with visible
// backgrounds from their neighbours without leaking the background
// color into the gap.
func spacer(height int) component.Responsive {
	return component.FuncResponsive(nopDraw{}, func(int) int { return height })
}

type nopDraw struct{}

func (nopDraw) Resize(int, int)  {}
func (nopDraw) Draw(term.Writer) {}
