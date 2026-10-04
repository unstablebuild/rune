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

package llamacpp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ggufBuilder writes well-formed GGUF v3 headers piece by piece so tests
// can construct corrupt or truncated payloads at byte granularity.
type ggufBuilder struct {
	buf    bytes.Buffer
	nKVPos int // byte offset of the n_kv field, so tests can override it later.
}

func newGGUFBuilder(t *testing.T) *ggufBuilder {
	t.Helper()
	b := &ggufBuilder{}
	b.buf.WriteString("GGUF")
	binary.Write(&b.buf, binary.LittleEndian, uint32(3)) // version
	binary.Write(&b.buf, binary.LittleEndian, uint64(0)) // n_tensors
	b.nKVPos = b.buf.Len()
	binary.Write(&b.buf, binary.LittleEndian, uint64(0)) // n_kv (patched later)
	return b
}

func (b *ggufBuilder) setNKV(n uint64) {
	binary.LittleEndian.PutUint64(b.buf.Bytes()[b.nKVPos:b.nKVPos+8], n)
}

func (b *ggufBuilder) writeString(s string) {
	binary.Write(&b.buf, binary.LittleEndian, uint64(len(s)))
	b.buf.WriteString(s)
}

func (b *ggufBuilder) writeKVString(key, val string) {
	b.writeString(key)
	binary.Write(&b.buf, binary.LittleEndian, ggufTypeString)
	b.writeString(val)
}

// writeKVTyped writes a key, then a value-type tag, then the raw value
// using the provided write callback. Lets tests emit any of the integer
// flavours that asInt accepts.
func (b *ggufBuilder) writeKVTyped(key string, t uint32, write func(*bytes.Buffer)) {
	b.writeString(key)
	binary.Write(&b.buf, binary.LittleEndian, t)
	write(&b.buf)
}

func (b *ggufBuilder) writeArrayHeader(elemType uint32, n uint64) {
	binary.Write(&b.buf, binary.LittleEndian, elemType)
	binary.Write(&b.buf, binary.LittleEndian, n)
}

// writePath dumps the current payload to a temp file and returns the path.
func (b *ggufBuilder) writePath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(p, b.buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write gguf: %v", err)
	}
	return p
}

func TestReadGGUFContextLength_Table(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		build     func(t *testing.T) string
		want      int
		wantErr   bool
		isMissing bool
	}{
		{
			name: "uint32_ctx_length",
			build: func(t *testing.T) string {
				b := newGGUFBuilder(t)
				b.setNKV(2)
				b.writeKVString("general.architecture", "llama")
				b.writeKVTyped("llama.context_length", ggufTypeUint32, func(buf *bytes.Buffer) {
					binary.Write(buf, binary.LittleEndian, uint32(8192))
				})
				return b.writePath(t)
			},
			want: 8192,
		},
		{
			name: "int32_ctx_length",
			build: func(t *testing.T) string {
				b := newGGUFBuilder(t)
				b.setNKV(2)
				b.writeKVString("general.architecture", "qwen3")
				b.writeKVTyped("qwen3.context_length", ggufTypeInt32, func(buf *bytes.Buffer) {
					binary.Write(buf, binary.LittleEndian, int32(4096))
				})
				return b.writePath(t)
			},
			want: 4096,
		},
		{
			name: "uint64_ctx_length",
			build: func(t *testing.T) string {
				b := newGGUFBuilder(t)
				b.setNKV(2)
				b.writeKVString("general.architecture", "llama")
				b.writeKVTyped("llama.context_length", ggufTypeUint64, func(buf *bytes.Buffer) {
					binary.Write(buf, binary.LittleEndian, uint64(16384))
				})
				return b.writePath(t)
			},
			want: 16384,
		},
		{
			name: "int64_ctx_length",
			build: func(t *testing.T) string {
				b := newGGUFBuilder(t)
				b.setNKV(2)
				b.writeKVString("general.architecture", "llama")
				b.writeKVTyped("llama.context_length", ggufTypeInt64, func(buf *bytes.Buffer) {
					binary.Write(buf, binary.LittleEndian, int64(2048))
				})
				return b.writePath(t)
			},
			want: 2048,
		},
		{
			name: "ctx_length_before_arch_then_arch",
			// ctx_length appears first; parser stashes it in the default
			// branch only if arch is already known. Without arch the
			// default branch is a no-op and the value is missed. We pin
			// this current behaviour: scanning resumes and arch arrives
			// later but ctx_length is already lost.
			build: func(t *testing.T) string {
				b := newGGUFBuilder(t)
				b.setNKV(2)
				b.writeKVTyped("llama.context_length", ggufTypeUint32, func(buf *bytes.Buffer) {
					binary.Write(buf, binary.LittleEndian, uint32(1234))
				})
				b.writeKVString("general.architecture", "llama")
				return b.writePath(t)
			},
			wantErr:   true,
			isMissing: true,
		},
		{
			name: "missing_ctx_length",
			build: func(t *testing.T) string {
				b := newGGUFBuilder(t)
				b.setNKV(1)
				b.writeKVString("general.architecture", "llama")
				return b.writePath(t)
			},
			wantErr:   true,
			isMissing: true,
		},
		{
			name: "missing_architecture",
			build: func(t *testing.T) string {
				b := newGGUFBuilder(t)
				b.setNKV(1)
				b.writeKVTyped("llama.context_length", ggufTypeUint32, func(buf *bytes.Buffer) {
					binary.Write(buf, binary.LittleEndian, uint32(8192))
				})
				return b.writePath(t)
			},
			wantErr:   true,
			isMissing: true,
		},
		{
			name: "bad_magic",
			build: func(t *testing.T) string {
				dir := t.TempDir()
				p := filepath.Join(dir, "bad.gguf")
				os.WriteFile(p, []byte("NOPEsomestuff"), 0o600)
				return p
			},
			wantErr: true,
		},
		{
			name: "unsupported_version",
			build: func(t *testing.T) string {
				var buf bytes.Buffer
				buf.WriteString("GGUF")
				binary.Write(&buf, binary.LittleEndian, uint32(1))
				binary.Write(&buf, binary.LittleEndian, uint64(0))
				binary.Write(&buf, binary.LittleEndian, uint64(0))
				dir := t.TempDir()
				p := filepath.Join(dir, "old.gguf")
				os.WriteFile(p, buf.Bytes(), 0o600)
				return p
			},
			wantErr: true,
		},
		{
			name: "truncated_header",
			build: func(t *testing.T) string {
				dir := t.TempDir()
				p := filepath.Join(dir, "trunc.gguf")
				os.WriteFile(p, []byte("GG"), 0o600)
				return p
			},
			wantErr: true,
		},
		{
			name: "unknown_kv_type",
			build: func(t *testing.T) string {
				b := newGGUFBuilder(t)
				b.setNKV(1)
				// Fabricate an unknown type id (99).
				b.writeString("general.architecture")
				binary.Write(&b.buf, binary.LittleEndian, uint32(99))
				return b.writePath(t)
			},
			wantErr: true,
		},
		{
			name: "skips_string_array_values",
			build: func(t *testing.T) string {
				b := newGGUFBuilder(t)
				b.setNKV(3)
				b.writeKVString("general.architecture", "llama")
				// An array-of-strings KV in front of context_length so we
				// exercise the array-skipping path.
				b.writeString("tokenizer.ggml.tokens")
				binary.Write(&b.buf, binary.LittleEndian, ggufTypeArray)
				b.writeArrayHeader(ggufTypeString, 3)
				b.writeString("a")
				b.writeString("bb")
				b.writeString("ccc")
				b.writeKVTyped("llama.context_length", ggufTypeUint32, func(buf *bytes.Buffer) {
					binary.Write(buf, binary.LittleEndian, uint32(2048))
				})
				return b.writePath(t)
			},
			want: 2048,
		},
		{
			name: "skips_int_array_values",
			build: func(t *testing.T) string {
				b := newGGUFBuilder(t)
				b.setNKV(3)
				b.writeKVString("general.architecture", "llama")
				b.writeString("tokenizer.ggml.token_type")
				binary.Write(&b.buf, binary.LittleEndian, ggufTypeArray)
				b.writeArrayHeader(ggufTypeUint32, 4)
				for i := range 4 {
					binary.Write(&b.buf, binary.LittleEndian, uint32(i))
				}
				b.writeKVTyped("llama.context_length", ggufTypeUint32, func(buf *bytes.Buffer) {
					binary.Write(buf, binary.LittleEndian, uint32(512))
				})
				return b.writePath(t)
			},
			want: 512,
		},
		{
			name: "missing_file",
			build: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "does-not-exist")
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.build(t)
			got, err := readGGUFContextLength(path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (val=%d)", got)
				}
				if tc.isMissing && !errors.Is(err, errGGUFKeyNotFound) {
					t.Fatalf("err = %v, want errGGUFKeyNotFound", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("readGGUFContextLength: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ctx = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestReadGGUFString_OversizeRejected(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint64(1<<21)) // 2 MiB > 1 MiB cap
	_, err := readGGUFString(&buf)
	if err == nil {
		t.Fatal("want oversize error, got nil")
	}
	if !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("err = %v, want mention of 1 MiB cap", err)
	}
}

func TestAsInt_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   any
		want int
		ok   bool
	}{
		{"uint8", uint8(7), 7, true},
		{"int8", int8(-3), -3, true},
		{"uint16", uint16(65535), 65535, true},
		{"int16", int16(-12345), -12345, true},
		{"uint32", uint32(1 << 20), 1 << 20, true},
		{"int32", int32(-1 << 20), -1 << 20, true},
		{"int64", int64(1 << 40), 1 << 40, true},
		{"uint64_safe", uint64(1 << 40), 1 << 40, true},
		{"uint64_overflow", uint64(1 << 63), 0, false},
		{"string_rejected", "8192", 0, false},
		{"float_rejected", float32(1.0), 0, false},
		{"nil_rejected", nil, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := asInt(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("asInt(%v) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
	// NaN and Inf for float64: not int-typed so must be rejected.
	if _, ok := asInt(math.NaN()); ok {
		t.Fatal("asInt(NaN) returned ok=true")
	}
}

func TestReadGGUFTypedValue_AllScalarTypes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		typ  uint32
		// write emits one value of typ; readGGUFTypedValue must
		// return want with no error and consume exactly the bytes
		// written.
		write func(buf *bytes.Buffer)
		want  any
	}{
		{
			name:  "uint8",
			typ:   ggufTypeUint8,
			write: func(b *bytes.Buffer) { binary.Write(b, binary.LittleEndian, uint8(7)) },
			want:  uint8(7),
		},
		{
			name:  "int8",
			typ:   ggufTypeInt8,
			write: func(b *bytes.Buffer) { binary.Write(b, binary.LittleEndian, int8(-1)) },
			want:  int8(-1),
		},
		{
			name:  "uint16",
			typ:   ggufTypeUint16,
			write: func(b *bytes.Buffer) { binary.Write(b, binary.LittleEndian, uint16(65000)) },
			want:  uint16(65000),
		},
		{
			name:  "int16",
			typ:   ggufTypeInt16,
			write: func(b *bytes.Buffer) { binary.Write(b, binary.LittleEndian, int16(-12345)) },
			want:  int16(-12345),
		},
		{
			name:  "uint32",
			typ:   ggufTypeUint32,
			write: func(b *bytes.Buffer) { binary.Write(b, binary.LittleEndian, uint32(1<<20)) },
			want:  uint32(1 << 20),
		},
		{
			name:  "int32",
			typ:   ggufTypeInt32,
			write: func(b *bytes.Buffer) { binary.Write(b, binary.LittleEndian, int32(-7)) },
			want:  int32(-7),
		},
		{
			name:  "float32",
			typ:   ggufTypeFloat32,
			write: func(b *bytes.Buffer) { binary.Write(b, binary.LittleEndian, float32(1.5)) },
			want:  float32(1.5),
		},
		{
			name:  "bool_true",
			typ:   ggufTypeBool,
			write: func(b *bytes.Buffer) { binary.Write(b, binary.LittleEndian, uint8(1)) },
			want:  true,
		},
		{
			name:  "bool_false",
			typ:   ggufTypeBool,
			write: func(b *bytes.Buffer) { binary.Write(b, binary.LittleEndian, uint8(0)) },
			want:  false,
		},
		{
			name: "string",
			typ:  ggufTypeString,
			write: func(b *bytes.Buffer) {
				binary.Write(b, binary.LittleEndian, uint64(2))
				b.WriteString("ok")
			},
			want: "ok",
		},
		{
			name:  "uint64",
			typ:   ggufTypeUint64,
			write: func(b *bytes.Buffer) { binary.Write(b, binary.LittleEndian, uint64(1<<40)) },
			want:  uint64(1 << 40),
		},
		{
			name:  "int64",
			typ:   ggufTypeInt64,
			write: func(b *bytes.Buffer) { binary.Write(b, binary.LittleEndian, int64(-1)) },
			want:  int64(-1),
		},
		{
			name:  "float64",
			typ:   ggufTypeFloat64,
			write: func(b *bytes.Buffer) { binary.Write(b, binary.LittleEndian, float64(2.5)) },
			want:  float64(2.5),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			tc.write(&buf)
			got, err := readGGUFTypedValue(&buf, tc.typ)
			if err != nil {
				t.Fatalf("readGGUFTypedValue: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %v (%T), want %v (%T)", got, got, tc.want, tc.want)
			}
			if buf.Len() != 0 {
				t.Fatalf("readGGUFTypedValue left %d bytes unread", buf.Len())
			}
		})
	}
}

func TestReadGGUFTypedValue_ArrayOfScalars(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, ggufTypeInt32)
	binary.Write(&buf, binary.LittleEndian, uint64(3))
	for _, v := range []int32{1, 2, 3} {
		binary.Write(&buf, binary.LittleEndian, v)
	}
	got, err := readGGUFTypedValue(&buf, ggufTypeArray)
	if err != nil {
		t.Fatalf("readGGUFTypedValue(array): %v", err)
	}
	if got != nil {
		t.Fatalf("array branch returned %v, want nil", got)
	}
	if buf.Len() != 0 {
		t.Fatalf("array branch left %d bytes", buf.Len())
	}
}

func TestReadGGUFValue_TypeTagThenValue(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, ggufTypeUint32)
	binary.Write(&buf, binary.LittleEndian, uint32(99))
	got, err := readGGUFValue(&buf)
	if err != nil {
		t.Fatalf("readGGUFValue: %v", err)
	}
	if got != uint32(99) {
		t.Fatalf("got %v, want uint32(99)", got)
	}
}
