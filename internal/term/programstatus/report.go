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

// Package programstatus implements the host side of the Program Status
// Protocol (OSC 7501): parsing reports sent by programs and keeping the
// set of records a terminal holds for them.
package programstatus

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Limits from the protocol specification. A report that breaks one of
// them is discarded whole.
const (
	// MaxSequence bounds the whole OSC sequence, introducer through
	// terminator.
	MaxSequence = 4096
	// MaxRecords bounds the records a terminal keeps; the least recently
	// updated record is evicted past it.
	MaxRecords = 256

	maxKey          = 16
	maxMsgEncoded   = 2732
	maxMsgDecoded   = 2048
	maxTitleEncoded = 256
	maxTitleDecoded = 192
	maxApp          = 32
	maxID           = 128
	maxIDSegment    = 32
	maxIDDepth      = 8
)

// HostApp is the App of the reports the host makes on its own behalf.
const HostApp = "rune"

// State is what a program reports about itself.
type State uint8

// States in the order the protocol names them; Idle is the zero value
// so an empty summary reads as idle.
const (
	Idle State = iota
	Working
	Done
	Blocked
	Error
	// Clear is not a state: it removes the addressed record and every
	// record beneath it.
	Clear
)

var stateNames = map[string]State{
	"idle":    Idle,
	"working": Working,
	"done":    Done,
	"blocked": Blocked,
	"error":   Error,
	"clear":   Clear,
}

func (s State) String() string {
	switch s {
	case Idle:
		return "idle"
	case Working:
		return "working"
	case Done:
		return "done"
	case Blocked:
		return "blocked"
	case Error:
		return "error"
	case Clear:
		return "clear"
	}
	return fmt.Sprintf("state(%d)", uint8(s))
}

// Kind says what a blocked program is waiting for.
type Kind uint8

// Kinds of blocked state; KindNone means the program did not say.
const (
	KindNone Kind = iota
	KindPermission
	KindQuestion
	KindAuth
)

var kindNames = map[string]Kind{
	"permission": KindPermission,
	"question":   KindQuestion,
	"auth":       KindAuth,
}

func (k Kind) String() string {
	switch k {
	case KindNone:
		return ""
	case KindPermission:
		return "permission"
	case KindQuestion:
		return "question"
	case KindAuth:
		return "auth"
	}
	return fmt.Sprintf("kind(%d)", uint8(k))
}

// Report is one parsed OSC 7501 report. Free-text fields are already
// decoded and stripped of invisible formatting characters.
type Report struct {
	// ID addresses the record; empty is the root record.
	ID    string
	State State
	// Kind is only set with Blocked.
	Kind Kind
	// Progress is 0 to 100, or -1 when indeterminate. Only set with
	// Working and Blocked.
	Progress int
	App      string
	Title    string
	Msg      string
}

// Parent returns the id of the record above this one, and false when
// the report addresses the root record.
func (r Report) Parent() (string, bool) {
	if r.ID == "" {
		return "", false
	}
	if i := strings.LastIndexByte(r.ID, '/'); i >= 0 {
		return r.ID[:i], true
	}
	return "", true
}

var (
	errNoState  = errors.New("programstatus: missing state")
	errBadState = errors.New("programstatus: unknown state")
	errBadID    = errors.New("programstatus: invalid id")
	errKeyLimit = errors.New("programstatus: key exceeds 16 bytes")
	errTooLong  = errors.New("programstatus: value exceeds limit")
	errBadText  = errors.New("programstatus: invalid base64 text")
	errControl  = errors.New("programstatus: text contains control character")
)

// Parse decodes the body of an OSC 7501 report, the bytes between
// "7501;" and the terminator. It applies every rule and limit of the
// specification: malformed pairs and unknown keys are skipped, repeated
// keys take the last value, and the whole report is rejected when state
// is missing or unknown, the id is malformed, a limit is exceeded, or a
// free-text value does not decode to control-free UTF-8. The caller
// enforces the whole-sequence limit.
func Parse(body []byte) (Report, error) {
	r := Report{Progress: -1}
	var (
		haveState bool
		kind      string
		progress  string
		app       string
		title     []byte
		msg       []byte
		haveTitle bool
		haveMsg   bool
	)
	for pair := range bytes.SplitSeq(body, []byte{':'}) {
		rawKey, rawValue, ok := bytes.Cut(pair, []byte{'='})
		if !ok {
			continue
		}
		key := bytes.TrimSpace(rawKey)
		value := bytes.TrimSpace(rawValue)
		if len(key) == 0 || !validKey(key) || !validValue(value) {
			continue
		}
		if len(key) > maxKey {
			return Report{}, errKeyLimit
		}
		switch string(key) {
		case "state":
			s, ok := stateNames[string(value)]
			if !ok {
				return Report{}, errBadState
			}
			r.State, haveState = s, true
		case "id":
			if !validID(value) {
				return Report{}, errBadID
			}
			r.ID = string(value)
		case "kind":
			kind = string(value)
		case "progress":
			progress = string(value)
		case "app":
			app = string(value)
		case "title":
			title, haveTitle = value, true
		case "msg":
			msg, haveMsg = value, true
		}
	}
	if !haveState {
		return Report{}, errNoState
	}
	if haveTitle {
		t, err := decodeText(title, maxTitleEncoded, maxTitleDecoded)
		if err != nil {
			return Report{}, err
		}
		r.Title = t
	}
	if haveMsg {
		m, err := decodeText(msg, maxMsgEncoded, maxMsgDecoded)
		if err != nil {
			return Report{}, err
		}
		r.Msg = m
	}
	if r.State == Clear {
		return Report{ID: r.ID, State: Clear, Progress: -1}, nil
	}
	if r.State == Blocked {
		r.Kind = kindNames[kind]
	}
	if r.State == Working || r.State == Blocked {
		r.Progress = parseProgress(progress)
	}
	if validApp(app) {
		r.App = app
	}
	return r, nil
}

// Normalize returns r as Parse would have returned it, dropping the
// fields that are meaningless for its state and an invalid App. It
// fails where Parse rejects: an unknown state, a malformed ID, or a
// Title or Msg over its limit or holding a control character.
func Normalize(r Report) (Report, error) {
	if r.State > Clear {
		return Report{}, errBadState
	}
	if r.ID != "" && !validID([]byte(r.ID)) {
		return Report{}, errBadID
	}
	if r.State == Clear {
		return Report{ID: r.ID, State: Clear, Progress: -1}, nil
	}
	title, err := cleanText(r.Title, maxTitleDecoded)
	if err != nil {
		return Report{}, err
	}
	msg, err := cleanText(r.Msg, maxMsgDecoded)
	if err != nil {
		return Report{}, err
	}
	r.Title, r.Msg = title, msg
	if r.State != Blocked {
		r.Kind = KindNone
	} else if r.Kind > KindAuth {
		r.Kind = KindNone
	}
	if r.State != Working && r.State != Blocked || r.Progress < 0 || r.Progress > 100 {
		r.Progress = -1
	}
	if !validApp(r.App) {
		r.App = ""
	}
	return r, nil
}

func validKey(key []byte) bool {
	for _, c := range key {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

func validValue(value []byte) bool {
	for _, c := range value {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '_', c == '.', c == ',', c == '+', c == '/', c == '=', c == '-':
		default:
			return false
		}
	}
	return true
}

func validSegmentByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case c == '_', c == '.', c == '+', c == '-':
		return true
	}
	return false
}

func validID(id []byte) bool {
	if len(id) == 0 || len(id) > maxID {
		return false
	}
	segments := bytes.Split(id, []byte{'/'})
	if len(segments) > maxIDDepth {
		return false
	}
	for _, seg := range segments {
		if len(seg) == 0 || len(seg) > maxIDSegment {
			return false
		}
		for _, c := range seg {
			if !validSegmentByte(c) {
				return false
			}
		}
	}
	return true
}

func validApp(app string) bool {
	if len(app) == 0 || len(app) > maxApp {
		return false
	}
	for i := 0; i < len(app); i++ {
		if !validSegmentByte(app[i]) {
			return false
		}
	}
	return true
}

func parseProgress(s string) int {
	if len(s) == 0 || len(s) > 3 {
		return -1
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	if n > 100 {
		return -1
	}
	return n
}

func decodeText(encoded []byte, maxEncoded, maxDecoded int) (string, error) {
	if len(encoded) > maxEncoded {
		return "", errTooLong
	}
	encoded = bytes.TrimRight(encoded, "=")
	decoded, err := base64.RawStdEncoding.DecodeString(string(encoded))
	if err != nil {
		return "", errBadText
	}
	if len(decoded) > maxDecoded {
		return "", errTooLong
	}
	if !utf8.Valid(decoded) {
		return "", errBadText
	}
	return cleanText(string(decoded), maxDecoded)
}

// cleanText rejects text over maxLen bytes or holding a control
// character and strips invisible formatting characters from the rest.
func cleanText(text string, maxLen int) (string, error) {
	if len(text) > maxLen {
		return "", errTooLong
	}
	if !utf8.ValidString(text) {
		return "", errBadText
	}
	var sb strings.Builder
	sb.Grow(len(text))
	for _, c := range text {
		if isControl(c) {
			return "", errControl
		}
		if isInvisibleFormatting(c) {
			continue
		}
		sb.WriteRune(c)
	}
	return sb.String(), nil
}

func isControl(c rune) bool {
	return c < 0x20 || c == 0x7f || (c >= 0x80 && c <= 0x9f)
}

// isInvisibleFormatting reports zero-width and bidirectional formatting
// characters, which are stripped so a program cannot disguise what a
// host shows outside the terminal grid.
func isInvisibleFormatting(c rune) bool {
	return (c >= 0x200b && c <= 0x200f) ||
		(c >= 0x202a && c <= 0x202e) ||
		(c >= 0x2066 && c <= 0x2069)
}
