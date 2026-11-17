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
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateContinuation(t *testing.T) {
	tests := []struct {
		name  string
		bytes string
		err   error
	}{
		{name: "ground is empty", bytes: ""},
		{name: "bare escape", bytes: "\x1b"},
		{name: "partial csi", bytes: "\x1b[31"},
		{name: "partial csi intermediate", bytes: "\x1b[?2026"},
		{name: "partial osc", bytes: "\x1b]2;title"},
		{name: "partial dcs", bytes: "\x1bPqdata"},
		{name: "partial apc", bytes: "\x1b_Gdata"},
		{name: "partial utf8 two of four", bytes: "\xF0\x9F"},
		{name: "partial utf8 three of four", bytes: "\xF0\x9F\x98"},
		{name: "partial utf8 one of two", bytes: "\xC3"},
		{
			name:  "complete csi",
			bytes: "\x1b[31m",
			err:   ErrNoPendingState,
		},
		{
			name:  "complete utf8",
			bytes: "\xF0\x9F\x98\x84",
			err:   ErrNoPendingState,
		},
		{
			name:  "printable text",
			bytes: "hello",
			err:   ErrNoPendingState,
		},
		{
			name:  "bell inside unfinished csi",
			bytes: "\x1b[31\x07",
			err:   ErrReplayWouldCommit,
		},
		{
			name:  "printable prefix",
			bytes: "prefix\x1b[31",
			err:   ErrNonCanonicalContinuation,
		},
		{
			name:  "superseded escape prefix",
			bytes: "\x1b[31\x1b[4",
			err:   ErrNonCanonicalContinuation,
		},
		{
			name:  "utf8 continuation without its lead",
			bytes: "\x9F",
			err:   ErrNoPendingState,
		},
		{
			name:  "text before partial utf8",
			bytes: "text\xF0\x9F",
			err:   ErrNonCanonicalContinuation,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateContinuation([]byte(tc.bytes))
			if !errors.Is(err, tc.err) {
				t.Fatalf("ValidateContinuation(%q) = %v, want %v",
					tc.bytes, err, tc.err)
			}
		})
	}
}

func TestFindVTReplayStart(t *testing.T) {
	tests := []struct {
		input string
		want  int
		ok    bool
	}{
		{input: "", want: 0, ok: false},
		{input: "no escapes here", want: 0, ok: false},
		{input: "\x1b", want: 0, ok: true},
		{input: "\x1b[1m \x1b[2", want: 5, ok: true},
		{input: strings.Repeat("a", 200) + "\x1b[", want: 200, ok: true},
	}

	for _, tc := range tests {
		got, ok := findVTReplayStart([]byte(tc.input))
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("findVTReplayStart(%q) = %d,%v, want %d,%v",
				tc.input, got, ok, tc.want, tc.ok)
		}
	}
}

func TestFindUTF8ReplayStart(t *testing.T) {
	tests := []struct {
		input string
		want  int
		ok    bool
	}{
		{input: "", ok: false},
		{input: "text\xF0", want: 4, ok: true},
		{input: "text\xF0\x9F", want: 4, ok: true},
		{input: "text\xF0\x9F\x98", want: 4, ok: true},
		{input: "\xE0\xA0", want: 0, ok: true},
		{input: "\xE0\xA0\xF0", want: 2, ok: true},
		{input: "\x9F", ok: false},
		{input: "\x9F\x98", ok: false},
	}

	for _, tc := range tests {
		got, ok := findUTF8ReplayStart([]byte(tc.input))
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("findUTF8ReplayStart(%q) = %d,%v, want %d,%v",
				tc.input, got, ok, tc.want, tc.ok)
		}
	}
}

func TestContinuationTrackerRetainsReplaySafeBytes(t *testing.T) {
	tr := NewContinuationTracker(64)

	tr.Append(ContinuationVT, []byte("committed\x1b[1\x07"))
	tr.Append(ContinuationVT, []byte(";2"))
	if tr.Broken() {
		t.Fatal("tracker unexpectedly broken")
	}
	if got := string(tr.Bytes()); got != "\x1b[1;2" {
		t.Fatalf("Bytes() = %q, want %q", got, "\x1b[1;2")
	}

	// A feed carrying a later replay start drops the previous suffix.
	tr.Append(ContinuationVT, []byte("committed\x1b]0;t"))
	if got := string(tr.Bytes()); got != "\x1b]0;t" {
		t.Fatalf("Bytes() = %q, want %q", got, "\x1b]0;t")
	}

	tr.Append(ContinuationUTF8, []byte("committed\xF0"))
	tr.Append(ContinuationUTF8, []byte("\x9F"))
	if got := string(tr.Bytes()); got != "\xF0\x9F" {
		t.Fatalf("Bytes() = %q, want %q", got, "\xF0\x9F")
	}

	tr.Reset()
	if got := tr.Bytes(); got != nil {
		t.Fatalf("Bytes() after Reset = %q, want nil", got)
	}
}

func TestContinuationTrackerCapAndRecovery(t *testing.T) {
	tr := NewContinuationTracker(4)

	tr.Append(ContinuationVT, []byte("\x1b[123"))
	if !tr.Broken() {
		t.Fatal("suffix over the cap should break the tracker")
	}

	// Feeds without a replay start stay dropped while broken.
	tr.Append(ContinuationVT, []byte("4"))
	if !tr.Broken() || len(tr.bytes) != 0 {
		t.Fatalf("broken tracker retained %q", tr.bytes)
	}

	// Growing past the cap across feeds also breaks tracking.
	tr.Reset()
	tr.Append(ContinuationVT, []byte("\x1b[1"))
	tr.Append(ContinuationVT, []byte("23"))
	if !tr.Broken() {
		t.Fatal("suffix grown over the cap should break the tracker")
	}

	// A new replay start that fits recovers without a reset.
	tr.Append(ContinuationVT, []byte("\x1b["))
	if tr.Broken() {
		t.Fatal("new replay start should repair the tracker")
	}
	if got := string(tr.Bytes()); got != "\x1b[" {
		t.Fatalf("Bytes() = %q, want %q", got, "\x1b[")
	}

	tr.Append(ContinuationVT, []byte("\x1b[123"))
	tr.Reset()
	if tr.Broken() {
		t.Fatal("Reset should repair the tracker")
	}
}

func TestBoundaryScannerClassifiesEffects(t *testing.T) {
	var s boundaryScanner
	s.init()

	if !s.ground() {
		t.Fatal("fresh scanner should be at ground")
	}
	if got := s.next('A'); got != effectCommitted {
		t.Fatalf("next('A') = %v, want committed", got)
	}
	if !s.ground() {
		t.Fatal("print should return to ground")
	}

	for _, c := range []byte{0x1b, '[', '1'} {
		if got := s.next(c); got != effectUncommitted {
			t.Fatalf("next(%q) = %v, want uncommitted", c, got)
		}
	}
	// BEL executes without moving the unfinished CSI along.
	if got := s.next(0x07); got != effectOmittable {
		t.Fatalf("next(BEL) = %v, want omittable", got)
	}
	if s.ground() {
		t.Fatal("BEL should not ground an unfinished CSI")
	}
	if got := s.next('m'); got != effectCommitted {
		t.Fatalf("next('m') = %v, want committed", got)
	}
	if !s.ground() {
		t.Fatal("CSI dispatch should return to ground")
	}
}

func TestBoundaryScannerPreservesUnfinishedBuilders(t *testing.T) {
	var dcs boundaryScanner
	dcs.init()
	for _, c := range []byte{0x1b, 'P', 'q', 'x'} {
		if got := dcs.next(c); got != effectUncommitted {
			t.Fatalf("dcs next(%q) = %v, want uncommitted", c, got)
		}
	}
	if got := dcs.next(0x1b); got != effectCommitted {
		t.Fatalf("dcs next(ESC) = %v, want committed", got)
	}

	var apc boundaryScanner
	apc.init()
	for _, c := range []byte{0x1b, '_', 'G', 'd'} {
		if got := apc.next(c); got != effectUncommitted {
			t.Fatalf("apc next(%q) = %v, want uncommitted", c, got)
		}
	}
}

func TestParserTracksContinuationAcrossFeeds(t *testing.T) {
	tests := []struct {
		name  string
		feeds []string
		want  string
	}{
		{
			name:  "ground after complete sequence",
			feeds: []string{"hi\x1b[31m"},
			want:  "",
		},
		{
			name:  "sequence torn across feeds",
			feeds: []string{"hi\x1b[3", "1"},
			want:  "\x1b[31",
		},
		{
			name:  "codepoint torn across feeds",
			feeds: []string{"hi\xF0", "\x9F\x98"},
			want:  "\xF0\x9F\x98",
		},
		{
			name:  "torn then completed",
			feeds: []string{"\x1b[3", "1m done"},
			want:  "",
		},
		{
			name:  "bell inside torn sequence is dropped",
			feeds: []string{"\x1b[3\x07", "1"},
			want:  "\x1b[31",
		},
		{
			name:  "osc torn across three feeds",
			feeds: []string{"\x1b]0", ";some", " title"},
			want:  "\x1b]0;some title",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewParser(NopHandler())
			tr := NewContinuationTracker(DefaultContinuationBytes)
			p.TrackContinuation(tr)
			for _, feed := range tc.feeds {
				p.AdvanceBytes([]byte(feed))
			}
			if got := string(tr.Bytes()); got != tc.want {
				t.Fatalf("continuation = %q, want %q", got, tc.want)
			}
			if err := ValidateContinuation(tr.Bytes()); err != nil {
				t.Fatalf("ValidateContinuation(%q) = %v", tr.Bytes(), err)
			}
			if p.Grounded() != (tc.want == "") {
				t.Fatalf("Grounded() = %v, want %v",
					p.Grounded(), tc.want == "")
			}
		})
	}
}

func TestParserContinuationResumesTornStream(t *testing.T) {
	// Every cut of this stream must be resumable: the tail of the first
	// parser's continuation plus the remaining bytes has to drive a
	// fresh parser to the same state as the whole stream at once.
	const stream = "abc\x1b[31mred\xF0\x9F\x98\x84\x1b]0;title\x07tail\x1b[2J"

	for cut := 0; cut <= len(stream); cut++ {
		p := NewParser(NopHandler())
		tr := NewContinuationTracker(DefaultContinuationBytes)
		p.TrackContinuation(tr)
		p.AdvanceBytes([]byte(stream[:cut]))

		cont := tr.Bytes()
		if err := ValidateContinuation(cont); err != nil {
			t.Fatalf("cut %d: ValidateContinuation(%q) = %v", cut, cont, err)
		}

		resumed := NewParser(NopHandler())
		resumedTracker := NewContinuationTracker(DefaultContinuationBytes)
		resumed.TrackContinuation(resumedTracker)
		resumed.AdvanceBytes(cont)

		// Re-exporting immediately after replay must be byte-identical.
		if got := string(resumedTracker.Bytes()); got != string(cont) {
			t.Fatalf("cut %d: re-export = %q, want %q", cut, got, cont)
		}

		resumed.AdvanceBytes([]byte(stream[cut:]))
		whole := NewParser(NopHandler())
		wholeTracker := NewContinuationTracker(DefaultContinuationBytes)
		whole.TrackContinuation(wholeTracker)
		whole.AdvanceBytes([]byte(stream))

		if resumed.scanner.State() != whole.scanner.State() {
			t.Fatalf("cut %d: resumed state %v, want %v",
				cut, resumed.scanner.State(), whole.scanner.State())
		}
		if got, want := string(resumedTracker.Bytes()), string(wholeTracker.Bytes()); got != want {
			t.Fatalf("cut %d: resumed continuation %q, want %q", cut, got, want)
		}
	}
}

// TestContinuationCutPreservesEveryDispatch is the invariant the tee
// rests on, stated in terms of what the terminal actually sees: the
// work done by the parser that was cut, plus the work done by the one
// that resumed, must equal the work of an uncut parser exactly. A
// continuation that replayed committed effects would show up here as a
// duplicated dispatch, and one that dropped bytes as a missing one.
func TestContinuationCutPreservesEveryDispatch(t *testing.T) {
	tests := []struct {
		desc   string
		stream string
	}{
		{
			desc:   "plain text",
			stream: "hello world",
		},
		{
			desc:   "csi with parameters",
			stream: "a\x1b[1;31mb\x1b[0mc",
		},
		{
			desc:   "csi with private marker and intermediates",
			stream: "\x1b[?25l\x1b[?25h\x1b[?1049h",
		},
		{
			desc:   "osc terminated by bel",
			stream: "x\x1b]0;a title\x07y",
		},
		{
			desc:   "osc terminated by string terminator",
			stream: "x\x1b]0;a title\x1b\\y",
		},
		{
			desc:   "osc containing a semicolon run",
			stream: "\x1b]8;id=1;https://example.com\x07link\x1b]8;;\x07",
		},
		{
			desc:   "dcs passthrough",
			stream: "\x1bP1$r0m\x1b\\after",
		},
		{
			desc:   "apc string",
			stream: "\x1b_Gi=1,a=T\x1b\\tail",
		},
		{
			desc:   "sos and pm strings",
			stream: "\x1bXignored\x1b\\\x1b^also\x1b\\end",
		},
		{
			desc:   "two byte utf8",
			stream: "a\u00e9b",
		},
		{
			desc:   "three byte utf8",
			stream: "a\u4e16b",
		},
		{
			desc:   "four byte utf8",
			stream: "a\U0001F604b",
		},
		{
			desc:   "combining marks",
			stream: "e\u0301\u0327x",
		},
		{
			desc:   "invalid utf8 continuation bytes",
			stream: "a\xff\xfeb\x80c",
		},
		{
			desc:   "truncated utf8 followed by ascii",
			stream: "a\xf0\x9fb",
		},
		{
			desc:   "escape aborted by can",
			stream: "\x1b[31\x18m plain",
		},
		{
			desc:   "escape aborted by sub",
			stream: "\x1b]0;ti\x1atle\x07",
		},
		{
			desc:   "escape restarted mid sequence",
			stream: "\x1b[31\x1b[1mtext",
		},
		{
			desc:   "control characters inside text",
			stream: "line one\r\nline two\ttabbed\x07",
		},
		{
			desc:   "bel inside an unfinished csi",
			stream: "\x1b[31\x07m",
		},
		{
			desc:   "charset designation",
			stream: "\x1b(0qqq\x1b(B",
		},
		{
			desc:   "everything at once",
			stream: "abc\x1b[31mred\U0001F604\x1b]0;title\x07tail\x1b[2J\r\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			whole, _ := parserEventsWithAdvanceBytes([]byte(tc.stream))
			require.NotEmpty(t, whole,
				"a stream that dispatches nothing would compare equal to anything")

			for cut := 0; cut <= len(tc.stream); cut++ {
				prefix, suffix := tc.stream[:cut], tc.stream[cut:]

				first := new(parserRunHandler)
				p := NewParser(first)
				tracker := NewContinuationTracker(DefaultContinuationBytes)
				p.TrackContinuation(tracker)
				p.AdvanceBytes([]byte(prefix))

				cont := tracker.Bytes()
				if err := ValidateContinuation(cont); err != nil {
					t.Fatalf("cut %d: ValidateContinuation(%q): %v", cut, cont, err)
				}

				second := new(parserRunHandler)
				resumed := NewParser(second)
				resumed.TrackContinuation(
					NewContinuationTracker(DefaultContinuationBytes))
				resumed.AdvanceBytes(cont)
				resumed.AdvanceBytes([]byte(suffix))

				got := append(
					normalizeParserRunEvents(first.events),
					normalizeParserRunEvents(second.events)...)
				if !reflect.DeepEqual(got, whole) {
					t.Fatalf("cut %d of %q:\n cut+resumed: %v\n uncut:       %v",
						cut, tc.stream, got, whole)
				}
			}
		})
	}
}

// TestContinuationTrackerCapBoundaries pins the retention cap at the
// byte, since a suffix that outgrows it is the one case where a
// snapshot cannot describe where the stream was cut.
func TestContinuationTrackerCapBoundaries(t *testing.T) {
	t.Parallel()

	// A long OSC is the sequence that realistically grows without
	// bound, so the cases are built from one.
	osc := func(payload int) string {
		return "\x1b]0;" + strings.Repeat("x", payload)
	}

	tests := []struct {
		desc       string
		maxBytes   int
		feeds      []string
		wantBroken bool
	}{
		{
			desc:     "a suffix exactly at the cap is retained",
			maxBytes: len(osc(6)),
			feeds:    []string{osc(6)},
		},
		{
			desc:       "one byte past the cap breaks the tracker",
			maxBytes:   len(osc(6)) - 1,
			feeds:      []string{osc(6)},
			wantBroken: true,
		},
		{
			desc:     "extending to exactly the cap is retained",
			maxBytes: len(osc(6)),
			feeds:    []string{osc(3), "xxx"},
		},
		{
			desc:       "extending past the cap breaks the tracker",
			maxBytes:   len(osc(6)),
			feeds:      []string{osc(6), "x"},
			wantBroken: true,
		},
		{
			desc:       "a zero cap cannot retain anything",
			maxBytes:   0,
			feeds:      []string{"\x1b["},
			wantBroken: true,
		},
		{
			desc:     "a zero cap is fine while the stream stays at ground",
			maxBytes: 0,
			feeds:    []string{"plain text"},
		},
		{
			desc:     "reaching ground recovers a broken tracker",
			maxBytes: 4,
			feeds:    []string{osc(64), "done\x07"},
		},
		{
			desc:     "a fresh replay start recovers a broken tracker",
			maxBytes: 8,
			feeds:    []string{osc(64), "\x1b[3"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			p := NewParser(NopHandler())
			tracker := NewContinuationTracker(tc.maxBytes)
			p.TrackContinuation(tracker)
			for _, feed := range tc.feeds {
				p.AdvanceBytes([]byte(feed))
			}

			assert.Equal(t, tc.wantBroken, tracker.Broken())
			if tc.wantBroken {
				assert.Nil(t, tracker.Bytes(),
					"a broken tracker cannot describe the cut")
				return
			}
			assert.NoError(t, ValidateContinuation(tracker.Bytes()))
		})
	}
}

// TestMarkBrokenSurvivesUntilGround pins how a tracker attached partway
// through a stream behaves: it stays unusable until the stream reaches
// a point that needs nothing it missed.
func TestMarkBrokenSurvivesUntilGround(t *testing.T) {
	t.Parallel()

	p := NewParser(NopHandler())
	tracker := NewContinuationTracker(DefaultContinuationBytes)
	p.TrackContinuation(tracker)

	// Joining mid-sequence: the bytes that opened it were never seen.
	p.AdvanceBytes([]byte("\x1b]0;par"))
	tracker.MarkBroken()
	require.True(t, tracker.Broken())

	p.AdvanceBytes([]byte("tial"))
	assert.True(t, tracker.Broken(),
		"extending a broken suffix cannot make it whole")

	p.AdvanceBytes([]byte("\x07back at ground"))
	assert.False(t, tracker.Broken())
	assert.NoError(t, ValidateContinuation(tracker.Bytes()))
}
