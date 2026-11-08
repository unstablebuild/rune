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

package auth

import "net/url"

// MCPClientPath is the path at which the API server serves the OAuth
// client ID metadata document Rune Agent signs in to remote MCP servers
// with. The document's URL is Rune Agent's client ID, so the path must
// never change once authorization servers have seen it.
const MCPClientPath = "/o/oauth2/mcp-client.json"

// MCPClientID returns Rune Agent's MCP client ID at the API server api:
// the URL of its client ID metadata document.
func MCPClientID(api *url.URL) string {
	return api.JoinPath(MCPClientPath).String()
}
