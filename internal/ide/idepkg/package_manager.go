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

package idepkg

import (
	"context"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
)

// PackageManager installs and inspects packages on a single host. Every
// path it returns is a path on that host, which need not be the machine
// the caller runs on.
//
// Implementations report failures with this package's sentinel errors
// (ErrNotInstalled, ErrPackageNotFound, ErrVersionNotFound,
// ErrAlreadyInstalled, ErrVersionInUse, ...) so callers can branch with
// errors.Is regardless of where the host is.
type PackageManager interface {
	// LibDir lists the absolute paths under the in-use version of pkgID.
	// It returns ErrNotInstalled when no version is installed. While an
	// install of pkgID is in flight, the iterator blocks until it ends.
	LibDir(ctx context.Context, pkgID string) (iterator.Iterator[string], error)
	// LatestVersion resolves the newest published version of pkgID for
	// the host's platform. It returns ErrPackageNotFound when the package
	// does not exist and ErrNoReleases when it has no published bundles.
	LatestVersion(ctx context.Context, pkgID string) (release.Version, error)
	DescribePackage(ctx context.Context, pkgID string) (release.Package, error)
	DescribeRelease(ctx context.Context, pkgID string, version string) (release.Bundle, error)
	ListPackages(ctx context.Context, filters map[string]string) (
		iterator.Iterator[release.Package], error)
	ListPackageVersions(ctx context.Context, pkgID string, filters map[string]string) (
		iterator.Iterator[release.Bundle], error)
	// ListInstalledPackages lists each fully installed package once.
	ListInstalledPackages(ctx context.Context) (iterator.Iterator[string], error)
	ListInstalledPackageVersions(ctx context.Context, pkgID string) (
		iterator.Iterator[release.Version], error)
	// InstallPackageVersion installs version of pkgID, and its
	// requirements, and makes it the in-use version. It blocks until the
	// install ends and returns ErrAlreadyInstalled when that version is
	// already fully installed. pw may be nil.
	InstallPackageVersion(ctx context.Context, pkgID string, version release.Version,
		pw repl.ProgressWriter) error
	// UsePackageVersion makes an installed version the in-use one. It
	// returns ErrNotInstalled when version is not fully installed and
	// ErrVersionInUse when it already is the in-use version.
	UsePackageVersion(ctx context.Context, pkgID string, version release.Version) error
	// DeletePackage removes every installed version of pkgID, returning
	// ErrNotInstalled when there is none.
	DeletePackage(ctx context.Context, pkgID string) error
	// DeletePackageVersion removes one installed version. Removing the
	// in-use version fails with ErrVersionInUse unless force is set.
	DeletePackageVersion(ctx context.Context, pkgID string, version release.Version,
		force bool) error
	// PackageVersionInUse reports the in-use version of pkgID, and false
	// when no version of it is in use. It never contacts the release
	// server.
	PackageVersionInUse(ctx context.Context, pkgID string) (release.Version, bool, error)
}

var _ PackageManager = (*Manager)(nil)
