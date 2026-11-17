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
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"unstable.build/rune/internal/term/vsh"
)

// Wc is "wc [-lwc] [FILE...]".
var Wc = vsh.Command{Name: "wc", Synopsis: "wc [-lwc] [FILE...]", Run: wc}

type counts struct {
	lines, words, bytes int
}

func (c counts) add(o counts) counts {
	return counts{c.lines + o.lines, c.words + o.words, c.bytes + o.bytes}
}

func count(data []byte) counts {
	return counts{
		lines: bytes.Count(data, []byte{'\n'}),
		words: len(bytes.Fields(data)),
		bytes: len(data),
	}
}

func wc(ctx context.Context, inv *vsh.Invocation) error {
	o, err := parseArgs(inv.Name, inv.Args, "lwc", 0)
	if err != nil {
		return usage(inv, err)
	}
	showLines, showWords, showBytes := o.has('l'), o.has('w'), o.has('c')
	if !showLines && !showWords && !showBytes {
		showLines, showWords, showBytes = true, true, true
	}
	files := o.operands
	named := len(files) > 0
	if !named {
		files = []string{"-"}
	}
	failed := false
	var total counts
	type row struct {
		c    counts
		name string
	}
	var rows []row
	for _, name := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := openRead(inv, name)
		if err != nil {
			fail(inv, name, err, &failed)
			continue
		}
		data, err := io.ReadAll(&contextReader{ctx: ctx, r: r})
		_ = r.Close()
		if err != nil {
			return err
		}
		c := count(data)
		total = total.add(c)
		label := ""
		if named {
			label = name
		}
		rows = append(rows, row{c, label})
	}
	if len(files) > 1 {
		rows = append(rows, row{total, "total"})
	}
	width := 1
	for _, r := range rows {
		for _, n := range []struct {
			show bool
			n    int
		}{{showLines, r.c.lines}, {showWords, r.c.words}, {showBytes, r.c.bytes}} {
			if n.show {
				width = max(width, len(strconv.Itoa(n.n)))
			}
		}
	}
	if len(rows) > 1 || len(files) > 1 {
		width = max(width, 7)
	}
	for _, r := range rows {
		var fields []string
		if showLines {
			fields = append(fields, fmt.Sprintf("%*d", width, r.c.lines))
		}
		if showWords {
			fields = append(fields, fmt.Sprintf("%*d", width, r.c.words))
		}
		if showBytes {
			fields = append(fields, fmt.Sprintf("%*d", width, r.c.bytes))
		}
		line := strings.Join(fields, " ")
		if r.name != "" {
			line += " " + r.name
		}
		if _, err := fmt.Fprintln(inv.Stdout, line); err != nil {
			return err
		}
	}
	return status(failed)
}
