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

package vteparser

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestParserIntegration(t *testing.T) {
	suite := []struct {
		description string
		input       []byte
		assert      func(t *testing.T, handler mockHandler)
	}{
		{
			"parse control attribute",
			[]byte{0x1b, '[', '1', 'm'},
			func(t *testing.T, handler mockHandler) {
				assert.Equal(t, &Attr{Type: BoldAttr}, handler.attr)
			},
		},
		{
			"parse_terminal_identity_csi standard",
			[]byte{0x1b, '[', '1', 'c'},
			func(t *testing.T, handler mockHandler) {
				assert.False(t, handler.identityReported)
			},
		},
		{
			"parse_terminal_identity_csi",
			[]byte{0x1b, '[', 'c'},
			func(t *testing.T, handler mockHandler) {
				assert.True(t, handler.identityReported)
			},
		},
		{
			"parse_terminal_identity_csi with 0",
			[]byte{0x1b, '[', '0', 'c'},
			func(t *testing.T, handler mockHandler) {
				assert.True(t, handler.identityReported)
			},
		},
		{
			"parse_terminal_identity_esc",
			[]byte{0x1b, 'Z'},
			func(t *testing.T, handler mockHandler) {
				assert.True(t, handler.identityReported)
			},
		},
		{
			"parse_terminal_identity_esc no skip params",
			[]byte{0x1b, '#', 'Z'},
			func(t *testing.T, handler mockHandler) {
				assert.False(t, handler.identityReported)
			},
		},
		{
			"parse truecolor attr",
			[]byte{
				0x1b, '[', '3', '8', ';', '2', ';', '1', '2', '8', ';', '6', '6', ';',
				'2', '5', '5', 'm',
			},
			func(t *testing.T, handler mockHandler) {
				expected := term.NewRGBColor(128, 66, 255)
				assert.Equal(t, &Attr{Type: ForegroundAttr, Color: expected}, handler.attr)
			},
		},
		{
			"parsing ForegroundAttr must not parse blue component also as separate attr",
			[]byte{0x1b, '[', '3', '8', ';', '2', ';', '0', ';', '2', '5', '5', ';', '0', 'm'},
			func(t *testing.T, handler mockHandler) {
				expected := term.NewRGBColor(0, 255, 0)
				// The blue component was being parsed as iota's 0 value (ResetAttr) wiping the RGB attr.
				assert.NotEqual(t, &Attr{Type: ResetAttr}, handler.attr)
				assert.Equal(t, &Attr{Type: ForegroundAttr, Color: expected}, handler.attr)
				assert.Len(t, handler.attrs, 1)
			},
		},
		{
			"parsing BackgroundAttr must not parse blue component also as separate attr",
			[]byte{0x1b, '[', '4', '8', ';', '2', ';', '0', ';', '2', '5', '5', ';', '0', 'm'},
			func(t *testing.T, handler mockHandler) {
				expected := term.NewRGBColor(0, 255, 0)
				// The blue component was being parsed as iota's 0 value (ResetAttr) wiping the RGB attr.
				assert.NotEqual(t, &Attr{Type: ResetAttr}, handler.attr)
				assert.Equal(t, &Attr{Type: BackgroundAttr, Color: expected}, handler.attr)
				assert.Len(t, handler.attrs, 1)
			},
		},
		{
			"parse designate G0 as line drawing",
			[]byte{0x1b, '(', '0'},
			func(t *testing.T, handler mockHandler) {
				assert.Equal(t, CharsetIndexG0, handler.index)
				assert.Equal(t, StandardCharsetSpecialCharacterAndLineDrawing, handler.charset)
			},
		},
		{
			"parse designate G1 as line drawing and invoke",
			[]byte{0x1b, ')', '0', 0x0e},
			func(t *testing.T, handler mockHandler) {
				assert.Equal(t, CharsetIndexG1, handler.index)
				assert.Equal(t, StandardCharsetSpecialCharacterAndLineDrawing, handler.charset)
			},
		},
		{
			// DECSCUSR 0 means "reset to the terminal default" and must
			// not be conflated with the blinking-block shape (param 1).
			"parse DECSCUSR 0 as default cursor shape",
			[]byte{0x1b, '[', '0', ' ', 'q'},
			func(t *testing.T, handler mockHandler) {
				assert.Equal(t, CursorStyle{Shape: CursorShapeDefault, Blinking: false}, handler.cursorStyle)
			},
		},
		{
			"parse DECSCUSR with no param as default cursor shape",
			[]byte{0x1b, '[', ' ', 'q'},
			func(t *testing.T, handler mockHandler) {
				assert.Equal(t, CursorStyle{Shape: CursorShapeDefault, Blinking: false}, handler.cursorStyle)
			},
		},
		{
			"parse DECSCUSR 1 as blinking block",
			[]byte{0x1b, '[', '1', ' ', 'q'},
			func(t *testing.T, handler mockHandler) {
				assert.Equal(t, CursorStyle{Shape: CursorShapeBlock, Blinking: true}, handler.cursorStyle)
			},
		},
		{
			"parse DECSCUSR 2 as steady block",
			[]byte{0x1b, '[', '2', ' ', 'q'},
			func(t *testing.T, handler mockHandler) {
				assert.Equal(t, CursorStyle{Shape: CursorShapeBlock, Blinking: false}, handler.cursorStyle)
			},
		},
		{
			"parse DECSCUSR 6 as steady beam",
			[]byte{0x1b, '[', '6', ' ', 'q'},
			func(t *testing.T, handler mockHandler) {
				assert.Equal(t, CursorStyle{Shape: CursorShapeBeam, Blinking: false}, handler.cursorStyle)
			},
		},
	}

	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			var handler mockHandler
			handler.init()

			parser := NewParser(&handler, testSyncHandler{})

			for _, b := range test.input {
				parser.Advance(b)
			}

			test.assert(t, handler)
			handler.resetState()
		})
	}
}

func TestParserSetSyncMode(t *testing.T) {
	input := []byte{27, 91, 63, 50, 48, 50, 54, 104, 27}
	var handler mockHandler
	handler.init()

	parser := NewParser(loggingHandler{h: &handler}, new(StdTimeout))

	for _, b := range input {
		parser.Advance(b)
	}
}

var _ Handler = (*mockHandler)(nil)

type mockHandler struct {
	nopHandler
	index            CharsetIndex
	charset          StandardCharset
	attr             *Attr
	attrs            []*Attr
	identityReported bool
	cursorStyle      CursorStyle
}

func (m *mockHandler) init() {
	m.index = CharsetIndexG0
	m.charset = StandardCharsetASCII
	m.attr = nil
	m.attrs = make([]*Attr, 0)
	m.identityReported = false
}

func (m *mockHandler) TerminalAttribute(attr Attr) {
	m.attr = new(Attr)
	*m.attr = attr
	m.attrs = append(m.attrs, &attr)
}

func (m *mockHandler) ConfigureCharset(index CharsetIndex, charset StandardCharset) {
	m.index = index
	m.charset = charset
}

func (m *mockHandler) SetActiveCharset(index CharsetIndex) {
	m.index = index
}

func (m *mockHandler) IdentifyTerminal(identifySecondary bool) {
	m.identityReported = true
}

func (m *mockHandler) SetCursorStyle(style CursorStyle) {
	m.cursorStyle = style
}

func (m *mockHandler) resetState() {
	m.init()
}

type testSyncHandler struct {
}

func (t testSyncHandler) SetTimeout(duration time.Duration) {
	panic("unreachable")
}

func (t testSyncHandler) ClearTimeout() {
	panic("unreachable")
}

func (t testSyncHandler) PendingTimeout() bool {
	return false
}

func TestSGRScratchReuseLongToShort(t *testing.T) {
	h := new(sgrRecordingHandler)
	p := NewParser(h, new(StdTimeout))
	streams := []string{
		"\x1b[1;3;4;38;2;1;2;3;48;5;9m",
		"\x1b[31m",
		"\x1b[m",
		"\x1b[4:3m",
		"\x1b[0m",
	}
	want := [][]Attr{
		{
			{Type: BoldAttr},
			{Type: ItalicAttr},
			{Type: UnderlineAttr},
			{Type: ForegroundAttr, Color: term.NewRGBColor(1, 2, 3)},
			{Type: BackgroundAttr, Color: term.PaletteColor(9)},
		},
		{{Type: ForegroundAttr, Color: term.ColorMaroon}},
		{{Type: ResetAttr}},
		{{Type: UndercurlAttr}},
		{{Type: ResetAttr}},
	}

	for i, stream := range streams {
		before := len(h.attrs)
		p.AdvanceBytes([]byte(stream))
		assert.Equal(t, want[i], h.attrs[before:], "dispatch %d", i)
	}
	assert.Equal(t, appendAttrGroups(want...), h.attrs)
}

func TestSGRColorsTable(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		want   []Attr
	}{
		{
			name:   "semicolon foreground rgb",
			stream: "\x1b[38;2;10;20;30m",
			want:   []Attr{{Type: ForegroundAttr, Color: term.NewRGBColor(10, 20, 30)}},
		},
		{
			name:   "semicolon background rgb",
			stream: "\x1b[48;2;10;20;30m",
			want:   []Attr{{Type: BackgroundAttr, Color: term.NewRGBColor(10, 20, 30)}},
		},
		{
			name:   "semicolon underline rgb",
			stream: "\x1b[58;2;10;20;30m",
			want:   []Attr{{Type: UnderlineColorAttr, Color: term.NewRGBColor(10, 20, 30)}},
		},
		{
			name:   "reset underline colour",
			stream: "\x1b[59m",
			want:   []Attr{{Type: UnderlineColorAttr, Color: term.ColorDefault}},
		},
		{
			name:   "semicolon indexed foreground",
			stream: "\x1b[38;5;196m",
			want:   []Attr{{Type: ForegroundAttr, Color: term.PaletteColor(196)}},
		},
		{
			name:   "semicolon indexed background",
			stream: "\x1b[48;5;21m",
			want:   []Attr{{Type: BackgroundAttr, Color: term.PaletteColor(21)}},
		},
		{
			name:   "colon foreground rgb",
			stream: "\x1b[38:2:10:20:30m",
			want:   []Attr{{Type: ForegroundAttr, Color: term.NewRGBColor(10, 20, 30)}},
		},
		{
			name:   "colon background rgb",
			stream: "\x1b[48:2:10:20:30m",
			want:   []Attr{{Type: BackgroundAttr, Color: term.NewRGBColor(10, 20, 30)}},
		},
		{
			name:   "colon underline rgb",
			stream: "\x1b[58:2:10:20:30m",
			want:   []Attr{{Type: UnderlineColorAttr, Color: term.NewRGBColor(10, 20, 30)}},
		},
		{
			name:   "colon foreground indexed",
			stream: "\x1b[38:5:196m",
			want:   []Attr{{Type: ForegroundAttr, Color: term.PaletteColor(196)}},
		},
		{
			name:   "colon rgb with empty color space",
			stream: "\x1b[38:2::10:20:30m",
			want:   []Attr{{Type: ForegroundAttr, Color: term.NewRGBColor(10, 20, 30)}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := new(sgrRecordingHandler)
			p := NewParser(h, new(StdTimeout))
			p.AdvanceBytes([]byte(tt.stream))
			assert.Equal(t, tt.want, h.attrs)
		})
	}
}

func TestSGRResetAndMalformedTable(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		want   []Attr
	}{
		{name: "empty reset", stream: "\x1b[m", want: []Attr{{Type: ResetAttr}}},
		{name: "explicit reset", stream: "\x1b[0m", want: []Attr{{Type: ResetAttr}}},
		{
			name:   "reset between attrs",
			stream: "\x1b[1;0;31m",
			want: []Attr{
				{Type: BoldAttr},
				{Type: ResetAttr},
				{Type: ForegroundAttr, Color: term.ColorMaroon},
			},
		},
		{name: "default foreground", stream: "\x1b[39m", want: []Attr{{Type: ForegroundAttr, Color: term.ColorDefault}}},
		{name: "default background", stream: "\x1b[49m", want: []Attr{{Type: BackgroundAttr, Color: term.ColorDefault}}},
		{name: "missing extended selector", stream: "\x1b[38m"},
		{name: "truncated rgb selector", stream: "\x1b[38;2m"},
		{name: "truncated rgb components", stream: "\x1b[38;2;255;0m"},
		{name: "missing indexed color", stream: "\x1b[38;5m"},
		{name: "truncated colon rgb", stream: "\x1b[38:2:255m"},
		{name: "truncated colon index", stream: "\x1b[38:5m"},
		{name: "unknown sgr", stream: "\x1b[999m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := new(sgrRecordingHandler)
			p := NewParser(h, new(StdTimeout))
			p.AdvanceBytes([]byte(tt.stream))
			assert.Equal(t, tt.want, h.attrs)
		})
	}
}

type sgrRecordingHandler struct {
	nopHandler
	attrs []Attr
}

func (h *sgrRecordingHandler) TerminalAttribute(attr Attr) {
	h.attrs = append(h.attrs, attr)
}

func appendAttrGroups(groups ...[]Attr) []Attr {
	var attrs []Attr
	for _, group := range groups {
		attrs = append(attrs, group...)
	}
	return attrs
}

func TestAdvanceBytesEventsTable(t *testing.T) {
	tests := []struct {
		name          string
		stream        []byte
		wantEvents    []parserRunEvent
		wantPreceding rune
	}{
		{
			name:   "printable runs and controls",
			stream: []byte("ab\rcd\nef"),
			wantEvents: []parserRunEvent{
				runEvent("ab"),
				{kind: "cr"},
				runEvent("cd"),
				{kind: "lf"},
				runEvent("ef"),
			},
			wantPreceding: 'f',
		},
		{
			name:   "rep after ascii run",
			stream: []byte("A\x1b[3b"),
			wantEvents: []parserRunEvent{
				runEvent("A"),
				inputEvent('A'),
				inputEvent('A'),
				inputEvent('A'),
			},
			wantPreceding: 'A',
		},
		{
			name:   "rep after unicode run",
			stream: []byte("漢\x1b[2b"),
			wantEvents: []parserRunEvent{
				runEvent("漢"),
				inputEvent('漢'),
				inputEvent('漢'),
			},
			wantPreceding: '漢',
		},
		{
			name:   "control does not clear rep character",
			stream: []byte("A\r\x1b[2b"),
			wantEvents: []parserRunEvent{
				runEvent("A"),
				{kind: "cr"},
				inputEvent('A'),
				inputEvent('A'),
			},
			wantPreceding: 'A',
		},
		{
			name:   "sgr does not clear rep character",
			stream: []byte("A\x1b[31m\x1b[2b"),
			wantEvents: []parserRunEvent{
				runEvent("A"),
				{kind: "attr", attr: Attr{Type: ForegroundAttr, Color: term.ColorMaroon}},
				inputEvent('A'),
				inputEvent('A'),
			},
			wantPreceding: 'A',
		},
		{
			name:   "osc does not clear rep character",
			stream: []byte("A\x1b]2;title\x07\x1b[2b"),
			wantEvents: []parserRunEvent{
				runEvent("A"),
				{kind: "title", text: "title"},
				inputEvent('A'),
				inputEvent('A'),
			},
			wantPreceding: 'A',
		},
		{
			name:          "rep without preceding character",
			stream:        []byte("\x1b[3b"),
			wantPreceding: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := new(parserRunHandler)
			p := NewParser(h, new(StdTimeout))
			p.AdvanceBytes(tt.stream)

			assert.Equal(t, tt.wantEvents, h.events)
			assert.Equal(t, tt.wantPreceding, p.state.precedingChar)
		})
	}
}

func TestAdvanceBytesMatchesAdvanceAtEverySplit(t *testing.T) {
	tests := []struct {
		name   string
		stream []byte
	}{
		{
			name:   "shell title color and utf8",
			stream: []byte("\x1b]2;user@host: ~/src\x07prompt> \x1b[32mOK 漢字\x1b[0m\r\n"),
		},
		{
			name:   "cursor addressed progress redraw",
			stream: []byte("[          ]\r\x1b[5C#\x1b[3b\x1b[2Kdone"),
		},
		{
			name:   "alternate screen frame",
			stream: []byte("before\x1b[?1049h\x1b[H\x1b[31mALT 漢\x1b[0m\x1b[?1049lafter"),
		},
		{
			name:   "valid and malformed utf8",
			stream: []byte{'a', 0xc3, 0xa9, 0xe6, 0xbc, 0xa2, 0xf0, 0x9f, 0x98, 0x80, 0x80, 0xc0, 0x80, 0xff, 'z'},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantEvents, wantPreceding := parserEventsWithAdvance(tt.stream)
			for split := 0; split <= len(tt.stream); split++ {
				h := new(parserRunHandler)
				p := NewParser(h, new(StdTimeout))
				p.AdvanceBytes(tt.stream[:split])
				p.AdvanceBytes(tt.stream[split:])

				assert.Equal(t, wantEvents, normalizeParserRunEvents(h.events), "split=%d", split)
				assert.Equal(t, wantPreceding, p.state.precedingChar, "split=%d", split)
			}
		})
	}
}

func TestSynchronizedUpdateEventsTable(t *testing.T) {
	tests := []struct {
		name       string
		stream     []byte
		wantEvents []parserRunEvent
	}{
		{
			name:   "esu resumes trailing bytes",
			stream: []byte("\x1b[?2026hA\x1b[?2026lB\rC"),
			wantEvents: []parserRunEvent{
				{kind: "set-private", mode: PrivateModeSyncUpdate},
				runEvent("A"),
				{kind: "unset-private", mode: PrivateModeSyncUpdate},
				runEvent("B"),
				{kind: "cr"},
				runEvent("C"),
			},
		},
		{
			name:   "multiple regions in one batch",
			stream: []byte("\x1b[?2026hone\x1b[?2026lmid\x1b[?2026htwo\x1b[?2026ltail"),
			wantEvents: []parserRunEvent{
				{kind: "set-private", mode: PrivateModeSyncUpdate},
				runEvent("one"),
				{kind: "unset-private", mode: PrivateModeSyncUpdate},
				runEvent("mid"),
				{kind: "set-private", mode: PrivateModeSyncUpdate},
				runEvent("two"),
				{kind: "unset-private", mode: PrivateModeSyncUpdate},
				runEvent("tail"),
			},
		},
		{
			name:   "rep inside sync uses preceding body character",
			stream: []byte("\x1b[?2026hZ\x1b[2b\x1b[?2026l"),
			wantEvents: []parserRunEvent{
				{kind: "set-private", mode: PrivateModeSyncUpdate},
				runEvent("Z"),
				inputEvent('Z'),
				inputEvent('Z'),
				{kind: "unset-private", mode: PrivateModeSyncUpdate},
			},
		},
		{
			name:   "esu resets truncated csi before trailing bytes",
			stream: []byte("\x1b[?2026hA\x1b[\x1b[?2026ltail"),
			wantEvents: []parserRunEvent{
				{kind: "set-private", mode: PrivateModeSyncUpdate},
				runEvent("A"),
				{kind: "unset-private", mode: PrivateModeSyncUpdate},
				runEvent("tail"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := new(parserRunHandler)
			timeout := new(recordingParserTimeout)
			p := NewParser(h, timeout)
			p.AdvanceBytes(tt.stream)

			assert.Equal(t, tt.wantEvents, h.events)
			assert.Zero(t, p.SyncBytesCount())
			assert.False(t, timeout.pending)
		})
	}
}

func TestSynchronizedUpdateEveryThreeWaySplit(t *testing.T) {
	streams := [][]byte{
		[]byte("\x1b[?2026hA\x1b[?2026lB"),
		[]byte("\x1b[?2026h\x1b[31m漢\x1b[2b\x1b[?2026ltail"),
	}
	for streamIndex, stream := range streams {
		t.Run(fmt.Sprintf("stream_%d", streamIndex), func(t *testing.T) {
			want := parserSyncResult(stream, len(stream), len(stream))
			for first := 0; first <= len(stream); first++ {
				for second := first; second <= len(stream); second++ {
					got := parserSyncResult(stream, first, second)
					assert.Equal(t, want, got, "splits=%d,%d", first, second)
				}
			}
		})
	}
}

func TestSynchronizedUpdateNearMarkersMatchUnwrapped(t *testing.T) {
	nearMarkers := [][]byte{
		[]byte("\x1b[?2026X"),
		[]byte("\x1b[?2025l"),
		[]byte("\x1b[?2026m"),
		[]byte("\x1b[?20260l"),
		[]byte("\x1b[?2026;"),
	}

	for _, near := range nearMarkers {
		t.Run(fmt.Sprintf("%q", near), func(t *testing.T) {
			bare := append(append([]byte(nil), near...), []byte("tail")...)
			wrapped := append([]byte("\x1b[?2026h"), bare...)
			wrapped = append(wrapped, []byte("\x1b[?2026l")...)

			bareEvents, _ := parserEventsWithAdvanceBytes(bare)
			wrappedEvents, _ := parserEventsWithAdvanceBytes(wrapped)
			wrappedEvents = filterSyncModeEvents(wrappedEvents)

			assert.Equal(t, bareEvents, wrappedEvents)
		})
	}
}

func TestStopSyncTimeoutReplaysPartialMarker(t *testing.T) {
	h := new(parserRunHandler)
	timeout := new(recordingParserTimeout)
	p := NewParser(h, timeout)
	p.AdvanceBytes([]byte("\x1b[?2026hbody\x1b["))
	require.True(t, timeout.pending)
	require.Positive(t, p.SyncBytesCount())

	p.StopSync()
	p.AdvanceBytes([]byte("31mX"))

	assert.Equal(t, []parserRunEvent{
		{kind: "set-private", mode: PrivateModeSyncUpdate},
		runEvent("body"),
		{kind: "unset-private", mode: PrivateModeSyncUpdate},
		{kind: "attr", attr: Attr{Type: ForegroundAttr, Color: term.ColorMaroon}},
		runEvent("X"),
	}, h.events)
	assert.Zero(t, p.SyncBytesCount())
	assert.Equal(t, 1, timeout.clears)
}

func TestSynchronizedUpdateBufferAllocatesLazily(t *testing.T) {
	h := new(countingParserHandler)
	timeout := new(recordingParserTimeout)
	p := NewParser(h, timeout)

	assert.Zero(t, cap(p.state.syncState.buffer))
	p.AdvanceBytes([]byte("ordinary output"))
	assert.Zero(t, cap(p.state.syncState.buffer))

	p.AdvanceBytes(bsuCSI)
	p.AdvanceBytes([]byte("buffered"))
	assert.Equal(t, syncBufferSize, cap(p.state.syncState.buffer))
}

func TestSynchronizedUpdateBufferBoundaryTable(t *testing.T) {
	tests := []struct {
		name             string
		body             int
		firstChunk       int
		trailing         string
		wantBuffered     int
		wantInputBytes   int
		wantSets         int
		wantClears       int
		wantUnsetPrivate int
		wantPending      bool
	}{
		{
			name:         "one below flush threshold stays buffered",
			body:         syncBufferSize - 2,
			wantBuffered: syncBufferSize - 2,
			wantSets:     1,
			wantPending:  true,
		},
		{
			name:             "exact threshold flushes",
			body:             syncBufferSize - 1,
			wantInputBytes:   syncBufferSize - 1,
			wantSets:         1,
			wantClears:       1,
			wantUnsetPrivate: 1,
		},
		{
			name:             "triggering byte in separate call flushes",
			body:             syncBufferSize - 1,
			firstChunk:       syncBufferSize - 2,
			wantInputBytes:   syncBufferSize - 1,
			wantSets:         1,
			wantClears:       1,
			wantUnsetPrivate: 1,
		},
		{
			name:             "overshoot resumes trailing bytes",
			body:             syncBufferSize - 1,
			trailing:         "tail",
			wantInputBytes:   syncBufferSize - 1 + len("tail"),
			wantSets:         1,
			wantClears:       1,
			wantUnsetPrivate: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := new(countingParserHandler)
			timeout := new(recordingParserTimeout)
			p := NewParser(h, timeout)
			p.AdvanceBytes(bsuCSI)
			body := make([]byte, tt.body+len(tt.trailing))
			for i := range tt.body {
				body[i] = 'x'
			}
			copy(body[tt.body:], tt.trailing)
			if tt.firstChunk > 0 {
				p.AdvanceBytes(body[:tt.firstChunk])
				p.AdvanceBytes(body[tt.firstChunk:])
			} else {
				p.AdvanceBytes(body)
			}

			assert.Equal(t, tt.wantBuffered, p.SyncBytesCount())
			assert.Equal(t, tt.wantInputBytes, h.inputBytes)
			assert.Equal(t, 1, h.setPrivate)
			assert.Equal(t, tt.wantUnsetPrivate, h.unsetPrivate)
			assert.Equal(t, tt.wantSets, len(timeout.sets))
			for _, duration := range timeout.sets {
				assert.Equal(t, syncUpdateTimeout, duration)
			}
			assert.Equal(t, tt.wantClears, timeout.clears)
			assert.Equal(t, tt.wantPending, timeout.pending)
		})
	}
}

func TestSynchronizedUpdateTimeoutLifecycle(t *testing.T) {
	h := new(countingParserHandler)
	timeout := new(recordingParserTimeout)
	p := NewParser(h, timeout)
	p.AdvanceBytes(append(append([]byte(nil), bsuCSI...), bsuCSI...))

	require.Equal(t, []time.Duration{syncUpdateTimeout, syncUpdateTimeout}, timeout.sets)
	assert.True(t, timeout.pending)
	p.StopSync()
	assert.False(t, timeout.pending)
	assert.Equal(t, 1, timeout.clears)
	assert.Equal(t, 1, h.unsetPrivate)
}

type parserRunEvent struct {
	kind string
	text string
	r    rune
	mode PrivateMode
	attr Attr
}

type countingParserHandler struct {
	nopHandler
	inputBytes   int
	setPrivate   int
	unsetPrivate int
}

func (h *countingParserHandler) Input(c rune) {
	h.inputBytes += len(string(c))
}

func (h *countingParserHandler) InputRun(run []byte) {
	h.inputBytes += len(run)
}

func (h *countingParserHandler) SetPrivateMode(mode PrivateMode) {
	if mode == PrivateModeSyncUpdate {
		h.setPrivate++
	}
}

func (h *countingParserHandler) UnsetPrivateMode(mode PrivateMode) {
	if mode == PrivateModeSyncUpdate {
		h.unsetPrivate++
	}
}

func runEvent(text string) parserRunEvent {
	return parserRunEvent{kind: "run", text: text}
}

func inputEvent(r rune) parserRunEvent {
	return parserRunEvent{kind: "input", r: r}
}

type parserRunHandler struct {
	nopHandler
	events []parserRunEvent
}

func (h *parserRunHandler) Input(c rune) {
	h.events = append(h.events, inputEvent(c))
}

func (h *parserRunHandler) InputRun(run []byte) {
	h.events = append(h.events, runEvent(string(append([]byte(nil), run...))))
}

func (h *parserRunHandler) CarriageReturn() {
	h.events = append(h.events, parserRunEvent{kind: "cr"})
}

func (h *parserRunHandler) Linefeed() {
	h.events = append(h.events, parserRunEvent{kind: "lf"})
}

func (h *parserRunHandler) SetTitle(title string) {
	h.events = append(h.events, parserRunEvent{kind: "title", text: title})
}

func (h *parserRunHandler) TerminalAttribute(attr Attr) {
	h.events = append(h.events, parserRunEvent{kind: "attr", attr: attr})
}

func (h *parserRunHandler) SetPrivateMode(mode PrivateMode) {
	h.events = append(h.events, parserRunEvent{kind: "set-private", mode: mode})
}

func (h *parserRunHandler) UnsetPrivateMode(mode PrivateMode) {
	h.events = append(h.events, parserRunEvent{kind: "unset-private", mode: mode})
}

type recordingParserTimeout struct {
	pending bool
	sets    []time.Duration
	clears  int
}

func (t *recordingParserTimeout) SetTimeout(duration time.Duration) {
	t.pending = true
	t.sets = append(t.sets, duration)
}

func (t *recordingParserTimeout) ClearTimeout() {
	t.pending = false
	t.clears++
}

func (t *recordingParserTimeout) PendingTimeout() bool {
	return t.pending
}

func parserEventsWithAdvance(stream []byte) ([]parserRunEvent, rune) {
	h := new(parserRunHandler)
	p := NewParser(h, new(StdTimeout))
	for _, ch := range stream {
		p.Advance(ch)
	}
	return normalizeParserRunEvents(h.events), p.state.precedingChar
}

func parserEventsWithAdvanceBytes(stream []byte) ([]parserRunEvent, rune) {
	h := new(parserRunHandler)
	p := NewParser(h, new(recordingParserTimeout))
	p.AdvanceBytes(stream)
	return normalizeParserRunEvents(h.events), p.state.precedingChar
}

func normalizeParserRunEvents(events []parserRunEvent) []parserRunEvent {
	normalized := make([]parserRunEvent, 0, len(events))
	for _, event := range events {
		if event.kind != "run" {
			normalized = append(normalized, event)
			continue
		}
		for _, r := range event.text {
			normalized = append(normalized, inputEvent(r))
		}
	}
	return normalized
}

type parserSyncSnapshot struct {
	events       []parserRunEvent
	preceding    rune
	syncBytes    int
	timeoutOpen  bool
	timeoutSets  int
	timeoutClear int
}

func parserSyncResult(stream []byte, first, second int) parserSyncSnapshot {
	h := new(parserRunHandler)
	timeout := new(recordingParserTimeout)
	p := NewParser(h, timeout)
	p.AdvanceBytes(stream[:first])
	p.AdvanceBytes(stream[first:second])
	p.AdvanceBytes(stream[second:])
	return parserSyncSnapshot{
		events:       normalizeParserRunEvents(h.events),
		preceding:    p.state.precedingChar,
		syncBytes:    p.SyncBytesCount(),
		timeoutOpen:  timeout.pending,
		timeoutSets:  len(timeout.sets),
		timeoutClear: timeout.clears,
	}
}

func filterSyncModeEvents(events []parserRunEvent) []parserRunEvent {
	filtered := make([]parserRunEvent, 0, len(events))
	for _, event := range events {
		if (event.kind == "set-private" || event.kind == "unset-private") &&
			event.mode == PrivateModeSyncUpdate {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}
