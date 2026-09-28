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

package vtescanner

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testDispatcher struct {
	dispatched []any
}

type dispatchedOsc struct {
	params [][]byte
	bell   bool
}

type dispatchedCsi struct {
	params        [][]uint16
	intermediates []byte
	ignore        bool
	action        rune
}

type dispatchedHook struct {
	params        [][]uint16
	intermediates []byte
	ignore        bool
	action        rune
}

type dispatchedEsc struct {
	intermediates []byte
	ignore        bool
	ch            byte
}

type dispatchedPut struct {
	ch byte
}

type dispatchedUnhook struct {
}

type dispatchedPrint struct {
	r rune
}
type dispatchedExecute struct {
	ch byte
}

type dispatchedApc struct {
	data []byte
}

func (d *testDispatcher) Print(r rune) {
	d.dispatched = append(d.dispatched, dispatchedPrint{r: r})
}

func (d *testDispatcher) Execute(ch byte) {
	d.dispatched = append(d.dispatched, dispatchedExecute{ch: ch})
}

func (d *testDispatcher) Hook(params [][]uint16, intermediates []byte, ignore bool, action rune) {
	d.dispatched = append(d.dispatched, dispatchedHook{params, intermediates, ignore, action})
}

func (d *testDispatcher) Put(ch byte) {
	d.dispatched = append(d.dispatched, dispatchedPut{ch: ch})
}

func (d *testDispatcher) Unhook() {
	d.dispatched = append(d.dispatched, dispatchedUnhook{})
}

func (d *testDispatcher) OSCDispatch(params [][]byte, bellTerminated bool) {
	d.dispatched = append(d.dispatched, dispatchedOsc{params, bellTerminated})
}

func (d *testDispatcher) CSIDispatch(params [][]uint16, intermediates []byte, ignore bool, action rune) {
	d.dispatched = append(d.dispatched, dispatchedCsi{params, intermediates, ignore, action})
}

func (d *testDispatcher) ESCDispatch(intermediates []byte, ignore bool, ch byte) {
	d.dispatched = append(d.dispatched, dispatchedEsc{intermediates, ignore, ch})
}

func (d *testDispatcher) APCDispatch(data []byte) {
	d.dispatched = append(d.dispatched, dispatchedApc{append([]byte(nil), data...)})
}

// copyingDispatcher deep-copies the params of every CSI dispatch. The
// scanner owns the params storage and only guarantees it for the
// duration of the call, so a handler that wants them later must copy.
type copyingDispatcher struct {
	testDispatcher
	csiParams [][][]uint16
	hooks     []dispatchedHook
}

func copyScannerParams(params [][]uint16) [][]uint16 {
	record := make([][]uint16, len(params))
	for i, param := range params {
		record[i] = append([]uint16(nil), param...)
	}
	return record
}

func (d *copyingDispatcher) CSIDispatch(
	params [][]uint16, _ []byte, _ bool, _ rune,
) {
	d.csiParams = append(d.csiParams, copyScannerParams(params))
}

func (d *copyingDispatcher) Hook(
	params [][]uint16, intermediates []byte, ignore bool, action rune,
) {
	d.hooks = append(d.hooks, dispatchedHook{
		params:        copyScannerParams(params),
		intermediates: append([]byte(nil), intermediates...),
		ignore:        ignore,
		action:        action,
	})
}

// TestScannerCSIParamsPerDispatch pins that each CSI dispatch observes
// exactly its own parameters. Params storage is reused across
// dispatches, so a stale entry left over from a longer preceding
// sequence would surface here.
func TestScannerCSIParamsPerDispatch(t *testing.T) {
	var d copyingDispatcher
	scanner := NewScanner(&d)

	// A long sequence first, then progressively shorter ones, then
	// subparameters, so every way the reused storage could leak a stale
	// entry is exercised.
	input := "\x1b[1;2;3;4;5;6m" +
		"\x1b[9;8m" +
		"\x1b[7m" +
		"\x1bm" +
		"\x1b[38:2:10:20:30;48;5;9m" +
		"\x1b[11;22;33;44;55;66;77;88m"
	for _, b := range []byte(input) {
		scanner.Advance(b)
	}

	assert.Equal(t, [][][]uint16{
		{{1}, {2}, {3}, {4}, {5}, {6}},
		{{9}, {8}},
		{{7}},
		{{38, 2, 10, 20, 30}, {48}, {5}, {9}},
		{{11}, {22}, {33}, {44}, {55}, {66}, {77}, {88}},
	}, d.csiParams)
}

func TestScannerDCSParamsPerDispatch(t *testing.T) {
	var d copyingDispatcher
	scanner := NewScanner(&d)
	input := "\x1bP1;2:3;4;5$qbody\x1b\\" +
		"\x1bP9$qshort\x1b\\" +
		"\x1bP$qempty\x1b\\"
	for _, b := range []byte(input) {
		scanner.Advance(b)
	}

	assert.Equal(t, []dispatchedHook{
		{
			params:        [][]uint16{{1}, {2, 3}, {4}, {5}},
			intermediates: []byte{'$'},
			action:        'q',
		},
		{
			params:        [][]uint16{{9}},
			intermediates: []byte{'$'},
			action:        'q',
		},
		{
			params:        [][]uint16{{0}},
			intermediates: []byte{'$'},
			action:        'q',
		},
	}, d.hooks)
}

func TestScanner(t *testing.T) {
	t.Run("parse apc", func(t *testing.T) {
		var d testDispatcher
		scanner := NewScanner(&d)
		input := "\x1b_Ga=q,i=31,s=1,v=1;AAAA\x1b\\after"
		for _, ch := range []byte(input) {
			scanner.Advance(ch)
		}

		require.GreaterOrEqual(t, len(d.dispatched), 2)
		apc, ok := d.dispatched[0].(dispatchedApc)
		require.True(t, ok)
		assert.Equal(t, []byte("Ga=q,i=31,s=1,v=1;AAAA"), apc.data)
		// The ST's backslash reaches the driver as an ESC dispatch, and
		// printing resumes afterwards.
		esc, ok := d.dispatched[1].(dispatchedEsc)
		require.True(t, ok)
		assert.Equal(t, byte('\\'), esc.ch)
		assert.Equal(t, dispatchedPrint{r: 'a'}, d.dispatched[2])
	})

	t.Run("apc payload bytes are not printed", func(t *testing.T) {
		var d testDispatcher
		scanner := NewScanner(&d)
		for _, ch := range []byte("\x1b_G;\x1b\\\x1b_\x1b\\") {
			scanner.Advance(ch)
		}
		var apcs []string
		for _, ev := range d.dispatched {
			switch ev := ev.(type) {
			case dispatchedApc:
				apcs = append(apcs, string(ev.data))
			case dispatchedPrint:
				t.Fatalf("unexpected print %q", ev.r)
			}
		}
		assert.Equal(t, []string{"G;", ""}, apcs)
	})

	// kitty terminates an APC string on BEL as well as ST
	// (vt-parser.c:419-441, :466-468). Without it the scanner keeps
	// collecting and swallows every byte until the next ST.
	t.Run("bel terminates an apc string", func(t *testing.T) {
		var d testDispatcher
		scanner := NewScanner(&d)
		for _, ch := range []byte("\x1b_Gi=1;AAAA\x07hi") {
			scanner.Advance(ch)
		}
		var apcs []string
		var printed []rune
		for _, ev := range d.dispatched {
			switch ev := ev.(type) {
			case dispatchedApc:
				apcs = append(apcs, string(ev.data))
			case dispatchedPrint:
				printed = append(printed, ev.r)
			}
		}
		assert.Equal(t, []string{"Gi=1;AAAA"}, apcs)
		assert.Equal(t, []rune("hi"), printed)
	})

	t.Run("sos and pm strings are discarded", func(t *testing.T) {
		var d testDispatcher
		scanner := NewScanner(&d)
		for _, ch := range []byte("\x1bXsos\x1b\\\x1b^pm\x1b\\\x1b_apc\x1b\\") {
			scanner.Advance(ch)
		}
		var apcs [][]byte
		for _, ev := range d.dispatched {
			if ev, ok := ev.(dispatchedApc); ok {
				apcs = append(apcs, ev.data)
			}
		}
		assert.Equal(t, [][]byte{[]byte("apc")}, apcs)
	})

	t.Run("apc longer than the limit is dropped", func(t *testing.T) {
		var d testDispatcher
		scanner := NewScanner(&d)
		scanner.Advance(0x1b)
		scanner.Advance('_')
		for range MaxAPCRaw + 1 {
			scanner.Advance('x')
		}
		scanner.Advance(0x1b)
		scanner.Advance('\\')
		for _, ch := range []byte("\x1b_ok\x1b\\") {
			scanner.Advance(ch)
		}
		var apcs [][]byte
		for _, ev := range d.dispatched {
			if ev, ok := ev.(dispatchedApc); ok {
				apcs = append(apcs, ev.data)
			}
		}
		assert.Equal(t, [][]byte{[]byte("ok")}, apcs)
	})

	t.Run("parse osc", func(t *testing.T) {
		var d testDispatcher
		scanner := NewScanner(&d)
		oscBytes := []byte("\x1b]2;unstablebuild@ernests-mbp.lan: ~/src/go-tui\x07")

		for _, ch := range oscBytes {
			scanner.Advance(ch)
		}

		require.Len(t, d.dispatched, 1)
		osc, ok := d.dispatched[0].(dispatchedOsc)
		require.True(t, ok)
		require.Len(t, osc.params, 2)
		assert.Equal(t, oscBytes[2:3], osc.params[0])
		assert.Equal(t, oscBytes[4:len(oscBytes)-1], osc.params[1])
	})

	t.Run("parse empty osc", func(t *testing.T) {
		var d testDispatcher
		scanner := NewScanner(&d)

		for _, ch := range []byte{0x1b, 0x5d, 0x07} {
			scanner.Advance(ch)
		}

		require.Len(t, d.dispatched, 1)
		osc, ok := d.dispatched[0].(dispatchedOsc)
		require.True(t, ok)
		require.Len(t, osc.params, 1)
		assert.Equal(t, []byte{}, osc.params[0])
	})

	t.Run("parse max osc params", func(t *testing.T) {
		var d testDispatcher
		scanner := NewScanner(&d)
		var buf bytes.Buffer

		buf.WriteString("\x1b]")
		for range MaxOSCParams + 1 {
			buf.WriteByte(';')
		}
		buf.WriteString("\x1b")

		for _, ch := range buf.Bytes() {
			scanner.Advance(ch)
		}

		require.Len(t, d.dispatched, 1)
		osc, ok := d.dispatched[0].(dispatchedOsc)
		require.True(t, ok)
		require.Len(t, osc.params, MaxOSCParams)
		for _, params := range osc.params {
			assert.Equal(t, []byte{}, params)
		}
	})

	t.Run("osc bell terminated", func(t *testing.T) {
		var d testDispatcher
		scanner := NewScanner(&d)
		input := []byte("\x1b]11;ff/00/ff\x07")

		for _, ch := range input {
			scanner.Advance(ch)
		}

		require.Len(t, d.dispatched, 1)
		osc, ok := d.dispatched[0].(dispatchedOsc)
		require.True(t, ok)
		assert.True(t, osc.bell)
	})

	t.Run("osc c0 terminated", func(t *testing.T) {
		var dispatcher testDispatcher
		scanner := NewScanner(&dispatcher)
		input := []byte("\x1b]11;ff/00/ff\x1b\\")

		for _, ch := range input {
			scanner.Advance(ch)
		}

		require.Len(t, dispatcher.dispatched, 2)
		osc, ok := dispatcher.dispatched[0].(dispatchedOsc)
		require.True(t, ok)
		require.False(t, osc.bell)
	})

	t.Run("parse OSC with utf8 arguments", func(t *testing.T) {
		var dispatcher testDispatcher
		scanner := NewScanner(&dispatcher)
		input := []byte("\r\x1b]2;echo '\xaf\\_(`ツ`)_/\xaf' && sleep 1\x07")

		for _, ch := range input {
			scanner.Advance(ch)
		}

		require.Len(t, dispatcher.dispatched, 2)
		_, ok := dispatcher.dispatched[0].(dispatchedExecute)
		require.True(t, ok)
		osc, ok := dispatcher.dispatched[1].(dispatchedOsc)
		require.True(t, ok)
		require.Equal(t, []byte{'2'}, osc.params[0])
		require.Equal(t, input[5:len(input)-1], osc.params[1])
	})

	t.Run("osc containting string terminator", func(t *testing.T) {
		var dispatcher testDispatcher
		scanner := NewScanner(&dispatcher)
		input := []byte("\x1b]2;\xe6\x9c\xab\x1b\\")

		for _, ch := range input {
			scanner.Advance(ch)
		}

		require.Len(t, dispatcher.dispatched, 2)
		osc, ok := dispatcher.dispatched[0].(dispatchedOsc)
		require.True(t, ok)
		require.Equal(t, input[4:len(input)-2], osc.params[1])
	})

	t.Run("exceeds max buffer size", func(t *testing.T) {
		NUM_BYTES := MaxOSCRaw + 100
		input_START := []byte("\x1b]52;s")
		input_END := []byte("\x07")

		var d testDispatcher
		scanner := NewScanner(&d)

		for _, b := range input_START {
			scanner.Advance(b)
		}

		for range NUM_BYTES {
			scanner.Advance('a')
		}

		for _, b := range input_END {
			scanner.Advance(b)
		}

		require.Len(t, d.dispatched, 1)
		osc, ok := d.dispatched[0].(dispatchedOsc)
		require.True(t, ok)
		require.Len(t, osc.params, 2)
		assert.Equal(t, []byte("52"), osc.params[0])
		assert.Equal(t, NUM_BYTES+len(input_END), len(osc.params[1]))
	})

	t.Run("parse CSI max params", func(t *testing.T) {
		var buf bytes.Buffer
		buf.WriteString("\x1b[")
		buf.WriteString(strings.Repeat("1;", MaxParams-1))
		buf.WriteString("p")

		var d testDispatcher
		scanner := NewScanner(&d)

		for _, b := range buf.Bytes() {
			scanner.Advance(b)
		}

		require.Len(t, d.dispatched, 1)
		csi, ok := d.dispatched[0].(dispatchedCsi)
		require.True(t, ok)
		assert.Equal(t, len(csi.params), MaxParams)
		assert.False(t, csi.ignore)
	})

	t.Run("parse CSI params ignore long params", func(t *testing.T) {
		params := strings.Repeat("1;", MaxParams)
		input := fmt.Appendf(nil, "\x1b[%vp", params)

		var d testDispatcher
		scanner := NewScanner(&d)

		for _, b := range input {
			scanner.Advance(b)
		}

		require.Len(t, d.dispatched, 1)
		csi, ok := d.dispatched[0].(dispatchedCsi)
		require.True(t, ok)
		assert.Equal(t, len(csi.params), MaxParams)
		assert.True(t, csi.ignore)
	})

	t.Run("parse CSI params trailing semicolon", func(t *testing.T) {
		input := []byte("\x1b[4;m")
		var d testDispatcher
		scanner := NewScanner(&d)

		for _, b := range input {
			scanner.Advance(b)
		}

		require.Len(t, d.dispatched, 1)
		csi, ok := d.dispatched[0].(dispatchedCsi)
		require.True(t, ok)
		assert.Equal(t, csi.params, [][]uint16{{4}, {0}})
	})

	t.Run("parse CSI params leading semicolon", func(t *testing.T) {
		input := []byte("\x1b[;4m")
		var d testDispatcher
		scanner := NewScanner(&d)

		for _, b := range input {
			scanner.Advance(b)
		}

		require.Len(t, d.dispatched, 1)
		csi, ok := d.dispatched[0].(dispatchedCsi)
		require.True(t, ok)
		assert.Equal(t, csi.params, [][]uint16{{0}, {4}})
	})

	t.Run("parse long CSI param", func(t *testing.T) {
		input := []byte("\x1b[9223372036854775808m")
		var d testDispatcher
		scanner := NewScanner(&d)

		for _, b := range input {
			scanner.Advance(b)
		}

		require.Len(t, d.dispatched, 1)
		csi, ok := d.dispatched[0].(dispatchedCsi)
		require.True(t, ok)
		assert.Equal(t, csi.params, [][]uint16{{math.MaxUint16}})
	})

	t.Run("CSI reset", func(t *testing.T) {
		input := []byte("\x1b[3;1\x1b[?1049h")
		var d testDispatcher
		scanner := NewScanner(&d)

		for _, b := range input {
			scanner.Advance(b)
		}

		require.Len(t, d.dispatched, 1)
		csi, ok := d.dispatched[0].(dispatchedCsi)
		require.True(t, ok)
		assert.Equal(t, []byte{'?'}, csi.intermediates)
		assert.Equal(t, [][]uint16{{1049}}, csi.params)
		assert.False(t, csi.ignore)
	})

	t.Run("CSI subparameters", func(t *testing.T) {
		input := []byte("\x1b[38:2:255:0:255;1m")
		var d testDispatcher
		scanner := NewScanner(&d)

		for _, b := range input {
			scanner.Advance(b)
		}

		require.Len(t, d.dispatched, 1)
		csi, ok := d.dispatched[0].(dispatchedCsi)
		require.True(t, ok)
		assert.Equal(t, [][]uint16{{38, 2, 255, 0, 255}, {1}}, csi.params)
		assert.Empty(t, csi.intermediates)
		assert.False(t, csi.ignore)
	})

	t.Run("parse DCS max params", func(t *testing.T) {
		var buf bytes.Buffer
		buf.WriteString("\x1bP")
		buf.WriteString(strings.Repeat("1;", MaxParams+1))
		buf.WriteString("p")
		var d testDispatcher
		scanner := NewScanner(&d)

		for _, b := range buf.Bytes() {
			scanner.Advance(b)
		}

		require.Len(t, d.dispatched, 1)
		dcs, ok := d.dispatched[0].(dispatchedHook)
		require.True(t, ok)
		assert.Equal(t, len(dcs.params), MaxParams)
		for i, param := range dcs.params {
			assert.Equal(t, []uint16{1}, param, i)
		}
		assert.True(t, dcs.ignore)
	})

	t.Run("dcs reset", func(t *testing.T) {
		var d testDispatcher
		scanner := NewScanner(&d)

		input := []byte("\x1b[3;1\x1bP1$tx\x9c")
		for _, b := range input {
			scanner.Advance(b)
		}

		require.Len(t, d.dispatched, 3)

		dcsHook, ok := d.dispatched[0].(dispatchedHook)
		require.True(t, ok)
		assert.Equal(t, []byte{'$'}, dcsHook.intermediates)
		assert.Equal(t, [][]uint16{{1}}, dcsHook.params)
		assert.False(t, dcsHook.ignore)

		assert.Equal(t, dispatchedPut{'x'}, d.dispatched[1])
		assert.Equal(t, dispatchedUnhook{}, d.dispatched[2])
	})

	t.Run("parse dcs", func(t *testing.T) {
		var d testDispatcher
		scanner := NewScanner(&d)

		input := []byte("\x1bP0;1|17/ab\x9c")
		for _, b := range input {
			scanner.Advance(b)
		}

		require.Len(t, d.dispatched, 7)

		dcsHook, ok := d.dispatched[0].(dispatchedHook)
		require.True(t, ok)
		assert.Equal(t, [][]uint16{{0}, {1}}, dcsHook.params)
		assert.Equal(t, '|', dcsHook.action)

		for i, b := range []byte("17/ab") {
			assert.Equal(t, dispatchedPut{ch: b}, d.dispatched[1+i])
		}

		assert.Equal(t, dispatchedUnhook{}, d.dispatched[6])
	})

	t.Run("intermediate reset on dcs exit", func(t *testing.T) {
		var d testDispatcher
		scanner := NewScanner(&d)

		input := []byte("\x1bP=1sZZZ\x1b+\x5c")
		for _, b := range input {
			scanner.Advance(b)
		}

		require.Len(t, d.dispatched, 6)

		esc, ok := d.dispatched[5].(dispatchedEsc)
		require.True(t, ok)
		assert.Equal(t, []byte{'+'}, esc.intermediates)
	})

	t.Run("esc reset", func(t *testing.T) {
		var d testDispatcher
		scanner := NewScanner(&d)

		input := []byte("\x1b[3;1\x1b(A")
		for _, b := range input {
			scanner.Advance(b)
		}

		require.Len(t, d.dispatched, 1)

		esc, ok := d.dispatched[0].(dispatchedEsc)
		require.True(t, ok)
		assert.Equal(t, []byte{'('}, esc.intermediates)
		assert.Equal(t, byte('A'), esc.ch)
		assert.False(t, esc.ignore)
	})

	t.Run("params buffer filled with subparam", func(t *testing.T) {
		var d testDispatcher
		scanner := NewScanner(&d)

		input := []byte("\x1b[::::::::::::::::::::::::::::::::x\x1b")
		for _, b := range input {
			scanner.Advance(b)
		}

		require.Len(t, d.dispatched, 1)

		csi, ok := d.dispatched[0].(dispatchedCsi)
		require.True(t, ok)
		assert.Empty(t, csi.intermediates)
		assert.Equal(t, [][]uint16{{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
			0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}}, csi.params)
		assert.Equal(t, rune('x'), csi.action)
		assert.True(t, csi.ignore)
	})

	t.Run("csi attr dispatch", func(t *testing.T) {
		var d testDispatcher
		scanner := NewScanner(&d)

		input := []byte{
			0x1b, '[', '3', '8', ';', '2', ';', '1', '2', '8', ';', '6', '6', ';',
			'2', '5', '5', 'm',
		}
		for _, b := range input {
			scanner.Advance(b)
		}

		require.Len(t, d.dispatched, 1)
		csi, ok := d.dispatched[0].(dispatchedCsi)
		require.True(t, ok)
		assert.Equal(t, [][]uint16{{38}, {2}, {128}, {66}, {255}}, csi.params)
		assert.Empty(t, csi.intermediates)
		assert.False(t, csi.ignore)
		assert.Equal(t, 'm', csi.action)
	})
}
