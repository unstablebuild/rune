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

package gshader

import (
	"context"
	"errors"
	"image"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// ErrUnsupported is returned by a Device for what it cannot evaluate.
var ErrUnsupported = errors.New("gshader: unsupported by device")

// Device evaluates node trees. It is valid only during the Draw whose writer
// context carries it, on that goroutine.
type Device interface {
	// Render evaluates n over size cells and returns its pixels. src is what
	// Source stands for; nil is transparent. The same key and size with an
	// Equal tree, the same src and an unchanged theme do no GPU work and
	// return the same Version. Frame.Src is valid until the next Render of
	// key, the term.Image.Src contract.
	Render(key term.ImageID, size image.Point, src *Snapshot, n Node) (Frame, error)
}

// Snapshot is the rendering of a component, which a SourceNode stands for:
// its cells and the placements drawn over them.
type Snapshot struct {
	Cells  [][]term.Cell
	Images []term.Image
}

// Frame is the result of a Render, to be drawn as a term.Image with the
// same Src, ID and Version.
type Frame struct {
	Src     image.Image
	ID      term.ImageID
	Version uint64
}

type deviceKey struct{}

// ContextWithDevice returns ctx carrying d.
func ContextWithDevice(ctx context.Context, d Device) context.Context {
	return context.WithValue(ctx, deviceKey{}, d)
}

// DeviceFromContext returns the Device ctx carries, reporting false when it
// carries none.
func DeviceFromContext(ctx context.Context) (Device, bool) {
	if ctx == nil {
		return nil, false
	}
	d, ok := ctx.Value(deviceKey{}).(Device)
	return d, ok && d != nil
}
