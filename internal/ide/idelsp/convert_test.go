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

package idelsp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLSPCodeActionToSemanticGroup(t *testing.T) {
	t.Parallel()

	t.Run("group is preserved", func(t *testing.T) {
		t.Parallel()
		// A rust-analyzer response: two import actions sharing a group.
		raw := `[
			{"title":"Import from collections::HashMap","group":"import"},
			{"title":"Import from collections::BTreeMap","group":"import"}
		]`
		var decoded []lspCodeAction
		require.NoError(t, json.Unmarshal([]byte(raw), &decoded))

		first := lspCodeActionToSemantic(decoded[0])
		second := lspCodeActionToSemantic(decoded[1])
		assert.Equal(t, "import", first.Group)
		assert.Equal(t, "import", second.Group)
		assert.Equal(t, "Import from collections::HashMap", first.Title)
	})

	t.Run("absent group yields empty string", func(t *testing.T) {
		t.Parallel()
		// A plain LSP response from e.g. gopls has no group field.
		raw := `[{"title":"Organize Imports","kind":"source.organizeImports"}]`
		var decoded []lspCodeAction
		require.NoError(t, json.Unmarshal([]byte(raw), &decoded))

		action := lspCodeActionToSemantic(decoded[0])
		assert.Empty(t, action.Group)
		assert.Equal(t, "Organize Imports", action.Title)
	})
}
