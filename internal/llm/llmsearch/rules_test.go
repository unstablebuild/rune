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
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyCode(t *testing.T) {
	text := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader([]byte("x"))), nil }
	video := func() (io.ReadCloser, error) {
		b := bytes.Repeat([]byte{0xFF}, 188*4)
		for p := range 4 {
			b[p*188] = 0x47
		}
		return io.NopCloser(bytes.NewReader(b)), nil
	}
	unreadable := func() (io.ReadCloser, error) { return nil, errors.New("gone") }
	opts := DefaultOptions()
	excluding := DefaultOptions()
	excluding.ExcludedExtensions = []string{".SQL"}

	tests := []struct {
		name string
		size int64
		open func() (io.ReadCloser, error)
		opts Options
		want string
	}{
		{"retry.py", 1, text, opts, ""},
		{"App.swift", 1, text, opts, ""},
		{"README.md", 1, text, opts, ""},
		{"settings.yaml", 1, text, opts, ""},
		{"explore.ipynb", 1, text, opts, ""},
		{"Makefile", 1, text, opts, ""},
		{"Dockerfile.dev", 1, text, opts, ""},
		{"app.ts", 1, text, opts, ""},
		{"app.ts", 1, unreadable, opts, ""},
		{"clip.ts", 1, video, opts, skipOtherType},
		{"package.json", 1, text, opts, skipData},
		{"rows.csv", 1, text, opts, skipData},
		{"yarn.lock", 1, text, opts, skipData},
		{"bundle.min.js", 1, text, opts, skipMinified},
		{"LICENSE.md", 1, text, opts, skipLicense},
		{"prod.pem", 1, text, opts, skipSecret},
		{"id_ed25519", 1, text, opts, skipSecret},
		{"credentials.yaml", 1, text, opts, skipSecret},
		{".env.local", 1, text, opts, skipSecret},
		{"photo.jpg", 1, text, opts, skipOtherType},
		{"schema.sql", 1, text, excluding, skipExcludedType},
		{"big.go", 1<<20 + 1, text, opts, skipTooBig},
		{"big.ipynb", 1<<20 + 1, text, opts, ""},
		{"huge.ipynb", 20<<20 + 1, text, opts, skipTooBig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifyCode(tt.name, tt.size, tt.open, tt.opts))
		})
	}
}
