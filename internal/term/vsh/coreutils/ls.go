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
	"context"
	"fmt"
	"io/fs"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"unstable.build/rune/internal/term/vsh"
)

// Ls is "ls [-1aAdFlhR] [FILE...]". Output is in columns on a
// terminal and one name per line otherwise.
var Ls = vsh.Command{Name: "ls", Synopsis: "ls [-1aAdFlhR] [FILE...]", Run: ls}

type lsOptions struct {
	one, all, almostAll, dirs, classify, long, human, recursive bool
	width                                                       int
}

// lsEntry is one listed name with the stat long listings need.
type lsEntry struct {
	name string
	path string
	info fs.FileInfo
}

func ls(ctx context.Context, inv *vsh.Invocation) error {
	o, err := parseArgs(inv.Name, inv.Args, "1aAdFlhR", 0)
	if err != nil {
		return usage(inv, err)
	}
	opts := lsOptions{
		one: o.has('1'), all: o.has('a'), almostAll: o.has('A'), dirs: o.has('d'),
		classify: o.has('F'), long: o.has('l'), human: o.has('h'), recursive: o.has('R'),
	}
	if inv.Term != nil {
		opts.width, _ = inv.Term.Size()
	}
	operands := o.operands
	if len(operands) == 0 {
		operands = []string{"."}
	}
	slices.Sort(operands)
	failed := false
	var files []lsEntry
	var dirs []lsEntry
	for _, name := range operands {
		info, err := inv.FS.Stat(inv.Abs(name))
		if err != nil {
			fail(inv, name, err, &failed)
			continue
		}
		e := lsEntry{name: name, path: inv.Abs(name), info: info}
		if info.IsDir() && !opts.dirs {
			dirs = append(dirs, e)
		} else {
			files = append(files, e)
		}
	}
	if len(files) > 0 {
		if err := opts.list(inv, files); err != nil {
			return err
		}
	}
	for i, d := range dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(operands) > 1 || opts.recursive {
			sep := ""
			if i > 0 || len(files) > 0 {
				sep = "\n"
			}
			if _, err := fmt.Fprintf(inv.Stdout, "%s%s:\n", sep, d.name); err != nil {
				return err
			}
		}
		if err := opts.listDir(ctx, inv, d, &failed); err != nil {
			return err
		}
	}
	return status(failed)
}

func (o *lsOptions) listDir(ctx context.Context, inv *vsh.Invocation, d lsEntry, failed *bool) error {
	entries, err := sortedDir(inv.FS, d.path)
	if err != nil {
		fail(inv, d.name, err, failed)
		return nil
	}
	var list []lsEntry
	var subdirs []lsEntry
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !o.all && !o.almostAll {
			continue
		}
		p := path.Join(d.path, name)
		info, err := inv.FS.Stat(p)
		if err != nil {
			info, _ = e.Info()
		}
		le := lsEntry{name: name, path: p, info: info}
		list = append(list, le)
		if o.recursive && info.IsDir() {
			subdirs = append(subdirs, lsEntry{name: path.Join(d.name, name), path: p, info: info})
		}
	}
	if err := o.list(inv, list); err != nil {
		return err
	}
	for _, sd := range subdirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(inv.Stdout, "\n%s:\n", sd.name); err != nil {
			return err
		}
		if err := o.listDir(ctx, inv, sd, failed); err != nil {
			return err
		}
	}
	return nil
}

func (o *lsOptions) display(e lsEntry) string {
	name := e.name
	if o.classify && e.info != nil {
		switch {
		case e.info.IsDir():
			name += "/"
		case isExecutable(e.info):
			name += "*"
		}
	}
	return name
}

func (o *lsOptions) list(inv *vsh.Invocation, entries []lsEntry) error {
	if len(entries) == 0 {
		return nil
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = o.display(e)
	}
	switch {
	case o.long:
		return o.listLong(inv, entries, names)
	case o.one || o.width <= 0:
		for _, n := range names {
			if _, err := fmt.Fprintln(inv.Stdout, n); err != nil {
				return err
			}
		}
		return nil
	}
	_, err := inv.Stdout.Write([]byte(columns(names, o.width)))
	return err
}

// columns lays names out down columns that fit in width, as ls does
// on a terminal.
func columns(names []string, width int) string {
	if len(names) == 0 {
		return ""
	}
	maxw := 0
	for _, n := range names {
		maxw = max(maxw, utf8.RuneCountInString(n))
	}
	colw := maxw + 2
	cols := min(max(1, width/colw), len(names))
	rows := (len(names) + cols - 1) / cols
	cols = (len(names) + rows - 1) / rows
	var b strings.Builder
	for r := range rows {
		for c := range cols {
			i := c*rows + r
			if i >= len(names) {
				break
			}
			b.WriteString(names[i])
			if c < cols-1 && (c+1)*rows+r < len(names) {
				b.WriteString(strings.Repeat(" ", colw-utf8.RuneCountInString(names[i])))
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func (o *lsOptions) listLong(inv *vsh.Invocation, entries []lsEntry, names []string) error {
	sizes := make([]string, len(entries))
	sizew := 0
	for i, e := range entries {
		if e.info == nil {
			continue
		}
		sizes[i] = o.size(e.info.Size())
		sizew = max(sizew, len(sizes[i]))
	}
	for i, e := range entries {
		if e.info == nil {
			if _, err := fmt.Fprintf(inv.Stdout, "?????????? ? ? ? ? %s\n", names[i]); err != nil {
				return err
			}
			continue
		}
		_, err := fmt.Fprintf(inv.Stdout, "%s 1 rune rune %*s %s %s\n",
			vsh.FileMode(e.info).String(), sizew, sizes[i], modTime(e.info.ModTime()), names[i])
		if err != nil {
			return err
		}
	}
	return nil
}

func (o *lsOptions) size(n int64) string {
	if !o.human {
		return strconv.FormatInt(n, 10)
	}
	return humanSize(n)
}

// humanSize renders n as ls -h does: a unit suffix and one decimal
// below 10, rounded up so that a size never shows smaller than it is.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10)
	}
	suffixes := "KMGTPE"
	v := float64(n)
	i := -1
	for v >= unit && i < len(suffixes)-1 {
		v /= unit
		i++
	}
	if r := math.Ceil(v*10) / 10; r < 10 {
		return fmt.Sprintf("%.1f%c", r, suffixes[i])
	}
	if r := math.Ceil(v); r < unit || i == len(suffixes)-1 {
		return fmt.Sprintf("%.0f%c", r, suffixes[i])
	}
	return fmt.Sprintf("1.0%c", suffixes[i+1])
}

// modTime renders t as ls -l does for a recent file; a zero time, the
// memory scheme's default, shows as blanks of the same width.
func modTime(t time.Time) string {
	if t.IsZero() {
		return strings.Repeat(" ", len("Jan _2 15:04"))
	}
	return t.Format("Jan _2 15:04")
}
