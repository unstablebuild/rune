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

package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// SequenceMatchResult represents the result of sequencing.
type SequenceMatchResult uint8

// List of sequence match results.
const (
	SequenceNoMatch SequenceMatchResult = iota
	SequencePartialMatch
	SequenceMatch
)

// Sequence represents a term.EventKey sequence. For matching purposes,
// only Event.Mod, Event.Key and Event.Ch are considered,
// the rest of fields are ignored.
type Sequence struct {
	First term.KeyComb
	Last  term.KeyComb
}

// Sequencer is a key event sequencer which detects sequences of (2) term.EventKey
// events that are triggered within a specified time.
type Sequencer struct {
	interests map[term.KeyComb]map[term.KeyComb]struct{}
	timeout   time.Duration

	first    term.KeyComb
	firstCtx context.Context
	ctxClean func()
}

// NewSequencer allocates storage for a new Sequencer and initalizes it.
func NewSequencer(interests []Sequence, timeout time.Duration) *Sequencer {
	ret := new(Sequencer)
	ret.Init(interests, timeout)
	return ret
}

// Init initializes this sequencer with the given interests and a timeout.
func (s *Sequencer) Init(interests []Sequence, timeout time.Duration) {
	s.timeout = timeout
	s.interests = make(map[term.KeyComb]map[term.KeyComb]struct{})
	for _, i := range interests {
		first, last := i.First, i.Last
		if _, ok := s.interests[first]; !ok {
			s.interests[first] = make(map[term.KeyComb]struct{})
		}
		s.interests[first][last] = struct{}{}
	}
	// initialize now with canceled context so we don't need to check if ctxClean
	// or firstCtx has been initialized
	s.firstCtx, s.ctxClean = context.WithCancel(context.Background())
	s.ctxClean()
}

// Sequence processes ev and returns whether there's a sequence match or not,
// and if so, which sequence was processed.
func (s *Sequencer) Sequence(key term.KeyComb) (
	seq Sequence, match SequenceMatchResult,
) {
	first := s.first
	last := key

	firstCtx := s.firstCtx
	ctxClean := s.ctxClean
	defer ctxClean()

	s.firstCtx, s.ctxClean = context.WithTimeout(context.Background(), s.timeout)
	s.first = last

	if _, ok := s.interests[last]; ok {
		match = SequencePartialMatch
	} else {
		match = SequenceNoMatch
	}

	// Only a bare-character prefix is subject to the timeout: a plain key
	// such as vi's `d` also has a standalone meaning, so a stale `dd` must
	// not fire. A modifier-bearing prefix (e.g. <ctrl-x>) has no such
	// ambiguity and waits indefinitely for its second key.
	if first.Mod == 0 {
		select {
		case <-firstCtx.Done():
			return
		default:
		}
	}

	m, ok := s.interests[first]
	if !ok {
		return
	}

	_, ok = m[last]
	if !ok {
		return
	}

	// first is consumed if match is found
	s.Reset()
	seq = Sequence{First: first, Last: last}
	match = SequenceMatch
	return
}

// Reset resets this Sequencer such that the next event passed
// to Handle is considered as a first event.
func (s *Sequencer) Reset() {
	s.ctxClean()
}

// IsPrefix reports whether key is the first key of any sequence this
// Sequencer is interested in.
func (s *Sequencer) IsPrefix(key term.KeyComb) bool {
	_, ok := s.interests[key]
	return ok
}

// ParseSequence parses str into a Sequence or returns
// error if it fails to parse it. This function is not case sensitive.
// It accepts two key string representation, as they would be individually
// parsed by term.ParseKey: i.e. <c-x><c-p>, f<c-p>, <c-x>t, gf
func ParseSequence(str string) (Sequence, error) {
	keys, err := term.ParseKeys(str)
	if err != nil {
		return Sequence{}, fmt.Errorf("invalid sequence: %v", err)
	}
	if len(keys) != 2 {
		return Sequence{}, errors.New("invalid sequence: expected exactly 2 keys")
	}
	return Sequence{First: keys[0], Last: keys[1]}, nil
}

func (s Sequence) String() string {
	var ret strings.Builder
	ret.WriteString(s.First.String())
	ret.WriteString(s.Last.String())
	return ret.String()
}
