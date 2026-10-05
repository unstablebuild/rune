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

package ide

import (
	"sync"
	"testing"
)

func TestLazyParserConcurrentInit(t *testing.T) {
	lp := &lazyParser{root: &workspaceManagerHandler{}}

	const goroutines = 64
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	results := make([]any, goroutines)
	for i := range results {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			results[i] = lp.parser()
		}(i)
	}
	start.Done()
	done.Wait()

	first := results[0]
	if first == nil {
		t.Fatal("parser() returned nil")
	}
	for i, got := range results {
		if got != first {
			t.Fatalf("goroutine %d got a different parser instance: %v != %v", i, got, first)
		}
	}
}
