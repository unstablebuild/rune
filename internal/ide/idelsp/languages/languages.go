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

package languages

import (
	"errors"
	"path/filepath"
)

// this is for exceptions to the rule of languageID => file_extension[1:]
var extensionToLanguageID = map[string]string{
	// Ada
	".adb": "ada",
	".ads": "ada",

	// Apex
	".cls":     "apex",
	".trigger": "apex",

	// Arduino
	".ino": "arduino",

	// Assembly
	".s": "asm",

	// Bash/Shell
	".sh":           "bash",
	".bash":         "bash",
	".bashrc":       "bash",
	".bash_profile": "bash",
	".profile":      "bash",
	".localrc":      "bash",

	// BibTeX
	".bib": "bibtex",

	// BitBake
	".bb":       "bitbake",
	".bbappend": "bitbake",
	".bbclass":  "bitbake",

	// Blade (Laravel)
	".blade.php": "blade",

	// BPFtrace
	".bt": "bpftrace",

	// BrightScript
	".brs": "brightscript",

	// C
	".c": "c",
	".h": "c",

	// C#
	".cs": "c_sharp",

	// Clojure
	".clj":  "clojure",
	".cljs": "clojure",
	".cljc": "clojure",
	".edn":  "clojure",

	// CMake
	".cmake": "cmake",

	// Comment
	// (no unique extension)

	// Common Lisp
	".lisp": "commonlisp",
	".lsp":  "commonlisp",
	".cl":   "commonlisp",

	// Cooklang
	".cook": "cooklang",

	// C++
	".cpp": "cpp",
	".cxx": "cpp",
	".cc":  "cpp",
	".hpp": "cpp",
	".hxx": "cpp",
	".hh":  "cpp",
	".h++": "cpp",

	// CSS
	".css": "css",

	// CSV
	".csv": "csv",

	// CUDA
	".cu":  "cuda",
	".cuh": "cuda",

	// Device Tree
	".dts":  "devicetree",
	".dtsi": "devicetree",

	// Diff
	".diff":  "diff",
	".patch": "diff",

	// DOT (Graphviz)
	".gv": "dot",

	// Earthfile
	// (no extension, just filename)

	// Elixir
	".ex":  "elixir",
	".exs": "elixir",

	// Elvish
	".elv": "elvish",

	// Embedded Template (ERB/EJS)
	".erb": "embedded_template",
	".ejs": "embedded_template",

	// Enforce
	".enf": "enforce",

	// Erlang
	".erl": "erlang",
	".hrl": "erlang",

	// Facility
	".fsd": "facility",

	// Faust
	".dsp": "faust",

	// Fennel
	".fnl": "fennel",

	// FIRRTL
	".fir": "firrtl",

	// Foam
	// (no unique extension)

	// Forth
	".fth": "forth",
	".4th": "forth",

	// Fortran
	".f":   "fortran",
	".for": "fortran",
	".f90": "fortran",
	".f95": "fortran",
	".f03": "fortran",
	".f08": "fortran",

	// F#
	".fs":  "fsharp",
	".fsi": "fsharp",
	".fsx": "fsharp",

	// FunC
	".fc": "func",

	// GAP
	".g":  "gap",
	".gi": "gap",

	// GAP test
	".tst": "gaptst",

	// GDScript (Godot)
	".gd": "gdscript",

	// GDShader (Godot)
	".gdshader": "gdshader",

	// Git config
	".gitconfig": "git_config",

	// Git attributes
	".gitattributes": "gitattributes",

	// Git ignore
	".gitignore": "gitignore",

	// Glimmer (Handlebars)
	".hbs": "glimmer",

	// Glimmer JS
	".gjs": "glimmer_javascript",

	// Glimmer TS
	".gts": "glimmer_typescript",

	// GLSL
	".glsl": "glsl",
	".vert": "glsl",
	".frag": "glsl",
	".geom": "glsl",
	".comp": "glsl",

	// GN
	".gn":  "gn",
	".gni": "gn",

	// Gnuplot
	".gp": "gnuplot",

	// Goctl
	".api": "goctl",

	// Godot Resource
	".tres": "godot_resource",
	".tscn": "godot_resource",

	// Go template
	".tmpl": "gotmpl",

	// GraphQL
	".gql": "graphql",

	// Groovy
	".gradle": "groovy",

	// Hare
	".ha": "hare",

	// Haskell
	".hs":  "haskell",
	".lhs": "haskell",

	// Haskell Persistent
	".persistentmodels": "haskell_persistent",

	// HCL
	".hcl": "hcl",

	// Helm
	// (uses .yaml with gotmpl)

	// HLS Playlist
	".m3u":  "hlsplaylist",
	".m3u8": "hlsplaylist",

	// HOCON
	".conf": "hocon",

	// HTML
	".htm": "html",

	// HTML Django
	// (uses .html)

	// Hyprlang
	".hl": "hyprlang",

	// Idris
	".idr": "idris",

	// Janet
	".janet": "janet_simple",

	// JavaScript
	".js":  "javascript",
	".mjs": "javascript",
	".cjs": "javascript",
	// tree-sitter-javascript parses JSX; there is no separate grammar.
	".jsx": "javascript",

	// Jinja
	".jinja":  "jinja",
	".jinja2": "jinja",
	".j2":     "jinja",

	// JSDoc
	// (embedded in .js)

	// Jsonnet
	".jsonnet":   "jsonnet",
	".libsonnet": "jsonnet",

	// Julia
	".jl": "julia",

	// KCL
	".k": "kcl",

	// Kconfig
	// (filename based)

	// Kitty
	// (filename based)

	// Kotlin
	".kt":  "kotlin",
	".kts": "kotlin",

	// Kusto
	".kql": "kusto",
	".csl": "kusto",

	// LaTeX
	".tex": "latex",
	".ltx": "latex",
	".sty": "latex",

	// Ledger
	".journal": "ledger",

	// Linker Script
	".ld":  "linkerscript",
	".lds": "linkerscript",

	// Liquidsoap
	".liq": "liquidsoap",

	// LLVM IR
	".ll": "llvm",

	// Luadoc
	// (embedded in .lua)

	// Luap
	// (embedded patterns)

	// M68K Assembly
	".m68k": "m68k",

	// Make
	".mk": "make",

	// Markdown
	".md":       "markdown",
	".markdown": "markdown",

	// MATLAB/Octave
	// (.m is mapped to objc)

	// Menhir
	".mly": "menhir",

	// Mermaid
	".mmd": "mermaid",

	// Meson
	// (filename based: meson.build)

	// Muttrc
	".neomuttrc": "muttrc",

	// Nginx
	// (filename based)

	// Nickel
	".ncl": "nickel",

	// Nim
	".nim":  "nim",
	".nims": "nim",

	// Nim format string
	// (embedded)

	// Objective-C
	".m": "objc",

	// Objdump
	// (no standard extension)

	// OCaml
	".ml":  "ocaml",
	".mli": "ocaml_interface",
	".mll": "ocamllex",

	// Pascal
	".pas": "pascal",
	".pp":  "pascal",
	".inc": "pascal",

	// Passwd
	// (filename based)

	// Perl
	".pl": "perl",
	".pm": "perl",

	// PHPDoc
	// (embedded in .php)

	// PIO Assembly
	".pio": "pioasm",

	// PO (Gettext)
	".po":  "po",
	".pot": "po",

	// Path of Exile filter
	".filter": "poe_filter",

	// PowerShell
	".ps1":  "powershell",
	".psm1": "powershell",
	".psd1": "powershell",

	// Problog/Prolog
	".pro": "prolog",
	// ".pl":  "prolog", // conflicts with perl

	// Pug
	".pug":  "pug",
	".jade": "pug",

	// PureScript
	".purs": "purescript",

	// Python
	".py":  "python",
	".pyw": "python",
	".pyi": "python",

	// QL (CodeQL)
	".ql":  "ql",
	".qll": "ql",

	// QML
	".qml": "qmljs",

	// Query (tree-sitter)
	".scm": "query",

	// R
	".r": "r",
	".R": "r",

	// Racket
	".rkt": "racket",

	// Ralph
	".ral": "ralph",

	// Rasi (Rofi)
	".rasi": "rasi",

	// Razor
	".razor":  "razor",
	".cshtml": "razor",

	// RBS (Ruby type)
	".rbs": "rbs",

	// re2c
	".re": "re2c",

	// Readline
	".inputrc": "readline",

	// Regex
	// (no standard extension)

	// Requirements
	// (filename based)

	// ReScript
	".res":  "rescript",
	".resi": "rescript",

	// Rifleconf
	".rifle": "rifleconf",

	// Rnoweb
	".Rnw": "rnoweb",
	".rnw": "rnoweb",

	// Robots.txt
	// (filename based)

	// Ruby
	".rb":      "ruby",
	".rake":    "ruby",
	".gemspec": "ruby",

	// RuneScript
	".rs2": "runescript",

	// Rust
	".rs": "rust",

	// Scala
	".scala": "scala",
	".sc":    "scala",

	// Scheme
	".ss": "scheme",

	// Snakemake
	".smk": "snakemake",

	// Solidity
	".sol": "solidity",

	// SourcePawn
	".sp": "sourcepawn",

	// SPARQL
	".rq": "sparql",

	// Squirrel
	".nut": "squirrel",

	// SSH config
	".ssh/config": "ssh_config",

	// Starlark
	".star": "starlark",
	".bzl":  "starlark",

	// Strace
	// (no standard extension)

	// Styled
	// (embedded in .js/.ts)

	// SuperCollider
	".scd": "supercollider",

	// Superhtml
	".shtml": "superhtml",

	// Surface (Phoenix)
	".sface": "surface",

	// Sway
	".sw": "sway",

	// sxhkdrc
	// (filename based)

	// SystemTap
	".stp": "systemtap",

	// SystemVerilog
	".sv":  "systemverilog",
	".svh": "systemverilog",

	// T32
	".cmm": "t32",

	// TableGen
	".td": "tablegen",

	// Teal
	".tl": "teal",

	// Terraform
	".tf":     "terraform",
	".tfvars": "terraform",

	// Text Proto
	".pbtxt": "textproto",

	// Tiger
	".tig": "tiger",

	// TLA+
	".tla": "tlaplus",

	// Tmux
	// (filename based)

	// Todotxt
	// (filename based)

	// Turtle (RDF)
	".ttl": "turtle",

	// TypeScript
	".ts":  "typescript",
	".mts": "typescript",
	".cts": "typescript",

	// TypeSpec
	".tsp": "typespec",

	// Typst
	".typ": "typst",

	// udev rules
	".rules": "udev",

	// Ungrammar
	".ungram": "ungrammar",

	// Unison
	".u": "unison",

	// USD (Universal Scene Description)
	".usd":  "usd",
	".usda": "usd",
	".usdc": "usd",

	// Uxntal
	".tal": "uxntal",

	// V
	".v": "v",

	// Vala
	".vapi": "vala",

	// Vento
	".vto": "vento",

	// VHDL
	".vhd":  "vhdl",
	".vhdl": "vhdl",

	// VHS
	".tape": "vhs",

	// Vim
	".vim":   "vim",
	".vimrc": "vim",

	// Vimdoc
	// (no standard extension)

	// WGSL Bevy
	// (uses .wgsl)

	// Wing
	".w": "wing",

	// XCompose
	".XCompose": "xcompose",

	// Xresources
	".Xresources": "xresources",

	// YAML
	".yaml": "yaml",
	".yml":  "yaml",

	// Ziggy Schema
	".ziggy-schema": "ziggy_schema",

	// Zsh
	".zsh":    "zsh",
	".zshrc":  "zsh",
	".zshenv": "zsh",
}

var languageIDToExtension = map[string]string{
	"ada":                ".adb",
	"apex":               ".cls",
	"arduino":            ".ino",
	"asm":                ".s",
	"bash":               ".sh",
	"bibtex":             ".bib",
	"bitbake":            ".bb",
	"blade":              ".blade.php",
	"bpftrace":           ".bt",
	"brightscript":       ".brs",
	"c":                  ".c",
	"c_sharp":            ".cs",
	"clojure":            ".clj",
	"cmake":              ".cmake",
	"commonlisp":         ".lisp",
	"cooklang":           ".cook",
	"cpp":                ".cpp",
	"css":                ".css",
	"csv":                ".csv",
	"cuda":               ".cu",
	"devicetree":         ".dts",
	"diff":               ".diff",
	"dot":                ".gv",
	"elixir":             ".ex",
	"elvish":             ".elv",
	"embedded_template":  ".erb",
	"enforce":            ".enf",
	"erlang":             ".erl",
	"facility":           ".fsd",
	"faust":              ".dsp",
	"fennel":             ".fnl",
	"firrtl":             ".fir",
	"forth":              ".fth",
	"fortran":            ".f",
	"fsharp":             ".fs",
	"func":               ".fc",
	"gap":                ".g",
	"gaptst":             ".tst",
	"gdscript":           ".gd",
	"gdshader":           ".gdshader",
	"git_config":         ".gitconfig",
	"gitattributes":      ".gitattributes",
	"gitignore":          ".gitignore",
	"glimmer":            ".hbs",
	"glimmer_javascript": ".gjs",
	"glimmer_typescript": ".gts",
	"glsl":               ".glsl",
	"gn":                 ".gn",
	"gnuplot":            ".gp",
	"goctl":              ".api",
	"godot_resource":     ".tres",
	"gotmpl":             ".tmpl",
	"graphql":            ".gql",
	"groovy":             ".gradle",
	"hare":               ".ha",
	"haskell":            ".hs",
	"haskell_persistent": ".persistentmodels",
	"hcl":                ".hcl",
	"hlsplaylist":        ".m3u",
	"hocon":              ".conf",
	"html":               ".htm",
	"hyprlang":           ".hl",
	"idris":              ".idr",
	"janet_simple":       ".janet",
	"javascript":         ".js",
	"jinja":              ".jinja",
	"jsonnet":            ".jsonnet",
	"julia":              ".jl",
	"kcl":                ".k",
	"kotlin":             ".kt",
	"kusto":              ".kql",
	"latex":              ".tex",
	"ledger":             ".journal",
	"linkerscript":       ".ld",
	"liquidsoap":         ".liq",
	"llvm":               ".ll",
	"m68k":               ".m68k",
	"make":               ".mk",
	"markdown":           ".md",
	"menhir":             ".mly",
	"mermaid":            ".mmd",
	"muttrc":             ".neomuttrc",
	"nickel":             ".ncl",
	"nim":                ".nim",
	"objc":               ".m",
	"ocaml":              ".ml",
	"ocaml_interface":    ".mli",
	"ocamllex":           ".mll",
	"pascal":             ".pas",
	"perl":               ".pl",
	"pioasm":             ".pio",
	"po":                 ".po",
	"poe_filter":         ".filter",
	"powershell":         ".ps1",
	"prolog":             ".pro",
	"pug":                ".pug",
	"purescript":         ".purs",
	"python":             ".py",
	"ql":                 ".ql",
	"qmljs":              ".qml",
	"query":              ".scm",
	"r":                  ".r",
	"racket":             ".rkt",
	"ralph":              ".ral",
	"rasi":               ".rasi",
	"razor":              ".razor",
	"rbs":                ".rbs",
	"re2c":               ".re",
	"readline":           ".inputrc",
	"rescript":           ".res",
	"rifleconf":          ".rifle",
	"rnoweb":             ".Rnw",
	"ruby":               ".rb",
	"runescript":         ".rs2",
	"rust":               ".rs",
	"scala":              ".scala",
	"scheme":             ".ss",
	"snakemake":          ".smk",
	"solidity":           ".sol",
	"sourcepawn":         ".sp",
	"sparql":             ".rq",
	"ssh_config":         ".ssh/config",
	"squirrel":           ".nut",
	"starlark":           ".star",
	"supercollider":      ".scd",
	"superhtml":          ".shtml",
	"surface":            ".sface",
	"sway":               ".sw",
	"systemtap":          ".stp",
	"systemverilog":      ".sv",
	"t32":                ".cmm",
	"tablegen":           ".td",
	"teal":               ".tl",
	"terraform":          ".tf",
	"textproto":          ".pbtxt",
	"tiger":              ".tig",
	"tlaplus":            ".tla",
	"turtle":             ".ttl",
	"typescript":         ".ts",
	"typespec":           ".tsp",
	"typst":              ".typ",
	"udev":               ".rules",
	"ungrammar":          ".ungram",
	"unison":             ".u",
	"usd":                ".usd",
	"uxntal":             ".tal",
	"v":                  ".v",
	"vala":               ".vapi",
	"vento":              ".vto",
	"vhdl":               ".vhd",
	"vhs":                ".tape",
	"vim":                ".vim",
	"wing":               ".w",
	"xcompose":           ".XCompose",
	"xresources":         ".Xresources",
	"yaml":               ".yaml",
	"ziggy_schema":       ".ziggy-schema",
	"zsh":                ".zsh",
}

// special filenames that don't rely on extensions
var filenameToLanguageID = map[string]string{
	"CMakeLists.txt":   "cmake",
	"Caddyfile":        "caddy",
	"Dockerfile":       "dockerfile",
	"Earthfile":        "earthfile",
	"Gemfile":          "ruby",
	"Justfile":         "just",
	"justfile":         "just",
	"Kconfig":          "kconfig",
	"Makefile":         "make",
	"makefile":         "make",
	"GNUmakefile":      "make",
	"meson.build":      "meson",
	"go.mod":           "gomod",
	"go.sum":           "gosum",
	"go.work":          "gowork",
	"BUILD":            "starlark",
	"BUILD.bazel":      "starlark",
	"WORKSPACE":        "starlark",
	"WORKSPACE.bazel":  "starlark",
	"Snakefile":        "snakemake",
	"Rakefile":         "ruby",
	"qmldir":           "qmldir",
	"COMMIT_EDITMSG":   "gitcommit",
	"MANIFEST.in":      "pymanifest",
	"requirements.txt": "requirements",
	"robots.txt":       "robots_txt",
	"todo.txt":         "todotxt",
	".inputrc":         "readline",
	".gitconfig":       "git_config",
	".gitignore":       "gitignore",
	".gitattributes":   "gitattributes",
	".editorconfig":    "editorconfig",
	"nginx.conf":       "nginx",
	"sxhkdrc":          "sxhkdrc",
	".zathurarc":       "zathurarc",
}

// ExtensionForLanguage returns the file extension (including the leading dot)
// for the given language ID, or ("", false) if the language has no
// explicit mapping in the extension table.
func ExtensionForLanguage(language string) (string, bool) {
	ext, ok := languageIDToExtension[language]
	return ext, ok
}

// ExtensionForLanguageWithFallback returns the file extension for the given
// language ID. It first consults the explicit extension table via
// ExtensionForLanguage; if the language is not found there, it falls
// back to "." + language. This mirrors the implicit rule used by
// LanguageForFile, where any file with extension ".xyz" is assigned
// language ID "xyz" when no explicit mapping exists. The fallback
// therefore reverses that convention, producing a plausible extension
// for languages that were derived from their file extension.
func ExtensionForLanguageWithFallback(language string) string {
	if ext, ok := ExtensionForLanguage(language); ok {
		return ext
	}
	return "." + language
}

// FilenameForLanguage returns a synthetic filename suitable for identifying
// the language to tools that operate on file URIs (e.g. "foo.go").
func FilenameForLanguage(language string) string {
	return "foo" + ExtensionForLanguageWithFallback(language)
}

// LanguageForFile returns the language id for the given file,
// or an error if it couldn't be determined.
func LanguageForFile(filename string) (string, error) {
	id, ok := filenameToLanguageID[filename]
	if ok {
		return id, nil
	}
	ext := filepath.Ext(filename)
	if ext == "" {
		return "", errors.New("file does not have an extension " +
			"and it's not a recognized file")
	}
	id, ok = extensionToLanguageID[ext]
	if !ok {
		id = ext[1:]
	}
	return id, nil
}
