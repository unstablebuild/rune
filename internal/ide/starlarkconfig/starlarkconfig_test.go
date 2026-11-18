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

package starlarkconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
)

// failingWriter returns errFailingWriter on the (1+failAfter)th write,
// counting from zero. failAfter == 0 fails on the very first write.
type failingWriter struct {
	failAfter int
	count     int
}

var errFailingWriter = errors.New("failing writer")

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.count >= w.failAfter {
		return 0, errFailingWriter
	}
	w.count++
	return len(p), nil
}

// -- Decode -------------------------------------------------------------------

func TestDecode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		src       Source
		wantCfg   map[string]any
		wantErr   string
		assertion func(t *testing.T, cfg map[string]any)
	}{
		{
			name: "base mode binds top-level config",
			src: Source{
				Src: []byte(`config = {"foo": "bar", "n": 42}`),
			},
			wantCfg: map[string]any{"foo": "bar", "n": 42},
		},
		{
			name: "default filename when empty",
			// Failing script confirms the default filename is reported.
			src: Source{
				Src: []byte(`fail("boom")`),
			},
			wantErr: "config.star",
		},
		{
			name: "explicit filename appears in errors",
			src: Source{
				Src:      []byte(`fail("boom")`),
				Filename: "custom.star",
			},
			wantErr: "custom.star",
		},
		{
			name: "params exposed as predeclared globals",
			src: Source{
				Src:    []byte(`config = {"mode": mode, "tui": tui}`),
				Params: map[string]any{"mode": "modal", "tui": true},
			},
			wantCfg: map[string]any{"mode": "modal", "tui": true},
		},
		{
			name: "params encode error surfaces",
			src: Source{
				Src:    []byte(`config = {}`),
				Params: map[string]any{"weird": complex(1, 2)},
			},
			wantErr: "encode params",
		},
		{
			name: "overlay mode preserves existing keys",
			src: Source{
				Src:  []byte(`config["added"] = True`),
				Base: map[string]any{"existing": "yes"},
			},
			wantCfg: map[string]any{"existing": "yes", "added": true},
		},
		{
			name: "overlay mode rebind config layers over the base",
			src: Source{
				Src:  []byte(`config = {"only": 1}`),
				Base: map[string]any{"existing": "kept", "only": 0},
			},
			wantCfg: map[string]any{"existing": "kept", "only": 1},
		},
		{
			name: "overlay mode rebind keeps earlier mutations under it",
			src: Source{
				Src: []byte(`
config["a"] = {"x": 1, "y": 1}
config = {"a": {"y": 2}, "b": 3}
`),
				Base: map[string]any{},
			},
			wantCfg: map[string]any{
				"a": map[string]any{"x": 1, "y": 2},
				"b": 3,
			},
		},
		{
			name: "base FromGo error surfaces",
			src: Source{
				Src:  []byte(`config["x"] = 1`),
				Base: map[string]any{"weird": complex(1, 2)},
			},
			wantErr: "encode base config",
		},
		{
			name: "param collision with config in overlay mode",
			src: Source{
				Src:    []byte(`config["x"] = 1`),
				Params: map[string]any{"config": "stomp"},
				Base:   map[string]any{},
			},
			wantErr: "collides with predeclared base config",
		},
		{
			name: "starlark eval error formatted with backtrace",
			src: Source{
				Src: []byte(`config = unknown_name`),
			},
			wantErr: "undefined: unknown_name",
		},
		{
			name: "starlark syntax error surfaces",
			src: Source{
				// syntax error → starlark.SyntaxError, not EvalError
				Src: []byte(`config = {`),
			},
			wantErr: "starlark:",
		},
		{
			name: "load() is blocked",
			src: Source{
				Src:      []byte(`load("foo.star", "x"); config = {"x": x}`),
				Filename: "loader.star",
			},
			wantErr: `load() is not allowed in loader.star`,
		},
		{
			name: "missing config global",
			src: Source{
				Src: []byte(`x = 1`),
			},
			wantErr: `missing top-level "config" dict`,
		},
		{
			name: "config not a dict",
			src: Source{
				Src: []byte(`config = "not a dict"`),
			},
			wantErr: `"config" must be a dict, got string`,
		},
		{
			name: "DictToMap error from non-string key bubbles up",
			src: Source{
				Src: []byte(`config = {1: "v"}`),
			},
			wantErr: "dict keys must be strings",
		},
		{
			name: "print is silently discarded",
			src: Source{
				Src: []byte(`print("hello"); config = {"ok": True}`),
			},
			wantCfg: map[string]any{"ok": true},
		},
		{
			name: "top-level if is allowed",
			src: Source{
				Src: []byte(`
if True:
    config = {"branch": "yes"}
else:
    config = {"branch": "no"}
`),
			},
			wantCfg: map[string]any{"branch": "yes"},
		},
		{
			name: "overlay mode without rebinding still returns dict from predeclared",
			src: Source{
				// Script never assigns to `config` directly — it only mutates.
				Src:  []byte(`config["a"] = 1`),
				Base: map[string]any{},
			},
			wantCfg: map[string]any{"a": 1},
		},
		{
			name: "builtins callable from script",
			src: Source{
				Src: []byte(`config = {"echoed": echo("hi")}`),
				Builtins: starlark.StringDict{
					"echo": starlark.NewBuiltin("echo",
						func(_ *starlark.Thread, _ *starlark.Builtin,
							args starlark.Tuple, _ []starlark.Tuple,
						) (starlark.Value, error) {
							if len(args) != 1 {
								return nil, errors.New("echo: want 1 arg")
							}
							s, ok := args[0].(starlark.String)
							if !ok {
								return nil, errors.New("echo: want string")
							}
							return starlark.String("echo:" + string(s)), nil
						}),
				},
			},
			wantCfg: map[string]any{"echoed": "echo:hi"},
		},
		{
			name: "builtin name collides with param",
			src: Source{
				Src:    []byte(`config = {}`),
				Params: map[string]any{"echo": "x"},
				Builtins: starlark.StringDict{
					"echo": starlark.NewBuiltin("echo",
						func(_ *starlark.Thread, _ *starlark.Builtin,
							_ starlark.Tuple, _ []starlark.Tuple,
						) (starlark.Value, error) {
							return starlark.None, nil
						}),
				},
			},
			wantErr: `builtin "echo" collides with predeclared param`,
		},
		{
			name: "include is undefined in base mode",
			src: Source{
				Src:     []byte(`include("x.star"); config = {}`),
				Include: func(string, map[string]any) (map[string]any, error) { return nil, nil },
			},
			wantErr: "undefined: include",
		},
		{
			name: "include is undefined without a loader",
			src: Source{
				Src:  []byte(`include("x.star")`),
				Base: map[string]any{},
			},
			wantErr: "undefined: include",
		},
		{
			name: "include param collides with the builtin",
			src: Source{
				Src:     []byte(`config["x"] = 1`),
				Params:  map[string]any{"include": "stomp"},
				Base:    map[string]any{},
				Include: func(string, map[string]any) (map[string]any, error) { return nil, nil },
			},
			wantErr: `param "include" collides with the include builtin`,
		},
		{
			name: "include wants one string argument",
			src: Source{
				Src:     []byte(`include(1)`),
				Base:    map[string]any{},
				Include: func(string, map[string]any) (map[string]any, error) { return nil, nil },
			},
			wantErr: "include: for parameter 1: got int, want string",
		},
		{
			name: "include merges at the call point and later statements win",
			src: Source{
				Src: []byte(`
config["before"] = "script"
config["shared"] = "script"
include("layer.star")
config["after"] = config["from_include"] + "!"
config["shared"] = "script-after"
`),
				Base: map[string]any{"base": true},
				Include: func(path string, current map[string]any) (map[string]any, error) {
					if path != "layer.star" {
						return nil, fmt.Errorf("unexpected path %q", path)
					}
					// The loader sees what the script set before the call.
					if current["before"] != "script" || current["base"] != true {
						return nil, fmt.Errorf("unexpected current %v", current)
					}
					current["from_include"] = "inc"
					current["shared"] = "include"
					return current, nil
				},
			},
			wantCfg: map[string]any{
				"base":         true,
				"before":       "script",
				"from_include": "inc",
				"after":        "inc!",
				"shared":       "script-after",
			},
		},
		{
			name: "rebind after include keeps the included keys",
			src: Source{
				Src: []byte(`
include("layer.star")
config = {"own": 1}
`),
				Base: map[string]any{},
				Include: func(_ string, current map[string]any) (map[string]any, error) {
					current["from_include"] = "inc"
					return current, nil
				},
			},
			wantCfg: map[string]any{"from_include": "inc", "own": 1},
		},
		{
			name: "include error ends the script with it",
			src: Source{
				Src:      []byte("x = 1\ninclude(\"missing.star\")\n"),
				Filename: "user.star",
				Base:     map[string]any{},
				Include: func(path string, _ map[string]any) (map[string]any, error) {
					return nil, fmt.Errorf("include %q (from user.star): boom", path)
				},
			},
			wantErr: `include "missing.star" (from user.star): boom`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Decode(tt.src)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.wantCfg != nil {
				assert.Equal(t, tt.wantCfg, cfg)
			}
			if tt.assertion != nil {
				tt.assertion(t, cfg)
			}
		})
	}
}

// -- ToGo ---------------------------------------------------------------------

func TestToGo(t *testing.T) {
	t.Parallel()

	bigOverflow := new(big.Int).Lsh(big.NewInt(1), 100) // > int64 max
	bigOverflowFloat, _ := new(big.Float).SetInt(bigOverflow).Float64()

	// A starlark.Set is a hashable, iterable value but is intentionally
	// not in ToGo's switch — we use it to exercise the default branch.
	unsupported := starlark.NewSet(0)

	tests := []struct {
		name    string
		input   starlark.Value
		want    any
		wantErr string
	}{
		{name: "none", input: starlark.None, want: nil},
		{name: "bool true", input: starlark.Bool(true), want: true},
		{name: "bool false", input: starlark.Bool(false), want: false},
		{name: "small int fits in int64", input: starlark.MakeInt(7), want: 7},
		{
			name:  "large int falls back to float64",
			input: starlark.MakeBigInt(bigOverflow),
			want:  bigOverflowFloat,
		},
		{name: "float", input: starlark.Float(1.5), want: 1.5},
		{name: "string", input: starlark.String("hi"), want: "hi"},
		{
			name:  "empty list",
			input: starlark.NewList(nil),
			want:  []any{},
		},
		{
			name: "list of mixed scalars",
			input: starlark.NewList([]starlark.Value{
				starlark.MakeInt(1),
				starlark.String("two"),
				starlark.Bool(true),
			}),
			want: []any{1, "two", true},
		},
		{
			name: "list with unsupported element",
			input: starlark.NewList([]starlark.Value{
				starlark.NewSet(0),
			}),
			wantErr: "unsupported starlark value of type set",
		},
		{
			name: "tuple of mixed scalars",
			input: starlark.Tuple{
				starlark.MakeInt(1),
				starlark.String("two"),
			},
			want: []any{1, "two"},
		},
		{
			name: "tuple with unsupported element",
			input: starlark.Tuple{
				starlark.NewSet(0),
			},
			wantErr: "unsupported starlark value of type set",
		},
		{
			name: "dict of mixed values",
			input: func() *starlark.Dict {
				d := starlark.NewDict(2)
				_ = d.SetKey(starlark.String("k"), starlark.MakeInt(1))
				_ = d.SetKey(starlark.String("nested"), starlark.NewList(
					[]starlark.Value{starlark.String("x")},
				))
				return d
			}(),
			want: map[string]any{"k": 1, "nested": []any{"x"}},
		},
		{
			name:    "unsupported type",
			input:   unsupported,
			wantErr: "unsupported starlark value of type set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ToGo(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// -- DictToMap ----------------------------------------------------------------

func TestDictToMap(t *testing.T) {
	t.Parallel()

	t.Run("empty dict", func(t *testing.T) {
		t.Parallel()
		got, err := DictToMap(starlark.NewDict(0))
		require.NoError(t, err)
		assert.Equal(t, map[string]any{}, got)
	})

	t.Run("string keys with mixed values", func(t *testing.T) {
		t.Parallel()
		d := starlark.NewDict(2)
		require.NoError(t, d.SetKey(starlark.String("a"), starlark.MakeInt(1)))
		require.NoError(t, d.SetKey(starlark.String("b"), starlark.String("two")))
		got, err := DictToMap(d)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"a": 1, "b": "two"}, got)
	})

	t.Run("rejects non-string key", func(t *testing.T) {
		t.Parallel()
		d := starlark.NewDict(1)
		require.NoError(t, d.SetKey(starlark.MakeInt(1), starlark.String("v")))
		_, err := DictToMap(d)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dict keys must be strings")
	})

	t.Run("propagates value conversion error", func(t *testing.T) {
		t.Parallel()
		d := starlark.NewDict(1)
		require.NoError(t, d.SetKey(starlark.String("k"), starlark.NewSet(0)))
		_, err := DictToMap(d)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported starlark value of type set")
	})
}

// -- StringDictFromMap --------------------------------------------------------

func TestStringDictFromMap(t *testing.T) {
	t.Parallel()

	t.Run("nil input yields empty dict", func(t *testing.T) {
		t.Parallel()
		got, err := StringDictFromMap(nil)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("converts each entry through FromGo", func(t *testing.T) {
		t.Parallel()
		got, err := StringDictFromMap(map[string]any{
			"s": "hi",
			"n": 7,
			"b": true,
		})
		require.NoError(t, err)
		require.Len(t, got, 3)
		assert.Equal(t, starlark.String("hi"), got["s"])
		assert.Equal(t, "7", got["n"].String())
		assert.Equal(t, starlark.Bool(true), got["b"])
	})

	t.Run("propagates FromGo errors with key prefix", func(t *testing.T) {
		t.Parallel()
		_, err := StringDictFromMap(map[string]any{
			"bad": complex(1, 2),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `param "bad"`)
		assert.Contains(t, err.Error(), "unsupported go value of type complex128")
	})
}

// -- FromGo -------------------------------------------------------------------

func TestFromGo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    any
		wantStr  string // starlark .String() representation; used as primary check when set
		wantType string // starlark .Type() name; used for type assertion
		wantErr  string
	}{
		{name: "nil", input: nil, wantStr: "None", wantType: "NoneType"},
		{name: "bool true", input: true, wantStr: "True", wantType: "bool"},
		{name: "bool false", input: false, wantStr: "False", wantType: "bool"},
		{name: "string", input: "hello", wantStr: `"hello"`, wantType: "string"},
		{name: "int", input: 42, wantStr: "42", wantType: "int"},
		{name: "int64", input: int64(9000), wantStr: "9000", wantType: "int"},
		{name: "float64", input: 1.5, wantStr: "1.5", wantType: "float"},
		{
			name:     "empty list",
			input:    []any{},
			wantStr:  "[]",
			wantType: "list",
		},
		{
			name:     "list of mixed scalars",
			input:    []any{1, "two", true},
			wantStr:  `[1, "two", True]`,
			wantType: "list",
		},
		{
			name:     "empty map",
			input:    map[string]any{},
			wantStr:  "{}",
			wantType: "dict",
		},
		{
			name:     "map with sorted key emission",
			input:    map[string]any{"b": 2, "a": 1},
			wantStr:  `{"a": 1, "b": 2}`,
			wantType: "dict",
		},
		{
			name:    "list propagates child errors",
			input:   []any{complex(1, 2)},
			wantErr: "unsupported go value of type complex128",
		},
		{
			name:    "map propagates child errors",
			input:   map[string]any{"x": complex(1, 2)},
			wantErr: "unsupported go value of type complex128",
		},
		{
			name:    "unsupported type",
			input:   complex(1, 2),
			wantErr: "unsupported go value of type complex128",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := FromGo(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.wantType != "" {
				assert.Equal(t, tt.wantType, got.Type())
			}
			if tt.wantStr != "" {
				assert.Equal(t, tt.wantStr, got.String())
			}
		})
	}
}

// -- Write --------------------------------------------------------------------

func TestWrite(t *testing.T) {
	t.Parallel()

	// Successful encodings via a buffer. These cover the happy path of
	// every supported type.
	encodings := []struct {
		name   string
		input  any
		want   string
		indent int
	}{
		{name: "nil", input: nil, want: "None"},
		{name: "bool true", input: true, want: "True"},
		{name: "bool false", input: false, want: "False"},
		{name: "string with quote", input: `he"llo`, want: `"he\"llo"`},
		{name: "int", input: 7, want: "7"},
		{name: "int64", input: int64(8), want: "8"},
		{name: "float64", input: 1.25, want: "1.25"},
		{name: "empty list", input: []any{}, want: "[]"},
		{
			name:  "list of scalars",
			input: []any{1, "two"},
			want:  "[\n    1,\n    \"two\"\n]",
		},
		{
			name:   "list nested with indent",
			input:  []any{1},
			indent: 2,
			want:   "[\n      1\n  ]",
		},
		{name: "empty map", input: map[string]any{}, want: "{}"},
		{
			name:  "map sorted",
			input: map[string]any{"b": 2, "a": 1},
			want:  "{\n    \"a\": 1,\n    \"b\": 2\n}",
		},
		{
			name:   "map nested with indent",
			input:  map[string]any{"k": "v"},
			indent: 2,
			want:   "{\n      \"k\": \"v\"\n  }",
		},
		{
			name:  "unsupported type formatted as string fallback",
			input: complex(1, 2),
			want:  `"(1+2i)"`,
		},
	}

	for _, tt := range encodings {
		t.Run("encodes "+tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			require.NoError(t, Write(&buf, tt.input, tt.indent))
			assert.Equal(t, tt.want, buf.String())
		})
	}

	// Round-trip: writing a complex map and parsing it back should produce
	// the same Go value.
	t.Run("round-trips through Decode", func(t *testing.T) {
		t.Parallel()
		original := map[string]any{
			"flag":   true,
			"name":   "rune",
			"items":  []any{1, "two", false},
			"nested": map[string]any{"k": "v"},
		}
		var buf bytes.Buffer
		_, _ = io.WriteString(&buf, "config = ")
		require.NoError(t, Write(&buf, original, 0))
		got, err := Decode(Source{Src: buf.Bytes()})
		require.NoError(t, err)
		assert.Equal(t, original, got)
	})

	// Failure injection: every io.WriteString / fmt.Fprintf in Write
	// returns an error in turn. Each entry below specifies an input value
	// and the maximum write index after which the writer must fail. By
	// iterating from 0 to the success count we exercise every error
	// branch in the function.
	failureCases := []struct {
		name  string
		input any
	}{
		{name: "nil", input: nil},
		{name: "bool true", input: true},
		{name: "bool false", input: false},
		{name: "string", input: "x"},
		{name: "int", input: 1},
		{name: "non-empty list", input: []any{1, 2}},
		{name: "non-empty map", input: map[string]any{"a": 1, "b": 2}},
		{name: "default fallback", input: complex(1, 2)},
		{name: "empty list", input: []any{}},
		{name: "empty map", input: map[string]any{}},
	}
	for _, fc := range failureCases {
		t.Run("write error during "+fc.name, func(t *testing.T) {
			t.Parallel()
			// Determine how many writes the success case performs.
			var counter countingWriter
			require.NoError(t, Write(&counter, fc.input, 0))
			require.GreaterOrEqual(t, counter.count, 1)
			for i := 0; i < counter.count; i++ {
				w := &failingWriter{failAfter: i}
				err := Write(w, fc.input, 0)
				require.Errorf(t, err, "expected failure at write %d for %s", i, fc.name)
				assert.ErrorIs(t, err, errFailingWriter)
			}
		})
	}
}

// countingWriter counts writes without ever failing.
type countingWriter struct{ count int }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.count++
	return len(p), nil
}

// -- WriteConfigFileAtomic ----------------------------------------------------

func TestWriteConfigFileAtomic(t *testing.T) {
	t.Parallel()

	t.Run("writes config = { ... } and round-trips through Decode", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "out.star")
		cfg := map[string]any{
			"a": 1,
			"b": []any{"x", "y"},
			"c": map[string]any{"nested": true},
		}
		require.NoError(t, WriteConfigFileAtomic(path, cfg))

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(string(data), "config = "),
			"expected output to start with `config = `, got %q", string(data))
		assert.True(t, strings.HasSuffix(string(data), "\n"),
			"expected output to end with newline, got %q", string(data))

		got, err := Decode(Source{Src: data})
		require.NoError(t, err)
		assert.Equal(t, cfg, got)
	})

	t.Run("creates parent directories", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "deeply", "nested", "config.star")
		require.NoError(t, WriteConfigFileAtomic(path, map[string]any{"k": 1}))
		_, err := os.Stat(path)
		require.NoError(t, err)
	})

	t.Run("leaves no temp file behind on success", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "out.star")
		require.NoError(t, WriteConfigFileAtomic(path, map[string]any{"k": 1}))

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		assert.Equal(t, []string{"out.star"}, names)
	})

	t.Run("mkdir parent fails when parent path is a regular file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		// Create a regular file where WriteConfigFileAtomic expects a
		// directory: "<dir>/blocker" is a file, but we ask it to create
		// "<dir>/blocker/out.star", whose parent ("<dir>/blocker") is
		// not a directory and cannot become one.
		blocker := filepath.Join(dir, "blocker")
		require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

		path := filepath.Join(blocker, "out.star")
		err := WriteConfigFileAtomic(path, map[string]any{"k": 1})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mkdir")
	})

	t.Run("create temp fails when parent dir is unwritable", func(t *testing.T) {
		t.Parallel()
		if os.Getuid() == 0 {
			t.Skip("root bypasses unwritable directories")
		}
		dir := t.TempDir()
		readonly := filepath.Join(dir, "ro")
		require.NoError(t, os.MkdirAll(readonly, 0o555))
		t.Cleanup(func() { _ = os.Chmod(readonly, 0o755) })

		path := filepath.Join(readonly, "out.star")
		err := WriteConfigFileAtomic(path, map[string]any{"k": 1})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create temp")
	})

	t.Run("rename fails when destination is a non-empty directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		// Make `dest` an already-existing non-empty directory — renaming
		// a regular file onto it fails on Linux and macOS.
		dest := filepath.Join(dir, "dest")
		require.NoError(t, os.MkdirAll(dest, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dest, "sentinel"), []byte("x"), 0o644))

		err := WriteConfigFileAtomic(dest, map[string]any{"k": 1})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rename")

		// The temp file should have been cleaned up.
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		assert.Equal(t, []string{"dest"}, names)
	})
}

// -- atomicSwap (internal) ----------------------------------------------------

// fakeWriteCloser records writes into an in-memory buffer and lets tests
// inject an error either during writes or at Close().
type fakeWriteCloser struct {
	buf       bytes.Buffer
	writeErr  error
	closeErr  error
	closeOnce bool
}

func (f *fakeWriteCloser) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.buf.Write(p)
}

func (f *fakeWriteCloser) Close() error {
	if f.closeOnce {
		return nil
	}
	f.closeOnce = true
	return f.closeErr
}

func TestAtomicSwap(t *testing.T) {
	t.Parallel()

	cfg := map[string]any{"k": 1}
	okRename := func(string, string) error { return nil }

	t.Run("happy path writes, closes, renames", func(t *testing.T) {
		t.Parallel()
		f := &fakeWriteCloser{}
		var renamedFrom, renamedTo string
		rename := func(src, dest string) error {
			renamedFrom, renamedTo = src, dest
			return nil
		}
		var removed []string
		remove := func(name string) error {
			removed = append(removed, name)
			return nil
		}
		require.NoError(t, atomicSwap(f, cfg, "tmp", "final", rename, remove))
		assert.Equal(t, "config = {\n    \"k\": 1\n}\n", f.buf.String())
		assert.Equal(t, "tmp", renamedFrom)
		assert.Equal(t, "final", renamedTo)
		assert.True(t, f.closeOnce, "Close() should have been called")
		assert.Empty(t, removed, "no cleanup on success")
	})

	t.Run("write error triggers cleanup and propagates", func(t *testing.T) {
		t.Parallel()
		f := &fakeWriteCloser{writeErr: errFailingWriter}
		var removed []string
		remove := func(name string) error {
			removed = append(removed, name)
			return nil
		}
		err := atomicSwap(f, cfg, "tmp", "final", okRename, remove)
		require.Error(t, err)
		assert.ErrorIs(t, err, errFailingWriter)
		assert.Equal(t, []string{"tmp"}, removed)
	})

	t.Run("close error triggers cleanup and wraps", func(t *testing.T) {
		t.Parallel()
		f := &fakeWriteCloser{closeErr: errors.New("close boom")}
		var removed []string
		remove := func(name string) error {
			removed = append(removed, name)
			return nil
		}
		err := atomicSwap(f, cfg, "tmp", "final", okRename, remove)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "close temp")
		assert.Contains(t, err.Error(), "close boom")
		assert.Equal(t, []string{"tmp"}, removed)
	})

	t.Run("rename error triggers cleanup and wraps", func(t *testing.T) {
		t.Parallel()
		f := &fakeWriteCloser{}
		rename := func(string, string) error { return errors.New("rename boom") }
		var removed []string
		remove := func(name string) error {
			removed = append(removed, name)
			return nil
		}
		err := atomicSwap(f, cfg, "tmp", "final", rename, remove)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rename")
		assert.Contains(t, err.Error(), "rename boom")
		assert.Equal(t, []string{"tmp"}, removed)
	})

	t.Run("remove errors on cleanup are swallowed", func(t *testing.T) {
		t.Parallel()
		// Force a rename failure so cleanup runs, and make remove also
		// error — atomicSwap should still return the rename error, not
		// the cleanup error.
		f := &fakeWriteCloser{}
		rename := func(string, string) error { return errors.New("rename boom") }
		remove := func(string) error { return errors.New("remove boom") }
		err := atomicSwap(f, cfg, "tmp", "final", rename, remove)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rename")
		assert.NotContains(t, err.Error(), "remove boom")
	})
}

// -- writeConfigAssignment (internal) -----------------------------------------

func TestWriteConfigAssignment(t *testing.T) {
	t.Parallel()

	cfg := map[string]any{"k": 1}

	t.Run("writes config = <value>\\n", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		require.NoError(t, writeConfigAssignment(&buf, cfg))
		assert.Equal(t, "config = {\n    \"k\": 1\n}\n", buf.String())
	})

	t.Run("prefix write error", func(t *testing.T) {
		t.Parallel()
		err := writeConfigAssignment(&failingWriter{failAfter: 0}, cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write prefix")
		assert.ErrorIs(t, err, errFailingWriter)
	})

	t.Run("value write error", func(t *testing.T) {
		t.Parallel()
		// Succeed on the prefix, fail during Write(cfg).
		err := writeConfigAssignment(&failingWriter{failAfter: 1}, cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write value")
		assert.ErrorIs(t, err, errFailingWriter)
	})

	t.Run("newline write error", func(t *testing.T) {
		t.Parallel()
		// Count writes to a buffer to find exactly how many writes
		// happen before the trailing newline; fail on that write.
		var counter countingWriter
		require.NoError(t, writeConfigAssignment(&counter, cfg))
		require.GreaterOrEqual(t, counter.count, 2)
		err := writeConfigAssignment(&failingWriter{failAfter: counter.count - 1}, cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write newline")
		assert.ErrorIs(t, err, errFailingWriter)
	})
}
