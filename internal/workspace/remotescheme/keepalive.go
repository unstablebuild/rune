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

package remotescheme

import (
	"time"

	"google.golang.org/grpc/keepalive"
)

// ClientKeepalive is how often a tunneled gRPC client pings to detect a
// silently dead transport. [ServerEnforcement] must permit this cadence
// or the server sends GOAWAY too_many_pings and kills the connection
// (dropping terminals, invalidating cached pty fds, and forcing a
// reconnect).
var ClientKeepalive = keepalive.ClientParameters{
	Time:                10 * time.Second,
	Timeout:             5 * time.Second,
	PermitWithoutStream: true,
}

// ServerEnforcement permits [ClientKeepalive]: MinTime must be <=
// ClientKeepalive.Time and PermitWithoutStream must be true, since the
// client pings even when no RPC stream is active.
var ServerEnforcement = keepalive.EnforcementPolicy{
	MinTime:             5 * time.Second, // <= ClientKeepalive.Time
	PermitWithoutStream: true,
}
