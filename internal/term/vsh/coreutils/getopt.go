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

// Package coreutils implements the file and text utilities the Rune
// shell runs in-process against a workspace filesystem.
package coreutils

import (
	"fmt"
	"strings"

	"unstable.build/rune/internal/term/vsh"
)

// options is a parsed command line: short flags, the values of the
// flags that take one, and the operands. values holds the last value
// of a repeated flag and lists every one, in order.
type options struct {
	flags    map[byte]bool
	values   map[byte]string
	lists    map[byte][]string
	longs    map[string]string
	operands []string
}

func (o *options) has(flag byte) bool { return o.flags[flag] }

func (o *options) set(flag byte, v string) {
	o.values[flag] = v
	o.lists[flag] = append(o.lists[flag], v)
}

// usageError is an option error: GNU utilities report it with status
// 2 after the message.
type usageError struct {
	name, msg string
}

func (e *usageError) Error() string { return e.msg }

// parseArgs parses args as getopt does. spec lists the short flags,
// each followed by ":" when it takes a value; longs lists the long
// options accepted, each followed by "=" when it takes a value. A
// bare numeric option like -5 is accepted as the value of numeric
// when numeric is not 0. "--" ends the options; a lone "-" is an
// operand.
func parseArgs(name string, args []string, spec string, numeric byte, longs ...string) (*options, error) {
	takesValue := map[byte]bool{}
	for i := 0; i < len(spec); i++ {
		c := spec[i]
		if i+1 < len(spec) && spec[i+1] == ':' {
			takesValue[c] = true
			i++
		} else {
			takesValue[c] = false
		}
	}
	longTakesValue := map[string]bool{}
	for _, l := range longs {
		if n, ok := strings.CutSuffix(l, "="); ok {
			longTakesValue[n] = true
		} else {
			longTakesValue[l] = false
		}
	}
	o := &options{flags: map[byte]bool{}, values: map[byte]string{}, lists: map[byte][]string{}, longs: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			o.operands = append(o.operands, args[i+1:]...)
			return o, nil
		case strings.HasPrefix(a, "--"):
			k, v, hasV := strings.Cut(a[2:], "=")
			tv, ok := longTakesValue[k]
			if !ok {
				return nil, &usageError{name, fmt.Sprintf("unrecognized option '%s'", a)}
			}
			if tv && !hasV {
				if i+1 >= len(args) {
					return nil, &usageError{name, fmt.Sprintf("option '%s' requires an argument", a)}
				}
				i++
				v = args[i]
			}
			o.longs[k] = v
		case len(a) > 1 && a[0] == '-':
			if numeric != 0 && a[1] >= '0' && a[1] <= '9' {
				o.set(numeric, a[1:])
				continue
			}
			for j := 1; j < len(a); j++ {
				c := a[j]
				tv, ok := takesValue[c]
				if !ok {
					return nil, &usageError{name, fmt.Sprintf("invalid option -- '%c'", c)}
				}
				if !tv {
					o.flags[c] = true
					continue
				}
				if j+1 < len(a) {
					o.set(c, a[j+1:])
				} else {
					if i+1 >= len(args) {
						return nil, &usageError{name, fmt.Sprintf("option requires an argument -- '%c'", c)}
					}
					i++
					o.set(c, args[i])
				}
				break
			}
		default:
			o.operands = append(o.operands, a)
		}
	}
	return o, nil
}

// usage reports a usage error: the message on Stderr and status 2.
func usage(inv *vsh.Invocation, err error) error {
	fmt.Fprintf(inv.Stderr, "%s: %v\n", inv.Name, err)
	return &vsh.ExitError{Code: 2}
}
