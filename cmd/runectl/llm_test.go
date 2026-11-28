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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

func TestParseProviderModel(t *testing.T) {
	tests := []struct {
		in            string
		wantProvider  string
		wantModel     string
		wantErrSubstr string
	}{
		{"openai/gpt-5", "openai", "gpt-5", ""},
		{"anthropic/claude-3", "anthropic", "claude-3", ""},
		{"llamacpp/gemma-3-4b-it-Q4", "llamacpp", "gemma-3-4b-it-Q4", ""},
		{"missingSlash", "", "", "expected provider/model"},
		{"/leading", "", "", "expected provider/model"},
		{"trailing/", "", "", "expected provider/model"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			p, m, err := parseProviderModel(tc.in)
			if tc.wantErrSubstr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrSubstr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantProvider, p)
			assert.Equal(t, tc.wantModel, m)
		})
	}
}

func TestLoadMessageBody_FileVsLiteral(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "msg.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello from file"), 0o644))

	body, err := loadMessageBody(path)
	require.NoError(t, err)
	assert.Equal(t, "hello from file", body)

	// Non-file argument is treated as a literal.
	body, err = loadMessageBody("just a literal string")
	require.NoError(t, err)
	assert.Equal(t, "just a literal string", body)
}

func TestParseResponseFormat(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		rf, err := parseResponseFormat("")
		require.NoError(t, err)
		assert.Nil(t, rf)
	})
	t.Run("text", func(t *testing.T) {
		rf, err := parseResponseFormat("text")
		require.NoError(t, err)
		require.NotNil(t, rf)
		assert.Equal(t, llmapi.ResponseFormatTypeText, rf.Type)
	})
	t.Run("json", func(t *testing.T) {
		rf, err := parseResponseFormat("json")
		require.NoError(t, err)
		require.NotNil(t, rf)
		assert.Equal(t, llmapi.ResponseFormatTypeJSONObject, rf.Type)
	})
	t.Run("json schema with nested schema", func(t *testing.T) {
		raw := `{"name":"city","schema":{"type":"object","properties":{"name":{"type":"string"}}},"strict":true}`
		rf, err := parseResponseFormat(raw)
		require.NoError(t, err)
		require.NotNil(t, rf)
		assert.Equal(t, llmapi.ResponseFormatTypeJSONSchema, rf.Type)
		require.NotNil(t, rf.JSONSchema)
		assert.Equal(t, "city", rf.JSONSchema.Name)
		assert.True(t, rf.JSONSchema.Strict)
		b, err := rf.JSONSchema.Schema.MarshalJSON()
		require.NoError(t, err)
		assert.Contains(t, string(b), `"type":"object"`)
	})
	t.Run("invalid json", func(t *testing.T) {
		_, err := parseResponseFormat("{not json")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--response-format")
	})
}
