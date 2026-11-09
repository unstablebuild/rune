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

package retry

import "time"

// Policy is how a client retries failed calls.
type Policy struct {
	Attempts int
	First    time.Duration
	Factor   float64
}

// DefaultPolicy retries five times, doubling from 100ms.
func DefaultPolicy() Policy {
	first, factor, _ := parseRetryBackoff("100ms*2")
	return Policy{Attempts: 5, First: first, Factor: factor}
}

// SlowPolicy is for calls to rate-limited services.
func SlowPolicy() Policy {
	first, factor, _ := parseRetryBackoff("1s*3")
	return Policy{Attempts: 3, First: first, Factor: factor}
}
