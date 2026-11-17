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
	"bytes"
	"errors"

	"unstable.build/rune/internal/term/vte/vtescanner"
)

// DefaultContinuationBytes is the retention cap a tracker is given
// when the caller has no reason to pick another one. Real escape
// sequences are tiny; the headroom only exists so a program that
// dribbles a huge OSC payload does not break tracking.
const DefaultContinuationBytes = 64 << 10

// Errors reported by ValidateContinuation.
var (
	// ErrNoPendingState means nonempty bytes left the scanner at
	// ground. Such bytes are a complete fragment whose effects are
	// already part of the snapshot, so replaying them would repeat
	// work.
	ErrNoPendingState = errors.New("vteparser: continuation leaves no pending state")

	// ErrNonCanonicalContinuation means the bytes do not begin at the
	// replay start for the state they leave behind. The extra prefix
	// would vanish when the resumed parser exported its own
	// continuation, breaking byte-identical re-export.
	ErrNonCanonicalContinuation = errors.New("vteparser: continuation does not begin at its replay start")

	// ErrReplayWouldCommit means replaying the bytes would perform
	// handler-visible work that the snapshot already reflects, such as
	// a BEL embedded in an unfinished CSI sequence.
	ErrReplayWouldCommit = errors.New("vteparser: continuation replay would commit work")
)

// ContinuationPending identifies which unfinished state a feed ended in.
type ContinuationPending uint8

// Kinds of unfinished state a feed can leave behind.
const (
	// ContinuationVT is an unfinished escape, CSI, OSC, DCS or APC
	// sequence.
	ContinuationVT ContinuationPending = iota

	// ContinuationUTF8 is an incomplete UTF-8 codepoint.
	ContinuationUTF8
)

// ContinuationTracker retains the bytes needed to reconstruct
// unfinished parser state at a feed boundary.
//
// A feed is one chunk of bytes handed to a Parser. It can end in the
// middle of a VT sequence or a UTF-8 codepoint. To continue in another
// parser, that parser starts at ground and reads the retained bytes
// again. The first retained byte is the replay start.
//
// The tracker is updated once per feed: a feed ending at ground resets
// it, and any other feed appends. Append replaces the suffix outright
// when the feed contains a new replay start and extends it otherwise,
// which keeps the retained bytes minimal without re-parsing old input.
//
// A suffix that outgrows the cap marks the tracker broken until a
// later feed reaches ground or supplies a new replay start, both of
// which need nothing that was dropped.
type ContinuationTracker struct {
	maxBytes int
	bytes    []byte
	broken   bool
}

// NewContinuationTracker returns a tracker retaining at most maxBytes.
func NewContinuationTracker(maxBytes int) *ContinuationTracker {
	return &ContinuationTracker{maxBytes: maxBytes}
}

// Broken reports whether retained bytes were dropped because the
// suffix outgrew the cap. A broken tracker cannot describe the parser
// state, so a snapshot taken while broken cannot be resumed.
func (t *ContinuationTracker) Broken() bool { return t.broken }

// Reset clears the suffix and the broken flag.
func (t *ContinuationTracker) Reset() {
	t.broken = false
	t.bytes = t.bytes[:0]
}

// MarkBroken declares the retained state unusable. Callers that start
// tracking partway through a stream use it so a suffix is not
// reconstructed from bytes whose beginning was never seen.
func (t *ContinuationTracker) MarkBroken() { t.markBroken() }

// Append retains the part of input needed to replay a feed that ended
// with pending state unfinished.
func (t *ContinuationTracker) Append(pending ContinuationPending, input []byte) {
	var start int
	var ok bool
	if pending == ContinuationUTF8 {
		start, ok = findUTF8ReplayStart(input)
	} else {
		start, ok = findVTReplayStart(input)
	}
	if !ok {
		t.extend(input)
		return
	}
	t.replace(input[start:])
}

// Bytes returns the replay-safe continuation suffix. The retained
// bytes already begin at the replay start; this drops only the bytes
// that would repeat committed effects without contributing to the
// unfinished state. It returns nil when the tracker is broken.
func (t *ContinuationTracker) Bytes() []byte {
	if t.broken || len(t.bytes) == 0 {
		return nil
	}
	out := make([]byte, 0, len(t.bytes))
	var s boundaryScanner
	s.init()
	for _, c := range t.bytes {
		if s.next(c) == effectOmittable {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (t *ContinuationTracker) replace(suffix []byte) {
	t.bytes = t.bytes[:0]
	if len(suffix) > t.maxBytes {
		t.markBroken()
		return
	}
	t.bytes = append(t.bytes, suffix...)
	t.broken = false
}

func (t *ContinuationTracker) extend(fragment []byte) {
	if t.broken {
		return
	}
	if len(fragment) > t.maxBytes-len(t.bytes) {
		t.markBroken()
		return
	}
	t.bytes = append(t.bytes, fragment...)
}

func (t *ContinuationTracker) markBroken() {
	t.broken = true
	t.bytes = t.bytes[:0]
}

// track records one feed given the scanner state it left behind.
func (t *ContinuationTracker) track(state vtescanner.State, input []byte) {
	switch state {
	case vtescanner.Ground:
		t.Reset()
	case vtescanner.Utf8:
		t.Append(ContinuationUTF8, input)
	default:
		t.Append(ContinuationVT, input)
	}
}

// ValidateContinuation reports whether bytes are a well-formed
// continuation: they must leave the parser outside ground, begin at
// the replay start for that state, and commit nothing on replay. Empty
// bytes denote ground and are always valid.
func ValidateContinuation(b []byte) error {
	if len(b) == 0 {
		return nil
	}

	var s boundaryScanner
	s.init()
	committed := false
	for _, c := range b {
		if s.next(c) != effectUncommitted {
			committed = true
		}
	}

	if s.ground() {
		return ErrNoPendingState
	}

	var start int
	var ok bool
	if s.scanner.State() == vtescanner.Utf8 {
		start, ok = findUTF8ReplayStart(b)
	} else {
		start, ok = findVTReplayStart(b)
	}
	if !ok || start != 0 {
		return ErrNonCanonicalContinuation
	}

	if committed {
		return ErrReplayWouldCommit
	}
	return nil
}

// findVTReplayStart returns the index of the last ESC in input.
//
// ESC abandons whatever the parser was doing and begins a fresh escape
// state, so feeding a grounded parser everything from the last ESC
// onward recreates the unfinished state at the end of input. No ESC
// means the sequence began in an earlier feed.
func findVTReplayStart(input []byte) (int, bool) {
	i := bytes.LastIndexByte(input, c0ESC)
	return i, i >= 0
}

// findUTF8ReplayStart returns the index of the lead byte of the
// codepoint still being decoded. An incomplete codepoint holds at most
// three bytes, so only the final three positions can carry its lead.
func findUTF8ReplayStart(input []byte) (int, bool) {
	const maxPending = 3
	for i := len(input) - 1; i >= 0 && len(input)-i <= maxPending; i-- {
		if input[i] >= 0xC0 {
			return i, true
		}
	}
	return 0, false
}

// continuationEffect classifies how one byte of a continuation suffix
// affects committed work.
type continuationEffect uint8

const (
	// effectUncommitted commits nothing and may build unfinished state.
	effectUncommitted continuationEffect = iota

	// effectCommitted commits work and also moves the state machine, so
	// it cannot be dropped in isolation.
	effectCommitted

	// effectOmittable commits work without moving the state machine, so
	// dropping it avoids a duplicate effect.
	effectOmittable
)

// boundaryScanner replays continuation bytes through a real scanner
// against a driver that only records whether a byte reached the
// handler. It runs at export and validation time, never on the feed
// path.
type boundaryScanner struct {
	driver  boundaryDriver
	scanner vtescanner.Scanner
}

func (s *boundaryScanner) init() {
	s.scanner.Init(&s.driver)
}

func (s *boundaryScanner) ground() bool {
	return s.scanner.State() == vtescanner.Ground
}

func (s *boundaryScanner) next(c byte) continuationEffect {
	before := s.scanner.State()
	s.driver.committed = false
	s.scanner.Advance(c)
	if !s.driver.committed {
		return effectUncommitted
	}
	if before == s.scanner.State() && !s.ground() {
		return effectOmittable
	}
	return effectCommitted
}

// boundaryDriver records handler-visible dispatches. Hook and Put only
// build unfinished DCS state, which replay must reconstruct, so they
// are not committed work; their terminating Unhook is.
type boundaryDriver struct{ committed bool }

func (d *boundaryDriver) Print(rune)                                 { d.committed = true }
func (d *boundaryDriver) Execute(byte)                               { d.committed = true }
func (d *boundaryDriver) Hook([][]uint16, []byte, bool, rune)        {}
func (d *boundaryDriver) Put(byte)                                   {}
func (d *boundaryDriver) Unhook()                                    { d.committed = true }
func (d *boundaryDriver) OSCDispatch([][]byte, bool)                 { d.committed = true }
func (d *boundaryDriver) CSIDispatch([][]uint16, []byte, bool, rune) { d.committed = true }
func (d *boundaryDriver) ESCDispatch([]byte, bool, byte)             { d.committed = true }
func (d *boundaryDriver) APCDispatch([]byte)                         { d.committed = true }
