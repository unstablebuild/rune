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

import (
	"errors"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

// parseRetryBackoff reads a backoff setting such as "250ms*2" into the
// first delay and the factor every attempt multiplies it by.
func parseRetryBackoff(spec string) (time.Duration, float64, error) {
	base, factor, ok := strings.Cut(spec, "*")
	if !ok {
		return 0, 0, errors.New("retry: backoff must look like 250ms*2")
	}
	delay, err := time.ParseDuration(base)
	if err != nil {
		return 0, 0, err
	}
	f, err := strconv.ParseFloat(factor, 64)
	if err != nil {
		return 0, 0, err
	}
	return delay, f, nil
}

// Do calls fn until it succeeds or the attempts run out, sleeping between
// attempts with exponential backoff and random jitter.
func Do(attempts int, spec string, fn func() error) error {
	delay, factor, err := parseRetryBackoff(spec)
	if err != nil {
		return err
	}
	for attempt := 1; ; attempt++ {
		if err = fn(); err == nil || attempt == attempts {
			return err
		}
		jitter := time.Duration(rand.Int63n(int64(delay) / 2))
		time.Sleep(delay + jitter)
		delay = time.Duration(float64(delay) * factor)
	}
}
