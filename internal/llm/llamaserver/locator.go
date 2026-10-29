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

package llamaserver

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// PackageID is the package the llama-server binary is installed from
// (`pkg install llama-server`).
const PackageID = "llama-server"

// MinPackageVersion is the oldest version of PackageID that runs every
// model Rune downloads: it ships llama.cpp b11972, and the search
// model's gemma-embedding2 architecture needs b11452 or later.
const MinPackageVersion = "v0.0.4"

// serverBinaryName is the executable name shipped by the llama-server
// package.
const serverBinaryName = "llama-server"

// ErrServerNotInstalled is returned by a BinaryLocator when the
// llama-server binary cannot be resolved via the package manager. The
// Service translates it into the user-facing install instruction.
var ErrServerNotInstalled = errors.New("llamaserver: llama-server binary not installed")

// BinaryLocator resolves the absolute path to the llama-server executable.
// Locate returns ErrServerNotInstalled when the binary is unavailable so the
// Service can distinguish "not installed" from real failures.
type BinaryLocator interface {
	Locate(ctx context.Context) (string, error)
}

// pkgLibDirLister is the subset of the package manager the locator needs. It
// mirrors ide's pkgManager.LibDir so the production locator takes a narrow,
// mockable dependency. LibDir resolves paths on the workspace host, which may
// be remote, so the locator must never touch the local filesystem or $PATH.
type pkgLibDirLister interface {
	LibDir(ctx context.Context, pkgID string) (iterator.Iterator[string], error)
}

// fixedLocator returns a preconfigured path; used when the config carries an
// explicit ServerBinPath override.
type fixedLocator struct{ path string }

// NewFixedLocator returns a BinaryLocator that always resolves to path.
func NewFixedLocator(path string) BinaryLocator { return fixedLocator{path: path} }

func (l fixedLocator) Locate(context.Context) (string, error) {
	if l.path == "" {
		return "", ErrServerNotInstalled
	}
	return l.path, nil
}

// pkgLocator resolves llama-server through the package manager. It never
// consults $PATH or the local filesystem because the workspace, and hence the
// server, may run on a remote host.
type pkgLocator struct {
	pkg pkgLibDirLister
}

// NewPkgLocator returns a BinaryLocator that resolves llama-server via the
// llama-server package's LibDir. It panics if pkg is nil.
func NewPkgLocator(pkg pkgLibDirLister) BinaryLocator {
	if pkg == nil {
		panic("llamaserver: NewPkgLocator: pkg must not be nil")
	}
	return &pkgLocator{pkg: pkg}
}

func (l *pkgLocator) Locate(ctx context.Context) (string, error) {
	it, err := l.pkg.LibDir(ctx, PackageID)
	if err != nil {
		return "", ErrServerNotInstalled
	}
	defer func() { _ = it.Close() }()
	paths, err := iterator.ToSlice(ctx, it)
	if err != nil {
		return "", ErrServerNotInstalled
	}
	// LibDir enumerates the files the package shipped on the workspace host,
	// so a base-name match is proof the binary exists there; the process is
	// launched through the (possibly remote) executor, never locally.
	for _, path := range paths {
		if filepath.Base(path) == serverBinaryName {
			return path, nil
		}
	}
	return "", ErrServerNotInstalled
}
