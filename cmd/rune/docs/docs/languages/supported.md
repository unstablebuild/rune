---
sidebar_position: 1
sidebar_label: Supported
---

# Supported Languages

Rune groups language support into [tiers](#support-tiers). Each tier builds on the one below it,
so every supported capability remains available as a language moves up.

## Support tiers

| Tier | Capabilities |
| --- | --- |
| **Tier 1** | All Tier 2 features, plus [LSP](./intelligence.md) and full ecosystem workflows. |
| **Tier 2** | All Tier 3 features, plus the [symbol indexer](./symbol-index.md). |
| **Tier 3** | Syntax highlighting and structural queries. |

## Tier 1 languages

| Language | Status |
| --- | --- |
| [Go](./go.md) | Supported |
| [Python](./python.md) | Supported |
| [Rust](./rust.md) | Beta |
| [Zig](./zig.md) | Beta |
| TypeScript | Roadmap |

Beta means the language is complete enough for daily work and shipped
by default, while its commands and defaults may still change between
releases.

Code intelligence works the same way across [Tier 1](#support-tiers) languages. See the [Code
Intelligence](./intelligence.md) guide for the cross-language `lsp`
command: go to definition, find references, diagnostics, hover, rename,
and formatting.

Working in a repository with several projects side by side? See the
[Monorepos](./monorepo.md) guide for how Rune discovers each project and
keeps search fast at scale.

## Tier 3 language packages

Rune offers Tier 3 support for over 300 additional languages through
downloadable language packages. Each package bundles a Tree-sitter grammar and
a set of query files, giving Rune a real parse tree for the file instead of
plain text. That parse tree powers [syntax
highlighting](../learn/syntax-highlighting.md), code folding,
indentation, and (most usefully) [structural search and navigation](../learn/search.md).

| | | | |
| --- | --- | --- | --- |
| `ada` | `git_config` | `markdown_inline` | `sflog` |
| `agda` | `git_rebase` | `matlab` | `slang` |
| `angular` | `gitattributes` | `mdx` | `slim` |
| `apex` | `gitcommit` | `menhir` | `slint` |
| `arduino` | `gitignore` | `mermaid` | `smali` |
| `asm` | `gleam` | `meson` | `smithy` |
| `astro` | `glimmer` | `mlir` | `snakemake` |
| `authzed` | `glimmer_javascript` | `nasm` | `snl` |
| `awk` | `glimmer_typescript` | `nginx` | `solidity` |
| `bash` | `glsl` | `nickel` | `soql` |
| `bass` | `gn` | `nim` | `sosl` |
| `beancount` | `gnuplot` | `nim_format_string` | `sourcepawn` |
| `bibtex` | `go` | `ninja` | `sparql` |
| `bicep` | `goctl` | `nix` | `sproto` |
| `bitbake` | `godot_resource` | `nqc` | `sql` |
| `blade` | `gomod` | `nu` | `squirrel` |
| `bp` | `gosum` | `objc` | `ssh_config` |
| `bpftrace` | `gotmpl` | `objdump` | `starlark` |
| `brightscript` | `gowork` | `ocaml` | `strace` |
| `c` | `gpg` | `ocaml_interface` | `styled` |
| `c_sharp` | `graphql` | `ocamllex` | `superhtml` |
| `c3` | `gren` | `odin` | `surface` |
| `caddy` | `groovy` | `pascal` | `svelte` |
| `cairo` | `groq` | `passwd` | `sway` |
| `capnp` | `gstlaunch` | `pem` | `swift` |
| `chatito` | `hack` | `perl` | `sxhkdrc` |
| `circom` | `hare` | `php` | `systemtap` |
| `clojure` | `haskell` | `php_only` | `systemverilog` |
| `cmake` | `haskell_persistent` | `phpdoc` | `t32` |
| `comment` | `hcl` | `pioasm` | `tablegen` |
| `commonlisp` | `heex` | `pkl` | `tact` |
| `cooklang` | `helm` | `po` | `tcl` |
| `corn` | `hjson` | `pod` | `teal` |
| `cpon` | `hlsl` | `poe_filter` | `templ` |
| `cpp` | `hocon` | `pony` | `tera` |
| `css` | `hoon` | `powershell` | `terraform` |
| `csv` | `html` | `printf` | `textproto` |
| `cuda` | `htmldjango` | `prisma` | `thrift` |
| `cue` | `http` | `problog` | `tiger` |
| `cylc` | `hurl` | `prolog` | `tlaplus` |
| `d` | `hyprlang` | `promql` | `todotxt` |
| `dart` | `idl` | `properties` | `toml` |
| `desktop` | `idris` | `proto` | `tsv` |
| `devicetree` | `ini` | `prql` | `tsx` |
| `dhall` | `inko` | `psv` | `turtle` |
| `diff` | `ispc` | `pug` | `twig` |
| `disassembly` | `janet_simple` | `puppet` | `typescript` |
| `djot` | `java` | `purescript` | `typespec` |
| `dockerfile` | `javadoc` | `pymanifest` | `typoscript` |
| `dot` | `javascript` | `python` | `typst` |
| `doxygen` | `jinja` | `ql` | `udev` |
| `dtd` | `jinja_inline` | `qmldir` | `ungrammar` |
| `earthfile` | `jq` | `qmljs` | `unison` |
| `ebnf` | `jsdoc` | `query` | `usd` |
| `editorconfig` | `json` | `r` | `uxntal` |
| `eds` | `json5` | `racket` | `v` |
| `eex` | `jsonnet` | `rasi` | `vala` |
| `elixir` | `julia` | `razor` | `vento` |
| `elm` | `just` | `rbs` | `vhdl` |
| `elsa` | `kcl` | `re2c` | `vhs` |
| `elvish` | `kconfig` | `readline` | `vim` |
| `embedded_template` | `kdl` | `regex` | `vimdoc` |
| `enforce` | `kos` | `rego` | `vrl` |
| `erlang` | `kotlin` | `requirements` | `vue` |
| `facility` | `koto` | `rescript` | `wgsl` |
| `faust` | `kusto` | `rifleconf` | `wgsl_bevy` |
| `fennel` | `lalrpop` | `rnoweb` | `wing` |
| `fidl` | `latex` | `robot` | `wit` |
| `firrtl` | `ledger` | `robots_txt` | `wxml` |
| `fish` | `linkerscript` | `roc` | `xcompose` |
| `forth` | `liquid` | `ron` | `xml` |
| `fortran` | `llvm` | `rst` | `xresources` |
| `fsh` | `lua` | `ruby` | `yaml` |
| `fsharp` | `luadoc` | `runescript` | `yang` |
| `func` | `luap` | `rust` | `yuck` |
| `gap` | `luau` | `scala` | `zig` |
| `gaptst` | `m68k` | `scfg` | `ziggy` |
| `gdscript` | `make` | `scheme` | `ziggy_schema` |
| `gdshader` | `markdown` | `scss` | `zsh` |

### Installing a language package

You do not install these ahead of time. When you open a file in a
language whose package is not yet installed, Rune offers to download and
install it, so the right grammar is always ready when you open the
matching file type. This is governed by the `updates.auto_install`
setting, which is `False` by default: Rune prompts before installing.
During first-run onboarding, Rune installs packages without prompting
regardless of this setting.

To install packages silently without being asked, set `auto_install` to
`True`:

```yaml tab
updates:
  auto_install: true
```

```python tab
config["updates"]["auto_install"] = True
```

With auto-install turned off (the default), opening a file in a language whose package
is not yet installed makes Rune prompt you:

| Option | Effect |
| --- | --- |
| **Yes** | Install the package for this file. |
| **Yes, Always** | Install now, and auto-install future languages on open without asking again. |
| **No** | Skip it this time. |

**Yes** and **No** apply only to the current file. **Yes, Always**
persists your choice: it installs this package and auto-installs future
languages on open without asking again.
