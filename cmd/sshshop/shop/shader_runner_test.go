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

package shop

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/handler/handlertest"
)

type noopTUIHandler struct{}

func (noopTUIHandler) Resize(int, int)                {}
func (noopTUIHandler) Draw(term.Writer)               {}
func (noopTUIHandler) Handle(term.Event) (bool, bool) { return false, false }
func (noopTUIHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, 0, false
}
func (noopTUIHandler) Selection() (string, bool) { return "", false }

func TestNewShaderRootUsesProvidedInterrupter(t *testing.T) {
	var calls atomic.Int32
	interrupter := term.FuncInterrupter(func(context.Context) error {
		calls.Add(1)
		return nil
	})

	r := NewShaderRoot(noopTUIHandler{}, interrupter)
	defer func() { _ = r.Close() }()

	require.Eventually(t, func() bool {
		return calls.Load() > 0
	}, time.Second, 20*time.Millisecond,
		"startup shader should tick through the supplied interrupter")
}

func TestShaderRunnerPointerLeaves(t *testing.T) {
	t.Parallel()
	for _, size := range [][2]int{{60, 15}, {1, 1}} {
		root := NewRoot(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		r := NewShaderRoot(root, term.NopInterrupter())
		handlertest.RunPointerLeaves(t, r, size[0], size[1])
		require.NoError(t, r.Close())
		_ = root.Close()
	}
}
