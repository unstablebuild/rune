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
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestLSP(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
		wantOut string
	}{
		// hover
		{
			name:    "hover",
			args:    []string{"lsp", "hover", "$FILE", "0", "5"},
			wantOut: "func main()\n",
		},
		{
			name:    "hover/tpl",
			args:    []string{"lsp", "hover", "-F", "{{.Contents}}", "$FILE", "0", "5"},
			wantOut: "func main()\n",
		},
		{
			name:    "hover/err_line",
			args:    []string{"lsp", "hover", "$FILE", "abc", "0"},
			wantErr: "invalid line",
		},
		{
			name:    "hover/err_col",
			args:    []string{"lsp", "hover", "$FILE", "0", "abc"},
			wantErr: "invalid column",
		},

		// definition
		{
			name:    "definition",
			args:    []string{"lsp", "definition", "$FILE", "0", "5"},
			wantOut: "file:///src/main.go 10:5-10:15\n",
		},
		{
			name:    "definition/tpl",
			args:    []string{"lsp", "definition", "-F", "{{.URI}}", "$FILE", "0", "5"},
			wantOut: "file:///src/main.go\n",
		},

		// references
		{
			name: "references",
			args: []string{"lsp", "references", "$FILE", "0", "5"},
			wantOut: "file:///src/main.go 10:5-10:15\n" +
				"file:///src/util.go 20:3-20:13\n",
		},
		{
			name: "references/decl",
			args: []string{"lsp", "references", "-d", "$FILE", "0", "5"},
			wantOut: "file:///src/main.go 10:5-10:15\n" +
				"file:///src/util.go 20:3-20:13\n",
		},

		// symbols
		{
			name: "symbols",
			args: []string{"lsp", "symbols", "$FILE"},
			wantOut: "main [Function] 5:0-10:1\n" +
				"  x [Variable] 6:1-6:10\n",
		},
		{
			name:    "symbols/tpl",
			args:    []string{"lsp", "symbols", "-F", "{{.Name}} {{.Kind}}", "$FILE"},
			wantOut: "main Function\nx Variable\n",
		},

		// workspace-symbols
		{
			name: "workspace-symbols",
			args: []string{"lsp", "workspace-symbols", "My"},
			wantOut: "MyFunc Function file:///src/main.go:5:0\n" +
				"MyInterface Interface file:///src/types.go:15:5\n" +
				"MyStruct Struct file:///src/types.go:20:5\n" +
				"MyStruct.MyMethod Method file:///src/types.go:30:0\n",
		},

		// diagnostics
		{
			name:    "diagnostics",
			args:    []string{"lsp", "diagnostics", "$FILE"},
			wantOut: "[Error] 3:10: undefined variable (test, E001)\n",
		},

		// workspace-diagnostics
		{
			name: "workspace-diagnostics",
			args: []string{"lsp", "workspace-diagnostics"},
			wantOut: "[Error] file:///src/main.go 3:10: undefined variable (test, E001)\n" +
				"[Warning] file:///src/util.go 7:0: unused import (test, W002)\n",
		},
		{
			name: "workspace-diagnostics/j",
			args: []string{"lsp", "workspace-diagnostics", "-F", "json"},
			wantOut: `{"uri":"file:///src/main.go",` +
				`"severity":"Error",` +
				`"start_line":3,"start_char":10,` +
				`"end_line":3,"end_char":15,` +
				`"message":"undefined variable",` +
				`"source":"test","code":"E001"}` +
				"\n" +
				`{"uri":"file:///src/util.go",` +
				`"severity":"Warning",` +
				`"start_line":7,"start_char":0,` +
				`"end_line":7,"end_char":5,` +
				`"message":"unused import",` +
				`"source":"test","code":"W002"}` +
				"\n",
		},

		// rename
		{
			name: "rename",
			args: []string{
				"lsp", "rename", "--dry-run",
				"$FILE", "5", "5", "newName",
			},
			wantOut: "file:///src/main.go 2\n",
		},

		// code-actions
		{
			name: "code-actions",
			args: []string{
				"lsp", "code-actions", "$FILE", "5", "5",
			},
			wantOut: "[refactor.extract] \"Extract variable\"\n" +
				"[source.organizeImports] \"Organize imports\"\n" +
				"[command] \"Run test\"\n",
		},
		{
			name: "code-actions/edits",
			args: []string{
				"lsp", "code-actions", "edits",
				"$FILE", "5", "5",
			},
			wantOut: "\"Organize imports\"" +
				" [source.organizeImports]" +
				" file:///src/main.go" +
				" 2:0-4:0" +
				" \"import (\\n\\t\\\"fmt\\\"\\n)\\n\"\n",
		},
		{
			name: "code-actions/edits/j",
			args: []string{
				"lsp", "code-actions", "edits",
				"-F", "json", "$FILE", "5", "5",
			},
			wantOut: `{"title":"Organize imports",` +
				`"kind":"source.organizeImports",` +
				`"uri":"file:///src/main.go",` +
				`"start_line":2,"start_char":0,` +
				`"end_line":4,"end_char":0,` +
				`"new_text":"import (\n\t\"fmt\"\n)\n"}` +
				"\n",
		},
		{
			name: "code-actions/cmds",
			args: []string{
				"lsp", "code-actions", "commands",
				"$FILE", "5", "5",
			},
			wantOut: `"Run test" test.run` +
				` {"uri":"file:///test.go"}` +
				"\n",
		},
		{
			name: "code-actions/cmds/j",
			args: []string{
				"lsp", "code-actions", "commands",
				"-F", "json", "$FILE", "5", "5",
			},
			wantOut: `{"title":"Run test",` +
				`"command":"test.run",` +
				`"arguments":` +
				`[{"uri":"file:///test.go"}]}` +
				"\n",
		},

		// completion
		{
			name: "completion",
			args: []string{"lsp", "completion", "$FILE", "0", "5"},
			wantOut: "[Module] fmt - package fmt\n" +
				"[Function] Println - func Println(a ...any)\n",
		},
		{
			name:    "completion/tpl",
			args:    []string{"lsp", "completion", "-F", "{{.Label}} {{.Kind}}", "$FILE", "0", "5"},
			wantOut: "fmt Module\nPrintln Function\n",
		},

		// signature-help
		{
			name: "signature-help",
			args: []string{"lsp", "signature-help", "$FILE", "0", "5"},
			wantOut: "func Println(a ...any) (n int, err error)\n" +
				"  Parameters: a ...any\n",
		},
		{
			name: "signature-help/j",
			args: []string{"lsp", "signature-help", "-F", "json", "$FILE", "0", "5"},
			wantOut: `{"active_parameter":0,"active_signature":0,` +
				`"label":"func Println(a ...any) (n int, err error)",` +
				`"parameters":"a ...any","success":true}` + "\n",
		},

		// declaration
		{
			name:    "declaration",
			args:    []string{"lsp", "declaration", "$FILE", "0", "5"},
			wantOut: "file:///src/types.go 15:5-15:15\n",
		},
		{
			name: "declaration/j",
			args: []string{"lsp", "declaration", "-F", "json", "$FILE", "0", "5"},
			wantOut: `{"uri":"file:///src/types.go","start_line":15,"start_char":5,` +
				`"end_line":15,"end_char":15}` + "\n",
		},

		// type-definition
		{
			name:    "type-definition",
			args:    []string{"lsp", "type-definition", "$FILE", "0", "5"},
			wantOut: "file:///src/types.go 20:5-20:12\n",
		},

		// implementation
		{
			name: "implementation",
			args: []string{"lsp", "implementation", "$FILE", "0", "5"},
			wantOut: "file:///src/impl1.go 10:0-10:10\n" +
				"file:///src/impl2.go 25:0-25:10\n",
		},
		{
			name:    "implementation/tpl",
			args:    []string{"lsp", "implementation", "-F", "{{.URI}}", "$FILE", "0", "5"},
			wantOut: "file:///src/impl1.go\nfile:///src/impl2.go\n",
		},

		// formatting
		{
			name: "formatting",
			args: []string{
				"lsp", "formatting",
				"--dry-run", "--no-color", "$FILE",
			},
			wantOut: "--- a$PATH\n" +
				"+++ b$PATH\n" +
				"@@ -1,1 +1,1 @@\n" +
				"-hello world\n" +
				"+packageworld\n",
		},
		{
			name: "formatting/j",
			args: []string{
				"lsp", "formatting", "--dry-run",
				"-F", "json", "$FILE",
			},
			wantOut: `{"start_line":0,"start_char":0,"end_line":0,"end_char":5,` +
				`"new_text":"package"}` + "\n" +
				`{"start_line":5,"start_char":0,"end_line":5,"end_char":2,` +
				`"new_text":"\t"}` + "\n",
		},
		{
			name: "formatting/opts",
			args: []string{
				"lsp", "formatting",
				"--dry-run", "--no-color",
				"--tab-size", "2",
				"--insert-spaces=false", "$FILE",
			},
			wantOut: "--- a$PATH\n" +
				"+++ b$PATH\n" +
				"@@ -1,1 +1,1 @@\n" +
				"-hello world\n" +
				"+packageworld\n",
		},

		// prepare-rename
		{
			name:    "prepare-rename",
			args:    []string{"lsp", "prepare-rename", "$FILE", "5", "5"},
			wantOut: "5:5-5:9 \"main\"\n",
		},
		{
			name: "prepare-rename/j",
			args: []string{"lsp", "prepare-rename", "-F", "json", "$FILE", "5", "5"},
			wantOut: `{"end_char":9,"end_line":5,"placeholder":"main",` +
				`"start_char":5,"start_line":5,"success":true}` + "\n",
		},

		// document-highlight
		{
			name: "document-highlight",
			args: []string{"lsp", "document-highlight", "$FILE", "5", "5"},
			wantOut: "[Read] 5:5-5:9\n" +
				"[Write] 10:3-10:7\n",
		},
		{
			name: "document-highlight/j",
			args: []string{"lsp", "document-highlight", "-F", "json", "$FILE", "5", "5"},
			wantOut: `{"kind":"Read","start_line":5,"start_char":5,` +
				`"end_line":5,"end_char":9}` + "\n" +
				`{"kind":"Write","start_line":10,"start_char":3,` +
				`"end_line":10,"end_char":7}` + "\n",
		},

		// code-lens
		{
			name:    "code-lens",
			args:    []string{"lsp", "code-lens", "$FILE"},
			wantOut: "5:0-5:10 Run test (test.run)\n",
		},

		// range-formatting
		{
			name: "range-formatting",
			args: []string{
				"lsp", "range-formatting",
				"--dry-run", "--no-color",
				"$FILE", "0", "0", "10", "0",
			},
			wantOut: "--- a$PATH\n" +
				"+++ b$PATH\n" +
				"@@ -1,1 +1,1 @@\n" +
				"-hello world\n" +
				"+fixed world\n",
		},
		{
			name: "range-formatting/j",
			args: []string{
				"lsp", "range-formatting", "--dry-run",
				"-F", "json",
				"$FILE", "0", "0", "10", "0",
			},
			wantOut: `{"start_line":0,"start_char":0,"end_line":0,"end_char":5,` +
				`"new_text":"fixed"}` + "\n",
		},

		// folding-range
		{
			name: "folding-range",
			args: []string{"lsp", "folding-range", "$FILE"},
			wantOut: "5:0-20:1 [region]\n" +
				"1:0-3:0 [imports]\n",
		},
		{
			name: "folding-range/j",
			args: []string{"lsp", "folding-range", "-F", "json", "$FILE"},
			wantOut: `{"start_line":5,"start_char":0,"end_line":20,"end_char":1,` +
				`"kind":"region"}` + "\n" +
				`{"start_line":1,"start_char":0,"end_line":3,"end_char":0,` +
				`"kind":"imports"}` + "\n",
		},

		// selection-range
		{
			name: "selection-range",
			args: []string{"lsp", "selection-range", "$FILE", "5", "5"},
			wantOut: "5:5-5:9\n" +
				"  5:0-10:1\n",
		},
		{
			name: "selection-range/j",
			args: []string{"lsp", "selection-range", "-F", "json", "$FILE", "5", "5"},
			wantOut: `{"start_line":5,"start_char":5,"end_line":5,"end_char":9,"depth":0}` +
				"\n" +
				`{"start_line":5,"start_char":0,"end_line":10,"end_char":1,"depth":1}` +
				"\n",
		},

		// execute-command
		{
			name:    "execute-command",
			args:    []string{"lsp", "execute-command", "my.command"},
			wantOut: "command executed: my.command\n",
		},
		{
			name: "execute-command/j",
			args: []string{
				"lsp", "execute-command",
				"-F", "json", "my.command",
			},
			wantOut: `{"result":"command executed:` +
				` my.command","success":true}` + "\n",
		},
		{
			name: "execute-command/args",
			args: []string{
				"lsp", "execute-command",
				"my.command",
				`{"uri":"file:///test.go"}`,
				`42`,
				`true`,
				`"hello"`,
			},
			wantOut: "command executed: my.command" +
				` args={"uri":"file:///test.go"},` +
				`42,true,"hello"` + "\n",
		},
		{
			name: "execute-command/bad",
			args: []string{
				"lsp", "execute-command",
				"my.command", "not-json",
			},
			wantErr: "invalid argument",
		},

		// inlay-hint
		{
			name: "inlay-hint",
			args: []string{"lsp", "inlay-hint", "$FILE", "0", "0", "20", "0"},
			wantOut: "10:15 [Type] : string\n" +
				"12:8 [Parameter] name:\n",
		},
		{
			name: "inlay-hint/j",
			args: []string{"lsp", "inlay-hint", "-F", "json", "$FILE", "0", "0", "20", "0"},
			wantOut: `{"line":10,"char":15,"kind":"Type","label":": string"}` + "\n" +
				`{"line":12,"char":8,"kind":"Parameter","label":"name:"}` + "\n",
		},

		// prepare-call-hierarchy
		{
			name:    "prepare-call-hier",
			args:    []string{"lsp", "prepare-call-hierarchy", "$FILE", "5", "5"},
			wantOut: "main [Function] file:///src/main.go 5:0-10:1\n",
		},
		{
			name: "prepare-call-hier/j",
			args: []string{"lsp", "prepare-call-hierarchy", "-F", "json", "$FILE", "5", "5"},
			wantOut: `{"name":"main","kind":"Function","uri":"file:///src/main.go",` +
				`"start_line":5,"start_char":0,"end_line":10,"end_char":1}` + "\n",
		},

		// incoming-calls
		{
			name: "incoming-calls",
			args: []string{
				"lsp", "incoming-calls",
				"main", "12", "file:///src/main.go",
				"5", "0", "10", "1",
			},
			wantOut: "runApp [Function] file:///src/app.go:20:0\n",
		},
		{
			name: "incoming-calls/j",
			args: []string{
				"lsp", "incoming-calls", "-F", "json",
				"main", "12", "file:///src/main.go",
				"5", "0", "10", "1",
			},
			wantOut: `{"from_name":"runApp","from_kind":"Function",` +
				`"from_uri":"file:///src/app.go","from_start_line":20,` +
				`"from_start_char":0}` + "\n",
		},

		// outgoing-calls
		{
			name: "outgoing-calls",
			args: []string{
				"lsp", "outgoing-calls",
				"main", "12", "file:///src/main.go",
				"5", "0", "10", "1",
			},
			wantOut: "fmt.Println [Function] file:///go/src/fmt/print.go:100:0\n",
		},

		// prepare-type-hierarchy
		{
			name:    "prepare-type-hier",
			args:    []string{"lsp", "prepare-type-hierarchy", "$FILE", "15", "5"},
			wantOut: "Reader [Interface] file:///src/io.go 15:0-18:1\n",
		},
		{
			name: "prepare-type-hier/j",
			args: []string{"lsp", "prepare-type-hierarchy", "-F", "json", "$FILE", "15", "5"},
			wantOut: `{"name":"Reader","kind":"Interface","uri":"file:///src/io.go",` +
				`"detail":"interface","start_line":15,"start_char":0,` +
				`"end_line":18,"end_char":1}` + "\n",
		},

		// type-supertypes
		{
			name: "type-supertypes",
			args: []string{
				"lsp", "type-supertypes",
				"Reader", "11", "file:///src/io.go",
				"15", "0", "18", "1",
			},
			wantOut: "io.Reader [Interface] file:///go/src/io/io.go:50:0\n",
		},

		// type-subtypes
		{
			name: "type-subtypes",
			args: []string{
				"lsp", "type-subtypes",
				"Reader", "11", "file:///src/io.go",
				"15", "0", "18", "1",
			},
			wantOut: "MyReader [Struct] file:///src/reader.go:10:0\n" +
				"BufferedReader [Struct] file:///src/buffered.go:8:0\n",
		},
		{
			name: "type-subtypes/j",
			args: []string{
				"lsp", "type-subtypes", "-F", "json",
				"Reader", "11", "file:///src/io.go",
				"15", "0", "18", "1",
			},
			wantOut: `{"name":"MyReader","kind":"Struct","uri":"file:///src/reader.go",` +
				`"detail":"struct","start_line":10,"start_char":0,` +
				`"end_line":15,"end_char":1}` + "\n" +
				`{"name":"BufferedReader","kind":"Struct",` +
				`"uri":"file:///src/buffered.go","detail":"struct",` +
				`"start_line":8,"start_char":0,"end_line":12,"end_char":1}` + "\n",
		},

		// semantic-tokens-full
		{
			name: "semantic-tokens-full",
			args: []string{"lsp", "semantic-tokens-full", "$FILE"},
			wantOut: "ResultID: test-result-id\n" +
				"Data (2 tokens): [0 0 5 0 0 1 0 10 1 0]\n",
		},
		{
			name: "semantic-tokens-full/j",
			args: []string{"lsp", "semantic-tokens-full", "-F", "json", "$FILE"},
			wantOut: `{"data":"[0 0 5 0 0 1 0 10 1 0]",` +
				`"result_id":"test-result-id","success":true}` + "\n",
		},

		// semantic-tokens-range
		{
			name: "semantic-tokens-range",
			args: []string{"lsp", "semantic-tokens-range", "$FILE", "0", "0", "10", "0"},
			wantOut: "ResultID: range-result-id\n" +
				"Data (1 tokens): [0 5 8 2 0]\n",
		},
		{
			name: "sem-tokens-range/j",
			args: []string{"lsp", "semantic-tokens-range", "-F", "json", "$FILE", "0", "0", "10", "0"},
			wantOut: `{"data":"[0 5 8 2 0]",` +
				`"result_id":"range-result-id","success":true}` + "\n",
		},

		// document-color
		{
			name:    "document-color",
			args:    []string{"lsp", "document-color", "$FILE"},
			wantOut: "5:10-5:17 rgba(1.00, 0.00, 0.00, 1.00)\n",
		},
		{
			name: "document-color/j",
			args: []string{"lsp", "document-color", "-F", "json", "$FILE"},
			wantOut: `{"start_line":5,"start_char":10,"end_line":5,"end_char":17,` +
				`"red":1,"green":0,"blue":0,"alpha":1}` + "\n",
		},

		// color-presentation
		{
			name: "color-presentation",
			args: []string{
				"lsp", "color-presentation",
				"$FILE", "5", "10", "5", "17",
				"1.0", "0.0", "0.0", "1.0",
			},
			wantOut: "#FF0000\nrgb(255, 0, 0)\n",
		},
		{
			name: "color-presentation/j",
			args: []string{
				"lsp", "color-presentation", "-F", "json",
				"$FILE", "5", "10", "5", "17",
				"1.0", "0.0", "0.0", "1.0",
			},
			wantOut: `{"label":"#FF0000"}` + "\n" +
				`{"label":"rgb(255, 0, 0)"}` + "\n",
		},

		// document-link
		{
			name:    "document-link",
			args:    []string{"lsp", "document-link", "$FILE"},
			wantOut: "3:5-3:30 https://example.com/docs\n",
		},
		{
			name: "document-link/j",
			args: []string{"lsp", "document-link", "-F", "json", "$FILE"},
			wantOut: `{"start_line":3,"start_char":5,"end_line":3,"end_char":30,` +
				`"target":"https://example.com/docs","tooltip":"Documentation link"}` +
				"\n",
		},

		// on-type-formatting
		{
			name: "on-type-formatting",
			args: []string{
				"lsp", "on-type-formatting",
				"--dry-run", "--no-color",
				"$FILE", "5", "0", "}",
			},
			wantOut: "--- a$PATH\n" +
				"+++ b$PATH\n" +
				"@@ -1,1 +1,1 @@\n" +
				"-hello world\n" +
				"+\tllo world\n",
		},
		{
			name: "on-type-formatting/j",
			args: []string{
				"lsp", "on-type-formatting", "--dry-run",
				"-F", "json",
				"$FILE", "5", "0", "}",
			},
			wantOut: `{"start_line":5,"start_char":0,"end_line":5,"end_char":2,` +
				`"new_text":"\t"}` + "\n",
		},

		// linked-editing-range
		{
			name: "linked-editing-range",
			args: []string{"lsp", "linked-editing-range", "$FILE", "1", "5"},
			wantOut: "1:5-1:10\n" +
				"5:8-5:13\n" +
				"Word pattern: \\w+\n",
		},
		{
			name: "linked-edit-range/j",
			args: []string{"lsp", "linked-editing-range", "-F", "json", "$FILE", "1", "5"},
			wantOut: `{"start_line":1,"start_char":5,"end_line":1,"end_char":10,` +
				`"word_pattern":"\\w+"}` + "\n" +
				`{"start_line":5,"start_char":8,"end_line":5,"end_char":13,` +
				`"word_pattern":"\\w+"}` + "\n",
		},

		// moniker
		{
			name:    "moniker",
			args:    []string{"lsp", "moniker", "$FILE", "10", "5"},
			wantOut: "go:github.com/example/pkg.Func [Export] Global\n",
		},
		{
			name: "moniker/j",
			args: []string{"lsp", "moniker", "-F", "json", "$FILE", "10", "5"},
			wantOut: `{"scheme":"go","identifier":"github.com/example/pkg.Func",` +
				`"unique":"Global","kind":"Export"}` + "\n",
		},

		// inline-value
		{
			name:    "inline-value",
			args:    []string{"lsp", "inline-value", "$FILE", "5", "0", "15", "0"},
			wantOut: "10:5-10:15 text=\"value: 42\" var=x\n",
		},
		{
			name: "inline-value/j",
			args: []string{"lsp", "inline-value", "-F", "json", "$FILE", "5", "0", "15", "0"},
			wantOut: `{"start_line":10,"start_char":5,"end_line":10,"end_char":15,` +
				`"text":"value: 42","variable_name":"x"}` + "\n",
		},

		// symbol-mode tests
		{
			name:    "hover/sym",
			args:    []string{"lsp", "hover", "MyFunc"},
			wantOut: "func main()\n",
		},
		{
			name:    "definition/sym",
			args:    []string{"lsp", "definition", "MyFunc"},
			wantOut: "file:///src/main.go 10:5-10:15\n",
		},
		{
			name: "references/sym",
			args: []string{"lsp", "references", "MyFunc"},
			wantOut: "file:///src/main.go 10:5-10:15\n" +
				"file:///src/util.go 20:3-20:13\n",
		},
		{
			name: "rename/sym",
			args: []string{
				"lsp", "rename", "--dry-run",
				"MyFunc", "newName",
			},
			wantOut: "file:///src/main.go 2\n",
		},
		{
			name:    "declaration/sym",
			args:    []string{"lsp", "declaration", "MyFunc"},
			wantOut: "file:///src/types.go 15:5-15:15\n",
		},
		{
			name:    "type-definition/sym",
			args:    []string{"lsp", "type-definition", "myVar"},
			wantOut: "file:///src/types.go 20:5-20:12\n",
		},
		{
			name: "implementation/sym",
			args: []string{"lsp", "implementation", "MyInterface"},
			wantOut: "file:///src/impl1.go 10:0-10:10\n" +
				"file:///src/impl2.go 25:0-25:10\n",
		},
		{
			name:    "prepare-rename/sym",
			args:    []string{"lsp", "prepare-rename", "MyFunc"},
			wantOut: "5:5-5:9 \"main\"\n",
		},
		{
			name:    "prepare-call-hier/sym",
			args:    []string{"lsp", "prepare-call-hierarchy", "MyFunc"},
			wantOut: "main [Function] file:///src/main.go 5:0-10:1\n",
		},
		{
			name:    "prepare-type-hier/sym",
			args:    []string{"lsp", "prepare-type-hierarchy", "MyInterface"},
			wantOut: "Reader [Interface] file:///src/io.go 15:0-18:1\n",
		},
		{
			name:    "hover/sym_not_found",
			args:    []string{"lsp", "hover", "NonExistent"},
			wantErr: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			defer env.cleanup()

			testFile := filepath.Join(env.datadir, "test.go")
			args := make([]string, len(tt.args))
			for i, arg := range tt.args {
				if arg == "$FILE" {
					args[i] = testFile
				} else {
					args[i] = arg
				}
			}

			out, err := env.run(args...)

			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			want := strings.ReplaceAll(
				tt.wantOut, "$PATH", testFile,
			)
			require.Equal(t, want, out)
		})
	}
}

// A qualified name resolves through the symbol index: the command asks the language server
// at the site the index found, without searching the workspace's symbols.
func TestLSPSymbolThroughIndex(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	out, err := env.run("lsp", "definition", "pkg.Symbol")
	require.NoError(t, err)
	require.Equal(t, "file:///src/main.go 10:5-10:15\n", out)
	var methods []string
	rpcs := env.rpcs.take()
	for _, r := range rpcs {
		methods = append(methods, r.method)
	}
	require.Equal(t, []string{"Syntax/ResolveSymbol", "LSP/Definition"}, methods)
	req, err := protojson.Marshal(rpcs[1].req)
	require.NoError(t, err)
	require.JSONEq(t, `{"textDocument":{"uri":"file:///src/main.go"},`+
		`"position":{"line":10,"character":5}}`, string(req))
}
