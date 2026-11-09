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

// Package multipkg composes several release.Manager backends into one,
// dispatching each package operation to the backend that owns the
// package ID. Git IDs (<host>/<path>, e.g. github.com/owner/repo) route
// to a git-backed manager; everything else routes to the official
// distribution. A single composed manager lets idepkg.Manager and the
// rest of the package machinery stay unaware of where a package comes
// from — including a git package's `requirements`, which resolve
// through the official backend transparently.
package multipkg

import (
	"context"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"unstable.build/rune/internal/ide/idepkg/gitpkg"
)

// Manager is a release.Manager that routes by package ID.
type Manager struct {
	git      release.Manager
	official release.Manager
}

var _ release.Manager = (*Manager)(nil)

// New composes a git-backed manager and the official distribution
// manager into one release.Manager. Git package IDs route to git; all
// other IDs route to official.
func New(git, official release.Manager) *Manager {
	return &Manager{git: git, official: official}
}

// managerFor routes a package ID to its backend.
func (m *Manager) managerFor(pkgID string) release.Manager {
	if gitpkg.IsGitPkgID(pkgID) {
		return m.git
	}
	return m.official
}

// GetPackage satisfies release.Manager.
func (m *Manager) GetPackage(ctx context.Context, pkgID string) (release.Package, error) {
	return m.managerFor(pkgID).GetPackage(ctx, pkgID)
}

// List satisfies release.Manager.
func (m *Manager) List(
	ctx context.Context, pkgID string, filters map[string]string,
) (iterator.Iterator[release.Bundle], error) {
	return m.managerFor(pkgID).List(ctx, pkgID, filters)
}

// Get satisfies release.Manager.
func (m *Manager) Get(
	ctx context.Context, pkgID string, version release.Version,
	pw release.ProgressWriter,
) (release.Bundle, error) {
	return m.managerFor(pkgID).Get(ctx, pkgID, version, pw)
}

// Create satisfies release.Manager.
func (m *Manager) Create(ctx context.Context, pkg release.Package) error {
	return m.managerFor(pkg.Name).Create(ctx, pkg)
}

// UpdatePackageMetadata satisfies release.Manager.
func (m *Manager) UpdatePackageMetadata(
	ctx context.Context, pkgID string, metadata map[string]string,
) error {
	return m.managerFor(pkgID).UpdatePackageMetadata(ctx, pkgID, metadata)
}

// DeletePackage satisfies release.Manager.
func (m *Manager) DeletePackage(ctx context.Context, pkgID string) error {
	return m.managerFor(pkgID).DeletePackage(ctx, pkgID)
}

// Upload satisfies release.Manager.
func (m *Manager) Upload(
	ctx context.Context, bundle release.Bundle, pr release.ProgressReader,
) error {
	return m.managerFor(bundle.Package).Upload(ctx, bundle, pr)
}

// Delete satisfies release.Manager.
func (m *Manager) Delete(
	ctx context.Context, pkgID string, version release.Version,
) error {
	return m.managerFor(pkgID).Delete(ctx, pkgID, version)
}

// ListPackages satisfies release.Manager. GitHub repositories cannot be
// enumerated, so only the official distribution's packages are listed.
func (m *Manager) ListPackages(
	ctx context.Context, filters map[string]string,
) (iterator.Iterator[release.Package], error) {
	return m.official.ListPackages(ctx, filters)
}
