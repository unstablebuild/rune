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

	"github.com/unstablebuild/rune-go-sdk/api/acpapi"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi/acprpc"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"unstable.build/rune/internal/rpc"
)

// ACPResources returns a map of Permission to a ResourceRegistrar
// capable of serving the workspace's ACP agents to extensions and plugins.
func ACPResources(svc acpapi.Service) map[extensionapi.Permission]ResourceRegistrar {
	return map[extensionapi.Permission]ResourceRegistrar{
		extensionapi.PermissionACP: acpResourceServer{svc: svc},
	}
}

type acpResourceServer struct {
	svc acpapi.Service
}

func (s acpResourceServer) Register(
	registrar rpc.ServiceRegistrar, _ sync.Locker,
) (io.Closer, error) {
	acprpc.RegisterACPServer(registrar, acprpc.NewServer(s.svc))
	return nopCloser{}, nil
}
