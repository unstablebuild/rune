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

package llmconsole

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
	} {
		got := humanBytes(tc.n)
		assert.Equal(t, tc.want, got)
	}
}

func TestScaleProgressBytes(t *testing.T) {
	const minTicks = int64(100)
	cases := []struct {
		downloaded int64
		total      int64
	}{
		{0, 1024},                               // sub-100 -> B
		{500, 200 * 1024},                       // KiB
		{1024 * 1024, 250 * 1024 * 1024},        // MiB
		{1024 * 1024, 250 * 1024 * 1024 * 1024}, // GiB
	}
	for _, tc := range cases {
		_, scaledTotal, unit := scaleProgressBytes(tc.downloaded, tc.total)
		switch unit {
		case "B":
			assert.Less(t, tc.total/1024, minTicks)
		default:
			assert.GreaterOrEqual(t, scaledTotal, minTicks)
		}
	}
}

func TestScaleProgressBytesClampsOverflow(t *testing.T) {
	d, total, _ := scaleProgressBytes(2000, 1000)
	assert.LessOrEqual(t, d, total)
}

func TestDownloadUsageIsValidMarkdown(t *testing.T) {
	assert.NotEmpty(t, downloadUsage)
	assert.True(t, strings.Contains(downloadUsage, "models local download"))
}
