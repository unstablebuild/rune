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

package markdown

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// parse parses markdown content and returns a slice of block elements.
func parse(content string, cfg *Config) ([]block, error) {
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	source := []byte(content)
	doc := md.Parser().Parse(text.NewReader(source))

	walker := &blockWalker{source: source, cfg: cfg}
	if err := ast.Walk(doc, walker.walk); err != nil {
		return nil, err
	}
	return walker.blocks, nil
}

type blockWalker struct {
	source []byte
	cfg    *Config
	blocks []block
}

func (w *blockWalker) walk(n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}

	switch node := n.(type) {
	case *ast.Document:
		return ast.WalkContinue, nil

	case *ast.Heading:
		w.blocks = append(w.blocks, newHeaderBlock(
			node.Level,
			w.parseInlineContent(node),
			w.cfg,
		))
		return ast.WalkSkipChildren, nil

	case *ast.Paragraph:
		w.blocks = append(w.blocks, newParagraphBlock(
			w.parseInlineContent(node),
			w.cfg,
		))
		return ast.WalkSkipChildren, nil

	case *ast.FencedCodeBlock:
		var lang string
		if node.Info != nil {
			lang = string(node.Info.Segment.Value(w.source))
		}
		w.blocks = append(w.blocks, newCodeBlock(
			lang,
			w.extractCodeContent(node),
			w.cfg,
		))
		return ast.WalkSkipChildren, nil

	case *ast.CodeBlock:
		w.blocks = append(w.blocks, newCodeBlock(
			"",
			w.extractCodeContent(node),
			w.cfg,
		))
		return ast.WalkSkipChildren, nil

	case *ast.List:
		w.blocks = append(w.blocks, w.parseList(node))
		return ast.WalkSkipChildren, nil

	case *ast.Blockquote:
		w.blocks = append(w.blocks, w.parseBlockquote(node))
		return ast.WalkSkipChildren, nil

	case *ast.ThematicBreak:
		w.blocks = append(w.blocks, newHorizontalRuleBlock(w.cfg))
		return ast.WalkSkipChildren, nil

	case *extast.Table:
		w.blocks = append(w.blocks, w.parseTable(node))
		return ast.WalkSkipChildren, nil
	}

	return ast.WalkContinue, nil
}

func (w *blockWalker) parseInlineContent(n ast.Node) textRun {
	var run textRun
	w.walkInline(n, styleNone, &run)
	return run
}

func (w *blockWalker) walkInline(n ast.Node, style inlineStyle, run *textRun) {
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		switch node := child.(type) {
		case *ast.Text:
			text := string(node.Segment.Value(w.source))
			if node.SoftLineBreak() || node.HardLineBreak() {
				text += " "
			}
			*run = append(*run, span{text: text, style: style})

		case *ast.String:
			*run = append(*run, span{text: string(node.Value), style: style})

		case *ast.Emphasis:
			newStyle := style
			if node.Level == 1 {
				newStyle |= styleItalic
			} else {
				newStyle |= styleBold
			}
			w.walkInline(node, newStyle, run)

		case *ast.CodeSpan:
			*run = append(*run, span{
				text:  w.extractCodeSpanText(node),
				style: style | styleCode,
			})

		case *ast.Link:
			linkRun := textRun{}
			w.walkInline(node, style|styleLink, &linkRun)
			for i := range linkRun {
				linkRun[i].url = string(node.Destination)
			}
			*run = append(*run, linkRun...)

		case *ast.AutoLink:
			url := string(node.URL(w.source))
			*run = append(*run, span{
				text:  url,
				style: style | styleLink,
				url:   url,
			})

		case *extast.Strikethrough:
			w.walkInline(node, style|styleStrikethrough, run)

		default:
			w.walkInline(child, style, run)
		}
	}
}

func (w *blockWalker) extractCodeContent(n ast.Node) string {
	var buf []byte
	lines := n.Lines()
	for i := range lines.Len() {
		line := lines.At(i)
		buf = append(buf, line.Value(w.source)...)
	}
	return string(buf)
}

func (w *blockWalker) extractCodeSpanText(n *ast.CodeSpan) string {
	var buf []byte
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		if text, ok := child.(*ast.Text); ok {
			buf = append(buf, text.Segment.Value(w.source)...)
		}
	}
	return string(buf)
}

func (w *blockWalker) parseList(n *ast.List) *listBlock {
	var items []listItem

	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		if item, ok := child.(*ast.ListItem); ok {
			li := listItem{}
			if cb, ok := item.FirstChild().(*ast.TextBlock); ok {
				if taskCheckBox, isTask := cb.FirstChild().(*extast.TaskCheckBox); isTask {
					li.isTask = true
					li.checked = taskCheckBox.IsChecked
					li.content = w.parseInlineContentSkipFirst(cb)
				} else {
					li.content = w.parseInlineContent(cb)
				}
			} else if p, ok := item.FirstChild().(*ast.Paragraph); ok {
				if taskCheckBox, isTask := p.FirstChild().(*extast.TaskCheckBox); isTask {
					li.isTask = true
					li.checked = taskCheckBox.IsChecked
					li.content = w.parseInlineContentSkipFirst(p)
				} else {
					li.content = w.parseInlineContent(p)
				}
			}
			for nested := item.FirstChild(); nested != nil; nested = nested.NextSibling() {
				if nestedList, ok := nested.(*ast.List); ok {
					li.nested = w.parseList(nestedList)
					break
				}
			}
			items = append(items, li)
		}
	}
	list := newListBlock(n.IsOrdered(), n.Start, items, w.cfg)
	list.loose = !n.IsTight
	return list
}

func (w *blockWalker) parseInlineContentSkipFirst(n ast.Node) textRun {
	var run textRun
	first := true
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		if first {
			first = false
			continue
		}
		w.walkInlineSingle(child, styleNone, &run)
	}
	return run
}

func (w *blockWalker) walkInlineSingle(child ast.Node, style inlineStyle, run *textRun) {
	switch node := child.(type) {
	case *ast.Text:
		text := string(node.Segment.Value(w.source))
		if node.SoftLineBreak() || node.HardLineBreak() {
			text += " "
		}
		*run = append(*run, span{text: text, style: style})

	case *ast.String:
		*run = append(*run, span{text: string(node.Value), style: style})

	case *ast.Emphasis:
		newStyle := style
		if node.Level == 1 {
			newStyle |= styleItalic
		} else {
			newStyle |= styleBold
		}
		w.walkInline(node, newStyle, run)

	case *ast.CodeSpan:
		*run = append(*run, span{
			text:  w.extractCodeSpanText(node),
			style: style | styleCode,
		})

	case *ast.Link:
		linkRun := textRun{}
		w.walkInline(node, style|styleLink, &linkRun)
		for i := range linkRun {
			linkRun[i].url = string(node.Destination)
		}
		*run = append(*run, linkRun...)

	case *ast.AutoLink:
		url := string(node.URL(w.source))
		*run = append(*run, span{
			text:  url,
			style: style | styleLink,
			url:   url,
		})

	case *extast.Strikethrough:
		w.walkInline(node, style|styleStrikethrough, run)

	default:
		w.walkInline(child, style, run)
	}
}

func (w *blockWalker) parseBlockquote(n *ast.Blockquote) *blockquoteBlock {
	var content []textRun
	var nested *blockquoteBlock
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		switch node := child.(type) {
		case *ast.Paragraph:
			content = append(content, w.parseInlineContent(node))
		case *ast.Blockquote:
			nested = w.parseBlockquote(node)
		}
	}
	return newBlockquoteBlock(content, nested, w.cfg)
}

func (w *blockWalker) parseTable(n *extast.Table) *tableBlock {
	var header []textRun
	var rows [][]textRun
	var alignments []tableAlignment

	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		switch row := child.(type) {
		case *extast.TableHeader:
			header = w.parseTableRow(row)
			alignments = w.parseTableAlignments(n)
		case *extast.TableRow:
			rows = append(rows, w.parseTableRow(row))
		}
	}
	return newTableBlock(header, rows, alignments, w.cfg)
}

func (w *blockWalker) parseTableRow(n ast.Node) []textRun {
	var cells []textRun
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		if _, ok := child.(*extast.TableCell); ok {
			cells = append(cells, w.parseInlineContent(child))
		}
	}
	return cells
}

func (w *blockWalker) parseTableAlignments(n *extast.Table) []tableAlignment {
	alignments := make([]tableAlignment, len(n.Alignments))
	for i, a := range n.Alignments {
		switch a {
		case extast.AlignLeft:
			alignments[i] = alignLeft
		case extast.AlignRight:
			alignments[i] = alignRight
		case extast.AlignCenter:
			alignments[i] = alignCenter
		default:
			alignments[i] = alignLeft
		}
	}
	return alignments
}
