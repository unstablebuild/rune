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
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	docsDir             = "docs/docs"
	docsKeybindingsData = "docs/src/data/keybindings.yaml"
)

var (
	docsPresets   = []string{"modal", "helix", "standard", "emacs"}
	docsPlatforms = []string{"darwin", "linux"}
)

// docsGuidePresets maps each editor guide to the one preset the site
// always shows on it, mirroring FIXED_GUIDES in the docs' preset switcher.
var docsGuidePresets = map[string]string{
	"learn/vim-editor.md":      "modal",
	"learn/helix-editor.md":    "helix",
	"learn/standard-editor.md": "standard",
	"learn/emacs-editor.md":    "emacs",
}

// docsPackageCommands are bound by bundled packages rather than by the
// presets, so the docs data cannot resolve them.
var docsPackageCommands = []string{"searchfunc", "searchvar", "searchtype"}

// docsLiteralChord is a preset key an editor guide writes as a literal
// chord on purpose, because the text around it is not about the command
// the preset binds to it. context is part of the line the chord is on,
// so the entry does not excuse the same chord elsewhere in the guide.
type docsLiteralChord struct {
	file, chord, context, reason string
}

var docsLiteralChords = []docsLiteralChord{
	{"learn/emacs-editor.md", "<meta-f>", "so terminal search shares",
		"names the terminal search key other presets use"},
	{"learn/helix-editor.md", "<shift-tab>", "Smart indent / literal indent",
		"describes insert mode, where the editor indents before the preset sees it"},
	{"learn/standard-editor.md", "<meta-k>", "## The `<meta-k>` / `<ctrl-k>` prefix",
		"names the macOS prefix in a heading shared by both platforms"},
}

// docsPresetData is what the docs site reads for one preset on one
// platform: the command prompt key and the command key bindings.
type docsPresetData struct {
	CommandKey  string         `yaml:"command_key"`
	KeyBindings map[string]any `yaml:"key_bindings"`
}

type docsKeybindings map[string]map[string]docsPresetData

func shippedDocsKeybindings(t *testing.T) docsKeybindings {
	t.Helper()
	out := docsKeybindings{}
	for _, preset := range docsPresets {
		out[preset] = map[string]docsPresetData{}
		for _, platform := range docsPlatforms {
			name := fmt.Sprintf("preset_%s_%s.yaml", preset, platform)
			raw, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			var cfg struct {
				Command struct {
					Key         string         `yaml:"key"`
					KeyBindings map[string]any `yaml:"key_bindings"`
				} `yaml:"command"`
			}
			if err := yaml.Unmarshal(raw, &cfg); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			key := cfg.Command.Key
			if key == "" {
				key = ":"
			}
			out[preset][platform] = docsPresetData{
				CommandKey: key, KeyBindings: cfg.Command.KeyBindings,
			}
		}
	}
	return out
}

func TestDocsKeybindingsDataIsFresh(t *testing.T) {
	raw, err := os.ReadFile(docsKeybindingsData)
	if err != nil {
		t.Fatal(err)
	}
	var got docsKeybindings
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s: %v", docsKeybindingsData, err)
	}
	if !reflect.DeepEqual(got, shippedDocsKeybindings(t)) {
		t.Fatalf("%s is out of date with the presets; run `make generate`",
			docsKeybindingsData)
	}
}

// bindingKey spells a binding value, or a <KeyBinding> command, so that a
// command string and a list of commands never compare equal, as in the
// KeyBinding component.
func bindingKey(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case []any:
		items := make([]string, len(v))
		for i, item := range v {
			items[i] = fmt.Sprint(item)
		}
		return "[" + strings.Join(items, "; ") + "]"
	case []string:
		return "[" + strings.Join(v, "; ") + "]"
	}
	return fmt.Sprint(v)
}

// docsScope is the presets and platforms a spot in a page renders for.
type docsScope struct {
	presets, platforms []string
}

func (s docsScope) String() string {
	return strings.Join(s.presets, ",") + " on " + strings.Join(s.platforms, ",")
}

// docsKeyRef is a <KeyBinding> element: command renders every chord
// bound to it; with chord set, it renders only that chord.
type docsKeyRef struct {
	pos, command, chord string
	scope               docsScope
}

// docsCodeSpan is a backticked span outside a fenced code block.
type docsCodeSpan struct {
	pos, line, text string
	scope           docsScope
}

type docsPage struct {
	file  string
	guide string
	refs  []docsKeyRef
	spans []docsCodeSpan
}

var (
	docsTokenRe = regexp.MustCompile(
		`<(Platform|Preset) when="([^"]*)">|</(Platform|Preset)>|` +
			`<KeyBinding((?:\s+\w+=(?:"[^"]*"|\{(?:[^{}]|\{[^{}]*\})*\}))*)\s*/>|` +
			"`([^`]+)`")
	docsAttrRe = regexp.MustCompile(`(\w+)=(?:"([^"]*)"|\{((?:[^{}]|\{[^{}]*\})*)\})`)
)

func loadDocsPages(t *testing.T) []docsPage {
	t.Helper()
	var pages []docsPage
	err := filepath.WalkDir(docsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") &&
			!strings.HasSuffix(path, ".mdx") {
			return err
		}
		rel, err := filepath.Rel(docsDir, path)
		if err != nil {
			return err
		}
		page, err := parseDocsPage(filepath.ToSlash(rel), path)
		if err != nil {
			return err
		}
		pages = append(pages, page)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return pages
}

func parseDocsPage(rel, path string) (docsPage, error) {
	page := docsPage{file: rel, guide: docsGuidePresets[rel]}
	f, err := os.Open(path)
	if err != nil {
		return page, err
	}
	defer f.Close()

	base := docsScope{presets: docsPresets, platforms: docsPlatforms}
	if page.guide != "" {
		base.presets = []string{page.guide}
	}
	// Blocks nest, and each narrows what its children render for.
	stack := []docsScope{base}
	fenced := false
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		pos := fmt.Sprintf("%s:%d", filepath.Join(docsDir, rel), n)
		matched := 0
		for _, m := range docsTokenRe.FindAllStringSubmatch(line, -1) {
			cur := stack[len(stack)-1]
			switch {
			case m[1] != "":
				when := strings.FieldsFunc(m[2], func(r rune) bool {
					return r == ' ' || r == ','
				})
				next := cur
				if m[1] == "Platform" {
					next.platforms = intersect(cur.platforms, when)
				} else {
					next.presets = intersect(cur.presets, when)
				}
				stack = append(stack, next)
			case m[3] != "":
				if len(stack) == 1 {
					return page, fmt.Errorf("%s: unbalanced </%s>", pos, m[3])
				}
				stack = stack[:len(stack)-1]
			case strings.HasPrefix(m[0], "<KeyBinding"):
				matched++
				ref, err := parseKeyBindingAttrs(m[4])
				if err != nil {
					return page, fmt.Errorf("%s: %s: %w", pos, m[0], err)
				}
				ref.pos, ref.scope = pos, cur
				page.refs = append(page.refs, ref)
			default:
				page.spans = append(page.spans, docsCodeSpan{
					pos: pos, line: line, text: m[5], scope: cur,
				})
			}
		}
		if strings.Count(line, "<KeyBinding") != matched {
			return page, fmt.Errorf("%s: a <KeyBinding> spells its attributes in a "+
				"form this test cannot read; keep it on one line with string, array "+
				"or String.fromCharCode attributes", pos)
		}
	}
	if len(stack) != 1 {
		return page, fmt.Errorf("%s: unclosed <Platform> or <Preset>", rel)
	}
	return page, sc.Err()
}

func intersect(a, b []string) []string {
	var out []string
	for _, s := range a {
		if slices.Contains(b, s) {
			out = append(out, s)
		}
	}
	return out
}

func parseKeyBindingAttrs(attrs string) (docsKeyRef, error) {
	var ref docsKeyRef
	for _, m := range docsAttrRe.FindAllStringSubmatch(attrs, -1) {
		var v any = m[2]
		if m[3] != "" {
			var err error
			if v, err = evalJSXValue(m[3]); err != nil {
				return ref, err
			}
		}
		switch m[1] {
		case "command":
			ref.command = bindingKey(v)
		case "chord":
			s, ok := v.(string)
			if !ok {
				return ref, fmt.Errorf("chord must be a string")
			}
			ref.chord = s
		default:
			return ref, fmt.Errorf("unknown attribute %s", m[1])
		}
	}
	if ref.command == "" {
		return ref, fmt.Errorf("no command")
	}
	return ref, nil
}

// evalJSXValue evaluates the JSX attribute expressions the docs use: a
// string, an array of strings, or a string concatenated from literals
// and String.fromCharCode calls, which spell characters MDX tables and
// attributes cannot hold.
func evalJSXValue(expr string) (any, error) {
	expr = strings.TrimSpace(expr)
	if strings.HasPrefix(expr, "[") && strings.HasSuffix(expr, "]") {
		var out []string
		for _, item := range splitOutsideQuotes(expr[1:len(expr)-1], ',') {
			if strings.TrimSpace(item) == "" {
				continue
			}
			s, err := evalJSXString(item)
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, nil
	}
	return evalJSXString(expr)
}

var fromCharCodeRe = regexp.MustCompile(`^String\.fromCharCode\((\d+)\)$`)

func evalJSXString(expr string) (string, error) {
	var b strings.Builder
	for _, term := range splitOutsideQuotes(expr, '+') {
		term = strings.TrimSpace(term)
		if m := fromCharCodeRe.FindStringSubmatch(term); m != nil {
			code, err := strconv.Atoi(m[1])
			if err != nil {
				return "", err
			}
			b.WriteRune(rune(code))
			continue
		}
		if len(term) < 2 || (term[0] != '"' && term[0] != '\'') ||
			term[len(term)-1] != term[0] || strings.ContainsRune(term, '\\') {
			return "", fmt.Errorf("unsupported expression %q", term)
		}
		b.WriteString(term[1 : len(term)-1])
	}
	return b.String(), nil
}

func splitOutsideQuotes(s string, sep rune) []string {
	var out []string
	var quote rune
	start := 0
	for i, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == sep:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func (d docsKeybindings) binds(preset, platform, command string) bool {
	for _, v := range d[preset][platform].KeyBindings {
		if bindingKey(v) == command {
			return true
		}
	}
	return false
}

func TestDocsKeyBindingsResolve(t *testing.T) {
	data := shippedDocsKeybindings(t)
	for _, page := range loadDocsPages(t) {
		for _, ref := range page.refs {
			if slices.Contains(docsPackageCommands, ref.command) {
				continue
			}
			var missing []string
			for _, preset := range ref.scope.presets {
				for _, platform := range ref.scope.platforms {
					bound := data.binds(preset, platform, ref.command)
					if ref.chord != "" {
						v, ok := data[preset][platform].KeyBindings[ref.chord]
						bound = ok && bindingKey(v) == ref.command
					}
					if !bound {
						missing = append(missing, preset+"/"+platform)
					}
				}
			}
			total := len(ref.scope.presets) * len(ref.scope.platforms)
			switch {
			case ref.chord != "" && len(missing) > 0:
				t.Errorf("%s: %s does not run %q on %s. Bind it in the preset, "+
					"pin the chord the preset binds, or wrap the text in a "+
					"<Platform> or <Preset> block.",
					ref.pos, ref.chord, ref.command, strings.Join(missing, ", "))
			case page.guide != "" && len(missing) > 0:
				t.Errorf("%s: %q is not bound on %s, so this guide renders "+
					"\"not bound by selected preset\". Bind it in the preset, "+
					"fix the command, or wrap the text in a <Platform> block.",
					ref.pos, ref.command, strings.Join(missing, ", "))
			case len(missing) == total:
				t.Errorf("%s: no preset binds %q on %s", ref.pos, ref.command, ref.scope)
			}
		}
	}
}

func TestDocsEditorGuidesDoNotHardcodePresetChords(t *testing.T) {
	data := shippedDocsKeybindings(t)
	used := make([]bool, len(docsLiteralChords))
	for _, page := range loadDocsPages(t) {
		if page.guide == "" {
			continue
		}
	spans:
		for _, span := range page.spans {
			for i, lit := range docsLiteralChords {
				if lit.file == page.file && lit.chord == span.text &&
					strings.Contains(span.line, lit.context) {
					used[i] = true
					continue spans
				}
			}
			for _, platform := range span.scope.platforms {
				v, ok := data[page.guide][platform].KeyBindings[span.text]
				if !ok {
					continue
				}
				t.Errorf("%s: `%s` is a %s preset chord; write "+
					"<KeyBinding command=%q chord=%q />, or list it in "+
					"docsLiteralChords with why the text is not about that command",
					span.pos, span.text, page.guide, bindingKey(v), span.text)
				break
			}
		}
	}
	for i, lit := range docsLiteralChords {
		if !used[i] {
			t.Errorf("docsLiteralChords lists `%s` in %s (%s), but the page no "+
				"longer writes it; remove the entry", lit.chord, lit.file, lit.reason)
		}
	}
}
