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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/debug"
)

func TestFormatStampNormalizesToUTC(t *testing.T) {
	loc := time.FixedZone("UTC+2", 2*60*60)
	got := formatStamp(time.Date(2026, 7, 9, 13, 35, 42, 0, loc))
	assert.Equal(t, "2026-07-09T11:35:42Z", got)
}

func TestFormatStampParsesWithBuildDateLayout(t *testing.T) {
	want := time.Date(2026, 7, 9, 13, 35, 42, 0, time.UTC)
	stamp := formatStamp(want)

	parsed, err := time.Parse(debug.BuildDateLayout, stamp)
	require.NoError(t, err, "emitted stamp must parse with the gating layout")
	assert.True(t, parsed.UTC().Equal(want),
		"emitted stamp must round-trip to the original instant")
}
