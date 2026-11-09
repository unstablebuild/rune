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

// Package main is a tiny program with a deliberate off-by-one
// bug used by the debugshell e2e tests. Sum iterates one past
// n, so Sum(5) returns 21 instead of 15. The bug is a clean
// breakpoint target: stop inside Sum, inspect i and total at
// the boundary.
//
// When invoked with the single argument "wait", main loops
// calling Sum forever (with a small sleep between iterations)
// so the attach test has time to spawn the process, connect
// dlv to it, and reliably hit a breakpoint inside Sum.
package main

import (
	"fmt"
	"os"
	"time"
)

// Sum is intentionally buggy: it iterates one past n.
func Sum(n int) int {
	total := 0

	for i := 1; i <= n+1; i++ {
		total += i
	}
	return total
}

// Other is a sibling helper that intentionally reuses the same
// local names (n, total) so the e2e tests can verify scope
// filtering: when the debuggee is stopped inside Sum, the
// references to n/total inside Other must NOT be highlighted
// as in-scope variables.
func Other(n int) int {
	total := n * 2
	return total
}

// AlwaysFalse exists only as a breakpoint target whose body
// contains a single literal-only statement (`return false`).
// Tree-sitter emits no identifier captures for this line, so
// the regression test for RUNE-177 can pin down that the
// breakpoint still binds.
func AlwaysFalse() bool {
	return false
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "wait" {
		allowAnyTracer()
		for {
			_ = Sum(5)
			_ = Other(3)
			_ = AlwaysFalse()
			time.Sleep(50 * time.Millisecond)
		}
	}
	fmt.Println("Sum:", Sum(5))
}
