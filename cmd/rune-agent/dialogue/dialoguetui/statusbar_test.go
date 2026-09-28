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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
)

const testStatusBarLayout = `{{ .Spinner }} {{ .Status }}` +
	`{{ .ShiftRight }}{{ .Model }} {{ .Effort }} {{ .ContextGauge }}` +
	` {{ .CacheGauge }} {{ .Elapsed }}`

// idlePillAttr is what the shipped palette paints the spinner and the
// status with between turns.
var idlePillAttr = DefaultStatuses[idleStatusText].Attrs

// shippedStatusBar builds a bar from the shipped layout and palette.
func shippedStatusBar(t *testing.T) *StatusBar {
	t.Helper()
	layout, err := ParseStatusBarLayout(DefaultStatusBarLayout)
	require.NoError(t, err)
	return NewStatusBar(StatusBarConfig{
		Enabled:         true,
		Layout:          layout,
		BackgroundColor: DefaultStatusBarBackground,
		ForegroundColor: DefaultStatusBarForeground,
	}, nil)
}

// Elements that do not name a foreground inherit the bar's, so a theme
// can restyle the whole row from one key instead of every element.
func TestStatusBarForegroundColorIsInherited(t *testing.T) {
	layout, err := ParseStatusBarLayout(`{{ .Effort }}{{ .ShiftRight }}{{ .Model | fg "white" }}`)
	require.NoError(t, err)
	bar := NewStatusBar(StatusBarConfig{
		Enabled: true, Layout: layout,
		BackgroundColor: term.ColorGray,
		ForegroundColor: term.ColorSilver,
	}, nil)
	bar.SetState(func(s *StatusBarState) {
		s.Model = "m"
		s.Effort = "e"
	})
	bar.Resize(20, 1)

	rec := newCellRecorder()
	bar.Draw(rec)
	row := strings.ReplaceAll(rec.row(20), "\x00", " ")

	effort := slices.Index([]rune(row), 'e')
	require.GreaterOrEqual(t, effort, 0)
	assert.Equal(t, term.ColorSilver,
		rec.cells[term.Coordinates{X: effort}].Fg,
		"an element without its own fg inherits the bar's")

	model := slices.Index([]rune(row), 'm')
	require.GreaterOrEqual(t, model, 0)
	assert.Equal(t, term.ColorWhite,
		rec.cells[term.Coordinates{X: model}].Fg,
		"an element with its own fg keeps it")
}

// The shipped palette matches the editor's status bar: a gray row that
// only the status pill and the gauges break out of.
func TestStatusBarShippedPaletteMatchesEditorBar(t *testing.T) {
	assert.Equal(t, term.ColorGray, DefaultStatusBarBackground)
	assert.Equal(t, term.ColorSilver, DefaultStatusBarForeground)

	bar := shippedStatusBar(t)
	bar.SetState(func(s *StatusBarState) {
		s.Model = "claude-opus-5"
		s.ContextWindow = 1_000_000
	})
	bar.Resize(120, 1)

	rec := newCellRecorder()
	bar.Draw(rec)
	for x := range 120 {
		cell := rec.cells[term.Coordinates{X: x}]
		switch cell.Bg {
		case idlePillAttr.Bg, defaultGaugeEmptyAttr.Bg:
			// The gauges are empty here, so their whole field is
			// track: a filled cell leaking outside one would still
			// show up as a cell that is neither.
			continue
		}
		assert.Equal(t, DefaultStatusBarBackground, cell.Bg,
			"cell %d breaks the bar background", x)
	}
}

func newTestStatusBar(t *testing.T) *StatusBar {
	t.Helper()
	layout, err := ParseStatusBarLayout(testStatusBarLayout)
	require.NoError(t, err)
	// The shipped palette, with every animation pinned to one frame so
	// a render is reproducible without freezing the draw counter.
	statuses := make(map[string]StatusBarStatusConfig, len(DefaultStatuses))
	for name, status := range DefaultStatuses {
		status.Animation.Frames = []string{"*"}
		statuses[name] = status
	}
	bar := NewStatusBar(StatusBarConfig{
		Enabled:    true,
		Layout:     layout,
		GaugeWidth: 11,
		Statuses:   statuses,
	}, nil)
	bar.SetState(func(s *StatusBarState) {
		s.Active = true
		s.TurnStart = time.Now().Add(-5 * time.Second)
		s.Phase = "REASONING"
		s.Model = "opus"
		s.Effort = "high"
		s.Usage = llmapi.DialogueUsage{
			TokensSent: 1000, TokensCached: 500,
			TotalDuration: 5 * time.Second,
		}
		s.ContextTokens = 50_000
		s.ContextWindow = 200_000
	})
	return bar
}

// render draws the bar at the given width and returns the row.
func render(t *testing.T, bar *StatusBar, width int) string {
	t.Helper()
	bar.Resize(width, 1)
	rec := newCellRecorder()
	bar.Draw(rec)
	return strings.ReplaceAll(rec.row(width), "\x00", " ")
}

func TestStatusBarDropsSegmentsInPriorityOrder(t *testing.T) {
	tests := []struct {
		width int
		want  string
	}{
		{60, "* REASONING             opus high  50k/200k    cache 50%  5s"},
		{50, "* REASONING   opus high  50k/200k    cache 50%  5s"},
		// The cache gauge goes first, then effort.
		{45, "* REASONING          opus high  50k/200k   5s"},
		{40, "* REASONING     opus high  50k/200k   5s"},
		{35, "* REASONING     opus  50k/200k   5s"},
		// Then the model, then the context gauge.
		{30, "* REASONING      50k/200k   5s"},
		{25, "* REASONING            5s"},
		{20, "* REASONING       5s"},
		// Spinner and status survive; the status gives up its padding
		// first, then clips.
		{10, "* REASONIN"},
		{3, "* R"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			bar := newTestStatusBar(t)
			got := render(t, bar, tt.width)
			assert.Equal(t, tt.want, strings.TrimRight(got, " "))
		})
	}
}

func TestStatusBarIdleKeepsModelContextAndCache(t *testing.T) {
	bar := newTestStatusBar(t)
	bar.SetState(func(s *StatusBarState) { s.Active = false })

	got := render(t, bar, 60)
	assert.NotContains(t, got, "REASONING")
	assert.Contains(t, got, idleStatusText)
	assert.Contains(t, got, "opus")
	assert.Contains(t, got, "high")
	assert.Contains(t, got, "50k/200k")
	assert.Contains(t, got, "cache 50%")
}

// Idle is a state like any other, so the shipped palette animates it
// rather than leaving the spinner cell blank.
func TestStatusBarIdleSpinnerShowsAnIcon(t *testing.T) {
	frames := DefaultStatuses[idleStatusText].Animation.Frames
	require.NotEmpty(t, frames)
	bar := shippedStatusBar(t)
	// The shipped layout opens with a space, then the spinner.
	for range len(frames) {
		row := []rune(render(t, bar, 100))
		assert.Contains(t, frames, string(row[1]))
	}
}

// A status the palette gives no animation still blanks the spinner
// while idle, so the elements to its right do not jump.
func TestStatusBarIdleSpinnerBlanksWithoutAnimation(t *testing.T) {
	layout, err := ParseStatusBarLayout(`[{{ .Spinner }}]`)
	require.NoError(t, err)
	bar := NewStatusBar(StatusBarConfig{
		Enabled: true, Layout: layout,
		Statuses: map[string]StatusBarStatusConfig{idleStatusText: {}},
	}, nil)
	assert.Equal(t, "[ ]", render(t, bar, 3))
}

// The spinner shares the status pill, so an animation of its own
// colours the icon without splitting the block in two: it overrides
// only the attributes it names.
func TestStatusBarAnimationAttrOverridesTheStatusForTheSpinnerOnly(t *testing.T) {
	layout, err := ParseStatusBarLayout(`{{ .Spinner }}{{ .Status }}`)
	require.NoError(t, err)
	bar := NewStatusBar(StatusBarConfig{
		Enabled: true, Layout: layout,
		Statuses: map[string]StatusBarStatusConfig{idleStatusText: {
			Attrs: term.Attributes{Fg: term.ColorWhite, Bg: term.ColorSilver},
			Animation: StatusBarAnimation{
				Frames: []string{"!"},
				Attrs:  term.Attributes{Fg: term.ColorYellow},
			},
		}},
	}, nil)
	bar.Resize(5, 1)

	rec := newCellRecorder()
	bar.Draw(rec)
	spinner := rec.cells[term.Coordinates{X: 0}]
	status := rec.cells[term.Coordinates{X: 1}]

	assert.Equal(t, term.ColorYellow, spinner.Fg)
	assert.Equal(t, term.ColorSilver, spinner.Bg,
		"an unnamed background must stay the pill's")
	assert.Equal(t, term.ColorWhite, status.Fg,
		"the animation must not colour the status text")
	assert.Equal(t, term.ColorSilver, status.Bg)
}

// TestStatusBarShippedDefaultsAreLegible pins the shipped palette. An
// element whose foreground matches the background it is drawn on is
// invisible, which shipped as a bug once.
func TestStatusBarShippedDefaultsAreLegible(t *testing.T) {
	assert.NotEqual(t, defaultGaugeEmptyAttr.Bg, defaultGaugeEmptyAttr.Fg,
		"the gauge label must be visible against its track")

	layout, err := ParseStatusBarLayout(DefaultStatusBarLayout)
	require.NoError(t, err)
	bar := NewStatusBar(StatusBarConfig{
		Enabled: true, Layout: layout, BackgroundColor: DefaultStatusBarBackground,
	}, nil)
	bar.SetState(func(s *StatusBarState) {
		s.Model = "claude-opus-5"
		s.ContextWindow = 1_000_000
	})
	bar.Resize(120, 1)

	rec := newCellRecorder()
	bar.Draw(rec)

	for x := range 120 {
		cell := rec.cells[term.Coordinates{X: x}]
		if cell.Ch == ' ' || cell.Ch == 0 {
			continue
		}
		if graphemecluster.IsBackground(cell.Ch) {
			// A shade fades one background into another, so it is
			// meant to vanish when the two ends match.
			continue
		}
		assert.NotEqual(t, cell.Bg, cell.Fg,
			"cell %d draws %q invisibly", x, cell.Ch)
	}
}

// The shipped layout fades the status pill into the bar with shade
// glyphs, which only reads correctly when the shade run is inverted
// against the bar background rather than the pill's own. The pill runs
// from the very first cell: the spinner and the spaces around it are
// part of it, and once shipped without the pill's background they left
// unpainted cells at the left edge of the bar.
func TestStatusBarShippedLayoutFadesStatusPill(t *testing.T) {
	layout, err := ParseStatusBarLayout(DefaultStatusBarLayout)
	require.NoError(t, err)
	bar := NewStatusBar(StatusBarConfig{
		Enabled: true, Layout: layout, BackgroundColor: DefaultStatusBarBackground,
	}, nil)
	bar.Resize(120, 1)

	rec := newCellRecorder()
	bar.Draw(rec)
	row := strings.ReplaceAll(rec.row(120), "\x00", " ")
	require.Contains(t, row, idleStatusText)
	require.Contains(t, row, "█▓▒░")

	shade := slices.Index([]rune(row), '█')
	require.Positive(t, shade)
	for x := range shade {
		cell := rec.cells[term.Coordinates{X: x}]
		assert.Equal(t, idlePillAttr.Bg, cell.Bg,
			"cell %d left of the fade is not part of the status pill", x)
	}
	for i, r := range []rune("█▓▒░") {
		cell := rec.cells[term.Coordinates{X: shade + i}]
		assert.Equal(t, r, cell.Ch)
		assert.Equal(t, idlePillAttr.Bg, cell.Fg, "shade %d", i)
		assert.Equal(t, DefaultStatusBarBackground, cell.Bg, "shade %d", i)
	}
}

// The status element matches the editor's status bar: bold, over
// colours the palette picks by status rather than the layout.
func TestStatusBarShippedStatusIsBold(t *testing.T) {
	layout, err := ParseStatusBarLayout(DefaultStatusBarLayout)
	require.NoError(t, err)

	var status StatusBarComponent
	for _, c := range layout {
		if c.Type == StatusBarStatus {
			status = c
		}
	}
	assert.Equal(t, term.Attributes{Attrs: term.AttrBold}, status.Attributes)

	bar := shippedStatusBar(t)
	bar.Resize(120, 1)
	rec := newCellRecorder()
	bar.Draw(rec)
	row := strings.ReplaceAll(rec.row(120), "\x00", " ")
	x := slices.Index([]rune(row), rune(idleStatusText[0]))
	require.GreaterOrEqual(t, x, 0)
	assert.Equal(t, term.Attributes{
		Fg: idlePillAttr.Fg, Bg: idlePillAttr.Bg,
		Attrs: term.AttrBold | term.AttrVerticalRenderOffset,
	}, rec.cells[term.Coordinates{X: x}].Attributes())
}

// The status pill reports the phase through its colour, so a state the
// palette covers must not keep the previous one's block.
func TestStatusBarStatusAttributesFollowPhase(t *testing.T) {
	bar := shippedStatusBar(t)
	bar.SetState(func(s *StatusBarState) {
		s.Active = true
		s.Phase = "REASONING"
	})
	bar.Resize(120, 1)

	rec := newCellRecorder()
	bar.Draw(rec)
	row := strings.ReplaceAll(rec.row(120), "\x00", " ")
	x := slices.Index([]rune(row), 'R')
	require.GreaterOrEqual(t, x, 0)
	assert.Equal(t, DefaultStatuses["REASONING"].Attrs.Bg,
		rec.cells[term.Coordinates{X: x}].Bg)
}

// The conversation name reads as another label on the bar rather than
// as a pill of its own, so it takes the same colours as the model.
func TestStatusBarShippedLayoutMatchesConversationToModel(t *testing.T) {
	layout, err := ParseStatusBarLayout(DefaultStatusBarLayout)
	require.NoError(t, err)
	var model, conversation term.Attributes
	for _, c := range layout {
		switch c.Type {
		case StatusBarModel:
			model = c.Attributes
		case StatusBarConversation:
			conversation = c.Attributes
		}
	}
	assert.Equal(t, model.Fg, conversation.Fg)
	assert.Equal(t, model.Bg, conversation.Bg)

	const width = 100
	bar := shippedStatusBar(t)
	bar.SetState(func(s *StatusBarState) { s.Conversation = "rolling-fox" })
	bar.Resize(width, 1)

	rec := newCellRecorder()
	bar.Draw(rec)
	row := strings.ReplaceAll(rec.row(width), "\x00", " ")
	require.Contains(t, row, "rolling-fox")
	assert.NotContains(t, row, "░▒▓█",
		"the conversation no longer fades in from the bar")

	before, _, _ := strings.Cut(row, "rolling-fox")
	assert.Equal(t, DefaultStatusBarBackground,
		rec.cells[term.Coordinates{X: len([]rune(before))}].Bg)
}

// An element with no value must take its literals down with it, or an
// unnamed conversation leaves a shade fading into nothing.
func TestStatusBarEmptyConversationDropsItsPill(t *testing.T) {
	bar := shippedStatusBar(t)
	assert.NotContains(t, render(t, bar, 100), "░▒▓█")
}

// The model and the effort identify the conversation, so they hug the
// status pill on the left the way the editor's filepath does, rather
// than floating next to the gauges on the right.
func TestStatusBarShippedLayoutAlignsModelLeft(t *testing.T) {
	bar := shippedStatusBar(t)
	bar.SetState(func(s *StatusBarState) {
		s.Model = "claude-opus-5"
		s.Effort = "high"
		s.ContextTokens = 114_000
		s.ContextWindow = 1_000_000
	})
	row := render(t, bar, 100)

	status := strings.Index(row, "READY")
	model := strings.Index(row, "claude-opus-5")
	effort := strings.Index(row, "high")
	gauge := strings.Index(row, "114k/1m")
	require.Positive(t, model)
	assert.Less(t, status, model)
	assert.Less(t, model, effort)
	assert.Less(t, effort, gauge, "the model group stays left of the gauges")
}

// The cache reads as a rate backed by the counts it came from, not as
// a bar, and says nothing at all until there is a turn to report on.
func TestStatusBarShippedLayoutReadsCacheAsAValue(t *testing.T) {
	bar := shippedStatusBar(t)
	assert.NotContains(t, render(t, bar, 100), "󰗂",
		"a conversation with no usage has no hit rate to report")

	bar.SetState(func(s *StatusBarState) {
		s.Usage = llmapi.DialogueUsage{
			TokensSent: 4_200_000, TokensCached: 3_700_000,
		}
	})
	assert.Contains(t, render(t, bar, 100), "󰗂 88%")
}

// The palette is keyed by the phase, not by the rendered label, so a
// task description does not cost the pill its colour.
func TestStatusBarStatusAttributesIgnoreActiveForm(t *testing.T) {
	bar := shippedStatusBar(t)
	bar.SetState(func(s *StatusBarState) {
		s.Active = true
		s.Phase = "REASONING"
		s.ActiveForm = "Running tests"
	})
	bar.Resize(120, 1)

	rec := newCellRecorder()
	bar.Draw(rec)
	row := strings.ReplaceAll(rec.row(120), "\x00", " ")
	require.Contains(t, row, "Running tests")
	x := slices.Index([]rune(row), 'R')
	require.GreaterOrEqual(t, x, 0)
	assert.Equal(t, DefaultStatuses["REASONING"].Attrs.Bg,
		rec.cells[term.Coordinates{X: x}].Bg)
}

func TestStatusBarActiveFormOverridesPhase(t *testing.T) {
	bar := newTestStatusBar(t)
	bar.SetState(func(s *StatusBarState) { s.ActiveForm = "Running tests" })
	assert.Contains(t, render(t, bar, 60), "Running tests")
}

// A pending prompt blocks the turn, so it outranks a task description:
// the description would otherwise read as work still moving.
func TestStatusBarAskingOverridesActiveForm(t *testing.T) {
	bar := newTestStatusBar(t)
	bar.SetState(func(s *StatusBarState) {
		s.Phase = AskingStatusText
		s.ActiveForm = "Running tests"
	})
	row := render(t, bar, 60)
	assert.Contains(t, row, AskingStatusText)
	assert.NotContains(t, row, "Running tests")
}

// Every status is padded to the widest one the palette knows, so the
// elements to the right of the pill do not shuffle sideways each time
// the turn moves to a phase with a shorter name. The padding all goes
// on the right, so the gap between the spinner and the text is the
// same for every status rather than growing with the name.
func TestStatusBarPadsEveryStatusToOneWidth(t *testing.T) {
	layout, err := ParseStatusBarLayout(`[{{ .Status }}]`)
	require.NoError(t, err)
	bar := NewStatusBar(StatusBarConfig{
		Enabled: true, Layout: layout,
		Statuses: map[string]StatusBarStatusConfig{
			"IDLE": {}, "SENDING": {}, "REASONING": {},
		},
	}, nil)

	widest := len("REASONING")
	for _, status := range []string{"IDLE", "SENDING", "REASONING"} {
		bar.SetState(func(s *StatusBarState) {
			s.Active = true
			s.Phase = status
		})
		row := render(t, bar, widest+2)
		assert.Equal(t, widest+2, len([]rune(row)), "status %q", status)
		assert.Equal(t, "[", string([]rune(row)[0]), "status %q", status)
		assert.Equal(t, "]", string([]rune(row)[widest+1]), "status %q", status)
		assert.True(t, strings.HasPrefix(row, "["+status),
			"status %q is not flush with the left edge of %q", status, row)
	}
}

// A description longer than any known status is not padded, and must
// not be truncated to their width either.
func TestStatusBarDoesNotPadOverlongStatus(t *testing.T) {
	bar := newTestStatusBar(t)
	bar.SetState(func(s *StatusBarState) {
		s.ActiveForm = "Running a very long task description"
	})
	assert.Contains(t, render(t, bar, 80), "Running a very long task description")
}

// The spinner is animated per status, so moving to another phase must
// swap the animation rather than keep cycling the previous one.
func TestStatusBarSpinnerAnimationIsPerStatus(t *testing.T) {
	layout, err := ParseStatusBarLayout(`{{ .Spinner }}`)
	require.NoError(t, err)
	bar := NewStatusBar(StatusBarConfig{
		Enabled: true, Layout: layout,
		Statuses: map[string]StatusBarStatusConfig{
			"SENDING":   {Animation: StatusBarAnimation{Frames: []string{"a", "b", "c"}}},
			"REASONING": {Animation: StatusBarAnimation{Frames: []string{"x", "y"}}},
		},
	}, nil)
	bar.SetState(func(s *StatusBarState) {
		s.Active = true
		s.Phase = "SENDING"
	})

	var got []string
	for range 4 {
		got = append(got, render(t, bar, 1))
	}
	assert.Equal(t, []string{"b", "c", "a", "b"}, got)

	bar.SetState(func(s *StatusBarState) { s.Phase = "REASONING" })
	assert.ElementsMatch(t, []string{"x", "y"},
		[]string{render(t, bar, 1), render(t, bar, 1)})
}

// A status with no animation of its own still has to spin, or the bar
// would read as hung on any phase the palette does not cover.
func TestStatusBarSpinnerAnimationDefault(t *testing.T) {
	layout, err := ParseStatusBarLayout(`{{ .Spinner }}`)
	require.NoError(t, err)
	bar := NewStatusBar(StatusBarConfig{Enabled: true, Layout: layout}, nil)
	bar.SetState(func(s *StatusBarState) {
		s.Active = true
		s.Phase = "UNKNOWN"
	})
	assert.Contains(t, defaultSpinnerFrames, render(t, bar, 1))
}

func TestStatusBarUnsetEffortRendersModelDefault(t *testing.T) {
	bar := newTestStatusBar(t)
	bar.SetState(func(s *StatusBarState) { s.Effort = "" })
	assert.Contains(t, render(t, bar, 60), "default")
}

// Literal text around a gauge is padding the layout asked for, so it
// has to survive; the gauge renders its own fixed-width field between
// whatever the layout put on either side of it.
func TestStatusBarGaugeKeepsSurroundingText(t *testing.T) {
	layout, err := ParseStatusBarLayout(`|{{ .ContextGauge }}|`)
	require.NoError(t, err)
	bar := NewStatusBar(StatusBarConfig{
		Enabled: true, Layout: layout, GaugeWidth: 11,
	}, nil)
	bar.SetState(func(s *StatusBarState) {
		s.ContextTokens = 50_000
		s.ContextWindow = 200_000
	})
	assert.Equal(t, "| 50k/200k  |", render(t, bar, 13))
}

// A gauge's literals are styled by the pipe operators on the gauge
// element, the same as any other element's. The gauge paints its own
// track, so those attributes are all the layout gets to say about it.
func TestStatusBarGaugeLiteralsTakeElementAttributes(t *testing.T) {
	layout, err := ParseStatusBarLayout(`|{{ .ContextGauge | fg "aqua" }}|`)
	require.NoError(t, err)
	bar := NewStatusBar(StatusBarConfig{
		Enabled: true, Layout: layout, GaugeWidth: 11,
		BackgroundColor: term.ColorGray,
		ForegroundColor: term.ColorSilver,
	}, nil)
	bar.Resize(13, 1)

	rec := newCellRecorder()
	bar.Draw(rec)
	for _, x := range []int{0, 12} {
		cell := rec.cells[term.Coordinates{X: x}]
		assert.Equal(t, '|', cell.Ch, "cell %d", x)
		assert.Equal(t, term.ColorAqua, cell.Fg, "cell %d", x)
		assert.Equal(t, term.ColorGray, cell.Bg, "cell %d", x)
	}
}

func TestStatusBarPaintsBackgroundAcrossRow(t *testing.T) {
	layout, err := ParseStatusBarLayout(`{{ .Status }}`)
	require.NoError(t, err)
	bar := NewStatusBar(StatusBarConfig{
		Enabled: true, Layout: layout, BackgroundColor: term.ColorGray,
	}, nil)
	bar.SetState(func(s *StatusBarState) {
		s.Active = true
		s.Phase = "ok"
	})
	bar.Resize(10, 1)

	rec := newCellRecorder()
	bar.Draw(rec)
	for x := range 10 {
		assert.Equal(t, term.ColorGray,
			rec.cells[term.Coordinates{X: x}].Bg, "cell %d", x)
	}
}

// The GUI renders the bar half a cell lower, like the editor's status
// bar, which it only does for cells carrying the offset flag. Missing
// it on any cell tears the row.
func TestStatusBarOffsetsWholeRowVertically(t *testing.T) {
	bar := shippedStatusBar(t)
	bar.Resize(40, 1)

	rec := newCellRecorder()
	bar.Draw(rec)
	for x := range 40 {
		assert.NotZero(t,
			rec.cells[term.Coordinates{X: x}].Attrs&term.AttrVerticalRenderOffset,
			"cell %d is not shifted down with the rest of the bar", x)
	}
}
