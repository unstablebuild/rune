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
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// errLSP fails every ExecuteRequest with a fixed error.
type errLSP struct {
	noopLSP
	err error
}

func (l *errLSP) Initialize(
	_ context.Context, _ semanticapi.InitializeParams,
) (semanticapi.InitializeResult, error) {
	return semanticapi.InitializeResult{}, nil
}

func (l *errLSP) ExecuteRequest(
	_ context.Context, _ semanticapi.ExecuteRequestParams,
) (json.RawMessage, error) {
	return nil, l.err
}

func TestExecRequestStripsGRPCStatusNoise(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "grpc status error",
			err: status.Error(codes.Unknown,
				"request handler panicked: index out of bounds: the len is 1 but the index is 18"),
			want: "rust-analyzer/viewMir: request handler panicked: " +
				"index out of bounds: the len is 1 but the index is 18",
		},
		{
			name: "plain error",
			err:  errors.New("connection lost"),
			want: "rust-analyzer/viewMir: connection lost",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := execRequest[string](
				context.Background(), &errLSP{err: tc.err}, "rust-analyzer/viewMir", nil)
			require.Error(t, err)
			assert.Equal(t, tc.want, err.Error())
			assert.NotContains(t, err.Error(), "rpc error")
		})
	}
}
