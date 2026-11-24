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
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"unstable.build/rune/internal/term/vsh"
)

// Cp is "cp [-r] SOURCE... DEST".
var Cp = vsh.Command{Name: "cp", Synopsis: "cp [-r] SOURCE... DEST", Run: cp}

// Mv is "mv SOURCE... DEST". Directories move by copy then remove.
var Mv = vsh.Command{Name: "mv", Synopsis: "mv SOURCE... DEST", Run: mv}

// Rm is "rm [-rf] FILE...".
var Rm = vsh.Command{Name: "rm", Synopsis: "rm [-rf] FILE...", Run: rm}

// Mkdir is "mkdir [-p] DIR...".
var Mkdir = vsh.Command{Name: "mkdir", Synopsis: "mkdir [-p] DIR...", Run: mkdir}

// Rmdir is "rmdir DIR...".
var Rmdir = vsh.Command{Name: "rmdir", Synopsis: "rmdir DIR...", Run: rmdir}

// Touch is "touch FILE...".
var Touch = vsh.Command{Name: "touch", Synopsis: "touch FILE...", Run: touch}

// destinations resolves the operands of cp and mv: the sources and
// whether dest is an existing directory to copy into.
func destinations(inv *vsh.Invocation, operands []string) (sources []string, dest string, intoDir bool, err error) {
	if len(operands) < 2 {
		return nil, "", false, &usageError{inv.Name, "missing destination file operand"}
	}
	sources, dest = operands[:len(operands)-1], operands[len(operands)-1]
	info, serr := inv.FS.Stat(inv.Abs(dest))
	intoDir = serr == nil && info.IsDir()
	if len(sources) > 1 && !intoDir {
		return nil, "", false, &usageError{inv.Name, fmt.Sprintf("target '%s' is not a directory", dest)}
	}
	return sources, dest, intoDir, nil
}

// copyFile copies the regular file at src to dst.
func copyFile(inv *vsh.Invocation, src, dst string, info fs.FileInfo) error {
	data, err := readFile(inv.FS, src)
	if err != nil {
		return err
	}
	return writeFile(inv, dst, data, false, vsh.FileMode(info).Perm())
}

// copyTree copies src, a file or a directory, to dst.
func copyTree(ctx context.Context, inv *vsh.Invocation, src, dst string) error {
	return walk(ctx, inv.FS, src, func(p string, info fs.FileInfo) error {
		target := path.Join(dst, strings.TrimPrefix(p, src))
		if info.IsDir() {
			return inv.FS.MkdirAll(target, 0o755)
		}
		return copyFile(inv, p, target, info)
	})
}

func cp(ctx context.Context, inv *vsh.Invocation) error {
	o, err := parseArgs(inv.Name, inv.Args, "rRf", 0)
	if err != nil {
		return usage(inv, err)
	}
	sources, dest, intoDir, err := destinations(inv, o.operands)
	if err != nil {
		return usage(inv, err)
	}
	recursive := o.has('r') || o.has('R')
	failed := false
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		abs := inv.Abs(src)
		info, err := inv.FS.Stat(abs)
		if err != nil {
			fail(inv, src, err, &failed)
			continue
		}
		target, shown := inv.Abs(dest), dest
		if intoDir {
			target = path.Join(target, path.Base(abs))
			shown = strings.TrimSuffix(dest, "/") + "/" + path.Base(abs)
		}
		switch {
		case target == abs && !info.IsDir():
			fail(inv, "", fmt.Errorf("'%s' and '%s' are the same file", src, shown), &failed)
		case info.IsDir() && !recursive:
			fail(inv, src, errors.New("-r not specified; omitting directory"), &failed)
		case info.IsDir():
			if strings.HasPrefix(target+"/", abs+"/") {
				fail(inv, src, fmt.Errorf("cannot copy a directory, '%s', into itself, '%s'", src, dest), &failed)
				continue
			}
			if err := copyTree(ctx, inv, abs, target); err != nil {
				fail(inv, src, err, &failed)
			}
		default:
			data, err := readFile(inv.FS, abs)
			if err != nil {
				fail(inv, src, err, &failed)
				continue
			}
			if err := writeFile(inv, target, data, false, vsh.FileMode(info).Perm()); err != nil {
				fail(inv, fmt.Sprintf("cannot create regular file '%s'", shown), err, &failed)
			}
		}
	}
	return status(failed)
}

func mv(ctx context.Context, inv *vsh.Invocation) error {
	o, err := parseArgs(inv.Name, inv.Args, "f", 0)
	if err != nil {
		return usage(inv, err)
	}
	sources, dest, intoDir, err := destinations(inv, o.operands)
	if err != nil {
		return usage(inv, err)
	}
	failed := false
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		abs := inv.Abs(src)
		info, err := inv.FS.Stat(abs)
		if err != nil {
			fail(inv, src, err, &failed)
			continue
		}
		target := inv.Abs(dest)
		if intoDir {
			target = path.Join(target, path.Base(abs))
		}
		if target == abs {
			continue
		}
		if !info.IsDir() {
			if err := inv.FS.Rename(abs, target); err != nil {
				fail(inv, src, err, &failed)
			}
			continue
		}
		if strings.HasPrefix(target+"/", abs+"/") {
			fail(inv, src, fmt.Errorf("cannot move '%s' to a subdirectory of itself, '%s'", src, dest), &failed)
			continue
		}
		// A scheme that cannot rename a directory reports EISDIR; the
		// tree is then copied and removed.
		err = inv.FS.Rename(abs, target)
		if !errors.Is(err, syscall.EISDIR) {
			if err != nil {
				fail(inv, src, err, &failed)
			}
			continue
		}
		if err := copyTree(ctx, inv, abs, target); err != nil {
			fail(inv, src, err, &failed)
			continue
		}
		if err := removeTree(ctx, inv.FS, abs); err != nil {
			fail(inv, src, err, &failed)
		}
	}
	return status(failed)
}

// removeTree removes p and everything beneath it, children first.
func removeTree(ctx context.Context, fsys schemeapi.FileSystem, p string) error {
	var paths []string
	err := walk(ctx, fsys, p, func(p string, _ fs.FileInfo) error {
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return err
	}
	for _, p := range slices.Backward(paths) {
		if err := fsys.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func rm(ctx context.Context, inv *vsh.Invocation) error {
	o, err := parseArgs(inv.Name, inv.Args, "rRf", 0)
	if err != nil {
		return usage(inv, err)
	}
	if len(o.operands) == 0 {
		if o.has('f') {
			return nil
		}
		return usage(inv, &usageError{inv.Name, "missing operand"})
	}
	recursive, force := o.has('r') || o.has('R'), o.has('f')
	failed := false
	for _, name := range o.operands {
		if err := ctx.Err(); err != nil {
			return err
		}
		abs := inv.Abs(name)
		if recursive {
			if base := path.Base(name); base == "." || base == ".." {
				fmt.Fprintf(inv.Stderr, "%s: refusing to remove '.' or '..' directory: skipping '%s'\n", inv.Name, name)
				failed = true
				continue
			}
			if abs == "/" {
				fmt.Fprintf(inv.Stderr, "%s: it is dangerous to operate recursively on '%s'\n", inv.Name, name)
				fmt.Fprintf(inv.Stderr, "%s: use --no-preserve-root to override this failsafe\n", inv.Name)
				failed = true
				continue
			}
		}
		info, err := inv.FS.Stat(abs)
		if err != nil {
			if !force {
				fail(inv, name, err, &failed)
			}
			continue
		}
		switch {
		case info.IsDir() && !recursive:
			fail(inv, name, syscall.EISDIR, &failed)
		case info.IsDir():
			if err := removeTree(ctx, inv.FS, abs); err != nil {
				fail(inv, name, err, &failed)
			}
		default:
			if err := inv.FS.Remove(abs); err != nil {
				fail(inv, name, err, &failed)
			}
		}
	}
	return status(failed)
}

func mkdir(ctx context.Context, inv *vsh.Invocation) error {
	o, err := parseArgs(inv.Name, inv.Args, "pm:", 0)
	if err != nil {
		return usage(inv, err)
	}
	if len(o.operands) == 0 {
		return usage(inv, &usageError{inv.Name, "missing operand"})
	}
	failed := false
	for _, name := range o.operands {
		if err := ctx.Err(); err != nil {
			return err
		}
		abs := inv.Abs(name)
		if !o.has('p') {
			if _, err := inv.FS.Stat(abs); err == nil {
				fail(inv, fmt.Sprintf("cannot create directory '%s'", name), fs.ErrExist, &failed)
				continue
			}
			if _, err := inv.FS.Stat(path.Dir(abs)); err != nil {
				fail(inv, fmt.Sprintf("cannot create directory '%s'", name), err, &failed)
				continue
			}
		}
		if err := inv.FS.MkdirAll(abs, 0o755); err != nil {
			fail(inv, fmt.Sprintf("cannot create directory '%s'", name), err, &failed)
		}
	}
	return status(failed)
}

func rmdir(ctx context.Context, inv *vsh.Invocation) error {
	o, err := parseArgs(inv.Name, inv.Args, "p", 0)
	if err != nil {
		return usage(inv, err)
	}
	if len(o.operands) == 0 {
		return usage(inv, &usageError{inv.Name, "missing operand"})
	}
	failed := false
	for _, name := range o.operands {
		if err := ctx.Err(); err != nil {
			return err
		}
		// With -p, each parent named in the operand goes too, as in
		// "rmdir a/b/c a/b a", until one fails.
		for p, shown := path.Clean(name), name; ; p, shown = path.Dir(p), path.Dir(p) {
			if err := removeDir(inv, p); err != nil {
				fail(inv, fmt.Sprintf("failed to remove '%s'", shown), err, &failed)
				break
			}
			if parent := path.Dir(p); !o.has('p') || parent == "." || parent == "/" {
				break
			}
		}
	}
	return status(failed)
}

// removeDir removes the empty directory at p.
func removeDir(inv *vsh.Invocation, p string) error {
	abs := inv.Abs(p)
	info, err := inv.FS.Stat(abs)
	switch {
	case err != nil:
		return err
	case !info.IsDir():
		return syscall.ENOTDIR
	}
	return inv.FS.Remove(abs)
}

func touch(ctx context.Context, inv *vsh.Invocation) error {
	o, err := parseArgs(inv.Name, inv.Args, "", 0)
	if err != nil {
		return usage(inv, err)
	}
	if len(o.operands) == 0 {
		return usage(inv, &usageError{inv.Name, "missing file operand"})
	}
	failed := false
	for _, name := range o.operands {
		if err := ctx.Err(); err != nil {
			return err
		}
		abs := inv.Abs(name)
		if info, err := inv.FS.Stat(abs); err == nil && info.IsDir() {
			continue
		}
		if err := checkParent(inv.FS, abs); err != nil {
			fail(inv, fmt.Sprintf("cannot touch '%s'", name), err, &failed)
			continue
		}
		f, err := inv.FS.OpenFile(abs, os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			fail(inv, fmt.Sprintf("cannot touch '%s'", name), err, &failed)
			continue
		}
		if err := f.Sync(); err != nil {
			fail(inv, fmt.Sprintf("cannot touch '%s'", name), err, &failed)
		}
		_ = f.Close()
	}
	return status(failed)
}
