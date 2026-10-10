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

package extension

import (
	"io"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
	"github.com/unstablebuild/rune-go-sdk/api/pkgapi/pkgrpc"
	"unstable.build/rune/internal/rpc"
)

// PackagesResources returns a map of Permission to a ResourceRegistrar
// capable of serving pkgs to extensions and plugins.
func PackagesResources(pkgs pkgapi.Manager) map[extensionapi.Permission]ResourceRegistrar {
	return map[extensionapi.Permission]ResourceRegistrar{
		extensionapi.PermissionPackages: packagesResourceServer{pkgs: pkgs},
	}
}

type packagesResourceServer struct {
	pkgs pkgapi.Manager
}

func (s packagesResourceServer) Register(
	registrar rpc.ServiceRegistrar, _ sync.Locker,
) (io.Closer, error) {
	pkgrpc.RegisterPackagesServer(registrar, pkgrpc.NewServer(s.pkgs))
	return nopCloser{}, nil
}
