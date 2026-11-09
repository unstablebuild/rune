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

// Package hostenv owns the process environment of a Rune host: the
// machine-local meaning of $RUNE_DATADIR, the gui.env overlay, Rune's PATH
// entries, and the shell fragments that re-apply them in terminals.
package hostenv

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// DataDirVar is the workspace-scoped variable naming the Rune data
// directory of the host that uses a value. Config keeps it literal; each
// host expands it when it starts a command or applies an environment.
const DataDirVar = "RUNE_DATADIR"

// ExpandVars expands only the variables for which lookup returns ok; every
// other $VAR / ${VAR} reference is left verbatim. s is parsed as a single
// shell word so brace forms are handled faithfully and any span that is
// not replaced is copied unchanged, including escaped dollars (\$X). A
// ${VAR...} with an operator is replaced by the plain value of VAR. If s
// does not parse, it is returned unchanged.
func ExpandVars(s string, lookup func(name string) (string, bool)) string {
	if !strings.Contains(s, "$") {
		return s
	}
	word, err := syntax.NewParser().Document(strings.NewReader(s))
	if err != nil || word == nil {
		return s
	}
	var b strings.Builder
	pos := 0
	syntax.Walk(word, func(node syntax.Node) bool {
		pe, ok := node.(*syntax.ParamExp)
		if !ok || pe.Param == nil {
			return true
		}
		val, ok := lookup(pe.Param.Value)
		if !ok {
			return true
		}
		start := int(pe.Pos().Offset())
		end := int(pe.End().Offset())
		if start < pos || end > len(s) {
			return true
		}
		b.WriteString(s[pos:start])
		b.WriteString(val)
		pos = end
		return true
	})
	b.WriteString(s[pos:])
	return b.String()
}

// ExpandDataDir replaces $RUNE_DATADIR and ${RUNE_DATADIR} in s with
// dataDir and leaves every other reference literal. An empty dataDir
// leaves s unchanged.
func ExpandDataDir(s, dataDir string) string {
	if dataDir == "" || !strings.Contains(s, DataDirVar) {
		return s
	}
	return ExpandVars(s, func(name string) (string, bool) {
		return dataDir, name == DataDirVar
	})
}
