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
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
)

// headerLen is the fixed frame header: type then payload length.
const headerLen = 1 + 4

// headerPad reserves header space in front of a payload so a frame is
// built, and written, in one buffer.
var headerPad [headerLen]byte

// Conn reads and writes frames over one byte stream.
//
// Writes are serialized so several goroutines may send, but reads are
// not: exactly one goroutine may call ReadFrame.
type Conn struct {
	rw io.ReadWriteCloser
	r  *bufio.Reader

	readBuf   []byte
	readHdr   [headerLen]byte
	writeMu   sync.Mutex
	writeBuf  []byte
	closeOnce sync.Once
	closeErr  error
}

// NewConn returns a connection speaking the protocol over rw.
func NewConn(rw io.ReadWriteCloser) *Conn {
	return &Conn{rw: rw, r: bufio.NewReaderSize(rw, 64<<10)}
}

// WriteFrame encodes and sends one frame.
func (c *Conn) WriteFrame(f Frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.writeBuf = appendFrame(c.writeBuf[:0], f)
	if len(c.writeBuf)-headerLen > MaxFrameLen {
		return fmt.Errorf("%w: %s is %d bytes",
			ErrFrameTooLarge, f.Type(), len(c.writeBuf)-headerLen)
	}
	_, err := c.rw.Write(c.writeBuf)
	return err
}

// writeRaw sends already-encoded frames back to back. It shares
// WriteFrame's lock, so a Sender draining its queue cannot interleave
// with a direct WriteFrame.
func (c *Conn) writeRaw(frames [][]byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	// net.Buffers writes the whole batch with one writev on a socket.
	bufs := net.Buffers(frames)
	_, err := bufs.WriteTo(c.rw)
	return err
}

// ReadFrame reads the next frame. Byte slices reachable from the
// result alias the connection's buffer and stay valid only until the
// next call.
func (c *Conn) ReadFrame() (Frame, error) {
	if _, err := io.ReadFull(c.r, c.readHdr[:]); err != nil {
		return nil, err
	}
	t := FrameType(c.readHdr[0])
	length := binary.LittleEndian.Uint32(c.readHdr[1:])
	if length > MaxFrameLen {
		return nil, fmt.Errorf("%w: %s declares %d bytes",
			ErrFrameTooLarge, t, length)
	}

	var payload []byte
	if int(length) <= maxRetainedReadBuf {
		if cap(c.readBuf) < int(length) {
			c.readBuf = make([]byte, length)
		}
		payload = c.readBuf[:length]
	} else {
		payload = make([]byte, length)
	}
	if _, err := io.ReadFull(c.r, payload); err != nil {
		return nil, err
	}
	return decodeFrame(t, payload)
}

// maxRetainedReadBuf bounds the read buffer a connection keeps between
// frames. Live output arrives in small frames; a snapshot that does not
// fit gets a buffer of its own instead of pinning its size for the life
// of the attachment.
const maxRetainedReadBuf = 64 << 10

// Close shuts the underlying stream down. It is safe to call from any
// goroutine and unblocks a peer blocked in ReadFrame.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.rw.Close() })
	return c.closeErr
}

// EncodeFrame returns a frame's bytes, header included, so a caller
// can queue it for later transmission without holding the state it was
// built from.
func EncodeFrame(f Frame) []byte { return appendFrame(nil, f) }

func appendFrame(dst []byte, f Frame) []byte {
	start := len(dst)
	e := enc{b: append(dst, headerPad[:]...)}
	f.encode(&e)
	out := e.b
	out[start] = byte(f.Type())
	binary.LittleEndian.PutUint32(out[start+1:],
		uint32(len(out)-start-headerLen))
	return out
}
