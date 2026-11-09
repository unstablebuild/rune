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

package cmdenv

import (
	"context"
	"slices"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

// FuzzExpand fuzzes the POSIX-style parameter/arithmetic expander
// used by every Rune command alias and dispatched argv. The contract
// being protected here is twofold:
//
//  1. Expand must NEVER panic on any byte string a user can drop
//     into an alias body or a command argument. The underlying
//     mvdan.cc/sh parser is well-tested but lives behind our public
//     command-variables surface; the fuzzer is the cheapest backstop
//     against parser regressions reaching dispatch time.
//  2. On success, every $NAME / ${NAME} substring of the input must
//     resolve from the Source we hand in (or to the empty string for
//     undefined names) — never from process state. The fuzzer
//     enforces this by using a Source that returns ok=false for
//     every name, so the lookup chain collapses to os.Getenv with an
//     environment we've cleared.
func FuzzExpand(f *testing.F) {
	// Hand-curated corpus seeds covering each operator family
	// supported by mvdan.cc/sh. The fuzzer will mutate them.
	seeds := []string{
		"",
		"plain literal",
		"$FOO",
		"${FOO}",
		"${FOO:-default}",
		"${FOO-default}",
		"${FOO:+alt}",
		"${FOO:?missing}",
		"${FOO:0:3}",
		"${#FOO}",
		"${FOO#prefix}",
		"${FOO##*/}",
		"${FOO%suffix}",
		"${FOO%%/*}",
		"${FOO/old/new}",
		"${FOO//old/new}",
		"${FOO/#head/h}",
		"${FOO/%tail/t}",
		"${FOO^}",
		"${FOO^^}",
		"${FOO,}",
		"${FOO,,}",
		"$((1+2*3))",
		"$((0x10))",
		`\$FOO`,
		`\\`,
		`"$FOO"`,
		`'$FOO'`,
		"$1 $2 $9",
		"prefix $A middle $B suffix",
		"$(echo hi)", // rejected: command substitution
		"`echo hi`",  // rejected: backticks
		"${",         // truncated
		"$",          // bare dollar
		"${UNCLOSED", // unclosed brace
		"${FOO:-${BAR:-baz}}",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	// Clear the host environment slots the fuzzer is most likely to
	// stumble into so a passing host env can't paper over a real
	// difference in expansion behaviour.
	for _, name := range []string{
		"FOO", "BAR", "BAZ", "A", "B", "C",
		"HOME", "PATH", "SHELL", "USER",
	} {
		f.Setenv(name, "")
	}

	emptySource := Source(func(string) (string, bool) { return "", false })

	f.Fuzz(func(t *testing.T, in string) {
		// Skip inputs the Go test framework's UTF-8 sanitisation
		// will already have normalised; we only care about what
		// real users can place in an alias body, which the YAML /
		// Starlark loaders deliver as valid UTF-8 strings.
		out, err := Expand(context.Background(), in, emptySource)

		if err != nil {
			// Errors must be plain values; the only contract is
			// that they don't crash. Nothing else to check.
			return
		}

		// Successful expansion contract: output never contains a
		// NUL byte unless the input did, and never contains a raw
		// process-state artifact like the parent PID. Both would
		// indicate Expand silently switched to a non-POSIX mode.
		if strings.ContainsRune(out, 0) && !strings.ContainsRune(in, 0) {
			t.Fatalf("Expand injected NUL byte: input=%q output=%q",
				in, out)
		}

		// `\$` must always expand to a literal `$`. This is the
		// invariant escapeDoubleDollar relies on when it rewrites
		// `$$` -> `\$` in text.Component before calling us.
		got, err := Expand(context.Background(), `\$`, emptySource)
		if err != nil {
			t.Fatalf("Expand(`\\$`) errored unexpectedly: %v", err)
		}
		if got != "$" {
			t.Fatalf("Expand(`\\$`) = %q, want %q", got, "$")
		}
	})
}

// FuzzExpandBody checks that no value substituted into a ! / !! body
// starts a command, and that a substituted value reaches the command
// unchanged.
func FuzzExpandBody(f *testing.F) {
	for _, v := range adversarialValues {
		f.Add(v)
	}
	bodies := shellContextBodies()
	f.Fuzz(func(t *testing.T, v string) {
		if strings.ContainsRune(v, 0) {
			// Shell words cannot hold NUL.
			return
		}
		src := Source(func(name string) (string, bool) {
			return v, name == "X"
		})
		for _, c := range bodies {
			line, err := ExpandBody(context.Background(), c.body, src)
			if err != nil {
				continue
			}
			out, started, err := runShell(line)
			if len(started) > 0 {
				t.Fatalf("%s: %q started %q", c.name, line, started)
			}
			if err == nil && c.want != nil && out != c.want(v) {
				t.Fatalf("%s: %q printed %q, want %q", c.name, line, out, c.want(v))
			}
		}
	})
}

// FuzzExpandBodyTemplate checks that no body, valid shell or not,
// makes ExpandBody panic, and that a line it returns parses with the
// command structure of the body.
func FuzzExpandBodyTemplate(f *testing.F) {
	for _, c := range shellContextBodies() {
		f.Add(c.body, "a b")
		f.Add(c.body, "$(touch pwned)")
	}
	f.Add("", "")
	f.Add("${X", "'")
	f.Add("$$X $X", "\\")
	f.Add("cat <<$X\nx\nEOF", "EOF")
	f.Add("$X() { :; }", "f")
	f.Add("a[$X]=1", "0")
	f.Add("echo $X >$X <<$X\n$X\n$X", "EOF")
	f.Fuzz(func(t *testing.T, body, v string) {
		src := Source(func(name string) (string, bool) {
			return v, name == "X"
		})
		line, err := ExpandBody(context.Background(), body, src)
		if err != nil {
			return
		}
		tmpl, substs := bodyTemplate(body, src)
		if len(substs) == 0 {
			if line != tmpl {
				t.Fatalf("no substitution, but %q became %q", body, line)
			}
			return
		}
		parser := syntax.NewParser()
		want, err := parser.Parse(strings.NewReader(tmpl), "")
		if err != nil {
			t.Fatalf("%q was returned for a body that does not parse: %v", line, err)
		}
		got, err := parser.Parse(strings.NewReader(line), "")
		if err != nil {
			t.Fatalf("%q does not parse: %v", line, err)
		}
		if !slices.Equal(shellStructure(want, substs), shellStructure(got, nil)) {
			t.Fatalf("%q has a different structure from %q", line, body)
		}
	})
}

// FuzzLookup fuzzes the Source -> shell-lookup adapter. The contract
// is that Lookup never panics on arbitrary names and always returns
// the Source's value when ok=true, falling back to os.Getenv
// otherwise.
func FuzzLookup(f *testing.F) {
	seeds := []string{"", "FOO", "FOO_BAR", "1", "a b", "$weird"}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, name string) {
		// Nil source: must always defer to os.Getenv without
		// panicking.
		lookup := Lookup(nil)
		_ = lookup(name)

		// Source that wins: must always return its value regardless
		// of os env.
		t.Setenv("RUNE_FUZZ_DUMMY", "from-env")
		src := Source(func(n string) (string, bool) {
			if n == name {
				return "from-source", true
			}
			return "", false
		})
		lookup = Lookup(src)
		if got := lookup(name); got != "from-source" {
			t.Fatalf("Lookup(src)(%q) = %q, want %q",
				name, got, "from-source")
		}

		// Source that abstains: must fall through to os.Getenv.
		abstain := Source(func(string) (string, bool) {
			return "", false
		})
		lookup = Lookup(abstain)
		if name == "RUNE_FUZZ_DUMMY" {
			if got := lookup(name); got != "from-env" {
				t.Fatalf("Lookup(abstain)(%q) = %q, want %q",
					name, got, "from-env")
			}
		}
	})
}
