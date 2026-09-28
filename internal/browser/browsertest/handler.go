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

package browsertest

import (
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"unstable.build/rune/internal/browser"
)

// TestHandler is a testing Handler.
type TestHandler struct {
	handler.TestHandler
	CloseCallback func() error
	URIVal        workspaceapi.URI
}

// NewTestHandler allocates storage for a new TestHandler and initializes it.
func NewTestHandler() *TestHandler {
	ret := new(TestHandler)
	ret.TestHandler = *handler.NewTestHandler()
	return ret
}

// URI satisfies the identity OnTabExit matches window content by.
func (t *TestHandler) URI() workspaceapi.URI {
	return t.URIVal
}

// Close calls t.Close.
func (t *TestHandler) Close() error {
	if t.CloseCallback != nil {
		return t.CloseCallback()
	}
	return nil
}

var _ browser.Floating = (*TestFloating)(nil)

// TestFloating is a testing Handler.
type TestFloating struct {
	handler.TestFloating
}

// NewTestFloating allocates storage for a new TestHandler and initializes it.
func NewTestFloating(width, height int) *TestFloating {
	ret := new(TestFloating)
	ret.TestFloating = *handler.NewTestFloating(width, height)
	return ret
}

// Close satisfies browser.Floating.
func (t *TestFloating) Close() error {
	return nil
}
