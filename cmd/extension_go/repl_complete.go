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

package main

import (
	"context"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

const completionLanguageID = "go"

// CompletionPrefix returns the trailing Go identifier the completion
// overlay must replace on accept. ideconsole deletes this many runes
// before inserting the chosen candidate, so it has to be just the
// partial member name at the cursor (e.g. "fmt.Pri" -> "Pri"), not the
// whole whitespace-delimited token. A line ending in a non-identifier
// byte (e.g. "fmt.") yields an empty prefix, so the candidate is
// inserted as-is.
func (s *goSession) CompletionPrefix(line string) string {
	if prefix, ok := importPathPrefix(line); ok {
		return prefix
	}
	// A builtin like /help is a single token; the candidate replaces it
	// whole, so the leading sigil must be part of the prefix.
	if strings.HasPrefix(line, builtinPrefix) && !strings.ContainsAny(line, " \t") {
		return line
	}
	i := len(line)
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(line[:i])
		if !isIdentRune(r) {
			break
		}
		i -= size
	}
	return line[i:]
}

// importPathPrefix reports whether line is a bare import whose path is
// being typed, returning the partial path already entered inside the
// opening quote. It matches lines like `import "fm` or
// `import "github.com/`, the only place package-path completion applies.
// An alias (`import w "io`) is allowed; only the text after the last
// quote is the prefix.
func importPathPrefix(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmed, "import") {
		return "", false
	}
	rest := trimmed[len("import"):]
	if rest != "" && !isSpace(rest[0]) {
		return "", false
	}
	// An odd number of quotes means the final one opened a string that is
	// still being typed (the partial path); an even count means every
	// path is closed, so there is nothing left to complete.
	if strings.Count(rest, `"`)%2 == 0 {
		return "", false
	}
	q := strings.LastIndexByte(rest, '"')
	return rest[q+1:], true
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t'
}

// rejoinArgs reconstructs the raw Go fragment from the cmd/args split
// ideconsole performs on whitespace. Completion needs the full line so the
// fragment ("buf.Wri") survives intact through gopls.
func rejoinArgs(cmd string, args []string) string {
	if len(args) == 0 {
		return cmd
	}
	return cmd + " " + strings.Join(args, " ")
}

// completeGo asks gopls to complete a partial Go fragment. It renders an
// accumulated program whose final line is the fragment, writes it to the
// shared program file, opens it as an LSP overlay, and requests
// completion at the cursor sitting just past the fragment. Candidates are
// the bare insert texts that replace the trailing identifier (see
// CompletionPrefix); the caller's overlay restores the text before it.
func (s *goSession) completeGo(
	ctx context.Context, fragment string,
) (iterator.Iterator[string], error) {
	if s.lsp == nil {
		return iterator.Empty[string](), nil
	}
	cr, ok := s.runner.(completionRunner)
	if !ok {
		return iterator.Empty[string](), nil
	}
	src, offset := s.renderForCompletion(fragment)
	path, err := cr.programPath(src)
	if err != nil {
		return iterator.Empty[string](), nil
	}
	uri, err := s.fs.URI(path)
	if err != nil {
		return iterator.Empty[string](), nil
	}
	lspURI := "file://" + uri.Path()
	pos := byteOffsetToPosition(src, offset)

	if err := s.lsp.DidOpen(ctx, semanticapi.DidOpenTextDocumentParams{
		TextDocument: semanticapi.TextDocumentItem{
			URI:        lspURI,
			LanguageID: completionLanguageID,
			Version:    1,
			Text:       src,
		},
	}); err != nil {
		return iterator.Empty[string](), nil
	}
	defer func() {
		_ = s.lsp.DidClose(ctx, semanticapi.DidCloseTextDocumentParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: lspURI},
		})
	}()

	result, err := s.lsp.Completion(ctx, semanticapi.CompletionParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: lspURI},
		Position:     pos,
		Context: &semanticapi.CompletionContext{
			TriggerKind: semanticapi.CompletionTriggerKindInvoked,
		},
	})
	if err != nil {
		return iterator.Empty[string](), nil
	}
	return iterator.FromSlice(completionInserts(result.Items)), nil
}

// SignatureHelp asks gopls for the signature of the call the cursor sits
// inside. line is the full input line being typed; col is the 0-based
// rune column of the cursor. It reuses the synthetic-program machinery
// from completeGo: the text up to the cursor is rendered as the trailing
// fragment, opened as an LSP overlay, and queried at the cursor. The
// returned label is the active signature's text with the active
// parameter emphasized; ok is false when no signature applies (no LSP,
// no runner, or gopls returns nothing), so the caller hides the hint.
func (s *goSession) SignatureHelp(
	ctx context.Context, line string, col int,
) (string, bool) {
	if s.lsp == nil {
		return "", false
	}
	cr, ok := s.runner.(completionRunner)
	if !ok {
		return "", false
	}
	fragment := runePrefix(line, col)
	src, offset := s.renderForCompletion(fragment)
	path, err := cr.programPath(src)
	if err != nil {
		return "", false
	}
	uri, err := s.fs.URI(path)
	if err != nil {
		return "", false
	}
	lspURI := "file://" + uri.Path()
	pos := byteOffsetToPosition(src, offset)

	if err := s.lsp.DidOpen(ctx, semanticapi.DidOpenTextDocumentParams{
		TextDocument: semanticapi.TextDocumentItem{
			URI:        lspURI,
			LanguageID: completionLanguageID,
			Version:    1,
			Text:       src,
		},
	}); err != nil {
		return "", false
	}
	defer func() {
		_ = s.lsp.DidClose(ctx, semanticapi.DidCloseTextDocumentParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: lspURI},
		})
	}()

	result, err := s.lsp.SignatureHelp(ctx, semanticapi.SignatureHelpParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: lspURI},
		Position:     pos,
	})
	if err != nil || result == nil {
		return "", false
	}
	return signatureLabel(result)
}

// runePrefix returns the first col runes of line, clamped to its length.
func runePrefix(line string, col int) string {
	runes := []rune(line)
	if col < 0 {
		col = 0
	}
	if col > len(runes) {
		col = len(runes)
	}
	return string(runes[:col])
}

// signatureLabel formats the active signature into a hint, wrapping the
// active parameter in brackets so the user can see which argument they
// are typing. It returns ok=false when the help carries no signatures.
func signatureLabel(help *semanticapi.SignatureHelp) (string, bool) {
	if help == nil || len(help.Signatures) == 0 {
		return "", false
	}
	idx := int(help.ActiveSignature)
	if idx < 0 || idx >= len(help.Signatures) {
		idx = 0
	}
	sig := help.Signatures[idx]
	return emphasizeActiveParam(sig, int(help.ActiveParameter)), true
}

// emphasizeActiveParam returns the signature label with the active
// parameter's substring wrapped in brackets. gopls reports each
// parameter either as a substring of the label or as a byte-offset pair
// into it; both are honored. When the active parameter cannot be located
// the plain label is returned.
func emphasizeActiveParam(sig semanticapi.SignatureInformation, active int) string {
	if active < 0 || active >= len(sig.Parameters) {
		return sig.Label
	}
	p := sig.Parameters[active]
	if p.LabelOffsets != nil {
		start, end := int(p.LabelOffsets[0]), int(p.LabelOffsets[1])
		if start >= 0 && end <= len(sig.Label) && start < end {
			return sig.Label[:start] + "[" + sig.Label[start:end] + "]" + sig.Label[end:]
		}
		return sig.Label
	}
	if p.Label == "" {
		return sig.Label
	}
	i := strings.Index(sig.Label, p.Label)
	if i < 0 {
		return sig.Label
	}
	return sig.Label[:i] + "[" + p.Label + "]" + sig.Label[i+len(p.Label):]
}

// completionInserts maps gopls completion items to the bare text that
// replaces the trailing identifier. gopls fills InsertText/TextEdit with
// the member name; Label is the fallback. Duplicate and empty inserts are
// dropped so the overlay shows each candidate once.
func completionInserts(items []semanticapi.CompletionItem) []string {
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		text := insertText(item)
		if text == "" {
			continue
		}
		if _, dup := seen[text]; dup {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	return out
}

// insertText extracts the text a completion item inserts, preferring the
// explicit edit text, then InsertText, then Label. A TextEdit's NewText
// can carry snippet placeholders or a leading prefix; the overlay inserts
// it verbatim, so the bare identifier forms gopls returns for member
// completion map cleanly.
func insertText(item semanticapi.CompletionItem) string {
	switch {
	case item.TextEdit != nil && item.TextEdit.NewText != "":
		return item.TextEdit.NewText
	case item.InsertText != "":
		return item.InsertText
	default:
		return item.Label
	}
}

// renderForCompletion builds a complete Go program whose final line is
// the partial fragment, returning the source and the byte offset of the
// cursor sitting immediately after the fragment. Unlike render it does
// not gofmt the result: the fragment is usually unfinished (e.g.
// "fmt.Pri"), which gofmt would reject, and gofmt could also shift the
// cursor. gopls completes unfinished code, so raw source is what it
// needs. Imports that the fragment references stay normal imports (not
// blank) so their members resolve.
func (s *goSession) renderForCompletion(fragment string) (string, int) {
	var b strings.Builder
	b.WriteString("package main\n\n")

	specs := s.sortedImports()
	if len(specs) > 0 {
		body := s.importBody(fragment)
		b.WriteString("import (\n")
		for _, spec := range specs {
			b.WriteString("\t" + renderImport(spec, body) + "\n")
		}
		b.WriteString(")\n\n")
	}

	for _, decl := range s.decls {
		b.WriteString(decl)
		b.WriteString("\n\n")
	}

	b.WriteString("func main() {\n")
	for _, stmt := range s.stmts {
		b.WriteString(stmt + "\n")
	}
	for _, name := range s.declared {
		b.WriteString("\t_ = " + name + "\n")
	}
	b.WriteString("\t")
	b.WriteString(fragment)
	offset := b.Len()
	b.WriteString("\n}\n")
	return b.String(), offset
}

// byteOffsetToPosition converts a byte offset in src to an LSP Position
// (0-based line, UTF-16 code-unit character), as gopls expects.
func byteOffsetToPosition(src string, offset int) semanticapi.Position {
	if offset > len(src) {
		offset = len(src)
	}
	line := 0
	lineStart := 0
	for i := 0; i < offset; i++ {
		if src[i] == '\n' {
			line++
			lineStart = i + 1
		}
	}
	character := utf16Len(src[lineStart:offset])
	return semanticapi.Position{
		Line:      uint32(line),
		Character: uint32(character),
	}
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += len(utf16.Encode([]rune{r}))
	}
	return n
}

// isIdentRune reports whether r can appear in a Go identifier. Selectors
// (".") are deliberately excluded so the completion prefix is only the
// trailing member name.
func isIdentRune(r rune) bool {
	return r == '_' ||
		(r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9')
}
