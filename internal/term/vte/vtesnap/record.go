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

// Package vtesnap encodes and decodes terminal emulator snapshots.
//
// A snapshot is a self-describing binary blob: one fixed envelope
// followed by CRC-framed records read back to back until READY.
// Scrollback is not part of it: a transport sends it after the
// snapshot as pages of their own (see [EncodePage]), so the screen
// renders without waiting for a deep history.
//
//	envelope
//	TERMINAL                  terminal-wide state and modes
//	SCREEN + PAGE...          primary screen and its live rows
//	SCREEN + PAGE...          alternate screen and its live rows
//	CONTINUATION              bytes needed to resume the byte stream
//	READY                     the terminal is renderable from here
//
// The layout follows the architecture of Ghostty's snapshot v1 format
// but shares neither its magic nor its payloads: the page records
// encode this emulator's cells, not Ghostty's memory model.
package vtesnap

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// Magic identifies a Rune terminal snapshot. All eight bytes are part
// of the wire value.
const Magic = "RUNEVTES"

// Version is the compatibility boundary for the whole blob. Readers
// require an exact match.
const Version uint16 = 1

// EnvelopeLen is the size of the fixed envelope: magic then version.
const EnvelopeLen = len(Magic) + 2

// HeaderLen is the size of the fixed record header: tag, payload
// length, then CRC32C.
const HeaderLen = 2 + 4 + 4

// MaxPayloadLen bounds a single record payload so a corrupt length
// cannot make a decoder allocate without limit.
const MaxPayloadLen = 64 << 20

// Tag identifies the layout and meaning of a record payload.
type Tag uint16

// Record tags. Unknown tags are rejected.
const (
	// TagTerminal carries terminal-wide state and configuration.
	TagTerminal Tag = 1

	// TagScreen introduces one screen and the pages that follow it.
	TagScreen Tag = 2

	// TagPage carries a contiguous run of rows.
	TagPage Tag = 3

	// TagReady terminates the snapshot.
	TagReady Tag = 4

	// TagContinuation carries the unfinished input needed to resume
	// the byte stream the snapshot was cut from.
	TagContinuation Tag = 5
)

func (t Tag) String() string {
	switch t {
	case TagTerminal:
		return "TERMINAL"
	case TagScreen:
		return "SCREEN"
	case TagPage:
		return "PAGE"
	case TagReady:
		return "READY"
	case TagContinuation:
		return "CONTINUATION"
	default:
		return fmt.Sprintf("Tag(%d)", uint16(t))
	}
}

func (t Tag) valid() bool {
	return t >= TagTerminal && t <= TagContinuation
}

// Errors reported while reading a snapshot.
var (
	// ErrInvalidMagic means the bytes are not a Rune snapshot.
	ErrInvalidMagic = errors.New("vtesnap: invalid magic")

	// ErrUnsupportedVersion means the blob was written by an
	// incompatible encoder.
	ErrUnsupportedVersion = errors.New("vtesnap: unsupported version")

	// ErrInvalidTag means a record declared a tag this version does
	// not define.
	ErrInvalidTag = errors.New("vtesnap: invalid record tag")

	// ErrChecksum means a record payload did not match its CRC.
	ErrChecksum = errors.New("vtesnap: record checksum mismatch")

	// ErrPayloadTooLarge means a record declared more payload than a
	// decoder is willing to allocate.
	ErrPayloadTooLarge = errors.New("vtesnap: record payload too large")

	// ErrMalformed means a payload did not match the layout its tag
	// promises.
	ErrMalformed = errors.New("vtesnap: malformed record payload")
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// RecordWriter frames records onto an underlying writer.
type RecordWriter struct {
	w      io.Writer
	header [HeaderLen]byte
}

// NewRecordWriter returns a writer that frames records onto w.
func NewRecordWriter(w io.Writer) *RecordWriter {
	return &RecordWriter{w: w}
}

// Envelope writes the fixed envelope. It must precede every record.
func (w *RecordWriter) Envelope() error {
	var buf [EnvelopeLen]byte
	copy(buf[:], Magic)
	binary.LittleEndian.PutUint16(buf[len(Magic):], Version)
	_, err := w.w.Write(buf[:])
	return err
}

// Record writes one complete record.
func (w *RecordWriter) Record(tag Tag, payload []byte) error {
	if len(payload) > MaxPayloadLen {
		return fmt.Errorf("%w: %s is %d bytes",
			ErrPayloadTooLarge, tag, len(payload))
	}
	binary.LittleEndian.PutUint16(w.header[0:], uint16(tag))
	binary.LittleEndian.PutUint32(w.header[2:], uint32(len(payload)))

	crc := crc32.Update(0, crcTable, w.header[:6])
	crc = crc32.Update(crc, crcTable, payload)
	binary.LittleEndian.PutUint32(w.header[6:], crc)

	if _, err := w.w.Write(w.header[:]); err != nil {
		return err
	}
	_, err := w.w.Write(payload)
	return err
}

// RecordReader reads framed records from an underlying reader.
type RecordReader struct {
	r      io.Reader
	header [HeaderLen]byte
	buf    []byte
}

// NewRecordReader returns a reader over the framed records in r.
func NewRecordReader(r io.Reader) *RecordReader {
	return &RecordReader{r: r}
}

// Envelope reads and validates the fixed envelope.
func (r *RecordReader) Envelope() error {
	var buf [EnvelopeLen]byte
	if _, err := io.ReadFull(r.r, buf[:]); err != nil {
		return err
	}
	if string(buf[:len(Magic)]) != Magic {
		return ErrInvalidMagic
	}
	if v := binary.LittleEndian.Uint16(buf[len(Magic):]); v != Version {
		return fmt.Errorf("%w: %d", ErrUnsupportedVersion, v)
	}
	return nil
}

// Next reads the next record. The returned payload is only valid until
// the following call to Next.
func (r *RecordReader) Next() (Tag, []byte, error) {
	if _, err := io.ReadFull(r.r, r.header[:]); err != nil {
		return 0, nil, err
	}
	tag := Tag(binary.LittleEndian.Uint16(r.header[0:]))
	if !tag.valid() {
		return 0, nil, fmt.Errorf("%w: %d", ErrInvalidTag, uint16(tag))
	}
	length := binary.LittleEndian.Uint32(r.header[2:])
	if length > MaxPayloadLen {
		return 0, nil, fmt.Errorf("%w: %s declares %d bytes",
			ErrPayloadTooLarge, tag, length)
	}

	if cap(r.buf) < int(length) {
		r.buf = make([]byte, length)
	}
	payload := r.buf[:length]
	if _, err := io.ReadFull(r.r, payload); err != nil {
		return 0, nil, err
	}

	crc := crc32.Update(0, crcTable, r.header[:6])
	crc = crc32.Update(crc, crcTable, payload)
	if crc != binary.LittleEndian.Uint32(r.header[6:]) {
		return 0, nil, fmt.Errorf("%w: %s", ErrChecksum, tag)
	}
	return tag, payload, nil
}
