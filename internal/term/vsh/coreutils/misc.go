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
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"mvdan.cc/sh/v3/expand"
	"unstable.build/rune/internal/term/vsh"
)

// Env is "env": it prints the exported environment.
var Env = vsh.Command{Name: "env", Synopsis: "env", Run: env}

// Sleep is "sleep NUMBER...", the sum of the numbers in seconds unless
// suffixed with s, m, h or d.
var Sleep = vsh.Command{Name: "sleep", Synopsis: "sleep NUMBER[smhd]...", Run: sleep}

// Tee is "tee [-a] [FILE...]".
var Tee = vsh.Command{Name: "tee", Synopsis: "tee [-a] [FILE...]", Run: tee}

// Clear is "clear": it clears the screen and the scrollback.
var Clear = vsh.Command{Name: "clear", Synopsis: "clear", Run: clear}

// Basename is "basename NAME [SUFFIX]".
var Basename = vsh.Command{Name: "basename", Synopsis: "basename NAME [SUFFIX]", Run: basename}

// Dirname is "dirname NAME...".
var Dirname = vsh.Command{Name: "dirname", Synopsis: "dirname NAME...", Run: dirname}

func env(_ context.Context, inv *vsh.Invocation) error {
	o, err := parseArgs(inv.Name, inv.Args, "", 0)
	if err != nil {
		return usage(inv, err)
	}
	if len(o.operands) > 0 {
		fmt.Fprintf(inv.Stderr, "%s: running a command is not supported; run it directly\n", inv.Name)
		return &vsh.ExitError{Code: 125}
	}
	var lines []string
	inv.Env.Each(func(name string, vr expand.Variable) bool {
		if vr.Exported && vr.Kind == expand.String {
			lines = append(lines, name+"="+vr.Str)
		}
		return true
	})
	slices.Sort(lines)
	for _, l := range lines {
		if _, err := fmt.Fprintln(inv.Stdout, l); err != nil {
			return err
		}
	}
	return nil
}

func sleep(ctx context.Context, inv *vsh.Invocation) error {
	o, err := parseArgs(inv.Name, inv.Args, "", 0)
	if err != nil {
		return usage(inv, err)
	}
	if len(o.operands) == 0 {
		return usage(inv, &usageError{inv.Name, "missing operand"})
	}
	var total time.Duration
	for _, a := range o.operands {
		d, err := parseSleep(a)
		if err != nil {
			return usage(inv, fmt.Errorf("invalid time interval '%s'", a))
		}
		total = min(total, math.MaxInt64-d) + d
	}
	timer := time.NewTimer(total)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func parseSleep(s string) (time.Duration, error) {
	unit := time.Second
	switch {
	case strings.HasSuffix(s, "s"):
		s = strings.TrimSuffix(s, "s")
	case strings.HasSuffix(s, "m"):
		s, unit = strings.TrimSuffix(s, "m"), time.Minute
	case strings.HasSuffix(s, "h"):
		s, unit = strings.TrimSuffix(s, "h"), time.Hour
	case strings.HasSuffix(s, "d"):
		s, unit = strings.TrimSuffix(s, "d"), 24*time.Hour
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) || !(f >= 0) {
		return 0, errors.New("invalid interval")
	}
	// Converting a float beyond the range of a Duration is
	// implementation-defined, and wraps to a negative one on amd64,
	// so longer sleeps, like inf, last as long as a Duration can.
	if d := f * float64(unit); d < math.MaxInt64 {
		return time.Duration(d), nil
	}
	return math.MaxInt64, nil
}

func tee(ctx context.Context, inv *vsh.Invocation) error {
	o, err := parseArgs(inv.Name, inv.Args, "ai", 0)
	if err != nil {
		return usage(inv, err)
	}
	data, err := io.ReadAll(&contextReader{ctx: ctx, r: inv.Stdin})
	if err != nil {
		return err
	}
	failed := false
	for _, name := range o.operands {
		if err := writeFile(inv, name, data, o.has('a'), 0o644); err != nil {
			fail(inv, name, err, &failed)
		}
	}
	if _, err := inv.Stdout.Write(data); err != nil {
		return err
	}
	return status(failed)
}

func clear(_ context.Context, inv *vsh.Invocation) error {
	_, err := inv.Stdout.Write([]byte("\x1b[H\x1b[2J\x1b[3J"))
	return err
}

func basename(_ context.Context, inv *vsh.Invocation) error {
	o, err := parseArgs(inv.Name, inv.Args, "", 0)
	if err != nil {
		return usage(inv, err)
	}
	switch len(o.operands) {
	case 0:
		return usage(inv, &usageError{inv.Name, "missing operand"})
	case 1, 2:
	default:
		return usage(inv, &usageError{inv.Name, fmt.Sprintf("extra operand '%s'", o.operands[2])})
	}
	name := path.Base(o.operands[0])
	if o.operands[0] == "" {
		name = ""
	}
	if len(o.operands) == 2 && name != o.operands[1] {
		name = strings.TrimSuffix(name, o.operands[1])
	}
	_, err = fmt.Fprintln(inv.Stdout, name)
	return err
}

func dirname(_ context.Context, inv *vsh.Invocation) error {
	o, err := parseArgs(inv.Name, inv.Args, "", 0)
	if err != nil {
		return usage(inv, err)
	}
	if len(o.operands) == 0 {
		return usage(inv, &usageError{inv.Name, "missing operand"})
	}
	for _, name := range o.operands {
		if _, err := fmt.Fprintln(inv.Stdout, dirOf(name)); err != nil {
			return err
		}
	}
	return nil
}

// dirOf is the directory part of name as POSIX dirname computes it:
// unlike path.Dir, trailing slashes do not count as a final empty
// component and the result is not cleaned.
func dirOf(name string) string {
	if name == "" {
		return "."
	}
	trimmed := strings.TrimRight(name, "/")
	if trimmed == "" {
		return "/"
	}
	i := strings.LastIndexByte(trimmed, '/')
	if i < 0 {
		return "."
	}
	if dir := strings.TrimRight(trimmed[:i], "/"); dir != "" {
		return dir
	}
	return "/"
}

// Commands returns every utility, for vsh.Config.Commands.
func Commands() []vsh.Command {
	return []vsh.Command{
		Basename, Cat, Clear, Cp, Dirname, Env, Grep, Head, Ls, Mkdir, Mv,
		Rm, Rmdir, Sleep, Tail, Tee, Touch, Wc,
	}
}
