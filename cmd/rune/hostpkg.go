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

package main

import (
	"context"
	"sync/atomic"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/pkgtrust"
)

// newHostPackageManager builds the manager a host with no editor of
// its own serves to the clients installing packages on it. applyEnv
// re-applies the user config's gui.env to this process after a package
// changed it, so commands started afterwards see the package's
// toolchain. The caller must Close the returned storage.
func newHostPackageManager(
	rootStorage storageapi.Service, releases release.Manager,
	scheme schemeapi.Scheme, applyEnv func(),
) (*idepkg.Manager, storageapi.Service) {
	trust := pkgtrust.NewStore(*flagDataPath, trustKeyringFetcher())
	mgr, storage := idepkg.NewProvisioningManager(
		rootStorage, releases, scheme,
		*flagDataPath, *flagConfigPath, resolveRemoteEditorMode(),
		defaultConfigTree, trust,
		idepkg.WithAfterConfigMerge(func(
			event idepkg.ConfigMergeEvent,
		) (idepkg.ConfigMergeResult, error) {
			if !event.TouchesPath("gui", "env") {
				return idepkg.ConfigMergeResult{}, nil
			}
			applyEnv()
			return idepkg.ConfigMergeResult{LiveApplied: true}, nil
		}))
	if err := mgr.Reconcile(context.Background()); err != nil {
		log.Warnf("packages: clean up incomplete installs: %v", err)
	}
	return mgr, storage
}

// defaultConfigTree is the config base package .star configs are read
// against; it must match the editor's.
func defaultConfigTree() map[string]any {
	tree, err := ide.DefaultConfigTree(runeDefaultConfig())
	if err != nil {
		log.Warnf("packages: decode default config tree: %v", err)
		return nil
	}
	return tree
}

// servedPackages is the package manager a network node serves to its
// peers. A node can join the mesh before the editor that owns this
// machine's packages exists, so requests fail with codes.Unavailable
// until set is called.
type servedPackages struct {
	pm atomic.Pointer[idepkg.PackageManager]
}

var _ idepkg.PackageManager = (*servedPackages)(nil)

func (s *servedPackages) set(pm idepkg.PackageManager) {
	s.pm.Store(&pm)
}

func (s *servedPackages) manager() (idepkg.PackageManager, error) {
	if pm := s.pm.Load(); pm != nil {
		return *pm, nil
	}
	return nil, status.Error(codes.Unavailable,
		"Rune on this machine is still starting")
}

func (s *servedPackages) LibDir(
	ctx context.Context, pkgID string,
) (iterator.Iterator[string], error) {
	pm, err := s.manager()
	if err != nil {
		return nil, err
	}
	return pm.LibDir(ctx, pkgID)
}

func (s *servedPackages) LatestVersion(
	ctx context.Context, pkgID string,
) (release.Version, error) {
	pm, err := s.manager()
	if err != nil {
		return "", err
	}
	return pm.LatestVersion(ctx, pkgID)
}

func (s *servedPackages) DescribePackage(
	ctx context.Context, pkgID string,
) (release.Package, error) {
	pm, err := s.manager()
	if err != nil {
		return release.Package{}, err
	}
	return pm.DescribePackage(ctx, pkgID)
}

func (s *servedPackages) DescribeRelease(
	ctx context.Context, pkgID, version string,
) (release.Bundle, error) {
	pm, err := s.manager()
	if err != nil {
		return release.Bundle{}, err
	}
	return pm.DescribeRelease(ctx, pkgID, version)
}

func (s *servedPackages) ListPackages(
	ctx context.Context, filters map[string]string,
) (iterator.Iterator[release.Package], error) {
	pm, err := s.manager()
	if err != nil {
		return nil, err
	}
	return pm.ListPackages(ctx, filters)
}

func (s *servedPackages) ListPackageVersions(
	ctx context.Context, pkgID string, filters map[string]string,
) (iterator.Iterator[release.Bundle], error) {
	pm, err := s.manager()
	if err != nil {
		return nil, err
	}
	return pm.ListPackageVersions(ctx, pkgID, filters)
}

func (s *servedPackages) ListInstalledPackages(
	ctx context.Context,
) (iterator.Iterator[string], error) {
	pm, err := s.manager()
	if err != nil {
		return nil, err
	}
	return pm.ListInstalledPackages(ctx)
}

func (s *servedPackages) ListInstalledPackageVersions(
	ctx context.Context, pkgID string,
) (iterator.Iterator[release.Version], error) {
	pm, err := s.manager()
	if err != nil {
		return nil, err
	}
	return pm.ListInstalledPackageVersions(ctx, pkgID)
}

func (s *servedPackages) InstallPackageVersion(
	ctx context.Context, pkgID string, version release.Version,
	pw repl.ProgressWriter,
) error {
	pm, err := s.manager()
	if err != nil {
		return err
	}
	return pm.InstallPackageVersion(ctx, pkgID, version, pw)
}

func (s *servedPackages) UsePackageVersion(
	ctx context.Context, pkgID string, version release.Version,
) error {
	pm, err := s.manager()
	if err != nil {
		return err
	}
	return pm.UsePackageVersion(ctx, pkgID, version)
}

func (s *servedPackages) DeletePackage(ctx context.Context, pkgID string) error {
	pm, err := s.manager()
	if err != nil {
		return err
	}
	return pm.DeletePackage(ctx, pkgID)
}

func (s *servedPackages) DeletePackageVersion(
	ctx context.Context, pkgID string, version release.Version, force bool,
) error {
	pm, err := s.manager()
	if err != nil {
		return err
	}
	return pm.DeletePackageVersion(ctx, pkgID, version, force)
}

func (s *servedPackages) PackageVersionInUse(
	ctx context.Context, pkgID string,
) (release.Version, bool, error) {
	pm, err := s.manager()
	if err != nil {
		return "", false, err
	}
	return pm.PackageVersionInUse(ctx, pkgID)
}
