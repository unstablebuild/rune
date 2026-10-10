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

package langext

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
)

// ErrNotShipped reports that the package is installed but does not ship
// the requested tool under its bin/.
var ErrNotShipped = errors.New("langext: tool not shipped by package")

// Installer reaches the Rune packages installed on the workspace host.
// It is satisfied by *extensionapi.Workspace.
type Installer interface {
	Packages(ctx context.Context) pkgapi.Manager
	FindInstalledExecutable(ctx context.Context, name string) (string, error)
}

// Tools finds the executables named in ProjectConfig.Tools in the
// language's Rune package on the workspace host. Nothing is looked up
// until the first Find, so a bring-up that never needs a packaged tool
// never asks the user to install the package. It is safe for concurrent
// use.
type Tools struct {
	inst  Installer
	pkgID string
	names []string

	mu    sync.Mutex
	done  bool
	paths map[string]string
	err   error
}

// Find returns the absolute path of name on the workspace host. The
// first call may wait for the user to decide whether to install the
// package; every later call reuses its result, unless ctx was done
// before it finished. name must be one of ProjectConfig.Tools.
//
// pkgapi.ErrNotInstalled means the package ended up not installed: the
// user declined, or it is not published for the host. Rune has already
// told the user why, so callers should fall back quietly. ErrNotShipped
// means the installed package lacks name, a packaging defect. Any other
// error means the lookup itself failed, say because the host is
// unreachable or the extension was denied package access, and the tool
// was not provisioned under the install root either.
func (t *Tools) Find(ctx context.Context, name string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.done {
		t.paths, t.err = resolveTools(ctx, t.inst, t.pkgID, t.names)
		// A lookup cut short by ctx says nothing about the package.
		t.done = ctx.Err() == nil
	}
	if p, ok := t.paths[name]; ok {
		return p, nil
	}
	if t.err != nil {
		return "", t.err
	}
	return "", fmt.Errorf("%s/bin/%s: %w", t.pkgID, name, ErrNotShipped)
}

// resolveTools finds names under bin/ in the files of package pkgID.
// paths holds the tools it found; err says why the rest could not be.
func resolveTools(
	ctx context.Context, inst Installer, pkgID string, names []string,
) (paths map[string]string, err error) {
	if len(names) == 0 {
		return nil, nil
	}
	paths, err = packageTools(ctx, inst.Packages(ctx), pkgID, names)
	if err == nil || errors.Is(err, pkgapi.ErrNotInstalled) {
		return paths, err
	}
	// A Rune that predates the packages service, or that denied the
	// extension package access, still provisions the tools under the
	// install root. A miss there reports the lookup failure.
	found, probeErr := installRootTools(ctx, inst, names)
	for name, p := range found {
		if _, ok := paths[name]; !ok {
			paths[name] = p
		}
	}
	return paths, errors.Join(err, probeErr)
}

// packageTools stops listing once every tool is found, since a
// toolchain package can hold tens of thousands of files.
func packageTools(
	ctx context.Context, pkgs pkgapi.Manager, pkgID string, names []string,
) (map[string]string, error) {
	tools := make(map[string]string, len(names))
	it, err := pkgs.LibDir(ctx, pkgID)
	if err != nil {
		return tools, err
	}
	defer func() { _ = it.Close() }()
	for len(tools) < len(names) {
		file, ok := it.Next(ctx)
		if !ok {
			return tools, it.Err()
		}
		name := path.Base(file)
		if path.Base(path.Dir(file)) != "bin" || !slices.Contains(names, name) {
			continue
		}
		if _, ok := tools[name]; !ok {
			tools[name] = file
		}
	}
	return tools, nil
}

func installRootTools(
	ctx context.Context, inst Installer, names []string,
) (map[string]string, error) {
	tools := make(map[string]string, len(names))
	var errs []error
	for _, name := range names {
		bin, err := inst.FindInstalledExecutable(ctx, name)
		switch {
		case err == nil:
			tools[name] = bin
		case !errors.Is(err, os.ErrNotExist):
			errs = append(errs, err)
		}
	}
	return tools, errors.Join(errs...)
}
