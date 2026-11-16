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

package ide

import (
	"unstable.build/rune/internal/ide/starlarkconfig"
)

// starlarkConfigSource is a thin in-package alias over starlarkconfig.Source so
// existing call sites and tests can keep using lowercase fields.
type starlarkConfigSource struct {
	src      []byte
	filename string
	params   map[string]any
	base     map[string]any
	// include is starlarkconfig.Source.Include.
	include func(path string, current map[string]any) (map[string]any, error)
}

// decodeStarlarkConfig parses and executes src via the shared
// starlarkconfig decoder. See starlarkconfig.Source for the two modes.
func decodeStarlarkConfig(s starlarkConfigSource) (map[string]any, error) {
	filename := s.filename
	if filename == "" {
		filename = "rune.star"
	}
	return starlarkconfig.Decode(starlarkconfig.Source{
		Src:      s.src,
		Filename: filename,
		Params:   s.params,
		Base:     s.base,
		Include:  s.include,
	})
}
