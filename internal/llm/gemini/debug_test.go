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

package gemini

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

func TestDebugTransportPreservesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assert.Equal(t, `{"in":1}`, string(body))
		_, _ = w.Write([]byte(`{"out":2}`))
	}))
	t.Cleanup(srv.Close)

	rt := &debugTransport{base: http.DefaultTransport}
	req, err := http.NewRequest(http.MethodPost, srv.URL,
		io.NopCloser(stringReader(`{"in":1}`)))
	require.NoError(t, err)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, `{"out":2}`, string(got))
}

func TestDebugHTTPStreamsEndToEnd(t *testing.T) {
	chunks := []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`,
	}
	srv, cfg := sseServer(t, chunks)
	_ = srv
	cfg.DebugHTTP = true
	svc := NewClient("k", cfg)

	model := llmapi.ModelEntry{Name: Gemini_3_Flash_Preview, Provider: LLMProvider}
	events := drain(t, svc, model, llmapi.Request{
		Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
	})

	var text strings.Builder
	for _, ev := range events {
		if ev.Type == llmapi.EventTextDelta {
			text.WriteString(ev.Text)
		}
	}
	assert.Equal(t, "hi", text.String())
}

type stringReader string

func (s stringReader) Read(p []byte) (int, error) {
	n := copy(p, s)
	if n < len(s) {
		return n, nil
	}
	return n, io.EOF
}
