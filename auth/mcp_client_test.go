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

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPClientID(t *testing.T) {
	for _, tt := range []struct{ api, want string }{
		{"https://api.rune.build", "https://api.rune.build/o/oauth2/mcp-client.json"},
		{"https://api.rune.build/", "https://api.rune.build/o/oauth2/mcp-client.json"},
		{"http://localhost:3001", "http://localhost:3001/o/oauth2/mcp-client.json"},
	} {
		t.Run(tt.api, func(t *testing.T) {
			api, err := url.Parse(tt.api)
			require.NoError(t, err)
			assert.Equal(t, tt.want, MCPClientID(api))
		})
	}
}
