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

//go:build windows

package ideupgrade

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
)

func defaultInstallRoot() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Rune")
}
func defaultAppName() string          { return "rune" }
func defaultCLIBinaryRelPath() string { return "rune.exe" }

type windowsPlatformOps struct{ httpClient *http.Client }

func newPlatformOps(client *http.Client) platformOps {
	return windowsPlatformOps{httpClient: client}
}

func (w windowsPlatformOps) Download(ctx context.Context, url, dest string, progress func(n, total int64)) error {
	return downloadOver(ctx, w.httpClient, url, dest, progress)
}
func (windowsPlatformOps) VerifySHA256(path, want string) error {
	return verifySHA256(path, want)
}
func (windowsPlatformOps) MountDMG(context.Context, string) (string, func() error, error) {
	return "", nil, ErrUnsupported
}
func (windowsPlatformOps) AssessGatekeeper(context.Context, string) error {
	return ErrUnsupported
}
func (windowsPlatformOps) VerifyCodesign(context.Context, string) error {
	return ErrUnsupported
}
func (windowsPlatformOps) Ditto(context.Context, string, string) error { return ErrUnsupported }
func (windowsPlatformOps) ExtractTarGz(context.Context, string, string, func(int64, int64)) error {
	return ErrUnsupported
}
func (windowsPlatformOps) Symlink(target, linkPath string) error {
	return os.Symlink(target, linkPath)
}
func (windowsPlatformOps) RenameAtomic(src, dst string) error { return os.Rename(src, dst) }
func (windowsPlatformOps) RemoveAll(path string) error        { return os.RemoveAll(path) }
func (windowsPlatformOps) FreeSpace(string) (uint64, error)   { return 1 << 40, nil }
