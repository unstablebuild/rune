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

// Package vtewire is the framed protocol a client speaks to attach to
// a terminal emulator owned by another process.
//
// The server owns the pty and a headless emulator; the client runs its
// own emulator over the same byte stream. An attachment therefore
// carries raw pty bytes rather than rendered cells:
//
//	client                                server
//	  HELLO        ───────────────────▶
//	               ◀───────────────────   HELLO_OK | HELLO_ERR
//	               ◀───────────────────   SNAPSHOT (through READY)
//	  INPUT        ───────────────────▶
//	  RESIZE       ───────────────────▶
//	  FOCUS        ───────────────────▶
//	               ◀───────────────────   OUTPUT | HISTORY_PAGE | GRAPHICS_MEDIUM
//	               ◀───────────────────   RESIZE | EXIT | DROPPED
//	  DETACH       ───────────────────▶
//
// Everything after HELLO_OK is interleaved. The server emits OUTPUT,
// GRAPHICS_MEDIUM, RESIZE and EXIT in exactly the order its own
// emulator processed them, which is what lets every attached replica
// reflow to the same screen.
//
// DROPPED ends an attachment from the server's side without ending the
// terminal: a client that fell too far behind the output is dropped
// rather than throttled, and resyncs by attaching again from a fresh
// snapshot.
//
// The protocol is transport-agnostic: it runs over any reliable,
// ordered byte stream, which in practice is a connection over a mesh
// or a unix socket.
package vtewire

import (
	"errors"
	"fmt"
)

// Version is the protocol version a client announces in HELLO. A
// server that does not implement it answers HELLO_ERR rather than
// guessing.
//
// It also covers the snapshot a server sends after HELLO_OK: that is
// the last point at which a client and a server that cannot understand
// each other can part before a terminal is created or attached, so a
// change to the snapshot encoding bumps it too.
const Version uint16 = 5

// MaxFrameLen bounds a single frame payload so a corrupt length cannot
// make a peer allocate without limit. The largest legitimate frame is
// the snapshot, which holds two screens' worth of cells.
const MaxFrameLen = 64 << 20

// MaxGraphicsMediumLen is the largest file-backed graphics transmission
// a GRAPHICS_MEDIUM frame carries.
const MaxGraphicsMediumLen = MaxFrameLen - 1

// FrameType identifies a frame's payload layout. Client and server
// types are numbered apart so a frame sent in the wrong direction is
// rejected rather than misread.
type FrameType uint8

// Frames sent by the client.
const (
	TypeHello  FrameType = 1
	TypeInput  FrameType = 2
	TypeFocus  FrameType = 3
	TypeDetach FrameType = 4
)

// Frames sent by the server.
const (
	TypeHelloOK        FrameType = 64
	TypeHelloErr       FrameType = 65
	TypeSnapshot       FrameType = 66
	TypeOutput         FrameType = 67
	TypeHistoryPage    FrameType = 68
	TypeExit           FrameType = 69
	TypeGraphicsMedium FrameType = 70
	TypeDropped        FrameType = 71
)

// TypeResize travels in both directions: from the client it requests a
// size, from the server it announces the size the pty actually has.
const TypeResize FrameType = 128

func (t FrameType) String() string {
	switch t {
	case TypeHello:
		return "HELLO"
	case TypeInput:
		return "INPUT"
	case TypeFocus:
		return "FOCUS"
	case TypeDetach:
		return "DETACH"
	case TypeHelloOK:
		return "HELLO_OK"
	case TypeHelloErr:
		return "HELLO_ERR"
	case TypeSnapshot:
		return "SNAPSHOT"
	case TypeOutput:
		return "OUTPUT"
	case TypeHistoryPage:
		return "HISTORY_PAGE"
	case TypeExit:
		return "EXIT"
	case TypeGraphicsMedium:
		return "GRAPHICS_MEDIUM"
	case TypeDropped:
		return "DROPPED"
	case TypeResize:
		return "RESIZE"
	default:
		return fmt.Sprintf("FrameType(%d)", uint8(t))
	}
}

// Errors reported while reading frames.
var (
	// ErrUnknownFrame means a frame declared a type this version does
	// not define.
	ErrUnknownFrame = errors.New("vtewire: unknown frame type")

	// ErrFrameTooLarge means a frame declared more payload than a peer
	// is willing to allocate.
	ErrFrameTooLarge = errors.New("vtewire: frame too large")

	// ErrMalformed means a payload did not match the layout its type
	// promises.
	ErrMalformed = errors.New("vtewire: malformed frame payload")
)

// Frame is one protocol message.
//
// Byte slices reachable from a frame returned by [Conn.ReadFrame] alias
// the connection's read buffer and stay valid only until the next read.
// Copy them to retain them.
type Frame interface {
	Type() FrameType
	encode(*enc)
}

// ErrCode classifies a rejected HELLO, or a dropped attachment, so the
// client can tell "this emulator does not exist" from every other
// failure. That distinction is what authorizes recreating a terminal
// from a saved snapshot, and what makes attaching again safe.
type ErrCode uint8

// Reasons a HELLO can be rejected or an attachment dropped.
const (
	// CodeUnknown is any failure whose meaning the client cannot act
	// on. The emulator's existence stays unknown.
	CodeUnknown ErrCode = 0

	// CodeNotFound means the server was reached and confirmed the
	// emulator does not exist.
	CodeNotFound ErrCode = 1

	// CodeUnsupported means the scheme does not own terminals.
	CodeUnsupported ErrCode = 2

	// CodeVersion means the client's protocol version is not
	// implemented by this server.
	CodeVersion ErrCode = 3

	// CodeSlow means the attachment fell further behind the terminal's
	// output than the server queues, and was dropped. The terminal
	// still exists: a fresh attachment resyncs from a new snapshot.
	CodeSlow ErrCode = 4
)

// CreateOptions is the serializable part of a new emulator's
// configuration. Client-local concerns such as clipboards, bells and
// event scheduling stay with the attaching client; the scrollback
// bound is the owner's.
type CreateOptions struct {
	// CommandAndArgs is the program to run. Empty runs the scheme's
	// default shell.
	CommandAndArgs []string
}

// Hello opens an attachment. A nonempty VTEID attaches to an existing
// emulator; otherwise the server creates one from Create.
type Hello struct {
	Version uint16
	// Root is the workspace the emulator belongs to. One server hosts
	// the terminals of every workspace on its machine, and each
	// workspace only sees its own. Empty means the server's own root.
	Root   string
	VTEID  string
	Create CreateOptions
	// Preview asks for a view of VTEID that leaves the terminal
	// detached: it is not counted among its attachments, and only owns
	// its size while no attachment does.
	Preview bool
	Width   int
	Height  int
	// CellPixelWidth and CellPixelHeight are the size of a cell on the
	// client's display, zero when it cannot draw images. They take
	// effect with the client's size, if the client wins authority.
	CellPixelWidth  int
	CellPixelHeight int
}

// HelloOK accepts an attachment and names the emulator behind it.
type HelloOK struct {
	VTEID string
	// URI is the emulator's pty, which clients use as its identity in
	// the window manager.
	URI    string
	Width  int
	Height int
	// Complete reports that the emulator's process already exited. Its
	// final output is still in the snapshot.
	Complete bool
}

// HelloErr rejects an attachment.
type HelloErr struct {
	Code    ErrCode
	Message string
}

// Snapshot carries the emulator's state through the snapshot format's
// READY marker: enough for the client to render immediately.
type Snapshot struct {
	Records []byte
}

// Output is raw pty output, teed from the exact stream position the
// snapshot was cut at.
type Output struct {
	Data []byte
}

// HistoryPage is one page of scrollback, sent after the snapshot so
// the screen is usable before the history finishes arriving. Pages
// arrive newest first.
type HistoryPage struct {
	// Remaining is how many scrollback rows are still to come.
	Remaining int
	// Page is an encoded snapshot page record.
	Page []byte
}

// Input is raw bytes for the pty.
type Input struct {
	Data []byte
}

// Resize requests a pty size from a client, and reports the size in
// force from the server.
//
// The size includes a cell's size in pixels. It is terminal state like
// the grid is: placing an image moves the cursor by the cells the image
// covers, so every replica interprets the stream with the one in force.
// A client that cannot measure one sends zero and keeps the one in
// force.
type Resize struct {
	Width           int
	Height          int
	CellPixelWidth  int
	CellPixelHeight int
}

// Focus tells the server this client took or lost focus, which decides
// whose size the shared pty follows.
type Focus struct {
	Focused bool
}

// Detach ends the attachment without touching the emulator.
type Detach struct{}

// Exit reports that the emulator's stream ended. Message is empty for
// an ordinary process exit. Err is the error the process exited with,
// as the server's emulator reports it: empty for a clean exit or an
// unknown status.
type Exit struct {
	Message string
	Err     string
}

// GraphicsMedium is what the server's emulator read for the next
// graphics transmission sent as a file or shared memory object. Such a
// transmission is only read once, by the server: the file may be
// deleted by that read, and may not be reachable from the client. OK
// is false when the read failed, which the client's emulator must then
// fail too.
type GraphicsMedium struct {
	OK   bool
	Data []byte
}

// Dropped ends the attachment from the server's side, with the reason
// as a code. The terminal is unaffected unless the code says otherwise;
// a client dropped for being slow attaches again.
type Dropped struct {
	Code    ErrCode
	Message string
}

// Type reports the wire tag each frame is sent under.
//
//revive:disable:exported // documented as a group
func (*Hello) Type() FrameType       { return TypeHello }
func (*HelloOK) Type() FrameType     { return TypeHelloOK }
func (*HelloErr) Type() FrameType    { return TypeHelloErr }
func (*Snapshot) Type() FrameType    { return TypeSnapshot }
func (*Output) Type() FrameType      { return TypeOutput }
func (*HistoryPage) Type() FrameType { return TypeHistoryPage }
func (*Input) Type() FrameType       { return TypeInput }
func (*Resize) Type() FrameType      { return TypeResize }
func (*Focus) Type() FrameType       { return TypeFocus }
func (*Detach) Type() FrameType      { return TypeDetach }
func (*Exit) Type() FrameType        { return TypeExit }

func (*GraphicsMedium) Type() FrameType { return TypeGraphicsMedium }
func (*Dropped) Type() FrameType        { return TypeDropped }

//revive:enable:exported

func (f *Hello) encode(e *enc) {
	e.u16(f.Version)
	e.str(f.Root)
	e.str(f.VTEID)
	e.bool(f.Preview)
	e.i32(f.Width)
	e.i32(f.Height)
	e.u32(uint32(len(f.Create.CommandAndArgs)))
	for _, arg := range f.Create.CommandAndArgs {
		e.str(arg)
	}
	e.i32(f.CellPixelWidth)
	e.i32(f.CellPixelHeight)
}

func (f *Hello) decode(d *dec) {
	f.Version = d.u16()
	if d.err == nil && f.Version != Version {
		// Another version may lay the rest out differently, and Accept
		// reports the mismatch only if decoding gets that far.
		d.rest()
		return
	}
	f.Root = d.str()
	f.VTEID = d.str()
	f.Preview = d.bool()
	f.Width = d.i32()
	f.Height = d.i32()
	if n := d.count(4); n > 0 {
		f.Create.CommandAndArgs = make([]string, n)
		for i := range f.Create.CommandAndArgs {
			f.Create.CommandAndArgs[i] = d.str()
		}
	}
	f.CellPixelWidth = d.i32()
	f.CellPixelHeight = d.i32()
}

func (f *HelloOK) encode(e *enc) {
	e.str(f.VTEID)
	e.str(f.URI)
	e.i32(f.Width)
	e.i32(f.Height)
	e.bool(f.Complete)
}

func (f *HelloOK) decode(d *dec) {
	f.VTEID = d.str()
	f.URI = d.str()
	f.Width = d.i32()
	f.Height = d.i32()
	f.Complete = d.bool()
}

func (f *HelloErr) encode(e *enc) {
	e.u8(uint8(f.Code))
	e.str(f.Message)
}

func (f *HelloErr) decode(d *dec) {
	f.Code = ErrCode(d.u8())
	f.Message = d.str()
}

func (f *Snapshot) encode(e *enc) { e.raw(f.Records) }
func (f *Snapshot) decode(d *dec) { f.Records = d.rest() }

func (f *Output) encode(e *enc) { e.raw(f.Data) }
func (f *Output) decode(d *dec) { f.Data = d.rest() }

func (f *Input) encode(e *enc) { e.raw(f.Data) }
func (f *Input) decode(d *dec) { f.Data = d.rest() }

func (f *HistoryPage) encode(e *enc) {
	e.i32(f.Remaining)
	e.raw(f.Page)
}

func (f *HistoryPage) decode(d *dec) {
	f.Remaining = d.i32()
	f.Page = d.rest()
}

func (f *Resize) encode(e *enc) {
	e.i32(f.Width)
	e.i32(f.Height)
	e.i32(f.CellPixelWidth)
	e.i32(f.CellPixelHeight)
}

func (f *Resize) decode(d *dec) {
	f.Width = d.i32()
	f.Height = d.i32()
	f.CellPixelWidth = d.i32()
	f.CellPixelHeight = d.i32()
}

func (f *Focus) encode(e *enc) { e.bool(f.Focused) }
func (f *Focus) decode(d *dec) { f.Focused = d.bool() }

func (*Detach) encode(*enc) {}
func (*Detach) decode(*dec) {}

func (f *Exit) encode(e *enc) {
	e.str(f.Message)
	e.str(f.Err)
}

func (f *Exit) decode(d *dec) {
	f.Message = d.str()
	f.Err = d.str()
}

func (f *GraphicsMedium) encode(e *enc) {
	e.bool(f.OK)
	e.raw(f.Data)
}

func (f *GraphicsMedium) decode(d *dec) {
	f.OK = d.bool()
	f.Data = d.rest()
}

func (f *Dropped) encode(e *enc) {
	e.u8(uint8(f.Code))
	e.str(f.Message)
}

func (f *Dropped) decode(d *dec) {
	f.Code = ErrCode(d.u8())
	f.Message = d.str()
}

// decodeFrame builds the frame a type declares from its payload.
func decodeFrame(t FrameType, payload []byte) (Frame, error) {
	var (
		frame   Frame
		decoder interface{ decode(*dec) }
	)
	switch t {
	case TypeHello:
		f := new(Hello)
		frame, decoder = f, f
	case TypeHelloOK:
		f := new(HelloOK)
		frame, decoder = f, f
	case TypeHelloErr:
		f := new(HelloErr)
		frame, decoder = f, f
	case TypeSnapshot:
		f := new(Snapshot)
		frame, decoder = f, f
	case TypeOutput:
		f := new(Output)
		frame, decoder = f, f
	case TypeHistoryPage:
		f := new(HistoryPage)
		frame, decoder = f, f
	case TypeInput:
		f := new(Input)
		frame, decoder = f, f
	case TypeResize:
		f := new(Resize)
		frame, decoder = f, f
	case TypeFocus:
		f := new(Focus)
		frame, decoder = f, f
	case TypeDetach:
		f := new(Detach)
		frame, decoder = f, f
	case TypeExit:
		f := new(Exit)
		frame, decoder = f, f
	case TypeGraphicsMedium:
		f := new(GraphicsMedium)
		frame, decoder = f, f
	case TypeDropped:
		f := new(Dropped)
		frame, decoder = f, f
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnknownFrame, uint8(t))
	}

	d := dec{b: payload}
	decoder.decode(&d)
	if err := d.done(); err != nil {
		return nil, fmt.Errorf("%s: %w", t, err)
	}
	return frame, nil
}
