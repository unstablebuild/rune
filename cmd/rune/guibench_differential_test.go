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

package main

import (
	"crypto/sha256"
	"maps"
	"testing"
	"time"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// differentialFrames is the number of scripted frames each scenario is
// driven for in the correctness harness. It is small enough to keep the
// test fast but large enough to exercise multi-frame damage evolution
// (cursor moves, scroll direction reversals, theme cycling).
const (
	differentialFrames = 48
	differentialWidth  = 960
	differentialHeight = 540
)

func TestGUIBenchSettleWaitsForInterruptRenders(t *testing.T) {
	if testing.Short() {
		t.Skip("GUI bench session is not short-mode friendly")
	}
	s := newGUIBenchSession(t, guiBenchConfig{
		pixelsW:           differentialWidth,
		pixelsH:           differentialHeight,
		disableAnimations: true,
	})
	defer s.close()

	const interruptFrames = 40
	processed := 0
	var schedule func()
	schedule = func() {
		processed++
		if processed < interruptFrames {
			s.publish(term.Event{Type: term.EventInterrupt, UserFunc: schedule})
		}
	}
	s.publish(term.Event{Type: term.EventInterrupt, UserFunc: schedule})

	s.settle(60 * time.Second)
	if processed != interruptFrames {
		t.Fatalf("settle returned after %d interrupt renders, want %d",
			processed, interruptFrames)
	}
}

// collectFrameHashes renders each scripted state through both repaint paths.
func collectFrameHashes(
	t *testing.T, s *guiBenchSession, sc guiBenchScenario,
) (optimized, reference [][sha256.Size]byte) {
	t.Helper()
	optimized = make([][sha256.Size]byte, 0, differentialFrames)
	reference = make([][sha256.Size]byte, 0, differentialFrames)
	for i := range differentialFrames {
		if sc.step != nil {
			sc.step(s, i)
		}
		optimizedHash, referenceHash := s.frameHashes()
		optimized = append(optimized, optimizedHash)
		reference = append(reference, referenceHash)
	}
	return optimized, reference
}

// differentialScenarios is the subset of the battery used for pixel
// differential testing: every scenario that reaches a quiescent state
// after settle (no free-running animation), so the reference and
// optimized runs render byte-identical grids frame for frame. The
// shader-animation scenario is intentionally excluded because it never
// settles and its content is wall-clock dependent.
func differentialScenarios() []guiBenchScenario {
	return []guiBenchScenario{
		idleScenario(),
		cursorMoveScenario(),
		scrollScenario(),
		selectionDragScenario(),
		invalidateBurstScenario(),
		unicodeStressScenario(),
		worstCaseScenario(),
		typingScenario(),
		splitsScenario(),
	}
}

func differentialConfig(transparent bool, scenarios []guiBenchScenario) guiBenchConfig {
	cfg := guiBenchConfig{
		pixelsW:           differentialWidth,
		pixelsH:           differentialHeight,
		transparent:       transparent,
		disableAnimations: true,
		workspaceFiles:    make(map[string]string),
	}
	languages := make(map[string]struct{})
	for _, sc := range scenarios {
		cfg.files = append(cfg.files, sc.files...)
		maps.Copy(cfg.workspaceFiles, sc.workspaceFiles)
		for _, lang := range sc.syntaxLangs {
			if _, ok := languages[lang]; ok {
				continue
			}
			languages[lang] = struct{}{}
			cfg.syntaxLangs = append(cfg.syntaxLangs, lang)
		}
	}
	return cfg
}

func TestGUIDamageDifferential(t *testing.T) {
	if testing.Short() {
		t.Skip("differential harness is not short-mode friendly")
	}
	scenarios := differentialScenarios()
	for _, transparent := range []bool{false, true} {
		variant := "opaque"
		if transparent {
			variant = "transparent"
		}
		t.Run(variant, func(t *testing.T) {
			s := newGUIBenchSession(t, differentialConfig(transparent, scenarios))
			defer s.close()
			s.settle(60 * time.Second)
			for _, sc := range scenarios {
				t.Run(sc.name, func(t *testing.T) {
					s.tb = t
					s.publish(term.Event{Type: term.EventKey, Key: term.KeyEsc})
					s.frame()
					s.prepareScenario(sc)
					optHashes, refHashes := collectFrameHashes(t, s, sc)
					if len(refHashes) != len(optHashes) {
						t.Fatalf("frame count mismatch: reference %d, optimized %d",
							len(refHashes), len(optHashes))
					}
					for i := range refHashes {
						if refHashes[i] != optHashes[i] {
							t.Fatalf("frame %d differs: damage-tracked render does not "+
								"match full-repaint reference (reference %x, optimized %x)",
								i, refHashes[i][:8], optHashes[i][:8])
						}
					}
				})
			}
		})
	}
}
