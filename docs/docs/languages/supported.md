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
| [TypeScript and JavaScript](./typescript.md) | Beta |

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
| `ada` | `git_config` | `matlab` | `sflog` |
| `agda` | `git_rebase` | `mdx` | `slang` |
| `angular` | `gitattributes` | `menhir` | `slim` |
| `apex` | `gitcommit` | `mermaid` | `slint` |
| `arduino` | `gitignore` | `meson` | `smali` |
| `asm` | `gleam` | `mlir` | `smithy` |
| `astro` | `glimmer` | `nasm` | `snakemake` |
| `authzed` | `glimmer_javascript` | `nginx` | `snl` |
| `awk` | `glimmer_typescript` | `nickel` | `solidity` |
| `bash` | `glsl` | `nim` | `soql` |
| `bass` | `gn` | `nim_format_string` | `sosl` |
| `beancount` | `gnuplot` | `ninja` | `sourcepawn` |
| `bibtex` | `go` | `nix` | `sparql` |
| `bicep` | `goctl` | `nqc` | `sproto` |
| `bitbake` | `godot_resource` | `nu` | `sql` |
| `blade` | `gomod` | `objc` | `squirrel` |
| `bp` | `gosum` | `objdump` | `ssh_config` |
| `bpftrace` | `gotmpl` | `ocaml` | `starlark` |
| `brightscript` | `gowork` | `ocaml_interface` | `strace` |
| `c` | `gpg` | `ocamllex` | `styled` |
| `c_sharp` | `graphql` | `odin` | `superhtml` |
| `c3` | `gren` | `pascal` | `surface` |
| `caddy` | `groovy` | `passwd` | `svelte` |
| `cairo` | `groq` | `pem` | `sway` |
| `capnp` | `gstlaunch` | `perl` | `swift` |
| `chatito` | `hack` | `php` | `sxhkdrc` |
| `circom` | `hare` | `php_only` | `systemtap` |
| `clojure` | `haskell` | `phpdoc` | `systemverilog` |
| `cmake` | `haskell_persistent` | `pioasm` | `t32` |
| `comment` | `hcl` | `pkl` | `tablegen` |
| `commonlisp` | `heex` | `po` | `tact` |
| `cooklang` | `helm` | `pod` | `tcl` |
| `corn` | `hjson` | `poe_filter` | `teal` |
| `cpon` | `hlsl` | `pony` | `templ` |
| `cpp` | `hocon` | `powershell` | `tera` |
| `css` | `hoon` | `printf` | `terraform` |
| `csv` | `html` | `prisma` | `textproto` |
| `cuda` | `htmldjango` | `problog` | `thrift` |
| `cue` | `http` | `prolog` | `tiger` |
| `cylc` | `hurl` | `promql` | `tlaplus` |
| `d` | `hyprlang` | `properties` | `todotxt` |
| `dart` | `idl` | `proto` | `toml` |
| `desktop` | `idris` | `prql` | `tsv` |
| `devicetree` | `ini` | `psv` | `turtle` |
| `dhall` | `inko` | `pug` | `twig` |
| `diff` | `ispc` | `puppet` | `typescript` |
| `disassembly` | `janet_simple` | `purescript` | `typespec` |
| `djot` | `java` | `pymanifest` | `typoscript` |
| `dockerfile` | `javadoc` | `python` | `typst` |
| `dot` | `jinja` | `ql` | `udev` |
| `doxygen` | `jinja_inline` | `qmldir` | `ungrammar` |
| `dtd` | `jq` | `qmljs` | `unison` |
| `earthfile` | `jsdoc` | `query` | `usd` |
| `ebnf` | `json` | `r` | `uxntal` |
| `editorconfig` | `json5` | `racket` | `v` |
| `eds` | `jsonnet` | `rasi` | `vala` |
| `eex` | `julia` | `razor` | `vento` |
| `elixir` | `just` | `rbs` | `vhdl` |
| `elm` | `kcl` | `re2c` | `vhs` |
| `elsa` | `kconfig` | `readline` | `vim` |
| `elvish` | `kdl` | `regex` | `vimdoc` |
| `embedded_template` | `kos` | `rego` | `vrl` |
| `enforce` | `kotlin` | `requirements` | `vue` |
| `erlang` | `koto` | `rescript` | `wgsl` |
| `facility` | `kusto` | `rifleconf` | `wgsl_bevy` |
| `faust` | `lalrpop` | `rnoweb` | `wing` |
| `fennel` | `latex` | `robot` | `wit` |
| `fidl` | `ledger` | `robots_txt` | `wxml` |
| `firrtl` | `linkerscript` | `roc` | `xcompose` |
| `fish` | `liquid` | `ron` | `xml` |
| `forth` | `llvm` | `rst` | `xresources` |
| `fortran` | `lua` | `ruby` | `yaml` |
| `fsh` | `luadoc` | `runescript` | `yang` |
| `fsharp` | `luap` | `rust` | `yuck` |
| `func` | `luau` | `scala` | `zig` |
| `gap` | `m68k` | `scfg` | `ziggy` |
| `gaptst` | `make` | `scheme` | `ziggy_schema` |
| `gdscript` | `markdown` | `scss` | `zsh` |
| `gdshader` | `markdown_inline` |  |  |

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
