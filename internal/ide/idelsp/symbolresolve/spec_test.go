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

package symbolresolve_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"

	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
)

// specIter wraps a single spec in an iterator for engine-level tests that
// exercise one language directly.
func specIter(spec *symbolresolve.Spec) iterator.Iterator[symbolresolve.Spec] {
	return iterator.FromSlice([]symbolresolve.Spec{*spec})
}

func TestSpecFor(t *testing.T) {
	t.Parallel()

	assert.Same(t, symbolresolve.Go, symbolresolve.SpecFor("go"))
	assert.Same(t, symbolresolve.Python, symbolresolve.SpecFor("python"))
	assert.Same(t, symbolresolve.Rust, symbolresolve.SpecFor("rust"))
	assert.Same(t, symbolresolve.TypeScript, symbolresolve.SpecFor("typescript"))
	assert.Same(t, symbolresolve.TSX, symbolresolve.SpecFor("tsx"))
	assert.Same(t, symbolresolve.JavaScript, symbolresolve.SpecFor("javascript"))
	assert.Nil(t, symbolresolve.SpecFor("ruby"))
}

func TestNewQualifierContextNilFSPanics(t *testing.T) {
	t.Parallel()

	var root workspaceapi.URI
	assert.Panics(t, func() {
		symbolresolve.NewQualifierContext(nil, root)
	})
}
