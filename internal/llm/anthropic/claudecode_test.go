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

package anthropic

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"testing"

	"github.com/cespare/xxhash/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestXXHash64MatchesReferenceSeedZero(t *testing.T) {
	inputs := [][]byte{
		nil,
		[]byte("a"),
		[]byte("abc"),
		[]byte("the quick brown fox jumps over the lazy dog"),
		bytes.Repeat([]byte("x"), 31),
		bytes.Repeat([]byte("y"), 32),
		bytes.Repeat([]byte("z"), 33),
		bytes.Repeat([]byte("claude-code-billing"), 100),
	}
	for _, in := range inputs {
		assert.Equalf(t, xxhash.Sum64(in), xxHash64Checksum(in, 0),
			"seed-0 mismatch for len=%d", len(in))
	}
}

func TestComputeFingerprintDeterministic(t *testing.T) {
	const text = "You are a helpful assistant for the rune editor project."
	fp1 := computeFingerprint(text, claudeCodeVersion)
	fp2 := computeFingerprint(text, claudeCodeVersion)
	require.Equal(t, fp1, fp2)
	assert.Len(t, fp1, 3)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{3}$`), fp1)

	// Short text falls back to '0' for out-of-range indices but is still stable.
	fpShort := computeFingerprint("abc", claudeCodeVersion)
	assert.Equal(t, fpShort, computeFingerprint("abc", claudeCodeVersion))
}

func TestSignClaudeCodeBodyProducesStable5HexCCH(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.63.abc; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`)

	signed := signClaudeCodeBody(body)
	header := gjson.GetBytes(signed, "system.0.text").String()

	cch := regexp.MustCompile(`\bcch=([0-9a-f]{5});`).FindStringSubmatch(header)
	require.Len(t, cch, 2, "expected a 5-hex cch token, got %q", header)
	assert.NotEqual(t, "00000", cch[1])

	// Signing is deterministic for the same body.
	again := signClaudeCodeBody(body)
	assert.Equal(t, string(signed), string(again))
}

func TestSignClaudeCodeBodyNoopWithoutBillingHeader(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"You are a helpful assistant."}],"messages":[]}`)
	assert.Equal(t, string(body), string(signClaudeCodeBody(body)))
}

func TestSignClaudeCodeBodyIsIndependentOfInitialCCH(t *testing.T) {
	withZero := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.63.abc; cc_entrypoint=cli; cch=00000;"}]}`)
	withOther := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.63.abc; cc_entrypoint=cli; cch=12345;"}]}`)
	assert.Equal(t, string(signClaudeCodeBody(withZero)), string(signClaudeCodeBody(withOther)),
		"cch must be derived from the body with cch zeroed, not from the incoming cch")
}

func TestClaudeCodeMiddlewareSignsBody(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.63.abc; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`)
	req, err := http.NewRequest(http.MethodPost, "https://example.test/v1/messages",
		io.NopCloser(bytes.NewReader(body)))
	require.NoError(t, err)

	var seen []byte
	mw := claudeCodeMiddleware()
	_, _ = mw(req, func(r *http.Request) (*http.Response, error) {
		seen, _ = io.ReadAll(r.Body)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})

	header := gjson.GetBytes(seen, "system.0.text").String()
	assert.Regexp(t, regexp.MustCompile(`\bcch=[0-9a-f]{5};`), header)
	assert.NotContains(t, header, "cch=00000;")
	assert.Equal(t, int64(len(seen)), req.ContentLength)
}
