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

// Package vtegraphics implements the kitty terminal graphics protocol on
// top of term.Writer.DrawImage. The protocol is specified in
// docs/kitty-graphics-protocol.md, which this package follows section by
// section; the reference implementation is kitty/graphics.c.
package vtegraphics

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
)

// Command is a parsed graphics command: the control data keys of
// spec §2.1 and the decoded payload. Absent keys hold their protocol
// defaults.
type Command struct {
	// Action is the 'a' key: t T q p d f a c.
	Action byte
	// Quiet is the 'q' key.
	Quiet uint32
	// Format is the 'f' key: 24, 32 or 100.
	Format uint32
	// Medium is the 't' key: d f t s.
	Medium byte
	// Width and Height are 's' and 'v'.
	Width, Height uint32
	// DataSize and DataOffset are 'S' and 'O'.
	DataSize, DataOffset uint32
	// Compression is the 'o' key, 'z' or 0.
	Compression byte
	// More is the 'm' key.
	More bool
	// Usage is the 'N' bitmask.
	Usage uint32
	// ImageID and ImageNumber are 'i' and 'I'.
	ImageID, ImageNumber uint32
	// PlacementID is 'p'.
	PlacementID uint32
	// X, Y, W, H are 'x', 'y', 'w', 'h'.
	X, Y, W, H uint32
	// CellX, CellY are 'X' and 'Y'.
	CellX, CellY uint32
	// Columns and Rows are 'c' and 'r'.
	Columns, Rows uint32
	// Z is the 'z' key.
	Z int32
	// CursorMovement is 'C'.
	CursorMovement uint32
	// Unicode is 'U'.
	Unicode uint32
	// ParentID and ParentPlacementID are 'P' and 'Q'.
	ParentID, ParentPlacementID uint32
	// OffsetH and OffsetV are 'H' and 'V'.
	OffsetH, OffsetV int32
	// Delete is the 'd' key.
	Delete byte
	// Payload is the base64-decoded payload, nil when absent.
	Payload []byte

	// mediumData is what ReadMedium read, if mediumRead.
	mediumData []byte
	mediumRead bool
}

// UsageTransient is bit 0 of the 'N' usage hints (spec §12).
const UsageTransient = 1

// ErrMalformed reports a syntactically invalid command. Such commands
// are dropped without a response (spec §2.2).
var ErrMalformed = errors.New("malformed graphics command")

// Parse parses the control data and payload that follow the 'G' of a
// graphics APC. It applies kitty's rejection rules (spec §2.2): an
// unknown key, a missing '=', a bad flag value, a missing or oversized
// integer, junk after a value, or invalid base64 makes the whole command
// malformed. Defaults are applied by the executor, so absent keys read
// as zero.
func Parse(data []byte) (Command, error) {
	var cmd Command
	pos := 0
	// An empty control block followed by a payload is accepted.
	if len(data) > 0 && data[0] == ';' {
		payload, err := decodePayload(data[1:])
		if err != nil {
			return cmd, err
		}
		cmd.Payload = payload
		return cmd, nil
	}
	for pos < len(data) {
		key := data[pos]
		pos++
		if pos >= len(data) {
			return cmd, fmt.Errorf("%w: no = after key %q", ErrMalformed, key)
		}
		if data[pos] != '=' {
			return cmd, fmt.Errorf("%w: expected = after key %q, found %q",
				ErrMalformed, key, data[pos])
		}
		pos++
		if pos >= len(data) {
			return cmd, fmt.Errorf("%w: missing value for key %q", ErrMalformed, key)
		}

		var err error
		switch key {
		case 'a':
			cmd.Action, err = parseFlag(data[pos], "tTqpdfac")
			pos++
		case 'd':
			cmd.Delete, err = parseFlag(data[pos], "aAiIcCfFnNpPqQrRxXyYzZ")
			pos++
		case 't':
			cmd.Medium, err = parseFlag(data[pos], "dfts")
			pos++
		case 'o':
			cmd.Compression, err = parseFlag(data[pos], "z")
			pos++
		case 'z', 'H', 'V':
			var v int32
			v, pos, err = parseInt(data, pos)
			switch key {
			case 'z':
				cmd.Z = v
			case 'H':
				cmd.OffsetH = v
			case 'V':
				cmd.OffsetV = v
			}
		default:
			var v uint32
			v, pos, err = parseUint(data, pos)
			if err != nil {
				break
			}
			err = cmd.setUint(key, v)
		}
		if err != nil {
			return cmd, err
		}

		if pos >= len(data) {
			return cmd, nil
		}
		switch data[pos] {
		case ',':
			pos++
			if pos >= len(data) {
				return cmd, fmt.Errorf("%w: trailing comma", ErrMalformed)
			}
		case ';':
			payload, err := decodePayload(data[pos+1:])
			if err != nil {
				return cmd, err
			}
			cmd.Payload = payload
			return cmd, nil
		default:
			return cmd, fmt.Errorf("%w: expected , or ; after value, found %q",
				ErrMalformed, data[pos])
		}
	}
	return cmd, nil
}

// ReadTransmission reads size bytes at offset from the file or shared
// memory object named by path, or all of it when size is 0. medium is
// 'f', 't' or 's' and says whether the object should be removed
// afterwards (spec §4.4). The error is never shown to the client: a
// single response for every failure keeps the terminal from being used
// to probe the filesystem.
type ReadTransmission func(medium byte, path string, offset, size int64) ([]byte, error)

// ReadMedium reads the file or shared memory object cmd transmits, for
// Handle to load (spec §4.4). Handle does no I/O, so a caller can read
// a slow or remote filesystem without holding the lock it serializes
// Handle with. A transmission that was not read, or failed to be, is
// answered EBADF.
func (cmd *Command) ReadMedium(read ReadTransmission) {
	if !cmd.readsMedium() {
		return
	}
	data, err := read(cmd.Medium, string(cmd.Payload),
		int64(cmd.DataOffset), int64(cmd.DataSize))
	cmd.mediumData, cmd.mediumRead = data, err == nil
}

// readsMedium reports whether Handle loads cmd from a file or shared
// memory object: whether cmd names one and passes the checks Handle
// makes before the read, so a rejected transmission leaves its object
// alone. Only Handle knows whether the image of an a=f frame exists.
func (cmd *Command) readsMedium() bool {
	switch cmd.Medium {
	case 'f', 't', 's':
	default:
		return false
	}
	switch cmd.Action {
	case 0, 't', 'T', 'f':
	case 'q':
		if cmd.ImageID == 0 {
			return false
		}
	default:
		return false
	}
	if cmd.ImageID != 0 && cmd.ImageNumber != 0 {
		return false
	}
	return len(cmd.Payload) <= MaxPathLength && newLoadSpec(*cmd).validate(*cmd) == nil
}

func (cmd *Command) setUint(key byte, v uint32) error {
	switch key {
	case 'q':
		cmd.Quiet = v
	case 'f':
		cmd.Format = v
	case 's':
		cmd.Width = v
	case 'v':
		cmd.Height = v
	case 'S':
		cmd.DataSize = v
	case 'O':
		cmd.DataOffset = v
	case 'm':
		cmd.More = v != 0
	case 'N':
		cmd.Usage = v
	case 'i':
		cmd.ImageID = v
	case 'I':
		cmd.ImageNumber = v
	case 'p':
		cmd.PlacementID = v
	case 'x':
		cmd.X = v
	case 'y':
		cmd.Y = v
	case 'w':
		cmd.W = v
	case 'h':
		cmd.H = v
	case 'X':
		cmd.CellX = v
	case 'Y':
		cmd.CellY = v
	case 'c':
		cmd.Columns = v
	case 'r':
		cmd.Rows = v
	case 'C':
		cmd.CursorMovement = v
	case 'U':
		cmd.Unicode = v
	case 'P':
		cmd.ParentID = v
	case 'Q':
		cmd.ParentPlacementID = v
	default:
		return fmt.Errorf("%w: invalid key %q", ErrMalformed, key)
	}
	return nil
}

func parseFlag(ch byte, allowed string) (byte, error) {
	for i := 0; i < len(allowed); i++ {
		if allowed[i] == ch {
			return ch, nil
		}
	}
	return 0, fmt.Errorf("%w: unknown flag value %q", ErrMalformed, ch)
}

// parseUint consumes at most ten decimal digits, as kitty does.
func parseUint(data []byte, pos int) (uint32, int, error) {
	var acc uint64
	start := pos
	for pos < len(data) && pos-start < 10 {
		ch := data[pos]
		if ch < '0' || ch > '9' {
			break
		}
		acc = acc*10 + uint64(ch-'0')
		pos++
	}
	if pos == start {
		return 0, pos, fmt.Errorf("%w: expecting an integer value", ErrMalformed)
	}
	if acc > math.MaxUint32 {
		return 0, pos, fmt.Errorf("%w: number is too large", ErrMalformed)
	}
	return uint32(acc), pos, nil
}

func parseInt(data []byte, pos int) (int32, int, error) {
	negative := false
	if pos < len(data) && data[pos] == '-' {
		negative = true
		pos++
	}
	v, pos, err := parseUint(data, pos)
	if err != nil {
		return 0, pos, err
	}
	if negative {
		return int32(-int64(v)), pos, nil
	}
	return int32(v), pos, nil
}

// decodePayload decodes standard base64, tolerating omitted padding.
// An empty payload decodes to an empty, non-nil slice so the executor
// can tell "no payload" from "a payload of zero bytes".
func decodePayload(src []byte) ([]byte, error) {
	trimmed := src
	for len(trimmed) > 0 && trimmed[len(trimmed)-1] == '=' {
		trimmed = trimmed[:len(trimmed)-1]
	}
	out := make([]byte, base64.RawStdEncoding.DecodedLen(len(trimmed)))
	n, err := base64.RawStdEncoding.Decode(out, trimmed)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid base64 payload: %v", ErrMalformed, err)
	}
	return out[:n], nil
}
