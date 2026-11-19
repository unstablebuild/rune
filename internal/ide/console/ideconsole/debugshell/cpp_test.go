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

//go:build e2e

package debugshell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-dap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestE2E_Cpp_Launch(t *testing.T) {
	t.Parallel()
	lldbDapBin := findLldbDap(t)
	cxx := findCompiler(t, "c++", "clang++", "g++")
	tmpDir, binPath := setupBuggyCcpp(t, "buggy_cpp", "main.cpp", cxx)
	mainPath := filepath.Join(tmpDir, "main.cpp")

	h := newCcppE2EHarness(t, lldbDapBin, tmpDir, "cpp", "main.cpp")
	defer h.close()
	ctx := h.ctx

	it, err := h.run(ctx, subInitialize, "cpp")
	require.NoError(t, err)
	go h.drainIterator(it)

	_, err = h.run(ctx, subLaunch, binPath)
	require.NoError(t, err)

	h.waitMilestone(t, "initialized", 30*time.Second)

	bpLine := ccppStmtLine(t, mainPath, "int total = 0;")
	h.setBreakpoint(t, mainPath, bpLine)

	_, err = h.run(ctx, subConfigured)
	require.NoError(t, err)
	h.waitMilestone(t, "stopped", 30*time.Second)

	// The breakpoint sits inside a member function, so the frame name
	// must carry the class qualifier lldb demangles from the binary.
	threadID := h.firstThreadID(t)
	stack := h.stackTrace(t, threadID)
	require.NotEmpty(t, stack)
	assert.Contains(t, stack[0].Name, "Accumulator::sum_to",
		"expected to be stopped inside Accumulator::sum_to, got %s", stack[0].Name)
	assert.Equal(t, bpLine, stack[0].Line)
	require.NotNil(t, stack[0].Source)
	assert.Equal(t, mainPath, stack[0].Source.Path)

	h.waitLocation(t, stoppedLocationID, 5*time.Second)
	stopLoc := h.findLocation(t, stoppedLocationID)
	require.True(t, stopLoc.To.X > 1,
		"expected stopped location to span the line, got To.X=%d", stopLoc.To.X)
	assert.Contains(t, stopLoc.Message, "sum_to",
		"expected stack-trace message, got %q", stopLoc.Message)

	// The variables location list must cover the stopped method's locals
	// only: `other` reuses the same names, so any highlight landing
	// there means the C++ grammar scopes were not honored. The
	// std::vector local must render through lldb's standard library
	// formatter rather than as a raw pointer triple.
	h.waitLocation(t, variablesLocationID, 5*time.Second)
	varLoc := h.findLocation(t, variablesLocationID)
	assert.True(t, varLoc.To.X > varLoc.From.X,
		"expected variable name range, got %v..%v", varLoc.From, varLoc.To)
	assert.NotEmpty(t, varLoc.Message,
		"expected variable value as Message")
	assert.Equal(t, textapi.LocationPriorityCritical,
		h.locationPrio(t, variablesLocationID),
		"variables list must be critical priority to sit on top of stopped")
	var seenMsg string
	for _, loc := range h.allLocations(t, variablesLocationID) {
		if loc.From.Y == bpLine-2 && strings.Contains(loc.Message, "size=") {
			seenMsg = loc.Message
		}
	}
	assert.Contains(t, seenMsg, "size=0",
		"expected the empty std::vector local to render with its size")
	otherStart, otherEnd := ccppFunctionLineRange(t, mainPath, "static int other(")
	assertNoVariableIn(t, h, otherStart, otherEnd)

	// Evaluate against the implicit this of the stopped method frame.
	ev, err := h.mgr.Evaluate(ctx, h.sessionID(t), &dap.EvaluateArguments{
		Expression: "limit_ + 1",
		FrameId:    stack[0].Id,
		Context:    "repl",
	})
	require.NoError(t, err)
	assert.Contains(t, ev.Result, "7", "expected limit_ + 1 to evaluate to 7, got %q", ev.Result)

	outPath := h.outputPath(t)
	require.NotEmpty(t, outPath, "output capture file path")
	milestoneStart := h.milestoneCount()
	h.continueUntilExit(t, threadID, 30*time.Second)
	h.waitMilestoneAfter(t, milestoneStart, "debuggee exited", 30*time.Second)
	_, _ = h.run(ctx, subTerminate)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Sum: 21",
		"expected debuggee output in capture file, got %q", string(data))
}

func TestE2E_Cpp_BreakpointOnEmptyLine(t *testing.T) {
	t.Parallel()
	lldbDapBin := findLldbDap(t)
	cxx := findCompiler(t, "c++", "clang++", "g++")
	tmpDir, binPath := setupBuggyCcpp(t, "buggy_cpp", "main.cpp", cxx)
	mainPath := filepath.Join(tmpDir, "main.cpp")

	h := newCcppE2EHarness(t, lldbDapBin, tmpDir, "cpp", "main.cpp")
	defer h.close()
	ctx := h.ctx

	it, err := h.run(ctx, subInitialize, "cpp")
	require.NoError(t, err)
	go h.drainIterator(it)

	_, err = h.run(ctx, subLaunch, binPath)
	require.NoError(t, err)
	h.waitMilestone(t, "initialized", 30*time.Second)

	firstStmt := ccppStmtLine(t, mainPath, "int total = 0;")
	emptyLine := rsBlankLineNear(t, mainPath, firstStmt+1)
	hndl, err := h.runPromptOnHandler(ctx, mainPath,
		term.Coordinates{Y: emptyLine - 1}, subSetBreakpoint)
	require.NoError(t, err)
	require.NotNil(t, hndl.LocationList)
	_, ok := hndl.LocationList.Current()
	require.True(t, ok, "no breakpoint location installed")
}
