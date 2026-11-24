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

// Head is "head [-n N | -c N] [FILE...]".
var Head = vsh.Command{Name: "head", Synopsis: "head [-n N | -c N] [FILE...]", Run: head}

// Tail is "tail [-n N | -c N] [FILE...]".
var Tail = vsh.Command{Name: "tail", Synopsis: "tail [-n N | -c N] [FILE...]", Run: tail}

// headTailOptions parses the shared head/tail options: a line count
// (-n N or -N, 10 by default), a byte count (-c N), and -q/-v for the
// headers printed between several files.
func headTailOptions(inv *vsh.Invocation) (lines, bytesN int, byBytes, quiet bool, files []string, err error) {
	o, err := parseArgs(inv.Name, inv.Args, "n:c:qv", 'n')
	if err != nil {
		return 0, 0, false, false, nil, usage(inv, err)
	}
	lines = 10
	if v, ok := o.values['n']; ok {
		lines, err = strconv.Atoi(strings.TrimPrefix(v, "-"))
		if err != nil {
			return 0, 0, false, false, nil, usage(inv, fmt.Errorf("invalid number of lines: '%s'", v))
		}
	}
	if v, ok := o.values['c']; ok {
		bytesN, err = strconv.Atoi(strings.TrimPrefix(v, "-"))
		if err != nil {
			return 0, 0, false, false, nil, usage(inv, fmt.Errorf("invalid number of bytes: '%s'", v))
		}
		byBytes = true
	}
	files = o.operands
	if len(files) == 0 {
		files = []string{"-"}
	}
	quiet = o.has('q') || (len(files) == 1 && !o.has('v'))
	return lines, bytesN, byBytes, quiet, files, nil
}

func head(ctx context.Context, inv *vsh.Invocation) error {
	return headTail(ctx, inv, true)
}

func tail(ctx context.Context, inv *vsh.Invocation) error {
	return headTail(ctx, inv, false)
}

func headTail(ctx context.Context, inv *vsh.Invocation, fromStart bool) error {
	lines, bytesN, byBytes, quiet, files, err := headTailOptions(inv)
	if err != nil {
		return err
	}
	failed, printed := false, false
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
		if !quiet {
			sep := ""
			if printed {
				sep = "\n"
			}
			printed = true
			header := name
			if name == "-" {
				header = "standard input"
			}
			if _, err := fmt.Fprintf(inv.Stdout, "%s==> %s <==\n", sep, header); err != nil {
				return err
			}
		}
		var out []byte
		switch {
		case byBytes && fromStart:
			out = data[:min(bytesN, len(data))]
		case byBytes:
			out = data[len(data)-min(bytesN, len(data)):]
		case fromStart:
			out = firstLines(data, lines)
		default:
			out = lastLines(data, lines)
		}
		if _, err := inv.Stdout.Write(out); err != nil {
			return err
		}
	}
	return status(failed)
}

func firstLines(data []byte, n int) []byte {
	end := 0
	for range n {
		if end >= len(data) {
			break
		}
		i := bytes.IndexByte(data[end:], '\n')
		if i < 0 {
			end = len(data)
			break
		}
		end += i + 1
	}
	return data[:end]
}

func lastLines(data []byte, n int) []byte {
	if n == 0 || len(data) == 0 {
		return nil
	}
	end := len(data)
	if data[end-1] == '\n' {
		end--
	}
	start := end
	for range n {
		i := bytes.LastIndexByte(data[:start], '\n')
		if i < 0 {
			return data
		}
		start = i
	}
	return data[start+1:]
}
