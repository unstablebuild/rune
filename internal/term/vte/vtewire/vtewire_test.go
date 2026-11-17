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

package vtewire

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"math"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte/vtesnap"
)

// snapshotDigest pins the encoding of the snapshot goldenSnapshot
// writes. A server sends a snapshot right after HELLO_OK, so a client
// and a server that agree on Version must agree on it as much as on
// the frames themselves.
const snapshotDigest = "7fb38fc337fd06363545d2090cc1f45dca303649d776beb7cf1706b1df918656"

func TestSnapshotEncodingIsPinned(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	require.NoError(t, goldenSnapshot(&buf))
	sum := sha256.Sum256(buf.Bytes())
	assert.Equal(t, snapshotDigest, hex.EncodeToString(sum[:]),
		"the snapshot encoding changed: bump Version so that a server and a "+
			"client that cannot decode each other's snapshots part at HELLO, "+
			"then update snapshotDigest")
}

// goldenSnapshot writes a small terminal that sets a value in every
// field a snapshot carries.
func goldenSnapshot(w io.Writer) error {
	cursor := vtesnap.Cursor{
		Position: term.Coordinates{X: 1, Y: 1},
		Attributes: term.Attributes{
			Fg: term.ColorRed, Bg: term.NewRGBColor(1, 2, 3),
			Attrs: term.AttrBold, Underline: term.ColorBlue,
		},
		Charsets: []vtesnap.Charset{{Index: 1, Standard: 2}},
	}
	rows := [][]term.Cell{
		{
			{Ch: 'a', Width: 1, Bytes: 1},
			{Ch: '世', Width: 2, Bytes: 3, Fg: term.NewRGBColor(4, 5, 6)},
			{},
			{
				Ch: 'e', Width: 1, Bytes: 1, Attrs: term.AttrUnderline,
				Extra: &term.CellExtra{
					Combining: []rune{'\u0301'}, Underline: term.ColorGreen,
				},
			},
		},
		{
			{Ch: ' ', Width: 1, Bytes: 1, Bg: term.PaletteColor(200),
				Attrs: term.AttrItalic | term.AttrReverse},
			{Ch: 'z', Width: 1, Bytes: 1, Attrs: term.AttrDim | term.AttrStrikeThrough},
			{Ch: 'z', Width: 1, Bytes: 1, Attrs: term.AttrBlink},
			{Ch: 'z', Width: 1, Bytes: 1},
		},
	}
	e, err := vtesnap.NewEncoder(w)
	if err != nil {
		return err
	}
	e.SetPageRows(1)
	if err := e.Terminal(vtesnap.Terminal{
		Width: 4, Height: 2,
		Title: "golden", TitleStack: []string{"outer"},
		Tabstops: []bool{true, false, false, true},
		Modes: vtesnap.Modes(0).
			Set(vtesnap.ModeShowCursor, true).
			Set(vtesnap.ModeWrap, true).
			Set(vtesnap.ModeBracketedPaste, true).
			Set(vtesnap.ModeReverseScreen, true),
		CursorStyle:     term.CursorStyleSteadyBar,
		CursorHidden:    true,
		PendingWrap:     true,
		CurrentCharset:  1,
		UseAlternate:    true,
		UsedAlternate:   true,
		MaxScrollback:   1000,
		MinWidth:        2,
		KittyKeyboard:   [2][]uint8{{1}, {1, 3}},
		ModifyOtherKeys: 2,
		CellPixelWidth:  9, CellPixelHeight: 18,
	}); err != nil {
		return err
	}
	if err := e.Screen(vtesnap.Screen{
		Kind: vtesnap.ScreenPrimary, Width: 4, Height: 2,
		BottomScrollableRegion: 1,
		Cursor:                 cursor,
		SavedCursor:            vtesnap.Cursor{Hidden: true},
	}, rows); err != nil {
		return err
	}
	if err := e.Screen(vtesnap.Screen{
		Kind: vtesnap.ScreenAlternate, Width: 4, Height: 2,
		TopScrollableRegion: 1, BottomScrollableRegion: 1,
		Cursor: cursor, SavedCursor: cursor,
	}, rows[1:]); err != nil {
		return err
	}
	if err := e.Continuation([]byte("\x1b[1")); err != nil {
		return err
	}
	return e.Ready()
}

func TestFrameRoundTrip(t *testing.T) {
	t.Parallel()

	frames := []Frame{
		&Hello{Version: Version, VTEID: "abc123", Width: 80, Height: 24},
		&Hello{
			Version: Version, VTEID: "abc123", Width: 80, Height: 24,
			CellPixelWidth: 9, CellPixelHeight: 18,
		},
		&Hello{Version: Version, Root: "/home/me/src/rune", VTEID: "abc123"},
		&Hello{Version: Version, VTEID: "abc123", Preview: true, Width: 80, Height: 24},
		&Hello{
			Version: Version,
			Root:    "~/workspace",
			Width:   120,
			Height:  40,
			Create: CreateOptions{
				CommandAndArgs: []string{"bash", "-lc", "echo hi"},
			},
		},
		&HelloOK{
			VTEID: "abc123", URI: "file:///dev/ttys004",
			Width: 80, Height: 24, Complete: true,
		},
		&HelloErr{Code: CodeNotFound, Message: "no such emulator"},
		&HelloErr{Code: CodeUnknown},
		&Snapshot{Records: []byte{0x00, 0xff, 0x10}},
		&Snapshot{},
		&Output{Data: []byte("\x1b[31mred\x1b[0m")},
		&Output{},
		&HistoryPage{Remaining: 137, Page: []byte("page bytes")},
		&Input{Data: []byte("ls -la\r")},
		&Resize{Width: 100, Height: 30},
		&Resize{Width: 100, Height: 30, CellPixelWidth: 9, CellPixelHeight: 18},
		&Resize{},
		&GraphicsMedium{OK: true, Data: []byte("\x89PNG")},
		&GraphicsMedium{OK: true},
		&GraphicsMedium{},
		&Focus{Focused: true},
		&Focus{},
		&Detach{},
		&Exit{Message: "read /dev/ptmx: input/output error"},
		&Exit{Err: "exit status 3"},
		&Exit{},
		&Dropped{Code: CodeSlow, Message: "4 MiB behind"},
		&Dropped{},
	}

	var buf bytes.Buffer
	conn := NewConn(readWriter{&buf, &buf})
	for _, f := range frames {
		require.NoError(t, conn.WriteFrame(f))
	}

	reader := NewConn(readWriter{bytes.NewReader(buf.Bytes()), io.Discard})
	for _, want := range frames {
		got, err := reader.ReadFrame()
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	_, err := reader.ReadFrame()
	assert.ErrorIs(t, err, io.EOF)
}

func TestReadFrameRejectsCorruption(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	conn := NewConn(readWriter{&buf, &buf})
	require.NoError(t, conn.WriteFrame(&Resize{Width: 10, Height: 20}))
	blob := buf.Bytes()

	t.Run("truncation", func(t *testing.T) {
		t.Parallel()
		for n := range len(blob) {
			r := NewConn(readWriter{bytes.NewReader(blob[:n]), io.Discard})
			_, err := r.ReadFrame()
			assert.Error(t, err, "prefix of %d bytes decoded", n)
		}
	})

	t.Run("unknown type", func(t *testing.T) {
		t.Parallel()
		corrupt := bytes.Clone(blob)
		corrupt[0] = 200
		r := NewConn(readWriter{bytes.NewReader(corrupt), io.Discard})
		_, err := r.ReadFrame()
		assert.ErrorIs(t, err, ErrUnknownFrame)
	})

	t.Run("oversized length", func(t *testing.T) {
		t.Parallel()
		corrupt := bytes.Clone(blob)
		binary.LittleEndian.PutUint32(corrupt[1:], MaxFrameLen+1)
		r := NewConn(readWriter{bytes.NewReader(corrupt), io.Discard})
		_, err := r.ReadFrame()
		assert.ErrorIs(t, err, ErrFrameTooLarge)
	})

	t.Run("short payload", func(t *testing.T) {
		t.Parallel()
		var short bytes.Buffer
		short.WriteByte(byte(TypeResize))
		_ = binary.Write(&short, binary.LittleEndian, uint32(4))
		short.Write([]byte{1, 2, 3, 4})
		r := NewConn(readWriter{bytes.NewReader(short.Bytes()), io.Discard})
		_, err := r.ReadFrame()
		assert.ErrorIs(t, err, ErrMalformed)
	})

	t.Run("trailing payload", func(t *testing.T) {
		t.Parallel()
		var long bytes.Buffer
		long.WriteByte(byte(TypeFocus))
		_ = binary.Write(&long, binary.LittleEndian, uint32(4))
		long.Write([]byte{1, 0, 0, 0})
		r := NewConn(readWriter{bytes.NewReader(long.Bytes()), io.Discard})
		_, err := r.ReadFrame()
		assert.ErrorIs(t, err, ErrMalformed)
	})
}

// TestSenderPreservesSubmissionOrder pins the invariant every replica
// depends on: frames leave in exactly the order they were submitted,
// even though the writes happen on another goroutine.
func TestSenderPreservesSubmissionOrder(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	sender := NewSender(NewConn(server), 0)
	done := make(chan error, 1)
	go debug.CapturePanicReport(func() { done <- sender.Run() })

	const count = 500
	go debug.CapturePanicReport(func() {
		for i := range count {
			switch i % 3 {
			case 0:
				_ = sender.Send(&Output{Data: []byte{byte(i)}})
			case 1:
				_ = sender.Send(&Resize{Width: i, Height: i})
			default:
				_ = sender.Send(&Focus{Focused: i%2 == 0})
			}
		}
		sender.Close()
	})

	reader := NewConn(client)
	for i := range count {
		frame, err := reader.ReadFrame()
		require.NoError(t, err)
		switch i % 3 {
		case 0:
			assert.Equal(t, &Output{Data: []byte{byte(i)}}, frame)
		case 1:
			assert.Equal(t, &Resize{Width: i, Height: i}, frame)
		default:
			assert.Equal(t, &Focus{Focused: i%2 == 0}, frame)
		}
	}
	require.NoError(t, <-done)
}

// TestSenderDropsSlowClient pins the backpressure policy: a client
// that stops reading is dropped rather than allowed to stall the
// emulator or silently miss frames.
func TestSenderDropsSlowClient(t *testing.T) {
	t.Parallel()

	// A writer parked inside one write is what a stalled client looks
	// like from here: the queue is the only thing absorbing sends. It
	// is gated rather than merely unbuffered so the drop is observed
	// on the path that finishes its write and finds the attachment
	// already over the bound, instead of on a broken socket.
	w := newGatedWriter()
	sender := NewSender(NewConn(readWriter{bytes.NewReader(nil), w}), 4096)
	done := make(chan error, 1)
	go debug.CapturePanicReport(func() { done <- sender.Run() })

	payload := make([]byte, 512)
	require.NoError(t, sender.Send(&Output{Data: payload}))
	select {
	case <-w.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the sender never started writing")
	}

	var sendErr error
	for range 100 {
		if sendErr = sender.Send(&Output{Data: payload}); sendErr != nil {
			break
		}
	}
	require.ErrorIs(t, sendErr, ErrSlowClient)

	// Once dropped, the attachment stays dropped.
	assert.ErrorIs(t, sender.Send(&Output{Data: payload}), ErrSlowClient)

	w.releaseAll()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, ErrSlowClient)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not report the dropped attachment")
	}
}

func TestSenderRejectsSendAfterClose(t *testing.T) {
	t.Parallel()

	var buf syncBuffer
	sender := NewSender(NewConn(readWriter{&buf, &buf}), 0)
	done := make(chan error, 1)
	go debug.CapturePanicReport(func() { done <- sender.Run() })

	require.NoError(t, sender.Send(&Exit{Message: "bye"}))
	sender.Close()
	require.NoError(t, <-done)

	assert.ErrorIs(t, sender.Send(&Output{}), ErrSenderClosed)

	// The frame queued before Close still reached the wire.
	r := NewConn(readWriter{bytes.NewReader(buf.Bytes()), io.Discard})
	frame, err := r.ReadFrame()
	require.NoError(t, err)
	assert.Equal(t, &Exit{Message: "bye"}, frame)
}

// TestSenderQueueAdmission pins the queue bound frame by frame, with
// no writer draining, so each case states exactly which submission the
// policy refuses. The bound exists to notice a client that stopped
// reading, which is a different thing from a client that was simply
// sent something big.
func TestSenderQueueAdmission(t *testing.T) {
	t.Parallel()

	const bound = 1024
	frame := func(n int) Frame { return &Output{Data: make([]byte, n)} }

	tests := []struct {
		desc string
		send []Frame
		// wantErr is the error expected from the send at the same
		// index; nil means the frame must be admitted.
		wantErr []error
	}{
		{
			desc:    "frames within the bound are admitted",
			send:    []Frame{frame(200), frame(200), frame(200)},
			wantErr: []error{nil, nil, nil},
		},
		{
			desc:    "the frame that would exceed the bound is refused",
			send:    []Frame{frame(600), frame(600)},
			wantErr: []error{nil, ErrSlowClient},
		},
		{
			desc:    "a frame larger than the whole bound still goes out",
			send:    []Frame{frame(bound * 4)},
			wantErr: []error{nil},
		},
		{
			// A snapshot bigger than the bound is normal for a large
			// screen. Dropping the output queued behind it would send
			// the client back for a snapshot just as large.
			desc:    "frames queued behind an oversized frame are admitted",
			send:    []Frame{frame(bound * 4), frame(10), frame(10)},
			wantErr: []error{nil, nil, nil},
		},
		{
			// The exemption is granted per oversized frame reaching an
			// empty queue. A second one arriving before the first has
			// drained is a client that genuinely stopped reading.
			desc:    "a second oversized frame before the first drains is refused",
			send:    []Frame{frame(bound * 4), frame(bound * 4), frame(10)},
			wantErr: []error{nil, ErrSlowClient, ErrSlowClient},
		},
		{
			desc:    "overflow latches for every later send",
			send:    []Frame{frame(900), frame(900), frame(1), frame(1)},
			wantErr: []error{nil, ErrSlowClient, ErrSlowClient, ErrSlowClient},
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			require.Len(t, tc.wantErr, len(tc.send), "malformed case")
			s := NewSender(NewConn(readWriter{
				bytes.NewReader(nil), io.Discard,
			}), bound)
			for i, f := range tc.send {
				err := s.Send(f)
				if tc.wantErr[i] == nil {
					assert.NoError(t, err, "send %d", i)
					continue
				}
				assert.ErrorIs(t, err, tc.wantErr[i], "send %d", i)
			}
		})
	}
}

// TestSenderAllowanceEndsWithTheOversizedFrame pins that the exemption
// granted to a big snapshot is spent once that snapshot is written: a
// client that stops reading afterwards is still dropped.
func TestSenderAllowanceEndsWithTheOversizedFrame(t *testing.T) {
	t.Parallel()

	const bound = 1024
	var buf syncBuffer
	s := NewSender(NewConn(readWriter{&buf, &buf}), bound)
	done := make(chan error, 1)
	go debug.CapturePanicReport(func() { done <- s.Run() })

	require.NoError(t, s.Send(&Output{Data: make([]byte, bound*4)}))
	require.Eventually(t, func() bool {
		return len(buf.Bytes()) >= bound*4
	}, 5*time.Second, time.Millisecond, "oversized frame never drained")

	// Back to the ordinary bound now that the queue emptied.
	var sendErr error
	for range 100 {
		if sendErr = s.Send(&Output{Data: make([]byte, bound)}); sendErr != nil {
			break
		}
	}
	assert.ErrorIs(t, sendErr, ErrSlowClient)
	assert.ErrorIs(t, <-done, ErrSlowClient)
}

// TestReadFrameBuffersAreReused documents the aliasing contract: a
// frame's bytes belong to the reader until the next read.
func TestReadFrameBuffersAreReused(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	conn := NewConn(readWriter{&buf, &buf})
	require.NoError(t, conn.WriteFrame(&Output{Data: []byte("first")}))
	require.NoError(t, conn.WriteFrame(&Output{Data: []byte("2nd__")}))

	r := NewConn(readWriter{bytes.NewReader(buf.Bytes()), io.Discard})
	first, err := r.ReadFrame()
	require.NoError(t, err)
	aliased := first.(*Output).Data

	_, err = r.ReadFrame()
	require.NoError(t, err)
	assert.Equal(t, "2nd__", string(aliased),
		"payloads alias the read buffer and must be copied to be retained")
}

// A snapshot frame can be tens of megabytes; an attachment that read
// one must not keep that much memory for the rest of its life.
func TestReadFrameDoesNotRetainLargeBuffers(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	conn := NewConn(readWriter{&buf, &buf})
	large := bytes.Repeat([]byte("x"), 4*maxRetainedReadBuf)
	require.NoError(t, conn.WriteFrame(&Output{Data: large}))
	require.NoError(t, conn.WriteFrame(&Output{Data: []byte("small")}))

	r := NewConn(readWriter{bytes.NewReader(buf.Bytes()), io.Discard})
	frame, err := r.ReadFrame()
	require.NoError(t, err)
	assert.Equal(t, large, frame.(*Output).Data)
	frame, err = r.ReadFrame()
	require.NoError(t, err)
	assert.Equal(t, "small", string(frame.(*Output).Data))
	assert.LessOrEqual(t, cap(r.readBuf), maxRetainedReadBuf)
}

type readWriter struct {
	io.Reader
	io.Writer
}

func (readWriter) Close() error { return nil }

// syncBuffer is a bytes.Buffer safe for the sender goroutine to write
// while the test reads it after Run returns.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Read(p)
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}

// gatedWriter parks its first write until released, standing in for a
// client that has stopped reading its socket.
type gatedWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedWriter() *gatedWriter {
	return &gatedWriter{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func (w *gatedWriter) releaseAll() { close(w.release) }

// TestFrameRoundTripEdgeValues pins what survives the wire and what the
// layout normalizes away, so a caller knows which differences it may
// not rely on. Dimensions are signed on purpose: a nonsense size must
// arrive as the nonsense it is, for the receiver to reject knowingly,
// rather than as a huge unsigned one.
func TestFrameRoundTripEdgeValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc string
		in   Frame
		want Frame // nil means "identical to in"
	}{
		{
			desc: "negative dimensions survive as negative",
			in:   &Resize{Width: -1, Height: -32768},
		},
		{
			desc: "int32 extremes round trip",
			in:   &Resize{Width: math.MaxInt32, Height: math.MinInt32},
		},
		{
			desc: "dimensions beyond int32 are truncated",
			in:   &Resize{Width: math.MaxInt32 + 1, Height: 1 << 40},
			want: &Resize{Width: math.MinInt32, Height: 0},
		},
		{
			desc: "empty payload decodes as nil, not empty slice",
			in:   &Output{Data: []byte{}},
			want: &Output{},
		},
		{
			desc: "empty input payload decodes as nil",
			in:   &Input{Data: []byte{}},
			want: &Input{},
		},
		{
			desc: "empty snapshot decodes as nil",
			in:   &Snapshot{Records: []byte{}},
			want: &Snapshot{},
		},
		{
			desc: "empty argv decodes as nil",
			in: &Hello{
				Version: Version,
				Create:  CreateOptions{CommandAndArgs: []string{}},
			},
			want: &Hello{Version: Version},
		},
		{
			desc: "argv keeps empty strings as distinct elements",
			in: &Hello{
				Version: Version,
				Create:  CreateOptions{CommandAndArgs: []string{"", "", "x"}},
			},
		},
		{
			desc: "output carries arbitrary binary, NUL included",
			in:   &Output{Data: []byte{0x00, 0x1b, 0xff, 0x00, 0x7f}},
		},
		{
			desc: "invalid UTF-8 in strings survives byte for byte",
			in:   &HelloErr{Code: CodeUnknown, Message: "\xff\xfe\x00bad"},
		},
		{
			desc: "unicode identifiers survive",
			in:   &HelloOK{VTEID: "id-\u00e9\u4e16", URI: "file:///tmp/\u00e9"},
		},
		{
			desc: "negative history remaining survives",
			in:   &HistoryPage{Remaining: -1, Page: []byte{1}},
		},
		{
			desc: "history page with no page bytes decodes as nil",
			in:   &HistoryPage{Remaining: 0, Page: []byte{}},
			want: &HistoryPage{},
		},
		{
			desc: "root keeps spaces and unicode byte for byte",
			in: &Hello{
				Version: Version,
				Root:    "/home/me/my projects/\u00e9t\u00e9",
			},
		},
		{
			desc: "long strings survive",
			in:   &Exit{Message: strings.Repeat("e", 100_000)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			want := tc.want
			if want == nil {
				want = tc.in
			}
			assert.Equal(t, want, roundTrip(t, tc.in))
		})
	}
}

// TestDecodeFrameRejectsMalformedPayload pins that every layout
// validates its own payload. A frame is attacker-shaped input: the
// process on the other end of an attachment socket is not necessarily
// a Rune that agrees about anything.
func TestDecodeFrameRejectsMalformedPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc    string
		typ     FrameType
		payload []byte
	}{
		{desc: "hello without version", typ: TypeHello},
		{
			desc:    "hello truncated inside root",
			typ:     TypeHello,
			payload: build(func(e *enc) { e.u16(Version); e.u32(8); e.raw([]byte("abc")) }),
		},
		{
			desc: "hello truncated inside id",
			typ:  TypeHello,
			payload: build(func(e *enc) {
				e.u16(Version)
				e.str("/root")
				e.u32(8)
				e.raw([]byte("abc"))
			}),
		},
		{
			desc: "hello argv count larger than the payload can back",
			typ:  TypeHello,
			payload: build(func(e *enc) {
				e.u16(Version)
				e.str("")
				e.str("")
				e.bool(false)
				e.i32(80)
				e.i32(24)
				e.u32(math.MaxUint32)
			}),
		},
		{
			desc: "hello argv count promises more args than present",
			typ:  TypeHello,
			payload: build(func(e *enc) {
				e.u16(Version)
				e.str("")
				e.str("")
				e.bool(false)
				e.i32(80)
				e.i32(24)
				e.u32(2)
				e.str("only-one")
			}),
		},
		{
			desc: "hello with trailing bytes",
			typ:  TypeHello,
			payload: build(func(e *enc) {
				e.u16(Version)
				e.str("")
				e.str("")
				e.bool(false)
				e.i32(80)
				e.i32(24)
				e.u32(0)
				e.i32(0)
				e.i32(0)
				e.u8(0)
			}),
		},
		{
			desc: "hello_ok truncated before complete flag",
			typ:  TypeHelloOK,
			payload: build(func(e *enc) {
				e.str("id")
				e.str("uri")
				e.i32(80)
				e.i32(24)
			}),
		},
		{
			desc:    "hello_err without message length",
			typ:     TypeHelloErr,
			payload: []byte{byte(CodeNotFound)},
		},
		{
			desc:    "hello_err message length exceeds payload",
			typ:     TypeHelloErr,
			payload: build(func(e *enc) { e.u8(0); e.u32(64); e.raw([]byte("short")) }),
		},
		{
			desc:    "dropped without message length",
			typ:     TypeDropped,
			payload: []byte{byte(CodeSlow)},
		},
		{desc: "history page truncated remaining", typ: TypeHistoryPage, payload: []byte{1, 2, 3}},
		{desc: "resize with half a dimension", typ: TypeResize, payload: []byte{1, 2, 3, 4, 5, 6, 7}},
		{desc: "resize with no payload", typ: TypeResize},
		{
			desc:    "resize without a cell size",
			typ:     TypeResize,
			payload: build(func(e *enc) { e.i32(80); e.i32(24) }),
		},
		{desc: "graphics medium with no payload", typ: TypeGraphicsMedium},
		{desc: "focus with no payload", typ: TypeFocus},
		{desc: "focus with trailing bytes", typ: TypeFocus, payload: []byte{1, 0}},
		{desc: "detach carrying a payload", typ: TypeDetach, payload: []byte{0}},
		{
			desc:    "exit message length exceeds payload",
			typ:     TypeExit,
			payload: build(func(e *enc) { e.u32(math.MaxUint32) }),
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			_, err := decodeFrame(tc.typ, tc.payload)
			assert.ErrorIs(t, err, ErrMalformed)
		})
	}
}

// TestDecodeFrameRejectsUnknownType pins that an unrecognized tag is
// refused rather than guessed at, including the gaps deliberately left
// between the client, server and shared type ranges.
func TestDecodeFrameRejectsUnknownType(t *testing.T) {
	t.Parallel()

	for _, typ := range []FrameType{0, 5, 63, 72, 127, 129, 255} {
		t.Run(typ.String(), func(t *testing.T) {
			t.Parallel()

			_, err := decodeFrame(typ, nil)
			assert.ErrorIs(t, err, ErrUnknownFrame)
		})
	}
}

// TestCountRejectsUnbackedLength pins the allocation guard: a length
// prefix is only believed once the payload proves it can back it, so a
// corrupt count cannot make a peer reserve gigabytes.
func TestCountRejectsUnbackedLength(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc     string
		payload  []byte
		elemSize int
		wantErr  bool
	}{
		{desc: "zero length is fine", payload: build(func(e *enc) { e.u32(0) })},
		{
			desc:     "length backed exactly",
			payload:  build(func(e *enc) { e.u32(2); e.raw([]byte{1, 2}) }),
			elemSize: 1,
		},
		{
			desc:     "length one past what remains",
			payload:  build(func(e *enc) { e.u32(3); e.raw([]byte{1, 2}) }),
			elemSize: 1,
			wantErr:  true,
		},
		{
			desc:     "max uint32 length on an empty payload",
			payload:  build(func(e *enc) { e.u32(math.MaxUint32) }),
			elemSize: 1,
			wantErr:  true,
		},
		{
			desc:     "length that only overflows once scaled by element size",
			payload:  build(func(e *enc) { e.u32(1000); e.raw(make([]byte, 1000)) }),
			elemSize: 8,
			wantErr:  true,
		},
		{
			desc:     "length that wraps a 32-bit int once scaled by element size",
			payload:  build(func(e *enc) { e.u32(1<<30 + 1); e.raw(make([]byte, 8)) }),
			elemSize: 4,
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			elemSize := max(tc.elemSize, 1)
			d := dec{b: tc.payload}
			d.count(elemSize)
			if tc.wantErr {
				assert.ErrorIs(t, d.err, ErrMalformed)
				return
			}
			assert.NoError(t, d.err)
		})
	}
}

// TestWriteFrameRejectsOversizedFrame pins that the sending side
// refuses what the receiving side would refuse to allocate, so the
// limit surfaces as an error on the peer that can act on it.
func TestWriteFrameRejectsOversizedFrame(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	conn := NewConn(readWriter{&buf, &buf})
	err := conn.WriteFrame(&Output{Data: make([]byte, MaxFrameLen+1)})
	assert.ErrorIs(t, err, ErrFrameTooLarge)
	assert.Zero(t, buf.Len(), "a rejected frame must not reach the wire")
}

// TestGraphicsMediumFitsAFrame pins that a transmission of
// MaxGraphicsMediumLen bytes is exactly as large as a frame may be, so a
// relayed image is never lost to the reader's allocation guard.
func TestGraphicsMediumFitsAFrame(t *testing.T) {
	t.Parallel()

	payload := build((&GraphicsMedium{OK: true, Data: make([]byte, 10)}).encode)
	assert.Len(t, payload, 10+MaxFrameLen-MaxGraphicsMediumLen)
}

// TestFrameTypeString pins the names that appear in protocol errors,
// including the fallback for a type this version does not define.
func TestFrameTypeString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		typ  FrameType
		want string
	}{
		{TypeHello, "HELLO"},
		{TypeInput, "INPUT"},
		{TypeFocus, "FOCUS"},
		{TypeDetach, "DETACH"},
		{TypeHelloOK, "HELLO_OK"},
		{TypeHelloErr, "HELLO_ERR"},
		{TypeSnapshot, "SNAPSHOT"},
		{TypeOutput, "OUTPUT"},
		{TypeHistoryPage, "HISTORY_PAGE"},
		{TypeExit, "EXIT"},
		{TypeResize, "RESIZE"},
		{TypeGraphicsMedium, "GRAPHICS_MEDIUM"},
		{FrameType(200), "FrameType(200)"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.typ.String())
		})
	}
}

// roundTrip sends a frame through a real connection and reads it back.
func roundTrip(t *testing.T, f Frame) Frame {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, NewConn(readWriter{&buf, &buf}).WriteFrame(f))

	got, err := NewConn(readWriter{
		bytes.NewReader(buf.Bytes()), io.Discard,
	}).ReadFrame()
	require.NoError(t, err)
	return got
}

// build assembles a payload with the encoder the frames themselves
// use, so a case describes a layout rather than a byte blob.
func build(fn func(*enc)) []byte {
	var e enc
	fn(&e)
	return e.b
}
