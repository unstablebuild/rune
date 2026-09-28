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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestSequencer(t *testing.T) {
	t.Run("reports only first keys as prefixes", func(t *testing.T) {
		s := NewSequencer([]Sequence{{
			First: term.KeyComb{Ch: 'g'},
			Last:  term.KeyComb{Ch: 'd'},
		}}, time.Hour)

		assert.True(t, s.IsPrefix(term.KeyComb{Ch: 'g'}))
		assert.False(t, s.IsPrefix(term.KeyComb{Ch: 'd'}))
		assert.False(t, s.IsPrefix(term.KeyComb{Ch: 'g', Mod: term.ModCtrl}))
	})
	t.Run("returns sequence match", func(t *testing.T) {
		interests := []Sequence{{
			First: term.KeyComb{Ch: 'd'},
			Last:  term.KeyComb{Ch: 'd'},
		}}
		s := NewSequencer(interests, 1*time.Hour)

		seq, match := s.Sequence(term.KeyComb{Ch: 'd'})
		assert.Equal(t, SequencePartialMatch, match)
		assert.Zero(t, seq)

		seq, match = s.Sequence(term.KeyComb{Ch: 'd'})
		require.Equal(t, SequenceMatch, match)
		assert.Equal(t, interests[0], seq)
	})
	t.Run("does not return match if it took too long for second event", func(t *testing.T) {
		interests := []Sequence{{
			First: term.KeyComb{Ch: 'd'},
			Last:  term.KeyComb{Ch: 'd'},
		}}
		s := NewSequencer(interests, 1*time.Nanosecond)

		seq, match := s.Sequence(term.KeyComb{Ch: 'd'})
		assert.Equal(t, SequencePartialMatch, match)
		assert.Zero(t, seq)

		time.Sleep(time.Millisecond)

		seq, match = s.Sequence(term.KeyComb{Ch: 'd'})
		require.Equal(t, SequencePartialMatch, match)
		assert.Zero(t, seq)
	})
	t.Run("returns sequence match even if last event failed to match with initial event", func(t *testing.T) {
		interests := []Sequence{{
			First: term.KeyComb{Ch: 'd'},
			Last:  term.KeyComb{Ch: 'd'},
		}}
		s := NewSequencer(interests, 25*time.Millisecond)

		seq, match := s.Sequence(term.KeyComb{Ch: 'd'})
		assert.Equal(t, SequencePartialMatch, match)
		assert.Zero(t, seq)

		time.Sleep(40 * time.Millisecond)

		seq, match = s.Sequence(term.KeyComb{Ch: 'd'})
		assert.Equal(t, SequencePartialMatch, match)
		assert.Zero(t, seq)

		seq, match = s.Sequence(term.KeyComb{Ch: 'd'})
		assert.Equal(t, SequenceMatch, match)
		assert.Equal(t, interests[0], seq)
	})
	t.Run("returns SequenceNoMatch if there's is no match", func(t *testing.T) {
		interests := []Sequence{{
			First: term.KeyComb{Ch: 'x'},
			Last:  term.KeyComb{Ch: 'd'},
		}}
		s := NewSequencer(interests, 1*time.Hour)

		seq, match := s.Sequence(term.KeyComb{Ch: 'd'})
		assert.Equal(t, SequenceNoMatch, match)
		assert.Zero(t, seq)

		seq, match = s.Sequence(term.KeyComb{Ch: 'x'})
		require.Equal(t, SequencePartialMatch, match)
		assert.Zero(t, seq)

		seq, match = s.Sequence(term.KeyComb{Ch: 'd'})
		require.Equal(t, SequenceMatch, match)
		assert.Equal(t, interests[0], seq)
	})
	t.Run("modifier prefix matches even after timeout elapses", func(t *testing.T) {
		// A modifier-bearing prefix such as <ctrl-x> is not ambiguous with
		// plain typed input, so it waits indefinitely for its second key.
		interests := []Sequence{{
			First: term.KeyComb{Ch: 'x', Mod: term.ModCtrl},
			Last:  term.KeyComb{Ch: 'p', Mod: term.ModCtrl},
		}}
		s := NewSequencer(interests, 1*time.Nanosecond)

		seq, match := s.Sequence(term.KeyComb{Ch: 'x', Mod: term.ModCtrl})
		assert.Equal(t, SequencePartialMatch, match)
		assert.Zero(t, seq)

		time.Sleep(5 * time.Millisecond)

		seq, match = s.Sequence(term.KeyComb{Ch: 'p', Mod: term.ModCtrl})
		require.Equal(t, SequenceMatch, match)
		assert.Equal(t, interests[0], seq)
	})
}

func TestParseSequence(t *testing.T) {
	tsuite := []struct {
		in      string
		wantSeq Sequence
		wantErr bool
	}{
		{"", Sequence{}, true},
		{"f", Sequence{}, true},
		{"<c-x>", Sequence{}, true},
		{"<><>", Sequence{}, true},
		{"<c-s> ", Sequence{}, true},
		{" <c-p>", Sequence{}, true},
		{"<c-pf", Sequence{}, true},
		{"f<c-p", Sequence{}, true},
		{"c-p>f", Sequence{}, true},
		{">c-p<f", Sequence{}, true},
		{"f>c-p<", Sequence{}, true},
		{"><><", Sequence{}, true},
		{">>", Sequence{}, true},
		{"<<", Sequence{}, true},
		{"                                               ", Sequence{}, true},
		{"ff", Sequence{
			First: term.KeyComb{Ch: 'f'},
			Last:  term.KeyComb{Ch: 'f'},
		}, false},
		{"\\>\\>", Sequence{
			First: term.KeyComb{Ch: '>'},
			Last:  term.KeyComb{Ch: '>'},
		}, false},
		{"\\<\\<", Sequence{
			First: term.KeyComb{Ch: '<'},
			Last:  term.KeyComb{Ch: '<'},
		}, false},
		{"<c-x>\\\\", Sequence{
			First: term.KeyComb{Ch: 'x', Mod: term.ModCtrl},
			Last:  term.KeyComb{Ch: '\\'},
		}, false},
		{"f<c-p>", Sequence{
			First: term.KeyComb{Ch: 'f'},
			Last:  term.KeyComb{Ch: 'p', Mod: term.ModCtrl},
		}, false},
		{"<c-p>f", Sequence{
			First: term.KeyComb{Ch: 'p', Mod: term.ModCtrl},
			Last:  term.KeyComb{Ch: 'f'},
		}, false},
		{"<c-x><c-p>", Sequence{
			First: term.KeyComb{Ch: 'x', Mod: term.ModCtrl},
			Last:  term.KeyComb{Ch: 'p', Mod: term.ModCtrl},
		}, false},
		{"<c-m-]><c-p>", Sequence{
			First: term.KeyComb{
				Ch:  ']',
				Mod: term.ModCtrlMeta,
			},
			Last: term.KeyComb{Ch: 'p', Mod: term.ModCtrl},
		}, false},
	}

	for _, tcase := range tsuite {
		t.Run(fmt.Sprintf("test case: '%s'", tcase.in), func(t *testing.T) {
			actualSeq, actualErr := ParseSequence(tcase.in)
			if tcase.wantErr {
				require.Error(t, actualErr)
			} else {
				require.NoError(t, actualErr)
			}
			assert.Equal(t, tcase.wantSeq, actualSeq)
			if tcase.wantErr {
				return
			}

			// test that it's able to parse Sequence.String again
			// into the original sequence
			actualSeq, actualErr = ParseSequence(actualSeq.String())
			if tcase.wantErr {
				require.Error(t, actualErr, actualSeq.String())
			} else {
				require.NoError(t, actualErr)
			}
			assert.Equal(t, tcase.wantSeq, actualSeq)
		})
	}
}
