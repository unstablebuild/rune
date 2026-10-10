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
	"strconv"

	log "github.com/sirupsen/logrus"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"unstable.build/rune/internal/cell"
)

/* NOTE: this is a port of nvim-treesitter's indents implementation,
   Licensed under Apache 2.0. Ported to Go, on October 2025.
*/

type indentCaptures struct {
	begin  map[uintptr]map[string]*string
	end    map[uintptr]map[string]*string
	branch map[uintptr]map[string]*string
	zero   map[uintptr]map[string]*string
	ignore map[uintptr]map[string]*string
	dedent map[uintptr]map[string]*string
	align  map[uintptr]map[string]*string
	auto   map[uintptr]map[string]*string
}

func newIndentCaptures() indentCaptures {
	return indentCaptures{
		begin:  map[uintptr]map[string]*string{},
		end:    map[uintptr]map[string]*string{},
		branch: map[uintptr]map[string]*string{},
		zero:   map[uintptr]map[string]*string{},
		ignore: map[uintptr]map[string]*string{},
		dedent: map[uintptr]map[string]*string{},
		align:  map[uintptr]map[string]*string{},
		auto:   map[uintptr]map[string]*string{},
	}
}

func (t *Tree) queryIndentCaptures() (ret indentCaptures) {
	ret = newIndentCaptures()
	root := t.tree.RootNode()

	cur := tree_sitter.NewQueryCursor()
	defer cur.Close()

	captureNames := t.indents.CaptureNames()
	matches := cur.Matches(t.indents, root, []byte(t.buf.String()))
	for {
		m, ok := matches.Next()
		if !ok {
			break
		}
		props := t.indents.PropertySettings(m.PatternIndex)
		propsMap := make(map[string]*string)
		for _, prop := range props {
			propsMap[prop.Key] = prop.Value
		}
		for _, cap := range m.Captures {
			name := captureNames[cap.Index]
			n := cap.Node
			switch name {
			case "indent.begin":
				ret.begin[n.Id()] = propsMap
			case "indent.dedent":
				ret.dedent[n.Id()] = propsMap
			case "indent.align":
				ret.align[n.Id()] = propsMap
			case "indent.auto":
				ret.auto[n.Id()] = propsMap
			case "indent.end":
				ret.end[n.Id()] = propsMap
			case "indent.branch":
				ret.branch[n.Id()] = propsMap
			case "indent.zero":
				ret.zero[n.Id()] = propsMap
			case "indent.ignore":
				ret.ignore[n.Id()] = propsMap
			default:
				// be forwards compatible with new indents.scm files
			}
		}
	}
	return
}

func (t *Tree) getIndentation(line uint) int {
	captures := t.queryIndentCaptures()
	isEmptyLine := isEmptyLine(t.buf, int(line))
	var node *tree_sitter.Node
	if isEmptyLine {
		prevLine := getPreviousNonBlankLine(t.buf, line)
		node = t.getLastNodeAtLine(prevLine)
		if node != nil {
			if _, ok := captures.end[node.Id()]; ok {
				node = t.getFirstNodeAtLine(line)
			}
		}
	} else {
		node = t.getFirstNodeAtLine(line)
	}
	if node == nil {
		t.log(log.DebugLevel, "get indentation aborted: nil node")
		return 0
	}

	if _, ok := captures.zero[node.Id()]; ok {
		t.log(log.DebugLevel, "get indentation aborted: node has zero property")
		return 0
	}

	indent := 0
	isProcessedByRow := make(map[uint]bool)
	for node != nil {
		beginMetadata, isBegin := captures.begin[node.Id()]
		_, isDedent := captures.dedent[node.Id()]
		alignMetadata, isAlign := captures.align[node.Id()]
		_, isAuto := captures.auto[node.Id()]
		_, isIgnore := captures.ignore[node.Id()]
		_, isBranch := captures.branch[node.Id()]
		rng := node.Range()
		startRow := rng.StartPoint.Row
		endRow := rng.EndPoint.Row

		if !isBegin && !isAlign && isAuto &&
			startRow < line &&
			line <= endRow {
			return -1
		}

		// do not indent inside ignore block
		if !isBegin && isIgnore &&
			startRow < line &&
			line <= endRow {
			return 0
		}

		shouldProcess := !isProcessedByRow[startRow]
		var isProcessed bool
		if shouldProcess &&
			((isBranch && startRow == line) || (isDedent && startRow == line)) {
			indent--
			isProcessed = true
		}

		isInErr := false
		if shouldProcess {
			parent := node.Parent()
			if parent != nil {
				isInErr = parent.HasError()
			}
		}

		_, beginHasImmediate := beginMetadata["indent.immediate"]
		_, beginHasStartAtSameLine := beginMetadata["indent.start_at_same_line"]
		if shouldProcess && isBegin &&
			(startRow != endRow || isInErr || beginHasImmediate) &&
			(startRow != line || beginHasStartAtSameLine) {
			indent++
			isProcessed = true
		}

		if isInErr && !isAlign {
			// only when the node is in error, promote the
			// first child's aligned indent to the error node
			cursor := node.Walk()
			defer cursor.Close()
			for _, child := range node.Children(cursor) {
				childAlignMetadata, childIsAlign := captures.align[child.Id()]
				if childIsAlign {
					captures.align[node.Id()] = childAlignMetadata
					break
				}
			}
		}

		if shouldProcess && isAlign && (endRow != startRow || isInErr) && startRow != line {
			metadata := alignMetadata
			var odelimNode *tree_sitter.Node
			var oIsLastLine bool
			var cdelimNode *tree_sitter.Node
			var cIsLastLine bool
			var indentIsAbsolute bool

			openDelimiter, isOpenDelimiter := metadata["indent.open_delimiter"]
			closeDelimiter, isCloseDelimiter := metadata["indent.close_delimiter"]

			if isOpenDelimiter && openDelimiter != nil {
				odelimNode, oIsLastLine = t.findDelimiter(node, *openDelimiter)
			} else {
				odelimNode = node
			}
			if isCloseDelimiter && closeDelimiter != nil {
				cdelimNode, cIsLastLine = t.findDelimiter(node, *closeDelimiter)
			} else {
				cdelimNode = node
			}

			if odelimNode == nil {
				isProcessedByRow[startRow] = isProcessedByRow[startRow] || isProcessed
				node = node.Parent()
				continue
			}

			osRow := odelimNode.Range().StartPoint.Row
			osCol := odelimNode.Range().StartPoint.Column
			var csRow *uint
			if cdelimNode != nil {
				csRow = new(uint)
				*csRow = cdelimNode.Range().StartPoint.Row
			}

			if oIsLastLine {
				if shouldProcess {
					indent++
					if cIsLastLine {
						if csRow != nil && *csRow < line {
							indent = max(indent-1, 0)
						}
					}
				}
			} else {
				if cIsLastLine && csRow != nil && *csRow != osRow && *csRow < line {
					indent = max(indent-1, 0)
				} else {
					var inc int
					incStr, ok := metadata["indent.increment"]
					if !ok || incStr == nil {
						inc = 1
					} else {
						var err error
						inc, err = strconv.Atoi(*incStr)
						if err != nil {
							inc = 1
						}
					}
					if inc < 0 {
						inc = 1
					}
					indent = int(osCol + uint(inc))
					indentIsAbsolute = true
				}
			}

			avoidLastMatchingNext := false
			if csRow != nil && *csRow != osRow && *csRow < line {
				_, avoidLastMatchingNext = metadata["indent.avoid_last_matching_next"]
			}

			if avoidLastMatchingNext {
				if indent <= getCurrentIndent(t.buf, osRow+1)+1 {
					indent++
				}
			}
			isProcessed = true
			if indentIsAbsolute {
				return indent
			}
		}

		isProcessedByRow[startRow] = isProcessedByRow[startRow] || isProcessed
		node = node.Parent()
	}

	return indent
}

func (t *Tree) findDelimiter(node *tree_sitter.Node, del string) (
	ret *tree_sitter.Node, isLastLine bool,
) {
	cursor := node.Walk()
	defer cursor.Close()
	for _, child := range node.Children(cursor) {
		if child.Kind() != del {
			continue
		}
		// NOTE: this is not very accurate (i.e. blank lines below last delim)
		end := child.EndPosition()
		rows := t.buf.Rows()
		return &child, end.Row == uint(rows)
	}

	return nil, false
}

func (t *Tree) getFirstNodeAtLine(line uint) *tree_sitter.Node {
	root := t.tree.RootNode()
	// we don't multiply by tabspaces because tree sitter point ranges
	// are based on byte columns.
	col := getCurrentIndent(t.buf, line)
	start := tree_sitter.Point{Row: line, Column: uint(col)}
	end := tree_sitter.Point{Row: line, Column: uint(col + 1)}
	return root.DescendantForPointRange(start, end)
}

func (t *Tree) getLastNodeAtLine(line uint) *tree_sitter.Node {
	indentCols := getCurrentIndent(t.buf, line)
	col := max(indentCols+nonEmptyColumns(t.buf, int(line))-1, 0)
	root := t.tree.RootNode()
	start := tree_sitter.Point{Row: line, Column: uint(col)}
	end := tree_sitter.Point{Row: line, Column: uint(col + 1)}
	return root.DescendantForPointRange(start, end)
}

// same semantics as vim's prevnonblank:
// return the line number of the first line at or above {lnum}
// that is not blank. A small difference:
// lnum is one-indexed, and line is zero-indexed.
func getPreviousNonBlankLine(buf *cell.Buffer, line uint) uint {
	if line == 0 || line >= uint(buf.Rows()) {
		return 0
	}

	for i := int(line); i >= 0; i-- {
		if !isEmptyLine(buf, i) {
			return uint(i)
		}
	}

	// if there's none, return current line
	return line
}

func getCurrentIndent(buf *cell.Buffer, line uint) (ret int) {
	cells := buf.RawCells()
	if line >= uint(len(cells)) {
		return 0
	}
	for _, cell := range cells[line] {
		if cell.Ch != '\t' {
			break
		}
		ret++
	}
	return
}

func nonEmptyColumns(buf *cell.Buffer, line int) (ret int) {
	cells := buf.RawCells()
	if line >= len(cells) {
		return 0
	}

	// trim start
	firstNonEmpty := -1
	for i, cell := range cells[line] {
		switch cell.Ch {
		case ' ', '\t':
			continue
		}
		firstNonEmpty = i
		break
	}
	if firstNonEmpty < 0 {
		return 0
	}

	// count middle
	lastNonEmpty := -1
	for i, cell := range cells[line][firstNonEmpty:] {
		switch cell.Ch {
		case ' ', '\t':
			ret++
		default:
			ret++
			lastNonEmpty = i
		}
	}
	lastNonEmpty += firstNonEmpty
	if lastNonEmpty < 0 {
		return ret
	}

	// trim end
	for _, cell := range cells[line][lastNonEmpty:] {
		switch cell.Ch {
		case ' ', '\t':
			ret--
		}
	}

	return
}

func isEmptyLine(buf *cell.Buffer, line int) bool {
	cells := buf.RawCells()
	if line >= len(cells) {
		return false
	}

	for _, cell := range cells[line] {
		switch cell.Ch {
		case ' ', '\t':
			continue
		}
		return false
	}
	return true
}

//nolint:unused
func nodeToString(buf *cell.Buffer, node *tree_sitter.Node) string {
	if node == nil {
		return "<nil>"
	}
	from, to := node.ByteRange()
	b := []byte(buf.String())
	return fmt.Sprintf("[%d,%d]: %s",
		node.Range().StartPoint, node.Range().EndPoint, string(b[from:to]))
}
