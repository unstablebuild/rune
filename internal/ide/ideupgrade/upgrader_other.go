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

//go:build !linux && !darwin

package ideupgrade

import (
	"context"
	"net/http"
	"path/filepath"
)

// The defaults mirror linux so Config stays well formed. Nothing is ever written to them,
// because detectRunningInstallOS refuses every install on this platform.
func defaultInstallRoot() string      { return filepath.Join(homeDir(), ".local") }
func defaultAppName() string          { return "rune.app" }
func defaultCLIBinaryRelPath() string { return filepath.Join("bin", "rune") }

// unsupportedPlatformOps implements platformOps where in-place upgrades are not
// supported. runUpgrade only runs after detectRunningInstall succeeds, which it never
// does here, so every operation reports ErrUnsupported.
type unsupportedPlatformOps struct{}

func newPlatformOps(_ *http.Client) platformOps {
	return unsupportedPlatformOps{}
}

func (unsupportedPlatformOps) Download(
	_ context.Context, _, _ string, _ func(n, total int64),
) error {
	return ErrUnsupported
}

func (unsupportedPlatformOps) VerifySHA256(_, _ string) error {
	return ErrUnsupported
}

func (unsupportedPlatformOps) MountDMG(
	_ context.Context, _ string,
) (string, func() error, error) {
	return "", nil, ErrUnsupported
}

func (unsupportedPlatformOps) AssessGatekeeper(_ context.Context, _ string) error {
	return ErrUnsupported
}

func (unsupportedPlatformOps) VerifyCodesign(_ context.Context, _ string) error {
	return ErrUnsupported
}

func (unsupportedPlatformOps) Ditto(_ context.Context, _, _ string) error {
	return ErrUnsupported
}

func (unsupportedPlatformOps) ExtractTarGz(
	_ context.Context, _, _ string, _ func(n, total int64),
) error {
	return ErrUnsupported
}

func (unsupportedPlatformOps) Symlink(_, _ string) error {
	return ErrUnsupported
}

func (unsupportedPlatformOps) RenameAtomic(_, _ string) error {
	return ErrUnsupported
}

func (unsupportedPlatformOps) RemoveAll(_ string) error {
	return ErrUnsupported
}

func (unsupportedPlatformOps) FreeSpace(_ string) (uint64, error) {
	return 0, ErrUnsupported
}
