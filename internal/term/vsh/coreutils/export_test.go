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
	"errors"
	"io"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// The helpers below give the external tests access to internals that
// cannot be reached deterministically through a command.
var (
	Columns      = columns
	HumanSize    = humanSize
	ModTime      = modTime
	ParseSleep   = parseSleep
	FirstLines   = firstLines
	LastLines    = lastLines
	DisplayPath  = displayPath
	ErrMessage   = errMessage
	SortedDir    = sortedDir
	Walk         = walk
	ReadFile     = readFile
	WriteFile    = writeFile
	IsExecutable = isExecutable
	OpenRead     = openRead
	CheckParent  = checkParent
)

// Options is a parsed command line with its fields exported.
type Options struct {
	Flags    map[byte]bool
	Values   map[byte]string
	Lists    map[byte][]string
	Longs    map[string]string
	Operands []string
}

// ParseArgs is parseArgs with an exported result. usage reports
// whether the error is a usage error.
func ParseArgs(name string, args []string, spec string, numeric byte, longs ...string) (o Options, usage bool, err error) {
	p, err := parseArgs(name, args, spec, numeric, longs...)
	if err != nil {
		_, usage = errors.AsType[*usageError](err)
		return Options{}, usage, err
	}
	return Options{Flags: p.flags, Values: p.values, Lists: p.lists, Longs: p.longs, Operands: p.operands}, false, nil
}

// NewFileReader reads f from its start with its own offset.
func NewFileReader(f workspaceapi.File) io.ReadCloser { return &fileReader{f: f} }
