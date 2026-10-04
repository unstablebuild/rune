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

package syntax

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestCaptureNameAttributesSupportsMarkupAndTextCaptures(t *testing.T) {
	attrs := DefaultConfig().CaptureNamesAttributes

	assert.Equal(t, term.ColorYellow, captureNameAttributes(attrs, "markup.heading").Fg)
	assert.Equal(t, term.ColorYellow, captureNameAttributes(attrs, "markup.heading.1").Fg)
	assert.Equal(t, term.ColorFuchsia, captureNameAttributes(attrs, "markup.raw.block").Fg)
	assert.Equal(t, term.ColorFuchsia, captureNameAttributes(attrs, "markup.link.url").Fg)
	assert.Equal(t, term.ColorYellow, captureNameAttributes(attrs, "markup.list.checked").Fg)
	assert.Equal(t, term.ColorBlue, captureNameAttributes(attrs, "markup.quote").Fg)
	assert.Equal(t, term.ColorYellow, captureNameAttributes(attrs, "text.title").Fg)
	assert.Equal(t, term.ColorFuchsia, captureNameAttributes(attrs, "text.uri").Fg)
}

func TestCaptureNameAttributesUserConfigOverridesFallback(t *testing.T) {
	attrs := DefaultConfig().CaptureNamesAttributes
	attrs["markup.heading"] = term.Attributes{Fg: term.ColorGreen}
	attrs["markup.heading.2"] = term.Attributes{Fg: term.ColorRed}

	assert.Equal(t, term.ColorRed, captureNameAttributes(attrs, "markup.heading.2").Fg)
	assert.Equal(t, term.ColorGreen, captureNameAttributes(attrs, "markup.heading.3").Fg)
}

func TestCaptureNameAttributesUnknownCaptureReturnsZeroAttributes(t *testing.T) {
	assert.Equal(t, term.Attributes{}, captureNameAttributes(nil, "unknown.capture"))
}

func TestCaptureNameAttributesFallsBackToBaseCapture(t *testing.T) {
	attrs := DefaultConfig().CaptureNamesAttributes

	for _, name := range []string{
		"keyword.function",
		"keyword.type",
		"keyword.return",
		"keyword.conditional",
		"keyword.repeat",
		"keyword.modifier",
		"keyword.operator",
		"keyword.exception",
		"keyword.import",
		"keyword.coroutine",
	} {
		assert.Equal(t, term.ColorYellow, captureNameAttributes(attrs, name).Fg, name)
	}

	assert.Equal(t, term.ColorFuchsia, captureNameAttributes(attrs, "string.escape").Fg)
	assert.Equal(t, term.ColorRed, captureNameAttributes(attrs, "number.float").Fg)
	assert.Equal(t, term.ColorBlue, captureNameAttributes(attrs, "comment.documentation").Fg)
}
