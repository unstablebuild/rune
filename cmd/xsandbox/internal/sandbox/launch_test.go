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

package sandbox

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	blueauth "github.com/unstablebuild/blue/auth"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi/workspacerpc"
	"unstable.build/rune/internal/ide/ideauthorizer"
)

func TestSandboxAuthorizer(t *testing.T) {
	t.Parallel()

	claims := blueauth.UserClaims[ideauthorizer.Extension]{Extra: ideauthorizer.Extension{
		Metadata: extensionapi.Metadata{
			Permissions: extensionapi.NewPermissions(extensionapi.PermissionExecute),
		},
	}}
	cases := []struct {
		name     string
		resource string
		wantErr  error
	}{
		{name: "declared", resource: workspacerpc.Executor_StartCommand_FullMethodName},
		{name: "undeclared", resource: workspacerpc.Files_Stat_FullMethodName,
			wantErr: blueauth.ErrForbidden},
		{name: "unknown", resource: "/unknown.Service/Method", wantErr: blueauth.ErrForbidden},
	}
	for _, tc := range cases {
		err := sandboxAuthorizer{}.Authorize(context.Background(), claims, tc.resource)
		if tc.wantErr != nil {
			assert.ErrorIs(t, err, tc.wantErr, tc.name)
		} else {
			assert.NoError(t, err, tc.name)
		}
	}
	assert.NoError(t, sandboxAuthorizer{}.AuthorizeCommand(context.Background(),
		workspaceapi.Cmd{Path: "/tmp/evil/git", Env: []string{"LD_PRELOAD=x"}}))
}
