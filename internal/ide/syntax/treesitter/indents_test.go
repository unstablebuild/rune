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
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"unstable.build/rune/internal/cell"
)

func TestGetPrevNonBlankLine(t *testing.T) {
	suite := []struct {
		at       uint
		expected uint
	}{
		{0, 0},
		{1, 0},
		{2, 2},
		{2, 2},
		{3, 3},
		{4, 3},
		{5, 5},
		{6, 6},
		{7, 6},
		{8, 8},
		{20, 20},
		{21, 20},
		{22, 0},
	}

	for _, test := range suite {
		t.Run(fmt.Sprintf("at %d", test.at), func(t *testing.T) {
			buf := cell.NewBuffer()
			_, _ = buf.ReadFrom(strings.NewReader(copy))
			actual := getPreviousNonBlankLine(buf, test.at)
			assert.Equal(t, test.expected, actual)
		})
	}
}

func TestNonEmptyColumns(t *testing.T) {
	suite := []struct {
		at       int
		expected int
	}{
		{0, 12},
		{1, 0},
		{2, 8},
		{3, 5},
		{4, 0},
		{5, 35},
		{6, 1},
		{12, 1},
		{13, 1},
		{20, 3},
		{21, 0},
		{22, 0},
	}

	for _, test := range suite {
		t.Run(fmt.Sprintf("at %d", test.at), func(t *testing.T) {
			buf := cell.NewBuffer()
			_, _ = buf.ReadFrom(strings.NewReader(copy))
			actual := nonEmptyColumns(buf, test.at)
			assert.Equal(t, test.expected, actual)
		})
	}
}

func TestGetCurrentIndentation(t *testing.T) {
	suite := []struct {
		at       uint
		expected int
	}{
		{0, 0},
		{1, 0},
		{2, 0},
		{3, 1},
		{4, 0},
		{5, 1},
		{6, 0},
		{12, 1},
		{13, 0},
		{20, 1},
		{21, 0},
		{22, 0},
	}

	for _, test := range suite {
		t.Run(fmt.Sprintf("at %d", test.at), func(t *testing.T) {
			buf := cell.NewBuffer()
			_, _ = buf.ReadFrom(strings.NewReader(copy))
			actual := getCurrentIndent(buf, test.at)
			assert.Equal(t, test.expected, actual)
		})
	}
}

func TestIsEmptyLine(t *testing.T) {
	suite := []struct {
		at       int
		expected bool
	}{
		{0, false},
		{1, true},
		{2, false},
		{3, false},
		{4, true},
		{5, false},
		{6, false},
		{12, false},
		{13, false},
		{20, false},
		{21, true},
		{22, false},
	}

	for _, test := range suite {
		t.Run(fmt.Sprintf("at %d", test.at), func(t *testing.T) {
			buf := cell.NewBuffer()
			_, _ = buf.ReadFrom(strings.NewReader(copy))
			actual := isEmptyLine(buf, test.at)
			assert.Equal(t, test.expected, actual)
		})
	}
}

func TestEdgeCaseIndentCopy2(t *testing.T) {
	t.Run("isEmptyLine", func(t *testing.T) {
		buf := cell.NewBuffer()
		_, _ = buf.ReadFrom(strings.NewReader(copy2))
		actual := isEmptyLine(buf, 10)
		assert.Equal(t, true, actual)
	})
	t.Run("getCurrentIndent", func(t *testing.T) {
		buf := cell.NewBuffer()
		_, _ = buf.ReadFrom(strings.NewReader(copy2))
		actual := getCurrentIndent(buf, 10)
		assert.Equal(t, 0, actual)
	})
	t.Run("nonEmptyColumns", func(t *testing.T) {
		buf := cell.NewBuffer()
		_, _ = buf.ReadFrom(strings.NewReader(copy2))
		actual := nonEmptyColumns(buf, 9)
		assert.Equal(t, 17, actual)
	})
	t.Run("getPreviousNonBlankLine", func(t *testing.T) {
		buf := cell.NewBuffer()
		_, _ = buf.ReadFrom(strings.NewReader(copy2))
		actual := getPreviousNonBlankLine(buf, 10)
		assert.Equal(t, uint(9), actual)
	})
}

const copy = `package main

import (
	"fmt"

	"github.com/unstablebuild/blue/cli"
)

func main() {
	fmt.Println("%+v", cli.NewCLI)
	for i := 0; i < 10; i++ {
		fmt.Println("%d", i)
	}
}

const fileContent = "package main\n" +
	"import (\n"+
	"\"fmt\"\n"+
	"\n"+
	"\"github.com/unstablebuild/blue/cli\"\n"
	")"
`

const copy2 = `package main

import (
	"fmt"

	"github.com/unstablebuild/blue/cli"
)

func main() {
	go debug(func() {

	fmt.Println("%+v", cli.NewCLI)
	for i := 0; i < 10; i++ {
		fmt.Println("%d", i)
	}
}
`
