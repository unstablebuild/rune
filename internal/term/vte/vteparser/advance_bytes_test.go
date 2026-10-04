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
	"testing"

	"github.com/stretchr/testify/assert"
)

// captureHandler records InputRun and Input calls so tests can assert
// how the parser split a stream between the batched and per-rune paths.
type captureHandler struct {
	nopHandler
	runs    [][]byte
	singles []rune
}

func (h *captureHandler) Input(c rune) {
	h.singles = append(h.singles, c)
}

func (h *captureHandler) InputRun(run []byte) {
	cp := make([]byte, len(run))
	copy(cp, run)
	h.runs = append(h.runs, cp)
}

func TestAdvanceBytesBatchesPrintableRuns(t *testing.T) {
	h := &captureHandler{}
	p := NewParser(h, new(StdTimeout))

	p.AdvanceBytes([]byte("hello"))

	assert.Equal(t, [][]byte{[]byte("hello")}, h.runs)
	assert.Empty(t, h.singles)
	assert.Equal(t, 'o', p.state.precedingChar)
}

func TestAdvanceBytesSplitsOnControlBytes(t *testing.T) {
	h := &captureHandler{}
	p := NewParser(h, new(StdTimeout))

	// "ab\rcd": the CR is executed per-byte, the two printable runs batch.
	p.AdvanceBytes([]byte("ab\rcd"))

	assert.Equal(t, [][]byte{[]byte("ab"), []byte("cd")}, h.runs)
	assert.Empty(t, h.singles)
}

func TestLoggingHandlerForwardsInputRun(t *testing.T) {
	h := &captureHandler{}
	wrapped := HandlerWithLogging("test", h)

	wrapped.InputRun([]byte("world"))

	assert.Equal(t, [][]byte{[]byte("world")}, h.runs)
}
