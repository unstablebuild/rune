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

package treesitter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// closedTree simulates the window where a lazy iterator was handed out
// before the Tree was ready and Tree.Close freed the native
// tree-sitter objects before the iterator's first Next. The zero-value
// natives stand in for freed objects: any native call through them is
// a use-after-free in production and crashes here.
func closedTree(ready chan struct{}) *Tree {
	return &Tree{
		ready:        true,
		closed:       true,
		waitingReady: ready,
		tree:         &tree_sitter.Tree{},
		folds:        &tree_sitter.Query{},
	}
}

func TestNodesIteratorAfterTreeClose(t *testing.T) {
	ready := make(chan struct{})
	it := newNodesIterator(closedTree(ready), ready, "folds.scm")
	close(ready)

	_, ok := it.Next(context.Background())
	require.False(t, ok)
	assert.ErrorContains(t, it.Err(), "closed")
}

func TestFoldsIteratorAfterTreeClose(t *testing.T) {
	tests := []struct {
		name string
		make func(tree *Tree, ready chan struct{}) *foldsIterator
	}{
		{"folds", func(tree *Tree, ready chan struct{}) *foldsIterator {
			return newFoldsIterator(false, tree, ready)
		}},
		{"initial folds", func(tree *Tree, ready chan struct{}) *foldsIterator {
			return newFoldsIterator(true, tree, ready)
		}},
		{"folds from", func(tree *Tree, ready chan struct{}) *foldsIterator {
			return newFoldsFromIterator(term.Coordinates{Y: 1}, tree, ready)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready := make(chan struct{})
			it := tt.make(closedTree(ready), ready)
			close(ready)

			_, ok := it.Next(context.Background())
			assert.False(t, ok)
			assert.NoError(t, it.Err())
		})
	}
}
