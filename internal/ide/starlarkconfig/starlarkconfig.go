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

// Package starlarkconfig contains the shared Starlark <-> Go conversion
// and config-decode helpers used by both the main rune config loader
// (ide/config_starlark.go) and the extension/package config merge path
// (ide/idepkg/manager.go).
//
// The decoder understands two modes controlled by Source.Base:
//
//   - Base mode (Base == nil): the script must bind a top-level
//     `config = {...}` dict. The returned map is that dict.
//   - Overlay mode (Base != nil): `config` is predeclared with the given
//     base map. The script may mutate it (config["x"] = y, etc.) or rebind
//     it entirely. The returned map is the predeclared dict's final
//     contents with a rebound `config`, if any, deep-merged over them.
//
// The helpers are intentionally low-level: callers wrap them with their own
// domain concerns (YAML fallback, filename suffix detection, param
// injection, serialization of merged results, etc.).
package starlarkconfig

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// ConfigGlobal is the name of the top-level dict the script reads from and
// writes to. Anything else is ignored.
const ConfigGlobal = "config"

// IncludeBuiltin is the name of the function an overlay-mode script calls
// to layer another config file over `config`. See Source.Include.
const IncludeBuiltin = "include"

// ErrMissingConfig is returned by Decode in base mode when the script does
// not bind a top-level `config` dict. Callers that treat a missing binding
// as an empty configuration (for example, a user config file that only
// contains comments) can branch on this with errors.Is.
var ErrMissingConfig = errors.New("starlark: missing top-level \"config\" dict")

// Source describes one config invocation. See package docs for the two modes.
type Source struct {
	// Src is the script source.
	Src []byte

	// Filename is used in error messages and as the Starlark thread name.
	// Defaults to "config.star" when empty.
	Filename string

	// Params are exposed as extra predeclared globals. Keys must be valid
	// Starlark identifiers and must not collide with ConfigGlobal when
	// Base is non-nil.
	Params map[string]any

	// Base, when non-nil, switches the decoder to overlay mode.
	Base map[string]any

	// Builtins, when non-empty, are merged into the predeclared
	// globals before the script runs. Keys must be valid Starlark
	// identifiers and must not collide with any key in Params or
	// with ConfigGlobal when Base is non-nil; collisions cause
	// Decode to return an error.
	Builtins starlark.StringDict

	// Include, when non-nil in overlay mode, predeclares the
	// IncludeBuiltin function. include(path) calls Include with path and
	// the current contents of the predeclared `config` dict, and replaces
	// those contents with the map it returns, so the statements after the
	// call read and mutate the merged tree. An error it returns ends the
	// script with that error. A script that rebound `config` before the
	// call still layers over the included tree in the result, as the
	// predeclared dict is what the call changes. The builtin is not
	// defined in base mode or when Include is nil.
	Include func(path string, current map[string]any) (map[string]any, error)
}

// Decode parses and executes src. See Source for the two modes.
//
// The script runs with stdlib Starlark only — no file I/O, no load(), no
// environment access. Conditionals (if/elif/else) and loops (for) at the
// top level are enabled via FileOptions.TopLevelControl.
func Decode(s Source) (map[string]any, error) {
	filename := s.Filename
	if filename == "" {
		filename = "config.star"
	}

	thread := &starlark.Thread{
		Name:  filename,
		Print: func(*starlark.Thread, string) {},
		Load: func(_ *starlark.Thread, module string) (starlark.StringDict, error) {
			return nil, fmt.Errorf("load() is not allowed in %s: cannot load %q",
				filename, module)
		},
	}
	opts := &syntax.FileOptions{
		TopLevelControl: true,
		GlobalReassign:  true,
		Recursion:       true,
	}

	predeclared, err := StringDictFromMap(s.Params)
	if err != nil {
		return nil, fmt.Errorf("starlark: encode params: %w", err)
	}

	for name, v := range s.Builtins {
		if _, taken := predeclared[name]; taken {
			return nil, fmt.Errorf("starlark: builtin %q collides with "+
				"predeclared param", name)
		}
		predeclared[name] = v
	}

	if s.Base != nil {
		baseVal, err := FromGo(s.Base)
		if err != nil {
			return nil, fmt.Errorf("starlark: encode base config: %w", err)
		}
		if _, taken := predeclared[ConfigGlobal]; taken {
			return nil, fmt.Errorf("starlark: param %q collides with "+
				"predeclared base config", ConfigGlobal)
		}
		predeclared[ConfigGlobal] = baseVal
		if s.Include != nil {
			if _, taken := predeclared[IncludeBuiltin]; taken {
				return nil, fmt.Errorf("starlark: param %q collides with "+
					"the include builtin", IncludeBuiltin)
			}
			predeclared[IncludeBuiltin] = includeBuiltin(
				baseVal.(*starlark.Dict), s.Include)
		}
	}

	globals, err := starlark.ExecFileOptions(opts, thread, filename, s.Src, predeclared)
	if err != nil {
		var evalErr *starlark.EvalError
		if errors.As(err, &evalErr) {
			return nil, fmt.Errorf("starlark: %s", evalErr.Backtrace())
		}
		return nil, fmt.Errorf("starlark: %w", err)
	}

	// If the script never rebinds `config`, it won't appear in globals —
	// in overlay mode the predeclared dict still holds the latest value
	// because dict mutations modify the object in place.
	val, rebound := globals[ConfigGlobal]
	ok := rebound
	if !rebound {
		val, ok = predeclared[ConfigGlobal]
	}
	if !ok {
		return nil, ErrMissingConfig
	}
	dict, ok := val.(*starlark.Dict)
	if !ok {
		return nil, fmt.Errorf("starlark: %q must be a dict, got %s",
			ConfigGlobal, val.Type())
	}
	out, err := DictToMap(dict)
	if err != nil {
		return nil, fmt.Errorf("starlark: %w", err)
	}
	if !rebound || s.Base == nil {
		return out, nil
	}
	// Mutations and includes made through the predeclared dict before the
	// rebind are a layer under the rebound one.
	predeclaredOut, err := DictToMap(predeclared[ConfigGlobal].(*starlark.Dict))
	if err != nil {
		return nil, fmt.Errorf("starlark: %w", err)
	}
	deepMergeMap(predeclaredOut, out)
	return predeclaredOut, nil
}

// includeBuiltin returns the include function bound to the predeclared
// config dict and the loader of included files.
func includeBuiltin(
	configDict *starlark.Dict,
	include func(path string, current map[string]any) (map[string]any, error),
) *starlark.Builtin {
	return starlark.NewBuiltin(IncludeBuiltin, func(
		_ *starlark.Thread, b *starlark.Builtin,
		args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		var path string
		if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &path); err != nil {
			return nil, err
		}
		current, err := DictToMap(configDict)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", b.Name(), err)
		}
		next, err := include(path, current)
		if err != nil {
			return nil, err
		}
		nextVal, err := FromGo(next)
		if err != nil {
			return nil, fmt.Errorf("%s: encode included config: %w", b.Name(), err)
		}
		nextDict := nextVal.(*starlark.Dict)
		if err := configDict.Clear(); err != nil {
			return nil, fmt.Errorf("%s: %w", b.Name(), err)
		}
		for _, item := range nextDict.Items() {
			// The dict is unfrozen and keyed by strings, so SetKey
			// cannot fail.
			_ = configDict.SetKey(item[0], item[1])
		}
		return starlark.None, nil
	})
}

// ToGo converts an arbitrary Starlark value to the closest Go equivalent
// used by the rest of the config pipeline (map[string]any / []any / scalar
// primitives / string).
func ToGo(v starlark.Value) (any, error) {
	switch t := v.(type) {
	case starlark.NoneType:
		return nil, nil
	case starlark.Bool:
		return bool(t), nil
	case starlark.Int:
		// Try int64 first, fall back to float64 only if it overflows.
		if i, ok := t.Int64(); ok {
			return int(i), nil
		}
		f, _ := starlark.AsFloat(t)
		return f, nil
	case starlark.Float:
		return float64(t), nil
	case starlark.String:
		return string(t), nil
	case *starlark.List:
		out := make([]any, 0, t.Len())
		iter := t.Iterate()
		defer iter.Done()
		var item starlark.Value
		for iter.Next(&item) {
			gv, err := ToGo(item)
			if err != nil {
				return nil, err
			}
			out = append(out, gv)
		}
		return out, nil
	case starlark.Tuple:
		out := make([]any, 0, t.Len())
		for i := range t.Len() {
			gv, err := ToGo(t.Index(i))
			if err != nil {
				return nil, err
			}
			out = append(out, gv)
		}
		return out, nil
	case *starlark.Dict:
		return DictToMap(t)
	default:
		return nil, fmt.Errorf("unsupported starlark value of type %s", v.Type())
	}
}

// DictToMap converts a Starlark dict with string keys to a Go map.
// Non-string keys are rejected because the downstream config consumers
// (config.MapConfig) assume string-keyed maps.
func DictToMap(d *starlark.Dict) (map[string]any, error) {
	out := make(map[string]any, d.Len())
	for _, item := range d.Items() {
		keyVal, value := item[0], item[1]
		keyStr, ok := keyVal.(starlark.String)
		if !ok {
			return nil, fmt.Errorf("dict keys must be strings, got %s", keyVal.Type())
		}
		gv, err := ToGo(value)
		if err != nil {
			return nil, err
		}
		out[string(keyStr)] = gv
	}
	return out, nil
}

// StringDictFromMap converts a Go map[string]any into a flat
// starlark.StringDict suitable for use as predeclared globals. Only the
// top level is expected to be a map; values themselves may be nested
// (dicts, lists, or primitives), which are converted recursively.
func StringDictFromMap(params map[string]any) (starlark.StringDict, error) {
	out := make(starlark.StringDict, len(params))
	for k, v := range params {
		sv, err := FromGo(v)
		if err != nil {
			return nil, fmt.Errorf("param %q: %w", k, err)
		}
		out[k] = sv
	}
	return out, nil
}

// FromGo converts a Go value to its closest Starlark equivalent. Map keys
// are emitted in sorted order so the resulting dicts have a deterministic
// iteration order downstream.
func FromGo(v any) (starlark.Value, error) {
	switch t := v.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(t), nil
	case string:
		return starlark.String(t), nil
	case int:
		return starlark.MakeInt(t), nil
	case int64:
		return starlark.MakeInt64(t), nil
	case float64:
		return starlark.Float(t), nil
	case []any:
		elems := make([]starlark.Value, 0, len(t))
		for _, e := range t {
			sv, err := FromGo(e)
			if err != nil {
				return nil, err
			}
			elems = append(elems, sv)
		}
		return starlark.NewList(elems), nil
	case map[string]any:
		d := starlark.NewDict(len(t))
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sv, err := FromGo(t[k])
			if err != nil {
				return nil, err
			}
			// SetKey on a freshly-allocated, unfrozen dict keyed by
			// a string is infallible: insert only errors on frozen
			// tables or unhashable keys, and neither applies here.
			_ = d.SetKey(starlark.String(k), sv)
		}
		return d, nil
	default:
		return nil, fmt.Errorf("unsupported go value of type %T", v)
	}
}

// Write writes v as a Starlark literal (None/True/False, quoted strings,
// numbers, [ ... ] lists, { ... } dicts) to w. Map keys are emitted sorted
// for determinism.
func Write(w io.Writer, v any, indent int) error {
	switch t := v.(type) {
	case nil:
		_, err := io.WriteString(w, "None")
		return err
	case bool:
		if t {
			_, err := io.WriteString(w, "True")
			return err
		}
		_, err := io.WriteString(w, "False")
		return err
	case string:
		_, err := fmt.Fprintf(w, "%q", t)
		return err
	case int, int64, float64:
		_, err := fmt.Fprintf(w, "%v", t)
		return err
	case []any:
		if len(t) == 0 {
			_, err := io.WriteString(w, "[]")
			return err
		}
		if _, err := io.WriteString(w, "[\n"); err != nil {
			return err
		}
		for i, item := range t {
			if _, err := io.WriteString(w, strings.Repeat(" ", indent+4)); err != nil {
				return err
			}
			if err := Write(w, item, indent+4); err != nil {
				return err
			}
			if i < len(t)-1 {
				if _, err := io.WriteString(w, ","); err != nil {
					return err
				}
			}
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, strings.Repeat(" ", indent)); err != nil {
			return err
		}
		_, err := io.WriteString(w, "]")
		return err
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			_, err := io.WriteString(w, "{}")
			return err
		}
		if _, err := io.WriteString(w, "{\n"); err != nil {
			return err
		}
		for i, k := range keys {
			if _, err := io.WriteString(w, strings.Repeat(" ", indent+4)); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "%q: ", k); err != nil {
				return err
			}
			if err := Write(w, t[k], indent+4); err != nil {
				return err
			}
			if i < len(keys)-1 {
				if _, err := io.WriteString(w, ","); err != nil {
					return err
				}
			}
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, strings.Repeat(" ", indent)); err != nil {
			return err
		}
		_, err := io.WriteString(w, "}")
		return err
	default:
		_, err := fmt.Fprintf(w, "%q", fmt.Sprint(t))
		return err
	}
}

// WriteConfigFileAtomic writes cfg to path as a source file assigning the
// top-level `config` variable: `config = { ... }`. The write is atomic
// (temp file + rename) and creates parent directories if needed.
func WriteConfigFileAtomic(path string, cfg map[string]any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.star")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	return atomicSwap(tmp, cfg, tmp.Name(), path, os.Rename, os.Remove)
}

// Managed-section markers delimit the block Rune owns at the end of a
// user's `.star` config. Everything between (and including) the markers is
// regenerated on every extension config update; everything else — comments,
// helper functions, and the user's own `config` mutations — is preserved.
const (
	ManagedBegin = "# --------------------------- MANAGED BY RUNE ---------------------------"
	ManagedEnd   = "# -----------------------------------------------------------------------"
)

const managedConfigGlobal = "rune_config"

const managedMergeDef = `def _rune_merge(dst, src):
    for k, v in src.items():
        if k in dst and type(dst[k]) == "dict" and type(v) == "dict":
            _rune_merge(dst[k], v)
        else:
            dst[k] = v
`

const managedHeaderComment = `# Generated by Rune when extensions update configuration. Do not edit;
# changes here are overwritten. Edit ` + "`config`" + ` above to override values.`

// ParseManagedConfig extracts the rune_config map from src's managed
// section. It returns (nil, false, nil) when no managed section is present.
func ParseManagedConfig(src []byte) (map[string]any, bool, error) {
	body, ok := managedSectionBody(src)
	if !ok {
		return nil, false, nil
	}
	literal, ok := extractManagedLiteral(body)
	if !ok {
		return nil, false, nil
	}
	cfg, err := Decode(Source{
		Src:      []byte(ConfigGlobal + " = " + literal + "\n"),
		Filename: "rune_config.star",
	})
	if err != nil {
		return nil, false, fmt.Errorf("decode managed config: %w", err)
	}
	return cfg, true, nil
}

// extractManagedLiteral returns the `{...}` literal text bound to
// rune_config inside a managed-section body.
func extractManagedLiteral(body string) (string, bool) {
	prefix := managedConfigGlobal + " = "
	_, rest, found := strings.Cut(body, prefix)
	if !found {
		return "", false
	}
	open := strings.IndexByte(rest, '{')
	if open < 0 {
		return "", false
	}
	depth := 0
	for i := open; i < len(rest); i++ {
		switch rest[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return rest[open : i+1], true
			}
		}
	}
	return "", false
}

// findManagedSection locates Rune's managed block in s and returns its
// byte range. begin is the offset of the begin-marker line, contentStart
// is the offset just past that line, and end is the offset of the
// end-marker line. ok is false when no well-formed section is present.
//
// Markers are matched only when they occupy a whole line, and the block is
// taken as the *last* begin-marker line paired with the *first* end-marker
// line that follows it. Rune always appends its block at the end of the
// file, so anchoring on the last begin marker keeps a stray marker that a
// user happened to paste into a comment from hijacking (and silently
// destroying) the real section.
func findManagedSection(s string) (begin, contentStart, end int, ok bool) {
	begin = lastLineIndex(s, ManagedBegin)
	if begin < 0 {
		return 0, 0, 0, false
	}
	contentStart = begin + len(ManagedBegin)
	if contentStart < len(s) && s[contentStart] == '\n' {
		contentStart++
	}
	rel := lineIndex(s[contentStart:], ManagedEnd)
	if rel < 0 {
		return 0, 0, 0, false
	}
	return begin, contentStart, contentStart + rel, true
}

// lineIndex returns the byte offset of the first line in s whose trimmed
// content equals marker, or -1 if none.
func lineIndex(s, marker string) int {
	for off := 0; off <= len(s); {
		nl := strings.IndexByte(s[off:], '\n')
		var line string
		if nl < 0 {
			line = s[off:]
		} else {
			line = s[off : off+nl]
		}
		if strings.TrimSpace(line) == marker {
			return off
		}
		if nl < 0 {
			break
		}
		off += nl + 1
	}
	return -1
}

// lastLineIndex is lineIndex but returns the offset of the last matching
// line.
func lastLineIndex(s, marker string) int {
	last := -1
	for off := 0; off <= len(s); {
		nl := strings.IndexByte(s[off:], '\n')
		var line string
		if nl < 0 {
			line = s[off:]
		} else {
			line = s[off : off+nl]
		}
		if strings.TrimSpace(line) == marker {
			last = off
		}
		if nl < 0 {
			break
		}
		off += nl + 1
	}
	return last
}

// managedSectionBody returns the text between the managed markers
// (exclusive). The second return is false when no section is present.
func managedSectionBody(src []byte) (string, bool) {
	s := string(src)
	_, contentStart, end, ok := findManagedSection(s)
	if !ok {
		return "", false
	}
	return s[contentStart:end], true
}

// definesConfig reports whether src binds a top-level `config` variable
// outside the managed section. It is a lightweight lexical check: a line
// whose first non-blank token is `config` followed by `=`, `[`, or `.`.
// Files that only carry comments or are empty return false, signalling the
// managed block must seed `config` itself.
func definesConfig(src []byte) bool {
	s := string(src)
	if begin, _, _, ok := findManagedSection(s); ok {
		s = s[:begin]
	}
	for line := range strings.SplitSeq(s, "\n") {
		trimmed := strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(trimmed, ConfigGlobal)
		if !ok {
			continue
		}
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			continue
		}
		switch rest[0] {
		case '[', '.':
			return true
		case '=':
			// Exclude `==` comparisons, which are not bindings.
			if strings.HasPrefix(rest, "==") {
				continue
			}
			return true
		}
	}
	return false
}

// RenderManagedSection returns the complete managed block for cfg: the
// begin marker, header comment, rune_config literal, the _rune_merge
// definition, the merge call, and the end marker. The block ends with a
// trailing newline.
//
// When seedConfig is true, the block also binds an empty `config` dict
// before merging, so files that never define `config` themselves (empty
// or comments-only configs) still produce a valid module.
func RenderManagedSection(cfg map[string]any, seedConfig bool) ([]byte, error) {
	var b strings.Builder
	b.WriteString(ManagedBegin)
	b.WriteByte('\n')
	b.WriteString(managedHeaderComment)
	b.WriteByte('\n')
	if seedConfig {
		b.WriteString(ConfigGlobal)
		b.WriteString(" = {}\n")
	}
	b.WriteString(managedConfigGlobal)
	b.WriteString(" = ")
	if err := Write(&b, cfg, 0); err != nil {
		return nil, fmt.Errorf("write managed config: %w", err)
	}
	b.WriteByte('\n')
	b.WriteByte('\n')
	b.WriteString(managedMergeDef)
	b.WriteByte('\n')
	b.WriteString("_rune_merge(")
	b.WriteString(ConfigGlobal)
	b.WriteString(", ")
	b.WriteString(managedConfigGlobal)
	b.WriteString(")\n")
	b.WriteString(ManagedEnd)
	b.WriteByte('\n')
	return []byte(b.String()), nil
}

// UpsertManagedConfig replaces src's managed section with a freshly
// rendered block for cfg, or appends one when none is present.
func UpsertManagedConfig(src []byte, cfg map[string]any) ([]byte, error) {
	block, err := RenderManagedSection(cfg, !definesConfig(src))
	if err != nil {
		return nil, err
	}
	s := string(src)
	if begin, _, end, ok := findManagedSection(s); ok {
		after := s[end+len(ManagedEnd):]
		after = strings.TrimPrefix(after, "\n")
		return []byte(s[:begin] + string(block) + after), nil
	}
	if len(s) > 0 && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	if len(s) > 0 {
		s += "\n"
	}
	return []byte(s + string(block)), nil
}

// WriteManagedConfigFileAtomic reads path (which may be missing or empty),
// deep-merges diff into any existing rune_config (diff wins), upserts the
// managed section, and writes the result atomically.
func WriteManagedConfigFileAtomic(path string, diff map[string]any) error {
	src, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config: %w", err)
	}

	existing, _, err := ParseManagedConfig(src)
	if err != nil {
		return err
	}
	if existing == nil {
		existing = map[string]any{}
	}
	deepMergeMap(existing, diff)

	out, err := UpsertManagedConfig(src, existing)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.star")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	return atomicSwapBytes(tmp, out, tmp.Name(), path, os.Rename, os.Remove)
}

// deepMergeMap recursively merges src into dst. When both sides hold a
// nested map under the same key, the merge recurses; otherwise the src
// value overwrites the dst value (src wins).
func deepMergeMap(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				deepMergeMap(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

// atomicSwapBytes writes payload into w, closes it, and renames src → dest.
// It mirrors atomicSwap but takes pre-rendered bytes rather than a config
// map, for callers (the managed-section writer) that build their own
// payload.
func atomicSwapBytes(
	w io.WriteCloser,
	payload []byte,
	src, dest string,
	rename func(oldpath, newpath string) error,
	remove func(name string) error,
) error {
	if _, err := w.Write(payload); err != nil {
		_ = w.Close()
		_ = remove(src)
		return fmt.Errorf("write config: %w", err)
	}
	if err := w.Close(); err != nil {
		_ = remove(src)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := rename(src, dest); err != nil {
		_ = remove(src)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// atomicSwap writes cfg into w, closes it, and then renames src → dest.
// On any error the temp file at src is best-effort cleaned up.
//
// The rename and cleanup functions are injected so tests can exercise
// every error path without relying on hard-to-provoke filesystem states.
// The production caller always passes os.Rename and os.Remove.
func atomicSwap(
	w io.WriteCloser,
	cfg map[string]any,
	src, dest string,
	rename func(oldpath, newpath string) error,
	remove func(name string) error,
) error {
	if err := writeConfigAssignment(w, cfg); err != nil {
		_ = w.Close()
		_ = remove(src)
		return err
	}
	if err := w.Close(); err != nil {
		_ = remove(src)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := rename(src, dest); err != nil {
		_ = remove(src)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// writeConfigAssignment writes `config = <cfg>\n` to w. It is extracted
// from WriteConfigFileAtomic so each underlying write error path is
// directly testable with an injected io.Writer.
func writeConfigAssignment(w io.Writer, cfg map[string]any) error {
	if _, err := io.WriteString(w, "config = "); err != nil {
		return fmt.Errorf("write prefix: %w", err)
	}
	if err := Write(w, cfg, 0); err != nil {
		return fmt.Errorf("write value: %w", err)
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return fmt.Errorf("write newline: %w", err)
	}
	return nil
}
