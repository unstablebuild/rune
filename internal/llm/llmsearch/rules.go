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

package llmsearch

import (
	"io"
	"path"
	"strings"
)

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

var (
	// sourceExtensions are the source files code search reads.
	sourceExtensions = set(
		"py", "pyi", "pyx", "c", "h", "cc", "cpp", "cxx", "hpp", "hh", "hxx", "m", "mm", "swift", "ts", "tsx", "mts",
		"cts", "js", "jsx", "mjs", "cjs", "go", "rs", "java", "kt", "kts", "scala", "groovy", "rb", "php", "cs", "fs",
		"sh", "bash", "zsh", "fish", "ps1", "lua", "pl", "pm", "r", "jl", "dart", "sql", "vue", "svelte", "astro",
		"html", "htm", "css", "scss", "sass", "less", "gradle", "cmake", "mk", "proto", "graphql", "gql", "tf", "hcl",
		"nix", "zig", "hs", "ex", "exs", "erl", "clj", "cljs", "elm", "ml", "mli", "vim", "el", "lisp", "asm", "s",
		"metal", "glsl", "hlsl", "wgsl", "cu", "cuh", "ino", "v", "sv", "tex",
	)
	// codeTextExtensions go with code: a repo's docs and settings.
	codeTextExtensions = set("md", "markdown", "rst", "adoc", "org", "txt", "yaml", "yml", "toml")
	// codeFileNames are code known by name alone.
	codeFileNames = set(
		"Makefile", "makefile", "GNUmakefile", "Dockerfile", "Containerfile", "Justfile", "justfile", "Rakefile",
		"Gemfile", "Podfile", "Brewfile", "Procfile", "Vagrantfile", "Jenkinsfile",
	)
	// dataExtensions are code-adjacent files that are data, not code:
	// reported as skipped rather than ignored.
	dataExtensions = set(
		"py", "ipynb", "c", "h", "cc", "cpp", "cxx", "hpp", "hh", "m", "mm", "swift", "tsx", "cts", "js", "jsx", "mjs",
		"cjs", "go", "rs", "java", "kt", "kts", "scala", "rb", "php", "cs", "fs", "sh", "bash", "zsh", "fish", "ps1",
		"lua", "pl", "r", "jl", "dart", "sql", "vue", "svelte", "css", "scss", "sass", "less", "json", "jsonl", "yaml",
		"yml", "toml", "xml", "plist", "gradle", "cmake", "mk", "lock", "log", "csv", "tsv", "ini", "cfg", "conf",
		"proto", "graphql", "tf", "nix", "zig", "hs", "ex", "exs", "erl", "clj", "elm", "ml", "vim", "el", "asm",
		"metal", "glsl", "hlsl", "wgsl", "cu", "pyi", "pyc",
	)
	// codeSkippedFolderNames are folders code search does not go into,
	// besides what git ignores: vendored code and what builds and runs
	// leave behind.
	codeSkippedFolderNames = set(
		"vendor", "Vendor", "vendored", "third_party", "third-party", "thirdparty", "3rdparty", "external", "extern",
		"deps", "build", "Build", "dist", "out", "target", "obj", "coverage", "htmlcov", "generated", "__generated__",
		"wandb", "mlruns", "lightning_logs",
	)
	// skippedFolderNames are never worth walking into.
	skippedFolderNames = set(
		"node_modules", "bower_components", "venv", "site-packages", "__pycache__", "DerivedData", "Pods", "Carthage",
	)
	secretExtensions = set("pem", "key", "p12", "pfx", "keystore", "jks", "ppk", "kdbx", "gpg", "asc",
		"crt", "cer", "der", "p8", "mobileprovision")
	secretPrefixes = []string{"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", ".env", "secret", "credential", ".netrc",
		".npmrc", ".pypirc", "htpasswd", ".htpasswd"}
)

// Skip reasons a file is not indexed for, as reported by Index.Status.
const (
	skipExcludedType = "excluded type"
	skipSecret       = "key or password file"
	skipMinified     = "minified"
	skipLicense      = "license"
	skipData         = "data file"
	skipOtherType    = "other type"
	skipTooBig       = "too big to be written by hand"
)

func extension(name string) string { return strings.ToLower(strings.TrimPrefix(path.Ext(name), ".")) }

// classifyCode decides whether the file name of size bytes is code to
// index: it returns "" when it is, else why not. open is called only to
// tell an MPEG transport stream from TypeScript by its first bytes.
func classifyCode(name string, size int64, open func() (io.ReadCloser, error), opts Options) string {
	ext := extension(name)
	lower := strings.ToLower(name)
	switch {
	case setHas(opts.ExcludedExtensions, ext):
		return skipExcludedType
	case isSecret(name):
		return skipSecret
	case strings.HasSuffix(lower, ".min.js") || strings.HasSuffix(lower, ".min.mjs") || strings.HasSuffix(lower, ".min.css"):
		return skipMinified
	case hasAnyPrefix(lower, "license", "licence", "copying", "notice"):
		return skipLicense
	case (ext == "ts" || ext == "mts") && isMPEGTransportStream(open):
		return skipOtherType
	case sourceExtensions[ext] || codeTextExtensions[ext] || ext == "ipynb" || codeFileNames[name] ||
		strings.HasPrefix(name, "Dockerfile"):
	case dataExtensions[ext]:
		return skipData
	default:
		return skipOtherType
	}
	limit := opts.MaxCodeBytes
	if ext == "ipynb" {
		limit = opts.MaxNotebookBytes
	}
	if size > limit {
		return skipTooBig
	}
	return ""
}

func setHas(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(strings.TrimPrefix(x, "."), s) {
			return true
		}
	}
	return false
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// isSecret reports keys, certificates, password and credential files: never
// read, wherever they are.
func isSecret(name string) bool {
	lower := strings.ToLower(name)
	return secretExtensions[extension(lower)] || hasAnyPrefix(lower, secretPrefixes...)
}

// isMPEGTransportStream reports a video: MPEG-TS packets start with 0x47
// every 188 bytes, or every 192 with a 4-byte timecode.
func isMPEGTransportStream(open func() (io.ReadCloser, error)) bool {
	f, err := open()
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	b := make([]byte, 600)
	n, _ := io.ReadFull(f, b)
	if n < 400 {
		return false
	}
	return (b[0] == 0x47 && b[188] == 0x47 && b[376] == 0x47) || (b[4] == 0x47 && b[196] == 0x47 && b[388] == 0x47)
}
