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

package coreutils

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"

	"unstable.build/rune/internal/term/vsh"
)

// Cat is "cat [-n] [FILE...]".
var Cat = vsh.Command{Name: "cat", Synopsis: "cat [-n] [FILE...]", Run: cat}

func cat(ctx context.Context, inv *vsh.Invocation) error {
	o, err := parseArgs(inv.Name, inv.Args, "n", 0)
	if err != nil {
		return usage(inv, err)
	}
	files := o.operands
	if len(files) == 0 {
		files = []string{"-"}
	}
	failed := false
	var num numberer
	for _, name := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := openRead(inv, name)
		if err != nil {
			fail(inv, name, err, &failed)
			continue
		}
		if !o.has('n') {
			_, err = io.Copy(inv.Stdout, &contextReader{ctx: ctx, r: r})
		} else {
			err = num.copy(ctx, inv.Stdout, r)
		}
		_ = r.Close()
		if err != nil {
			return err
		}
	}
	return status(failed)
}

// numberer copies files numbering their lines as cat -n does. A line
// that ends a file without a newline is not terminated, so the next
// file continues it under the same number.
type numberer struct {
	lineNo  int
	midLine bool
}

func (n *numberer) copy(ctx context.Context, w io.Writer, r io.Reader) error {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk, rerr := br.ReadSlice('\n')
		if len(chunk) > 0 {
			if !n.midLine {
				n.lineNo++
				if _, err := fmt.Fprintf(w, "%6d\t", n.lineNo); err != nil {
					return err
				}
			}
			if _, err := w.Write(chunk); err != nil {
				return err
			}
			n.midLine = chunk[len(chunk)-1] != '\n'
		}
		switch {
		case rerr == nil, errors.Is(rerr, bufio.ErrBufferFull):
		case errors.Is(rerr, io.EOF):
			return nil
		default:
			return rerr
		}
	}
}

// contextReader ends a copy when ctx does, so that cat on a terminal
// stops with the command.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
