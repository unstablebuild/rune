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

package debug

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartPProfHTTPRandomPortServesHeap(t *testing.T) {
	addr, err := StartPProfHTTP("127.0.0.1:0")
	require.NoError(t, err)
	require.NotEmpty(t, addr)

	url := "http://" + addr + "/debug/pprof/heap?debug=1"
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	// Heap profile in debug=1 format starts with "heap profile:".
	assert.True(t, strings.HasPrefix(string(body), "heap profile:"),
		"pprof heap response should start with heap profile prefix: %q",
		string(body[:min(64, len(body))]))
}

func TestStartPProfHTTPBindError(t *testing.T) {
	_, err := StartPProfHTTP("not-a-valid-addr")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pprof listen")
}
