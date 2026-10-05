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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRole_String(t *testing.T) {
	cases := []struct {
		role Role
		want string
	}{
		{RoleBasic, "basic"},
		{RoleUser, "user"},
		{RolePaid, "paid"},
		{RoleAdmin, "admin"},
		{RoleSuperAdmin, "superadmin"},
		{RoleOneOff, "oneoff"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.role.String())
		})
	}
}

func TestRole_StringPanicsOnUnknown(t *testing.T) {
	assert.Panics(t, func() {
		_ = Role(99).String()
	})
}

func TestWebUserReadsServeOnlyRPCUser(t *testing.T) {
	for _, serveOnly := range []bool{true, false} {
		data, err := json.Marshal(RPCUser{ID: "auth0|abc", Role: RolePaid, ServeOnly: serveOnly})
		require.NoError(t, err)
		var web WebUser
		require.NoError(t, json.Unmarshal(data, &web))
		assert.Equal(t, serveOnly, web.ServeOnly)
	}
}
