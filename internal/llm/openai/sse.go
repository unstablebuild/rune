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

package openai

import (
	"bufio"
	"bytes"
	"io"
	"mime"
	"net/http"

	"github.com/openai/openai-go/v2/option"
)

// sseDataOnlyMiddleware removes server-sent-event blocks that carry no data
// from event-stream responses.
//
// The openai-go v2 decoder dispatches an event on every blank line and
// JSON-decodes its payload, so a keep-alive comment, a data-less "event:"
// block or a stray blank line aborts the whole stream with "unexpected end
// of JSON input". The SSE spec says such blocks must not be dispatched;
// openai-go v3 skips them too (openai/openai-go#621).
func sseDataOnlyMiddleware(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	if isEventStream(resp) {
		resp.Body = &sseDataOnlyBody{rc: resp.Body, r: bufio.NewReader(resp.Body)}
	}
	return resp, nil
}

// isEventStream reports whether resp carries a server-sent-event stream.
//
// The ChatGPT Codex backend omits Content-Type on its streaming responses
// entirely, so the header alone cannot be trusted. Every request this client
// issues is a streaming completion, which makes any successful response an
// event stream; error responses are excluded because openai-go reads their
// body as a JSON error document that must reach it unfiltered.
func isEventStream(resp *http.Response) bool {
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		return resp.StatusCode >= 200 && resp.StatusCode < 300
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	return mediaType == "text/event-stream"
}

type sseDataOnlyBody struct {
	rc      io.ReadCloser
	r       *bufio.Reader
	block   []byte
	hasData bool
	out     []byte
	err     error
}

func (b *sseDataOnlyBody) Read(p []byte) (int, error) {
	for len(b.out) == 0 {
		if b.err != nil {
			return 0, b.err
		}
		line, err := b.r.ReadBytes('\n')
		b.consume(bytes.TrimRight(line, "\r\n"))
		if err != nil {
			b.err = err
			// An unterminated block is never dispatched by the decoder, but
			// forward it so the bytes it sees are unchanged.
			if b.hasData {
				b.out, b.block, b.hasData = b.block, nil, false
			}
		}
	}
	n := copy(p, b.out)
	b.out = b.out[n:]
	return n, nil
}

func (b *sseDataOnlyBody) consume(line []byte) {
	if len(line) == 0 {
		if b.hasData {
			b.out = append(b.block, '\n')
		}
		b.block, b.hasData = nil, false
		return
	}
	name, value, _ := bytes.Cut(line, []byte(":"))
	if len(name) == 0 {
		return
	}
	if string(name) == "data" && len(bytes.TrimSpace(value)) > 0 {
		b.hasData = true
	}
	b.block = append(b.block, line...)
	b.block = append(b.block, '\n')
}

func (b *sseDataOnlyBody) Close() error {
	return b.rc.Close()
}
