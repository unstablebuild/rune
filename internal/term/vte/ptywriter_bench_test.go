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

//go:build !windows

package vte

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/unstablebuild/pty"
	"golang.org/x/sys/unix"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte/vteparser"
)

// BenchmarkPtyWriterBlocked reproduces what vtebench actually measures:
// how long a program writing to the pty blocks, not how long the
// terminal takes to finish parsing. The gather ring holds only
// gatherBatchCount*gatherBatchSize bytes, so a payload larger than that
// makes the writer wait on the parse stage and the number tracks Rune's
// drain rate end to end, including gather and per-batch wakeups.
func BenchmarkPtyWriterBlocked(b *testing.B) {
	const height = 24

	cases := []struct {
		name  string
		width int
		setup func(width, height int) []byte
		make  func(width, height int) []byte
	}{
		{"scroll_short", 80, nil, func(int, int) []byte {
			return buildLinefeedPayload(scrollWorkloadLines)
		}},
		{"scroll_short", 240, nil, func(int, int) []byte {
			return buildLinefeedPayload(scrollWorkloadLines)
		}},
		{"scroll_alt_region", 240, func(_, height int) []byte {
			return fmt.Appendf(nil, "\x1b[?1049h\x1b[1;%dr", height-1)
		}, func(int, int) []byte {
			return buildLinefeedPayload(scrollWorkloadLines)
		}},
		{"dense_cells", 80, nil, buildDenseCellsPayload},
		{"sync_cells", 80, nil, func(w, h int) []byte {
			return buildSyncFramesPayload(w, h, true)
		}},
		{"unicode", 80, nil, buildWideUnicodePayload},
	}

	for _, tc := range cases {
		b.Run(fmt.Sprintf("%s/w%d", tc.name, tc.width), func(b *testing.B) {
			payload := tc.make(tc.width, height)
			var setup []byte
			if tc.setup != nil {
				setup = tc.setup(tc.width, height)
			}

			master, tty, err := pty.Open()
			if err != nil {
				b.Skipf("pty unavailable: %v", err)
			}
			defer master.Close()
			defer tty.Close()
			_ = unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ,
				&unix.Winsize{Row: uint16(height), Col: uint16(tc.width)})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			g, ok := newPtyGather(ctx, master)
			if !ok {
				b.Skip("gather stage unavailable")
			}

			ph := newBenchParserHandler(tc.width, height)
			parser := vteparser.NewParser(ph, new(vteparser.StdTimeout))
			done := make(chan struct{})
			go debug.CapturePanicReport(func() {
				defer close(done)
				for batch := range g.ready {
					parser.AdvanceBytes(batch)
					g.release(batch)
				}
			})

			if len(setup) > 0 {
				_, _ = tty.Write(setup)
			}
			// Warm up so the scrollback and row pool reach steady state.
			_, _ = tty.Write(payload)
			time.Sleep(50 * time.Millisecond)

			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for range b.N {
				_, _ = tty.Write(payload)
			}
			b.StopTimer()

			cancel()
			<-done
		})
	}
}
