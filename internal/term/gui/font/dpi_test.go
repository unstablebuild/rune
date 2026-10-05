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

package font

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeviceScaleNoMonitor(t *testing.T) {
	orig := monitorScaleFactor
	t.Cleanup(func() { monitorScaleFactor = orig })

	monitorScaleFactor = func() (float64, bool) { return 0, false }

	assert.NotPanics(t, func() {
		assert.Equal(t, 1.0, deviceScale())
	})
}

func TestDeviceScaleWithMonitor(t *testing.T) {
	orig := monitorScaleFactor
	t.Cleanup(func() { monitorScaleFactor = orig })

	monitorScaleFactor = func() (float64, bool) { return 2.0, true }

	assert.Equal(t, 2.0, deviceScale())
}
