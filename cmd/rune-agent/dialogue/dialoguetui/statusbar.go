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
	"strings"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/template"
	"unstable.build/rune/internal/debug"
)

// DefaultStatusBarLayout is used when the status bar is enabled but no
// layout could be resolved from configuration. Spinner and Status carry
// no colours of their own because the status palette supplies them.
const DefaultStatusBarLayout = ` {{ .Spinner }} ` +
	`{{ .Status | bold }} █▓▒░` +
	`  {{ .Model | fg "white" }}  {{ .Effort }}` +
	`{{ .ShiftRight }}󰗂 {{ .Cache }}  {{ .Elapsed }}` +
	`  {{ .Conversation | bold | fg "white" }}` +
	`  {{ .ContextGauge }} `

// DefaultStatusBarAnimation is what a status without an animation of
// its own spins. It is a braille snake: consecutive frames differ by
// one dot, so the spinner reads as something travelling around the
// cell rather than flickering between unrelated glyphs.
const DefaultStatusBarAnimation = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

// defaultGaugeWidth is the fixed-width field used by the context and
// cache gauges when configuration does not set one.
const defaultGaugeWidth = 18

// DefaultGaugeStartRune and DefaultGaugeEndRune bracket a gauge, each
// taking a cell out of its width. The ramp already marks the track's
// extent, so neither bracket earns its cell by default. A zero rune
// drops that bracket and gives its cell back to the track.
const (
	DefaultGaugeStartRune = 0
	DefaultGaugeEndRune   = 0
)

// idleStatusText is what the Status element reads between turns. The
// bar is always visible, so a blank left edge reads as a broken bar
// rather than as a waiting agent.
const idleStatusText = "IDLE"

// AskingStatusText is the phase a host reports while the turn is
// blocked on a prompt. The bar treats it specially: it outranks a task
// description, because what the turn was doing matters less than the
// fact that it cannot go on until the user answers.
const AskingStatusText = "ASKING"

// DefaultStatusBarBackground and DefaultStatusBarForeground are the
// bar's base attributes, matching the editor's status bar so both read
// as the same widget.
const (
	DefaultStatusBarBackground = term.ColorGray
	DefaultStatusBarForeground = term.ColorSilver
)

// The gauge track sits flush with the bar; the caps are what mark its
// extent, and its foreground doubles as the label colour over the
// unfilled side.
var (
	defaultGaugeEmptyAttr = term.Attributes{
		Fg: term.ColorSilver, Bg: term.ColorGray,
	}

	// The caps sit on the bar rather than on the track, so their
	// background is filled in from the bar at build time.
	defaultGaugeCapAttr = term.Attributes{Fg: term.ColorGreen}

	defaultSpinnerFrames = SpinnerFrames(DefaultStatusBarAnimation)
)

// DefaultContextGaugeFill and DefaultCacheGaugeFill are the shipped
// ramps. A full context window is bad news and a full cache is good
// news, so the two run the same stops in opposite directions. Every
// stop is dark-on-bright: the ramp only ever passes through saturated
// colours, which nothing lighter stays readable on.
var (
	DefaultContextGaugeFill = []term.Attributes{
		{Fg: term.ColorBlack, Bg: term.ColorGreen},
		{Fg: term.ColorBlack, Bg: term.ColorYellow},
		{Fg: term.ColorBlack, Bg: term.ColorRed},
	}
	DefaultCacheGaugeFill = []term.Attributes{
		{Fg: term.ColorBlack, Bg: term.ColorRed},
		{Fg: term.ColorBlack, Bg: term.ColorYellow},
		{Fg: term.ColorBlack, Bg: term.ColorGreen},
	}
)

// DefaultStatuses dresses the bar's left edge by what the turn is
// currently doing, so both the colour and the motion of the spinner
// read as a state at a glance rather than as generic busyness.
var DefaultStatuses = map[string]StatusBarStatusConfig{
	idleStatusText: {
		Attrs: term.Attributes{
			Fg: term.ColorWhite, Bg: term.ColorSilver, Attrs: term.AttrBold,
		},
		Animation: StatusBarAnimation{
			Frames: SpinnerFrames(""),
			Attrs:  term.Attributes{Fg: term.ColorYellow},
		},
	},
	"SENDING": {
		Attrs: term.Attributes{
			Fg: term.ColorBlack, Bg: term.ColorTeal, Attrs: term.AttrBold,
		},
		Animation: StatusBarAnimation{
			Frames: SpinnerFrames("⡘⡌⠆⢃⢡⠰"),
		},
	},
	"REASONING": {
		Attrs: term.Attributes{
			Fg: term.ColorWhite, Bg: term.ColorNavy, Attrs: term.AttrBold,
		},
		Animation: StatusBarAnimation{
			Frames: SpinnerFrames(DefaultStatusBarAnimation),
		},
	},
	"RECEIVING": {
		Attrs: term.Attributes{
			Fg: term.ColorBlack, Bg: term.ColorFuchsia, Attrs: term.AttrBold,
		},
		Animation: StatusBarAnimation{
			Frames: SpinnerFrames("⢡⢃⠆⡌⡘⠰"),
		},
	},
	"EXECUTING": {
		Attrs: term.Attributes{Bg: term.ColorRed, Attrs: term.AttrBold},
		Animation: StatusBarAnimation{
			Frames: SpinnerFrames("⢀⢀⣀⢄⢂⢀⣀⣠⣤⣦⣧⣶⣦⣧⣦⣤⣴⣼⣴⣶⣧⣦⣤⣴⣼⣴⣶⣧⣦⣤⣴⣼⣴⣶⣧⣦⣤⣄⣀⣀⡠⡠⠔⠊⠁⠁  "),
		},
	},
	"COMPACTING": {
		Attrs: term.Attributes{Bg: term.ColorGray, Attrs: term.AttrBold},
		Animation: StatusBarAnimation{
			Frames: SpinnerFrames("⣉⠶⠶⠒⠒⠒⠶⠶⣉"),
		},
	},
	AskingStatusText: {
		Attrs: term.Attributes{
			Fg: term.ColorBlack, Bg: term.ColorYellow, Attrs: term.AttrBold,
		},
		Animation: StatusBarAnimation{
			Frames: SpinnerFrames("⠁⠂⠄⡀⢀⠠⠐⠈"),
		},
	},
	"ERROR": {
		Attrs: term.Attributes{Bg: term.ColorMaroon, Attrs: term.AttrBold},
		Animation: StatusBarAnimation{
			Frames: SpinnerFrames(""),
			Attrs:  term.Attributes{Fg: term.ColorRed},
		},
	},
}

// SpinnerFrames splits an animation into the frames the Spinner element
// cycles through, one per rune.
func SpinnerFrames(animation string) []string {
	if animation == "" {
		return nil
	}
	return strings.Split(animation, "")
}

// StatusBarAnimation is what the Spinner element draws while the bar
// reports one status.
type StatusBarAnimation struct {
	// Frames are cycled one per repaint. A single frame is how an
	// animation reads as a still icon; none at all falls back to
	// DefaultStatusBarAnimation.
	Frames []string
	// Attrs overrides the status attributes for the spinner alone, so
	// the icon can carry a colour of its own without breaking the pill
	// it sits on. Fields left zero keep the status's.
	Attrs term.Attributes
}

// StatusBarStatusConfig is how the bar reports one turn status.
type StatusBarStatusConfig struct {
	// Attrs colours the Spinner and Status elements while the bar
	// reports this status.
	Attrs term.Attributes
	// Animation is what the Spinner element draws for this status.
	Animation StatusBarAnimation
}

// statusBarActiveInterval and statusBarIdleInterval are the repaint
// periods used while a turn runs and while the agent waits.
const (
	statusBarActiveInterval = 125 * time.Millisecond
	statusBarIdleInterval   = time.Second
)

// StatusBarConfig configures the dialogue status bar.
type StatusBarConfig struct {
	// Enabled reports whether the dialogue reserves its bottom row for
	// the status bar. A disabled bar occupies no rows at all.
	Enabled bool
	// Layout is the parsed element list. A nil layout falls back to
	// DefaultStatusBarLayout.
	Layout []StatusBarComponent
	// BackgroundColor fills the whole row, including the gaps between
	// elements.
	BackgroundColor term.Color
	// ForegroundColor is the default foreground for layout elements
	// that do not name one of their own. A zero value leaves every
	// element to the terminal default.
	ForegroundColor term.Color
	// GaugeWidth is the cell width of the context and cache gauges.
	GaugeWidth int
	// GaugeEmptyAttr styles the empty side of a gauge.
	GaugeEmptyAttr term.Attributes
	// GaugeStartRune and GaugeEndRune bracket a gauge, each taking one
	// cell out of GaugeWidth. A zero rune drops that bracket.
	GaugeStartRune rune
	GaugeEndRune   rune
	// GaugeCapAttr styles the brackets. A zero background inherits
	// BackgroundColor so they read as part of the bar.
	GaugeCapAttr term.Attributes
	// ContextGaugeFill and CacheGaugeFill are the ramps the two gauges
	// fill through, laid across the track in order. Reversing a ramp
	// is what reverses which end of a gauge reads as the good one.
	ContextGaugeFill []term.Attributes
	CacheGaugeFill   []term.Attributes
	// Statuses maps a turn phase, plus the idle status, to how the
	// Spinner and Status elements dress while the bar reports it.
	// Phases missing from the map keep the attributes their layout
	// element declared and spin DefaultStatusBarAnimation.
	Statuses map[string]StatusBarStatusConfig
	// Shader names an effect drawn over the whole bar while a turn
	// runs. An empty name leaves the bar unshaded.
	Shader string
	// ShaderFPS is the cadence the effect is redrawn at. A zero or
	// negative value falls back to DefaultStatusBarShaderFPS.
	ShaderFPS int
	// ShaderLoop is how long one visual loop of the effect lasts.
	// Effects whose clock runs off the frame index rather than
	// against the loop, such as blaze and inferno, animate in real
	// time and ignore it. A zero or negative value falls back to
	// DefaultStatusBarShaderLoop.
	ShaderLoop time.Duration
	// DurationPrecision, when positive, truncates the elapsed time to
	// this granularity.
	DurationPrecision time.Duration
}

// StatusBarState is everything the status bar reports. It is replaced
// wholesale by the host through Component.SetStatusBarState.
type StatusBarState struct {
	// Active reports whether a turn is running. An inactive bar blanks
	// the spinner and falls back to idleStatusText, but keeps every
	// other element.
	Active bool
	// Phase describes what the turn is currently doing.
	Phase string
	// ActiveForm is the running task description from the plan widget.
	// It takes precedence over Phase when set.
	ActiveForm string
	// TurnStart is when the current turn began.
	TurnStart time.Time

	// Conversation names the open chat, matching its tab label.
	Conversation string

	Model     string
	Provider  string
	Effort    string
	MaxTokens int

	// Usage is the latest cumulative usage of the current turn.
	Usage llmapi.DialogueUsage
	// ContextTokens and ContextWindow describe how much of the model's
	// context window the conversation occupies.
	ContextTokens int
	ContextWindow int
}

// statusBarSegment pairs an element's live reference with the template
// it renders through and the last component built for it. doRebuildBar
// restores every segment from built before re-evaluating overflow.
type statusBarSegment struct {
	ref      *component.FloatingReference
	template StatusBarComponent
	built    component.Floating
}

func (s *statusBarSegment) restore() {
	s.ref.Init(s.built)
}

func (s *statusBarSegment) hide() {
	s.ref.Init(nil)
}

// StatusBar renders the dialogue's bottom row.
type StatusBar struct {
	mu    sync.Mutex
	cfg   StatusBarConfig
	state StatusBarState

	width     int
	drawCount int
	// statusWidth is the widest status the palette can report, which
	// every status is padded to so the elements beside the pill hold
	// still from one turn phase to the next.
	statusWidth int

	segments map[StatusBarComponentType]*statusBarSegment
	barLeft  component.Virtual[component.Floating]
	barRight component.Virtual[component.Floating]

	cancel func()
}

// NewStatusBar allocates a status bar for the given layout. The
// interrupter drives the repaint ticker; a nil interrupter leaves the
// bar static, which is what tests want.
func NewStatusBar(cfg StatusBarConfig, interrupter term.Interrupter) *StatusBar {
	if cfg.GaugeWidth <= 0 {
		cfg.GaugeWidth = defaultGaugeWidth
	}
	if cfg.GaugeEmptyAttr == (term.Attributes{}) {
		cfg.GaugeEmptyAttr = defaultGaugeEmptyAttr
	}
	if cfg.GaugeCapAttr == (term.Attributes{}) {
		cfg.GaugeCapAttr = defaultGaugeCapAttr
	}
	if cfg.GaugeCapAttr.Bg == term.ColorDefault {
		cfg.GaugeCapAttr.Bg = cfg.BackgroundColor
	}
	if cfg.GaugeStartRune == 0 {
		cfg.GaugeStartRune = DefaultGaugeStartRune
	}
	if len(cfg.ContextGaugeFill) == 0 {
		cfg.ContextGaugeFill = DefaultContextGaugeFill
	}
	if len(cfg.CacheGaugeFill) == 0 {
		cfg.CacheGaugeFill = DefaultCacheGaugeFill
	}
	if cfg.Statuses == nil {
		cfg.Statuses = DefaultStatuses
	}
	if cfg.Layout == nil {
		layout, err := ParseStatusBarLayout(DefaultStatusBarLayout)
		if err != nil {
			panic(fmt.Sprintf("dialoguetui: default status bar layout: %v", err))
		}
		cfg.Layout = layout
	}
	b := &StatusBar{cfg: cfg, cancel: func() {}}
	for status := range cfg.Statuses {
		b.statusWidth = max(b.statusWidth, len([]rune(status)))
	}
	b.initLayout()
	b.rebuild()
	if interrupter != nil {
		b.startTicker(interrupter)
	}
	return b
}

func (b *StatusBar) initLayout() {
	b.segments = make(map[StatusBarComponentType]*statusBarSegment, len(b.cfg.Layout))

	var left, right []component.Floating
	var shifted bool
	for _, comp := range b.cfg.Layout {
		if comp.Type == StatusBarVoid {
			shifted = true
			continue
		}
		seg, ok := b.segments[comp.Type]
		if !ok {
			seg = &statusBarSegment{ref: component.NewFloatingReference(nil)}
			b.segments[comp.Type] = seg
		}
		seg.template = comp
		if shifted {
			right = append(right, seg.ref)
		} else {
			left = append(left, seg.ref)
		}
	}

	b.barLeft.C = component.Inline(left, component.AlignmentLeft)
	b.barRight.C = component.Inline(right, component.AlignmentRight)
}

// startTicker repaints the bar while its contents change on their own,
// which is only ever during a turn.
func (b *StatusBar) startTicker(interrupter term.Interrupter) {
	if interrupter == nil {
		return
	}
	if !b.has(StatusBarSpinner) && !b.has(StatusBarElapsed) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	go debug.CapturePanicReport(func() {
		ticker := time.NewTicker(statusBarActiveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			b.mu.Lock()
			active := b.state.Active
			b.mu.Unlock()
			if !active {
				// Nothing in the bar changes between turns; the
				// slow poll only notices the next one starting.
				ticker.Reset(statusBarIdleInterval)
				continue
			}
			ticker.Reset(statusBarActiveInterval)
			_ = interrupter.Interrupt(ctx)
		}
	})
}

func (b *StatusBar) has(t StatusBarComponentType) bool {
	_, ok := b.segments[t]
	return ok
}

// Close stops the repaint ticker.
func (b *StatusBar) Close() error {
	b.cancel()
	return nil
}

// SetState applies fn to the bar state and rebuilds the row.
func (b *StatusBar) SetState(fn func(*StatusBarState)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fn(&b.state)
	b.rebuild()
}

// State returns a copy of the current bar state.
func (b *StatusBar) State() StatusBarState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Resize satisfies tui.Component.
func (b *StatusBar) Resize(width, height int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.width = width
	b.doRebuildBar()
}

// Draw satisfies tui.Component.
func (b *StatusBar) Draw(w term.Writer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.drawCount++
	b.rebuild()

	if b.cfg.BackgroundColor != term.ColorDefault {
		attrs := term.Attributes{Bg: b.cfg.BackgroundColor}
		for x := range b.width {
			w.UnionAttributes(term.Coordinates{X: x}, attrs)
		}
	}
	b.barLeft.Draw(w)
	b.barRight.Draw(w)

	// The bar sits on the last row of the chat, so the GUI has to
	// shift it down half a cell like the editor's own status bar.
	offset := term.Attributes{Attrs: term.AttrVerticalRenderOffset}
	for x := range b.width {
		w.UnionAttributes(term.Coordinates{X: x}, offset)
	}
}

// spinnerText returns the current frame of the status's animation. A
// single-frame animation is how a status reads as a still icon. A
// status with none of its own spins the default one while a turn runs,
// and blanks between turns so the elements to its right do not jump.
func (b *StatusBar) spinnerText() string {
	frames := b.cfg.Statuses[b.statusKey()].Animation.Frames
	if len(frames) == 0 {
		if !b.state.Active {
			return " "
		}
		frames = defaultSpinnerFrames
	}
	return frames[b.drawCount%len(frames)]
}

func (b *StatusBar) statusLabel() string {
	return b.padStatus(b.statusName())
}

func (b *StatusBar) statusName() string {
	if !b.state.Active {
		return idleStatusText
	}
	if b.state.Phase == AskingStatusText {
		return b.state.Phase
	}
	if b.state.ActiveForm != "" {
		return b.state.ActiveForm
	}
	return b.state.Phase
}

// padStatus left-aligns a status in the widest one the palette
// reports. The elements beside the pill hold still from one phase to
// the next either way, but padding only the right keeps the gap
// between the spinner and the text the same for every status. A
// description longer than any of them is left alone.
func (b *StatusBar) padStatus(text string) string {
	pad := b.statusWidth - len([]rune(text))
	if pad <= 0 {
		return text
	}
	return text + strings.Repeat(" ", pad)
}

// statusKey is what the status palette is looked up by. It tracks the
// phase rather than statusLabel, so an ActiveForm description does not
// cost the element its colour.
func (b *StatusBar) statusKey() string {
	if !b.state.Active {
		return idleStatusText
	}
	return b.state.Phase
}

// elapsed reports the wall time spent in the current turn, or the
// duration of the last one once the turn has ended.
func (b *StatusBar) elapsed() time.Duration {
	if !b.state.Active {
		return b.state.Usage.TotalDuration
	}
	if b.state.TurnStart.IsZero() {
		return 0
	}
	d := time.Since(b.state.TurnStart)
	if b.cfg.DurationPrecision > 0 {
		d = d.Truncate(b.cfg.DurationPrecision)
	}
	return d
}

func (b *StatusBar) contextRatio() float64 {
	if b.state.ContextWindow <= 0 {
		return 0
	}
	return float64(b.state.ContextTokens) / float64(b.state.ContextWindow)
}

func (b *StatusBar) cacheRatio() float64 {
	if b.state.Usage.TokensSent <= 0 {
		return 0
	}
	return float64(b.state.Usage.TokensCached) / float64(b.state.Usage.TokensSent)
}

// rebuild renders every element of the layout and then re-evaluates
// which of them fit the current width.
func (b *StatusBar) rebuild() {
	b.buildText(StatusBarSpinner, b.spinnerText())
	b.buildText(StatusBarStatus, b.statusLabel())
	b.buildText(StatusBarConversation, b.state.Conversation)
	b.buildText(StatusBarElapsed, b.elapsedText())
	b.buildText(StatusBarModel, b.state.Model)
	b.buildText(StatusBarProvider, b.state.Provider)
	b.buildText(StatusBarEffort, b.effortText())
	b.buildText(StatusBarMaxTokens, b.maxTokensText())
	b.buildText(StatusBarTokensSent, FormatTokenNumber(b.state.Usage.TokensSent))
	b.buildText(StatusBarTokensReceived, FormatTokenNumber(b.state.Usage.TokensReceived))
	b.buildText(StatusBarContextTokens, FormatTokenNumber(b.state.ContextTokens))
	b.buildText(StatusBarContextWindow, FormatTokenNumber(b.state.ContextWindow))
	b.buildText(StatusBarContextPct, fmt.Sprintf("%.0f%%", b.contextRatio()*100))
	b.buildText(StatusBarCache, b.cacheText())
	b.buildText(StatusBarCachePct, fmt.Sprintf("%.0f%%", b.cacheRatio()*100))

	b.buildGauge(StatusBarContextGauge, b.cfg.ContextGaugeFill, b.contextRatio(),
		fmt.Sprintf("%s/%s",
			FormatTokenNumber(b.state.ContextTokens),
			FormatTokenNumber(b.state.ContextWindow)))
	b.buildGauge(StatusBarCacheGauge, b.cfg.CacheGaugeFill, b.cacheRatio(),
		fmt.Sprintf("cache %.0f%%", b.cacheRatio()*100))

	b.doRebuildBar()
}

func (b *StatusBar) elapsedText() string {
	d := b.elapsed()
	if d <= 0 {
		return ""
	}
	return FormatStatusDuration(d)
}

// effortText renders an unset reasoning effort as the model default
// rather than as an empty element.
func (b *StatusBar) effortText() string {
	if b.state.Effort == "" {
		return "default"
	}
	return b.state.Effort
}

func (b *StatusBar) maxTokensText() string {
	if b.state.MaxTokens <= 0 {
		return ""
	}
	return FormatTokenNumber(b.state.MaxTokens)
}

// cacheText drops rather than claiming a 0% miss on a conversation
// that has not sent anything to report a rate on yet.
func (b *StatusBar) cacheText() string {
	if b.state.Usage.TokensSent <= 0 {
		return ""
	}
	return fmt.Sprintf("%.0f%%", b.cacheRatio()*100)
}

func (b *StatusBar) buildText(t StatusBarComponentType, value string) {
	seg, ok := b.segments[t]
	if !ok {
		return
	}
	if value == "" {
		// The literals around an element belong to it, so an element
		// with nothing to say must not leave its separators or its
		// pill behind.
		seg.built = nil
		seg.hide()
		return
	}
	attrs := seg.template.Attributes
	if attrs.Fg == term.ColorDefault {
		attrs.Fg = b.cfg.ForegroundColor
	}
	components := template.Build(seg.template.Template, value,
		b.paletteAttrs(t), attrs, b.cfg.BackgroundColor)
	seg.built = component.Inline(components, component.AlignmentLeft)
	seg.ref.Init(seg.built)
}

// paletteAttrs returns the status-dependent override for an element, or
// the zero value for elements the palette does not colour.
func (b *StatusBar) paletteAttrs(t StatusBarComponentType) term.Attributes {
	status := b.cfg.Statuses[b.statusKey()]
	switch t {
	case StatusBarStatus:
		return status.Attrs
	case StatusBarSpinner:
		// The spinner shares the status pill, so the animation only
		// overrides what it names and inherits the rest. A colour of
		// its own on a background of its own would read as a second
		// element rather than as an icon on the pill.
		ret := status.Attrs
		if c := status.Animation.Attrs.Fg; c != term.ColorDefault {
			ret.Fg = c
		}
		if c := status.Animation.Attrs.Bg; c != term.ColorDefault {
			ret.Bg = c
		}
		ret.Attrs |= status.Animation.Attrs.Attrs
		return ret
	default:
		return term.Attributes{}
	}
}

func (b *StatusBar) buildGauge(
	t StatusBarComponentType, fill []term.Attributes, ratio float64, label string,
) {
	seg, ok := b.segments[t]
	if !ok {
		return
	}
	g := &gauge{
		label:     label,
		ratio:     ratio,
		width:     b.cfg.GaugeWidth,
		fill:      fill,
		empty:     b.cfg.GaugeEmptyAttr,
		startRune: b.cfg.GaugeStartRune,
		endRune:   b.cfg.GaugeEndRune,
		capAttr:   b.cfg.GaugeCapAttr,
	}
	// A gauge paints two attribute pairs of its own, so it cannot go
	// through template.Build; the literals the layout put on either
	// side of it still have to.
	attrs := seg.template.Attributes
	if attrs.Fg == term.ColorDefault {
		attrs.Fg = b.cfg.ForegroundColor
	}
	prefix, suffix, _ := strings.Cut(seg.template.Template, "%s")
	parts := b.gaugePadding(prefix, attrs)
	parts = append(parts, g)
	parts = append(parts, b.gaugePadding(suffix, attrs)...)
	seg.built = component.Inline(parts, component.AlignmentLeft)
	seg.ref.Init(seg.built)
}

func (b *StatusBar) gaugePadding(
	text string, attrs term.Attributes,
) []component.Floating {
	if text == "" {
		return nil
	}
	return template.Build(text, "", term.Attributes{}, attrs,
		b.cfg.BackgroundColor)
}

// statusBarDropOrder is the order in which elements give up their cells
// when the row is too narrow. Spinner and Status are never dropped;
// Status clips at whatever width is left.
var statusBarDropOrder = [][]StatusBarComponentType{
	{StatusBarMaxTokens},
	{StatusBarConversation},
	{StatusBarCacheGauge},
	{StatusBarCache, StatusBarCachePct},
	{StatusBarProvider},
	{StatusBarEffort},
	{StatusBarTokensSent, StatusBarTokensReceived},
	{StatusBarContextTokens, StatusBarContextWindow, StatusBarContextPct},
	{StatusBarModel},
	{StatusBarContextGauge},
	{StatusBarElapsed},
}

// doRebuildBar restores every element and then hides them in priority
// order until the row fits, rather than truncating the status text.
func (b *StatusBar) doRebuildBar() {
	for _, seg := range b.segments {
		seg.restore()
	}

	leftW, rightW := b.barDimensions()
	for _, group := range statusBarDropOrder {
		if leftW+rightW <= b.width {
			break
		}
		for _, t := range group {
			if seg, ok := b.segments[t]; ok {
				seg.hide()
			}
		}
		leftW, rightW = b.barDimensions()
	}

	if leftW+rightW > b.width {
		leftW, rightW = b.clipStatus(leftW, rightW)
	}

	if leftW > b.width {
		leftW = b.width
		rightW = 0
	} else if leftW+rightW > b.width {
		rightW = b.width - leftW
	}

	b.barLeft.Move(term.Coordinates{})
	b.barLeft.Resize(leftW, 1)
	b.barRight.Move(term.Coordinates{X: b.width - rightW})
	b.barRight.Resize(rightW, 1)
}

// clipStatus shortens the status text to whatever the other surviving
// elements leave behind.
func (b *StatusBar) clipStatus(leftW, rightW int) (int, int) {
	seg, ok := b.segments[StatusBarStatus]
	if !ok {
		return leftW, rightW
	}
	segW, _ := seg.ref.Dimensions()
	available := b.width - (leftW + rightW - segW)
	text := []rune(b.statusLabel())
	for len(text) > 0 && segW > available {
		text = text[:len(text)-1]
		b.buildText(StatusBarStatus, string(text))
		segW, _ = seg.ref.Dimensions()
		leftW, rightW = b.barDimensions()
		available = b.width - (leftW + rightW - segW)
	}
	return b.barDimensions()
}

func (b *StatusBar) barDimensions() (left, right int) {
	left, _ = b.barLeft.C.Dimensions()
	right, _ = b.barRight.C.Dimensions()
	return left, right
}

// FormatStatusDuration formats a duration for the status bar.
func FormatStatusDuration(d time.Duration) string {
	d = d.Truncate(time.Second)
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	m := s / 60
	s %= 60
	if m < 60 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := m / 60
	m %= 60
	return fmt.Sprintf("%dh %dm", h, m)
}

// FormatTokenCount formats a token count with its unit.
func FormatTokenCount(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d tokens", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk tokens", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fm tokens", float64(n)/1_000_000)
	}
}

// FormatTokenNumber formats a token count without the "tokens" suffix
// and without decimal places.
func FormatTokenNumber(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.0fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%.0fm", float64(n)/1_000_000)
	}
}
