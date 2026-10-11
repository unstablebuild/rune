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
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"mvdan.cc/sh/v3/shell"
	"mvdan.cc/sh/v3/syntax"
)

func TestExpandStrictRejectsCommandSubst(t *testing.T) {
	_, err := Expand(context.Background(), "$(echo hi)", nil)
	require.Error(t, err, "strict ctx must reject $(...)")
	_, err = Expand(context.Background(), "`echo hi`", nil)
	require.Error(t, err, "strict ctx must reject backticks")
}

func TestExpandPermissivePreservesCommandSubst(t *testing.T) {
	src := Source(func(name string) (string, bool) {
		if name == "GREETING" {
			return "hello", true
		}
		return "", false
	})
	ctx := WithCommandSubstitution(context.Background())

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain cmdsubst", "$(echo hi)", "$(echo hi)"},
		{"backticks", "`echo hi`", "`echo hi`"},
		{"var then cmdsubst",
			"$GREETING $(echo world)",
			"hello $(echo world)"},
		{"cmdsubst then var",
			"$(echo world) $GREETING",
			"$(echo world) hello"},
		{"nested cmdsubst",
			"$(echo $(date +%Y))", "$(echo $(date +%Y))"},
		{"mixed",
			"prefix-$GREETING-$(date)-end",
			"prefix-hello-$(date)-end"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Expand(ctx, tc.in, src)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestExpandBodyPreservesShellConstructs(t *testing.T) {
	src := Source(func(name string) (string, bool) {
		switch name {
		case "1":
			return "with space", true
		case "FILE":
			return "/tmp/a b.go", true
		}
		return "", false
	})
	ctx := context.Background()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"escape double dollar", "$$N", "$N"},
		{"positional quoted",
			"echo $1", `echo 'with space'`},
		{"named quoted",
			"echo $FILE", `echo '/tmp/a b.go'`},
		{"unknown var passthrough",
			"echo $UNKNOWN_X", "echo $UNKNOWN_X"},
		{"cmdsubst preserved",
			"echo $(date) $FILE",
			`echo $(date) '/tmp/a b.go'`},
		{"backtick preserved",
			"echo `date` $FILE",
			"echo `date` '/tmp/a b.go'"},
		{"parameter expansion suffix-strip preserved",
			`ROOT_NAME=${ROOT##*/}`, `ROOT_NAME=${ROOT##*/}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandBody(ctx, tc.in, src)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestExpandStrictAdversarial(t *testing.T) {
	ctx := context.Background()
	src := Source(func(name string) (string, bool) {
		v, ok := strictEnv[name]
		return v, ok
	})
	for _, tc := range strictExpandCases() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Expand(ctx, tc.in, src)
			if tc.wantErr {
				require.Error(t, err, "input %q", tc.in)
				return
			}
			require.NoError(t, err, "input %q", tc.in)
			assert.Equal(t, tc.want, got, "input %q", tc.in)
		})
	}
}

func TestExpandPermissiveAdversarial(t *testing.T) {
	ctx := WithCommandSubstitution(context.Background())
	src := Source(func(name string) (string, bool) {
		v, ok := permissiveEnv[name]
		return v, ok
	})
	for _, tc := range permissiveExpandCases() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Expand(ctx, tc.in, src)
			if tc.wantErr {
				require.Error(t, err, "input %q", tc.in)
				return
			}
			require.NoError(t, err, "input %q", tc.in)
			assert.Equal(t, tc.want, got, "input %q", tc.in)
		})
	}
}

func TestFindCmdSubstEnd(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		start int
		want  int
	}{
		{"simple", "$(a)", 2, 3},
		{"empty body", "$()", 2, 2},
		{"nested dollar paren", "$(a $(b))", 2, 8},
		{"bare paren group", "$(a (b) c)", 2, 9},
		{"escaped close paren", `$(a \) b)`, 2, 8},
		{"escaped open paren", `$(a \( b)`, 2, 8},
		{"escaped dollar paren still opens a bare group", `$(a \$(b))`, 2, 9},
		{"escaped dollar paren left unbalanced", `$(a \$(b)`, 2, -1},
		{"escaped backslash then close", `$(a \\)`, 2, 6},
		{"trailing backslash", `$(a \`, 2, -1},
		{"unclosed", "$(a", 2, -1},
		{"unclosed nested", "$(a $(b)", 2, -1},
		{"unbalanced bare paren", "$(a (b)", 2, -1},
		{"stops at first balanced close", "$(a) $(b)", 2, 3},
		{"dollar not followed by paren", "$(a $b)", 2, 6},
		{"dollar at end of body", "$(a$", 2, -1},
		{"start past end", "$(", 2, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findCmdSubstEnd(tc.in, tc.start)
			assert.Equal(t, tc.want, got, "input %q", tc.in)
			if got >= 0 {
				assert.Equal(t, byte(')'), tc.in[got])
			}
		})
	}
}

func TestFindBacktickEnd(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		start int
		want  int
	}{
		{"simple", "`a`", 1, 2},
		{"empty", "``", 1, 1},
		{"escaped backtick", "`a \\` b`", 1, 7},
		{"escaped backslash then backtick", "`a \\\\`", 1, 5},
		{"trailing backslash", "`a \\", 1, -1},
		{"unclosed", "`a", 1, -1},
		{"stops at first close", "`a` `b`", 1, 2},
		{"nested dollar paren is not special", "`a $(b` c)`", 1, 6},
		{"start past end", "`", 1, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findBacktickEnd(tc.in, tc.start)
			assert.Equal(t, tc.want, got, "input %q", tc.in)
			if got >= 0 {
				assert.Equal(t, byte('`'), tc.in[got])
			}
		})
	}
}

func TestExpandConcurrent(t *testing.T) {
	ctx := WithCommandSubstitution(context.Background())
	src := Source(func(name string) (string, bool) {
		if name == "A" {
			return "a", true
		}
		return "", false
	})
	const goroutines = 16
	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				got, err := Expand(ctx, "$A $(echo $A) $A", src)
				if err != nil {
					t.Errorf("Expand: %v", err)
					return
				}
				if got != "a $(echo $A) a" {
					t.Errorf("Expand: got %q", got)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestExpandBodyAdversarial(t *testing.T) {
	ctx := context.Background()
	src := Source(func(name string) (string, bool) {
		v, ok := bodyEnv[name]
		return v, ok
	})
	for _, tc := range expandBodyCases() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandBody(ctx, tc.in, src)
			if tc.wantErr {
				require.Error(t, err, "input %q", tc.in)
				return
			}
			require.NoError(t, err, "input %q", tc.in)
			assert.Equal(t, tc.want, got, "input %q", tc.in)
		})
	}
}

func TestExpandBodyNilSource(t *testing.T) {
	got, err := ExpandBody(context.Background(), "$X ${Y} $1 $$ literal", nil)
	require.NoError(t, err)
	assert.Equal(t, "$X ${Y} $1 $ literal", got)
}

func TestExpandBodyPreservesParameterExpansionWithChainVar(t *testing.T) {
	src := Source(func(name string) (string, bool) {
		if name == "ROOT" {
			return "/Users/ernestrc/src/idelsp", true
		}
		return "", false
	})
	got, err := ExpandBody(context.Background(),
		`ROOT_NAME=${ROOT##*/}`, src)
	require.NoError(t, err)
	assert.Equal(t, `ROOT_NAME=${ROOT##*/}`, got,
		"ExpandBody must pass ${VAR##pattern} through verbatim "+
			"so the downstream shell can evaluate it; rewriting "+
			"to the chain value would lose the suffix-strip")
}

// adversarialValues are values that break out of a shell quoting
// context, or start a command, when substituted without escaping.
var adversarialValues = []string{
	"",
	"plain",
	"a b",
	"it's",
	`say "hi"`,
	`back\slash`,
	`trailing\`,
	`\\`,
	"$(touch pwned)",
	"`touch pwned`",
	"'; touch pwned; '",
	`"; touch pwned; "`,
	`\"; touch pwned; \"`,
	`'"$(touch pwned)"'`,
	"$'\\x27'; touch pwned",
	"${IFS}touch${IFS}pwned",
	"$HOME",
	"$$",
	"*",
	"~",
	"!",
	"{a,b}",
	"a\nb",
	"tab\there",
	"crlf\r\n",
	"\x1b]52;c;cHduZWQ=\x07",
	"a\xffb",
	"EOF",
	"a\nEOF\ntouch pwned",
	"Ω",
	"-n",
	"%s",
	"41",
	"007",
	"-1",
	"1.5",
	"0x10",
	"9223372036854775807",
	"9223372036854775808",
	"https://example.com/?a=1&b=$(touch%20pwned)&c='x'`touch pwned`",
}

// shellContextBody references $X in one shell quoting context.
type shellContextBody struct {
	name string
	body string
	// want is what the body prints for a value it substitutes.
	want func(string) string
	// refused reports whether ExpandBody must refuse to substitute a
	// value rather than produce a line.
	refused func(string) bool
}

func shellContextBodies() []shellContextBody {
	same := func(v string) string { return v }
	angled := func(v string) string { return "<" + v + ">" }
	trimmed := func(v string) string { return strings.TrimRight(v, "\n") }
	endsHeredoc := func(v string) bool {
		return strings.Contains("\n<"+v+">\n", "\nEOF\n")
	}
	notPlain := func(v string) bool {
		return !utf8.ValidString(v) || strings.ContainsFunc(v, func(r rune) bool {
			return r != '\n' && r != '\t' && !unicode.IsPrint(r)
		})
	}
	notInt := func(v string) bool { return !isInt64(v) }
	intOf := func(v string) string {
		n, _ := strconv.ParseInt(v, 10, 64)
		return strconv.FormatInt(n, 10)
	}
	return []shellContextBody{
		{name: "unquoted", body: `printf '%s' $X`, want: same},
		{name: "braced", body: `printf '%s' ${X}`, want: same},
		{name: "double quotes", body: `printf '%s' "$X"`, want: same},
		{name: "double quotes with text", body: `printf '%s' "<$X>"`, want: angled},
		{name: "dollar double quotes", body: `printf '%s' $"<$X>"`, want: angled},
		{name: "single quotes", body: `printf '%s' '$X'`, want: same},
		{name: "single quotes with text", body: `printf '%s' '<$X>'`, want: angled},
		{name: "ANSI-C quotes", body: `printf '%s' $'<$X>'`, want: angled},
		{name: "single quotes in double quotes",
			body: `printf '%s' "'$X'"`,
			want: func(v string) string { return "'" + v + "'" }},
		{name: "assignment", body: `y=$X; printf '%s' "$y"`, want: same},
		{name: "command substitution",
			body: `printf '%s' "$(printf '%s' $X)"`, want: trimmed},
		{name: "double quotes in command substitution in double quotes",
			body: `printf '%s' "$(printf '%s' "<$X>")"`,
			want: func(v string) string { return trimmed("<" + v + ">") }},
		{name: "backquotes",
			body: "y=`printf '%s' $X`; printf '%s' \"$y\"", want: trimmed},
		{name: "double quotes in backquotes",
			body: "y=`printf '%s' \"<$X>\"`; printf '%s' \"$y\"",
			want: func(v string) string { return trimmed("<" + v + ">") }},
		{name: "heredoc", body: "cat <<EOF\n<$X>\nEOF",
			want: func(v string) string { return "<" + v + ">\n" },
			refused: func(v string) bool {
				return endsHeredoc(v) || notPlain(v)
			}},
		{name: "quoted heredoc", body: "cat <<'EOF'\n<$X>\nEOF",
			want: func(v string) string { return "<" + v + ">\n" },
			refused: func(v string) bool {
				return endsHeredoc(v) || notPlain(v) || strings.Contains(v, `\`)
			}},
		{name: "comment", body: "printf '%s' $X # it's $X",
			want: same},
		{name: "arithmetic", body: `printf '%s' $(( $X ))`,
			want: intOf, refused: notInt},
		{name: "arithmetic in double quotes", body: `printf '%s' "$(( $X ))"`,
			want: intOf, refused: notInt},
		{name: "arithmetic command", body: `(( i = $X )); printf '%s' "$i"`,
			want: intOf, refused: notInt},
		{name: "let", body: `let i=$X; printf '%s' "$i"`,
			want: intOf, refused: notInt},
		{name: "C-style for", body: `for (( i=$X; i<0; i++ )); do :; done; printf '%s' "$i"`,
			want: intOf, refused: notInt},
		{name: "arithmetic comparison",
			body: `[[ $X -eq 41 ]] && printf yes || printf no`,
			want: func(v string) string {
				if intOf(v) == "41" {
					return "yes"
				}
				return "no"
			},
			refused: notInt},
		{name: "array subscript", body: `a[$X]=1`,
			refused: func(string) bool { return true }},
		{name: "array element in arithmetic", body: `printf '%s' $(( a[$X] ))`,
			want: func(string) string { return "0" }, refused: notInt},
		{name: "variable name test", body: `[[ -v $X ]] || true`,
			want:    func(string) string { return "" },
			refused: func(v string) bool { return !isShellName(v) && !isDigits(v) }},
	}
}

// runShell runs line in Rune's interpreter and returns what it
// printed and the commands, other than cat, it tried to start.
func runShell(line string) (string, [][]string, error) {
	exe := &catOnlyExecutor{}
	var out strings.Builder
	r := Runner{Executor: exe, Stdout: &out}
	_, err := r.Run(context.Background(), line, nil)
	return out.String(), exe.commands(), err
}

func TestExpandBodySubstitutesDataInEveryShellContext(t *testing.T) {
	for _, c := range shellContextBodies() {
		for _, v := range adversarialValues {
			t.Run(c.name+"/"+snippet(v), func(t *testing.T) {
				src := Source(func(name string) (string, bool) {
					return v, name == "X"
				})
				line, err := ExpandBody(context.Background(), c.body, src)
				if c.refused != nil && c.refused(v) {
					require.Error(t, err, "line %q", line)
					return
				}
				require.NoError(t, err)

				out, started, err := runShell(line)
				assert.Empty(t, started,
					"line %q must not start any command but cat", line)
				require.NoError(t, err, "line %q", line)
				assert.Equal(t, c.want(v), out, "line %q", line)
			})
		}
	}
}

func TestShellStructureCatchesNaiveSubstitution(t *testing.T) {
	parser := syntax.NewParser()
	var caught int
	for _, c := range shellContextBodies() {
		for _, v := range adversarialValues {
			src := Source(func(name string) (string, bool) {
				return v, name == "X"
			})
			tmpl, substs := bodyTemplate(c.body, src)
			want, err := parser.Parse(strings.NewReader(tmpl), "")
			require.NoError(t, err)
			var b strings.Builder
			last := 0
			for _, sub := range substs {
				b.WriteString(tmpl[last:sub.start])
				b.WriteString(Quote(sub.value))
				last = sub.end
			}
			b.WriteString(tmpl[last:])
			naive := b.String()
			got, err := parser.Parse(strings.NewReader(naive), "")
			if err != nil {
				continue
			}
			if !startsCommand(got) {
				continue
			}
			caught++
			assert.NotEqual(t, shellStructure(want, substs), shellStructure(got, nil),
				"%s: %q starts a command with the structure of %q", c.name, naive, c.body)
		}
	}
	require.NotZero(t, caught, "the naive substitutions must include injections")
}

func TestBodyTemplate(t *testing.T) {
	env := Source(func(name string) (string, bool) {
		switch name {
		case "A":
			return "va", true
		case "B":
			return "vb", true
		case "1":
			return "v1", true
		case "":
			return "never", true
		}
		return "", false
	})
	for _, tc := range []struct {
		in, want string
		substs   []bodySubst
	}{
		{in: "", want: ""},
		{in: "echo hi", want: "echo hi"},
		{in: "$A", want: "${A}", substs: []bodySubst{{0, 4, "va"}}},
		{in: "${A}", want: "${A}", substs: []bodySubst{{0, 4, "va"}}},
		{in: "x $A y", want: "x ${A} y", substs: []bodySubst{{2, 6, "va"}}},
		{in: "$A$B", want: "${A}${B}", substs: []bodySubst{{0, 4, "va"}, {4, 8, "vb"}}},
		{in: "$A $A", want: "${A} ${A}", substs: []bodySubst{{0, 4, "va"}, {5, 9, "va"}}},
		{in: `"$A"`, want: `"${A}"`, substs: []bodySubst{{1, 5, "va"}}},
		{in: "`$A`", want: "`${A}`", substs: []bodySubst{{1, 5, "va"}}},
		{in: "$((1+$A))", want: "$((1+${A}))", substs: []bodySubst{{5, 9, "va"}}},
		{in: "$1", want: "${1}", substs: []bodySubst{{0, 4, "v1"}}},
		{in: "$12", want: "${1}2", substs: []bodySubst{{0, 4, "v1"}}},
		{in: "$Ab", want: "$Ab"},
		{in: "$A_", want: "$A_"},
		{in: "$UNKNOWN", want: "$UNKNOWN"},
		{in: "${UNKNOWN}", want: "${UNKNOWN}"},
		{in: "$9", want: "$9"},
		{in: "${A", want: "${A}", substs: []bodySubst{{0, 4, "va"}}},
		{in: "${UNKNOWN", want: "${UNKNOWN"},
		{in: "${", want: "${"},
		{in: "${}", want: "${}"},
		{in: "${A:-x}", want: "${A:-x}"},
		{in: "${A##*/}", want: "${A##*/}"},
		{in: "${a[$A]}", want: "${a[$A]}"},
		{in: "${a:$A}", want: "${a:$A}"},
		{in: "$$", want: "$"},
		{in: "$$A", want: "$A"},
		{in: "$$$A", want: "$${A}", substs: []bodySubst{{1, 5, "va"}}},
		{in: "$", want: "$"},
		{in: "a$", want: "a$"},
		{in: "$-", want: "$-"},
		{in: "$(date)", want: "$(date)"},
		{in: `\$A`, want: `\$A`},
		{in: `\\$A`, want: `\\${A}`, substs: []bodySubst{{2, 6, "va"}}},
		{in: `a\`, want: `a\`},
	} {
		t.Run(snippet(tc.in), func(t *testing.T) {
			tmpl, substs := bodyTemplate(tc.in, env)
			assert.Equal(t, tc.want, tmpl)
			assert.Equal(t, tc.substs, substs)
			for _, s := range substs {
				assert.True(t, strings.HasPrefix(tmpl[s.start:s.end], "${"),
					"substitution %+v must cover a ${name} in %q", s, tmpl)
				assert.True(t, strings.HasSuffix(tmpl[s.start:s.end], "}"))
			}
		})
	}
}

func TestBodyTemplateNilSource(t *testing.T) {
	tmpl, substs := bodyTemplate("$A ${B} $1 $$", nil)
	assert.Equal(t, "$A ${B} $1 $", tmpl)
	assert.Empty(t, substs)
}

func TestContextAt(t *testing.T) {
	dq := shellContext{quote: doubleQuoted}
	sq := shellContext{quote: singleQuoted}
	arith := shellContext{arith: true}
	for _, tc := range []struct {
		body string
		want shellContext
	}{
		{body: "echo $X", want: shellContext{}},
		{body: "x=$X", want: shellContext{}},
		{body: "echo ${X}suffix", want: shellContext{}},
		{body: "cat > $X", want: shellContext{}},
		{body: "cat <<< $X", want: shellContext{}},
		{body: "case $X in a) ;; esac", want: shellContext{}},
		{body: "for i in $X; do :; done", want: shellContext{}},
		{body: "[[ $X == a ]]", want: shellContext{}},
		{body: "[[ -z $X ]]", want: shellContext{}},
		{body: "[ $X -eq 1 ]", want: shellContext{}},
		{body: `echo "$X"`, want: dq},
		{body: `echo "<$X>"`, want: dq},
		{body: `echo $"$X"`, want: dq},
		{body: `echo "'$X'"`, want: dq},
		{body: `echo '$X'`, want: sq},
		{body: `echo '<$X>'`, want: sq},
		{body: `echo $'$X'`, want: shellContext{quote: ansiCQuoted}},
		{body: `echo $(echo $X)`, want: shellContext{}},
		{body: `echo "$(echo $X)"`, want: shellContext{}},
		{body: `echo "$(echo "$X")"`, want: dq},
		{body: `echo "$(echo '$X')"`, want: sq},
		{body: `echo <(cat $X)`, want: shellContext{}},
		{body: `echo "<(cat $X)"`, want: dq},
		{body: "echo `echo $X`", want: shellContext{backquotes: 1}},
		{body: "echo `echo \"$X\"`", want: shellContext{quote: doubleQuoted, backquotes: 1}},
		{body: "echo \"`echo $X`\"", want: shellContext{backquotes: 1}},
		{body: "echo `echo $(echo $X)`", want: shellContext{backquotes: 1}},
		{body: "echo $(echo `echo $X`)", want: shellContext{backquotes: 1}},
		{body: "echo `echo \\`echo $X\\``", want: shellContext{backquotes: 2}},
		{body: "echo $(( $X ))", want: arith},
		{body: "echo $(( 1 + $X ))", want: arith},
		{body: `echo "$(( $X ))"`, want: shellContext{quote: doubleQuoted, arith: true}},
		{body: "echo $(( a[$X] ))", want: arith},
		{body: "(( $X ))", want: arith},
		{body: "let i=$X", want: arith},
		{body: `let "i=$X"`, want: shellContext{quote: doubleQuoted, arith: true}},
		{body: "for (( i=$X; i<1; i++ )); do :; done", want: arith},
		{body: "for (( i=0; i<$X; i++ )); do :; done", want: arith},
		{body: "for (( i=0; i<1; i+=$X )); do :; done", want: arith},
		{body: "[[ $X -eq 1 ]]", want: arith},
		{body: "[[ 1 -eq $X ]]", want: arith},
		{body: "[[ $X -ne 1 ]]", want: arith},
		{body: "[[ $X -lt 1 ]]", want: arith},
		{body: "[[ $X -le 1 ]]", want: arith},
		{body: "[[ $X -gt 1 ]]", want: arith},
		{body: "[[ $X -ge 1 ]]", want: arith},
		{body: `[[ "$X" -eq 1 ]]`, want: shellContext{quote: doubleQuoted, arith: true}},
		{body: "[[ -v $X ]]", want: shellContext{name: true}},
		{body: "[[ -R $X ]]", want: shellContext{name: true}},
		{body: "a[$X]=1", want: shellContext{subscript: true}},
		{body: "a[$X]+=1", want: shellContext{subscript: true}},
		{body: "a=( [$X]=1 )", want: shellContext{subscript: true}},
		{body: "a=( $X )", want: shellContext{}},
		{body: "cat <<EOF\n$X\nEOF", want: shellContext{quote: heredoc}},
		{body: "cat <<-EOF\n$X\nEOF", want: shellContext{quote: heredoc}},
		{body: "cat <<EOF\n\"$X\" '$X'\nEOF", want: shellContext{quote: heredoc}},
		{body: "cat <<'EOF'\n$X\nEOF", want: shellContext{quote: literalHeredoc}},
		{body: "cat <<\"EOF\"\n$X\nEOF", want: shellContext{quote: literalHeredoc}},
		{body: "cat <<\\EOF\n$X\nEOF", want: shellContext{quote: literalHeredoc}},
		{body: "cat <<E'O'F\n$X\nEOF", want: shellContext{quote: literalHeredoc}},
		{body: "cat <<EOF\n$(echo $X)\nEOF", want: shellContext{}},
		{body: "echo $X # '$X'", want: shellContext{}},
	} {
		t.Run(snippet(tc.body), func(t *testing.T) {
			tmpl, substs := bodyTemplate(tc.body, Source(func(n string) (string, bool) {
				return "", n == "X"
			}))
			require.NotEmpty(t, substs)
			file, err := syntax.NewParser().Parse(strings.NewReader(tmpl), "")
			require.NoError(t, err)
			assert.Equal(t, tc.want, contextAt(shellSpans(file), substs[0].start))
		})
	}
}

func TestContextAtNesting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spans []shellSpan
		off   int
		want  shellContext
	}{
		{name: "no spans", off: 3},
		{name: "offset at the end of a span is outside it",
			spans: []shellSpan{{start: 0, end: 4, quote: singleQuoted}}, off: 4},
		{name: "offset at the start of a span is inside it",
			spans: []shellSpan{{start: 4, end: 8, quote: singleQuoted}}, off: 4,
			want: shellContext{quote: singleQuoted}},
		{name: "the innermost quoting wins",
			spans: []shellSpan{
				{start: 0, end: 10, quote: singleQuoted},
				{start: 2, end: 8, quote: doubleQuoted},
			},
			off: 4, want: shellContext{quote: doubleQuoted}},
		{name: "innermost is decided by start regardless of order",
			spans: []shellSpan{
				{start: 2, end: 8, quote: doubleQuoted},
				{start: 0, end: 10, quote: singleQuoted},
			},
			off: 4, want: shellContext{quote: doubleQuoted}},
		{name: "a substitution resets quoting and arithmetic",
			spans: []shellSpan{
				{start: 0, end: 20, quote: doubleQuoted},
				{start: 0, end: 20, arith: true},
				{start: 2, end: 18, subst: true},
			},
			off: 5},
		{name: "quoting inside a substitution applies",
			spans: []shellSpan{
				{start: 0, end: 20, quote: doubleQuoted},
				{start: 2, end: 18, subst: true},
				{start: 4, end: 10, quote: singleQuoted},
			},
			off: 5, want: shellContext{quote: singleQuoted}},
		{name: "backquotes are counted through nested substitutions",
			spans: []shellSpan{
				{start: 0, end: 30, subst: true, backquotes: true},
				{start: 2, end: 28, subst: true},
				{start: 4, end: 20, subst: true, backquotes: true},
			},
			off: 5, want: shellContext{backquotes: 2}},
		{name: "an operand and its quoting at the same offsets combine",
			spans: []shellSpan{
				{start: 0, end: 4, arith: true},
				{start: 0, end: 4, quote: doubleQuoted},
			},
			off: 1, want: shellContext{arith: true, quote: doubleQuoted}},
		{name: "the combination does not depend on order",
			spans: []shellSpan{
				{start: 0, end: 4, quote: doubleQuoted},
				{start: 0, end: 4, arith: true},
			},
			off: 1, want: shellContext{arith: true, quote: doubleQuoted}},
		{name: "a subscript inside arithmetic is a subscript",
			spans: []shellSpan{
				{start: 0, end: 10, arith: true},
				{start: 2, end: 6, subscript: true},
			},
			off: 3, want: shellContext{arith: true, subscript: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, contextAt(tc.spans, tc.off))
		})
	}
}

func TestShellSpansSkipsNodesWithoutPosition(t *testing.T) {
	one := &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: "1"}}}
	file := &syntax.File{Stmts: []*syntax.Stmt{{Cmd: &syntax.ArithmCmd{X: one}}}}
	assert.Empty(t, shellSpans(file))
}

func TestEscapeValue(t *testing.T) {
	sq := shellContext{quote: singleQuoted}
	dq := shellContext{quote: doubleQuoted}
	ansi := shellContext{quote: ansiCQuoted}
	hd := shellContext{quote: heredoc}
	lhd := shellContext{quote: literalHeredoc}
	arith := shellContext{arith: true}
	name := shellContext{name: true}
	for _, tc := range []struct {
		name    string
		ctx     shellContext
		in      string
		want    string
		wantErr string
	}{
		{name: "unquoted plain", in: "plain", want: "plain"},
		{name: "unquoted empty", in: "", want: "''"},
		{name: "unquoted space", in: "a b", want: "'a b'"},
		{name: "unquoted single quote", in: "it's", want: `"it's"`},
		{name: "unquoted substitution", in: "$(id)", want: "'$(id)'"},
		{name: "unquoted control", in: "a\tb", want: `$'a\tb'`},
		{name: "unquoted non-utf8", in: "a\xffb", want: `$'a\xffb'`},
		{name: "single quoted plain", ctx: sq, in: "plain", want: "'plain'"},
		{name: "single quoted empty", ctx: sq, in: "", want: "''''"},
		{name: "single quoted space", ctx: sq, in: "a b", want: "''a b''"},
		{name: "single quoted single quote", ctx: sq, in: "it's", want: `'"it's"'`},
		{name: "single quoted control", ctx: sq, in: "a\tb", want: `'$'a\tb''`},
		{name: "ANSI-C quoted plain", ctx: ansi, in: "plain", want: "'plain$'"},
		{name: "ANSI-C quoted single quote", ctx: ansi, in: "it's", want: `'"it's"$'`},
		{name: "ANSI-C quoted backslash", ctx: ansi, in: `a\nb`, want: `''a\nb'$'`},
		{name: "double quoted plain", ctx: dq, in: "plain", want: `"plain"`},
		{name: "double quoted empty", ctx: dq, in: "", want: `"''"`},
		{name: "double quoted space", ctx: dq, in: "a b", want: `"'a b'"`},
		{name: "double quoted single quote", ctx: dq, in: "it's", want: `""it's""`},
		{name: "double quoted specials", ctx: dq, in: "$`\"\\", want: "\"'$`\"\\'\""},
		{name: "double quoted substitution", ctx: dq, in: "$(id)", want: `"'$(id)'"`},
		{name: "double quoted braces", ctx: dq, in: "{a,b}", want: `"'{a,b}'"`},
		{name: "double quoted glob and tilde", ctx: dq, in: "~/*", want: `"'~/*'"`},
		{name: "double quoted newline", ctx: dq, in: "a\nb", want: `"$'a\nb'"`},
		{name: "double quoted carriage return", ctx: dq, in: "a\r\n", want: `"$'a\r\n'"`},
		{name: "double quoted non-utf8", ctx: dq, in: "a\xffb", want: `"$'a\xffb'"`},
		{name: "double quoted unicode", ctx: dq, in: "Ω", want: `"Ω"`},
		{name: "heredoc plain", ctx: hd, in: "plain", want: "plain"},
		{name: "heredoc specials", ctx: hd, in: "$`\\\"'", want: "\\$\\`\\\\\"'"},
		{name: "heredoc substitution", ctx: hd, in: "$(id)", want: `\$(id)`},
		{name: "heredoc newline and tab", ctx: hd, in: "a\n\tb", want: "a\n\tb"},
		{name: "heredoc carriage return", ctx: hd, in: "a\r", wantErr: "control characters"},
		{name: "heredoc escape", ctx: hd, in: "\x1b[0m", wantErr: "control characters"},
		{name: "heredoc non-utf8", ctx: hd, in: "a\xffb", wantErr: "control characters"},
		{name: "literal heredoc plain", ctx: lhd, in: "$`\"'", want: "$`\"'"},
		{name: "literal heredoc backslash", ctx: lhd, in: `a\b`, wantErr: "backslash"},
		{name: "literal heredoc control", ctx: lhd, in: "a\rb", wantErr: "control characters"},
		{name: "subscript", ctx: shellContext{subscript: true}, in: "1",
			wantErr: "array subscript"},
		{name: "subscript in arithmetic", ctx: shellContext{subscript: true, arith: true},
			in: "1", wantErr: "array subscript"},
		{name: "arithmetic integer", ctx: arith, in: "41", want: "41"},
		{name: "arithmetic zero", ctx: arith, in: "0", want: "0"},
		{name: "arithmetic leading zeros", ctx: arith, in: "007", want: "007"},
		{name: "arithmetic max int64", ctx: arith, in: "9223372036854775807",
			want: "9223372036854775807"},
		{name: "arithmetic beyond int64", ctx: arith, in: "9223372036854775808",
			wantErr: "non-negative integer"},
		{name: "arithmetic empty", ctx: arith, in: "", wantErr: "non-negative integer"},
		{name: "arithmetic negative", ctx: arith, in: "-1", wantErr: "non-negative integer"},
		{name: "arithmetic float", ctx: arith, in: "1.5", wantErr: "non-negative integer"},
		{name: "arithmetic hex", ctx: arith, in: "0x10", wantErr: "non-negative integer"},
		{name: "arithmetic variable", ctx: arith, in: "i", wantErr: "non-negative integer"},
		{name: "arithmetic in double quotes", ctx: shellContext{arith: true, quote: doubleQuoted},
			in: "41", want: "41"},
		{name: "arithmetic in single quotes", ctx: shellContext{arith: true, quote: singleQuoted},
			in: "41", want: "41"},
		{name: "name in double quotes", ctx: shellContext{name: true, quote: doubleQuoted},
			in: "foo", want: "foo"},
		{name: "name", ctx: name, in: "foo", want: "foo"},
		{name: "name with underscore and digits", ctx: name, in: "_a1", want: "_a1"},
		{name: "name positional", ctx: name, in: "1", want: "1"},
		{name: "name empty", ctx: name, in: "", wantErr: "variable name"},
		{name: "name leading digit", ctx: name, in: "1a", wantErr: "variable name"},
		{name: "name with dash", ctx: name, in: "a-b", wantErr: "variable name"},
		{name: "name with subscript", ctx: name, in: "a[0]", wantErr: "variable name"},
		{name: "name with substitution", ctx: name, in: "$(id)", wantErr: "variable name"},
		{name: "one backquote level", ctx: shellContext{backquotes: 1},
			in: "a$b", want: `'a\$b'`},
		{name: "two backquote levels", ctx: shellContext{backquotes: 2},
			in: "a$b", want: `'a\\\$b'`},
		{name: "backquotes escape the backslashes quoting adds",
			ctx: shellContext{backquotes: 1}, in: "a\tb", want: `\$'a\\tb'`},
		{name: "backquotes in double quotes",
			ctx: shellContext{backquotes: 1, quote: doubleQuoted}, in: "a$b", want: `"'a\$b'"`},
		{name: "backquotes in single quotes",
			ctx: shellContext{backquotes: 1, quote: singleQuoted}, in: "a`b", want: "''a\\`b''"},
		{name: "backquotes with arithmetic", ctx: shellContext{backquotes: 1, arith: true},
			in: "41", want: "41"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := escapeValue(tc.ctx, tc.in)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPlainText(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"", true},
		{"plain", true},
		{"a b\tc\nd", true},
		{"Ω é 日本", true},
		{"a\rb", false},
		{"\x00", false},
		{"\x1b[0m", false},
		{"\x7f", false},
		{"a\xffb", false},
		{"\u200b", false},
	} {
		assert.Equal(t, tc.want, plainText(tc.in), "%q", tc.in)
	}
}

func TestBackslashEscape(t *testing.T) {
	for _, tc := range []struct {
		in, special, want string
	}{
		{"", "$", ""},
		{"plain", "$", "plain"},
		{"a$b", "$", `a\$b`},
		{"$$", "$", `\$\$`},
		{"a$b`c\"d\\e", "\\$`\"", "a\\$b\\`c\\\"d\\\\e"},
		{"a$b`c\"d\\e", "\\$`", "a\\$b\\`c\"d\\\\e"},
		{"a$b", "", "a$b"},
		{"\\", "\\", `\\`},
	} {
		assert.Equal(t, tc.want, backslashEscape(tc.in, tc.special), "%q %q", tc.in, tc.special)
	}
}

func TestShellValueClassifiers(t *testing.T) {
	for _, tc := range []struct {
		in                   string
		digits, int64, named bool
	}{
		{in: ""},
		{in: "0", digits: true, int64: true, named: false},
		{in: "41", digits: true, int64: true},
		{in: "007", digits: true, int64: true},
		{in: "9223372036854775807", digits: true, int64: true},
		{in: "9223372036854775808", digits: true},
		{in: "99999999999999999999999", digits: true},
		{in: "-1"},
		{in: "+1"},
		{in: "1.5"},
		{in: "1e3"},
		{in: "0x10"},
		{in: " 1"},
		{in: "１"},
		{in: "a", named: true},
		{in: "_", named: true},
		{in: "_1a", named: true},
		{in: "foo_BAR9", named: true},
		{in: "1a"},
		{in: "a-b"},
		{in: "a.b"},
		{in: "a b"},
		{in: "a[0]"},
		{in: "é"},
	} {
		assert.Equal(t, tc.digits, isDigits(tc.in), "isDigits(%q)", tc.in)
		assert.Equal(t, tc.int64, isInt64(tc.in), "isInt64(%q)", tc.in)
		assert.Equal(t, tc.named, isShellName(tc.in), "isShellName(%q)", tc.in)
	}
}

func TestShellStructure(t *testing.T) {
	parse := func(t *testing.T, src string) *syntax.File {
		t.Helper()
		file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
		require.NoError(t, err)
		return file
	}
	stmt := "Stmt false false false"
	for _, tc := range []struct {
		src  string
		want []string
	}{
		{src: "", want: []string{"*syntax.File"}},
		{src: "echo hi", want: []string{"*syntax.File", stmt, "*syntax.CallExpr",
			"*syntax.Word", "*syntax.Word"}},
		{src: `echo "a" 'b' $'c' "d'e'f"`, want: []string{"*syntax.File", stmt,
			"*syntax.CallExpr", "*syntax.Word", "*syntax.Word", "*syntax.Word",
			"*syntax.Word", "*syntax.Word"}},
		{src: "! a", want: []string{"*syntax.File", "Stmt true false false",
			"*syntax.CallExpr", "*syntax.Word"}},
		{src: "a &", want: []string{"*syntax.File", "Stmt false true false",
			"*syntax.CallExpr", "*syntax.Word"}},
		{src: "a | b", want: []string{"*syntax.File", stmt, "BinaryCmd |",
			stmt, "*syntax.CallExpr", "*syntax.Word",
			stmt, "*syntax.CallExpr", "*syntax.Word"}},
		{src: "a && b", want: []string{"*syntax.File", stmt, "BinaryCmd &&",
			stmt, "*syntax.CallExpr", "*syntax.Word",
			stmt, "*syntax.CallExpr", "*syntax.Word"}},
		{src: "a; b", want: []string{"*syntax.File", stmt, "*syntax.CallExpr",
			"*syntax.Word", stmt, "*syntax.CallExpr", "*syntax.Word"}},
		{src: "a > f 2>&1", want: []string{"*syntax.File", stmt, "*syntax.CallExpr",
			"*syntax.Word", "Redirect >", "*syntax.Word", "Redirect >&", "*syntax.Word"}},
		{src: "a <<EOF\nx\nEOF", want: []string{"*syntax.File", stmt, "*syntax.CallExpr",
			"*syntax.Word", "Redirect <<", "*syntax.Word", "*syntax.Word"}},
		{src: "echo $y ${z}", want: []string{"*syntax.File", stmt, "*syntax.CallExpr",
			"*syntax.Word", "*syntax.Word", "ParamExp y", "*syntax.Word", "ParamExp z"}},
		{src: "echo $(( 1 + -x ))", want: []string{"*syntax.File", stmt, "*syntax.CallExpr",
			"*syntax.Word", "*syntax.Word", "*syntax.ArithmExp", "BinaryArithm +",
			"*syntax.Word", "UnaryArithm - false", "*syntax.Word"}},
		{src: "(( x++ ))", want: []string{"*syntax.File", stmt, "*syntax.ArithmCmd",
			"UnaryArithm ++ true", "*syntax.Word"}},
		{src: "[[ a == b ]]", want: []string{"*syntax.File", stmt, "*syntax.TestClause",
			"BinaryTest ==", "*syntax.Word", "*syntax.Word"}},
		{src: "[[ -n a ]]", want: []string{"*syntax.File", stmt, "*syntax.TestClause",
			"UnaryTest -n", "*syntax.Word"}},
		{src: "case x in y) ;; z) ;& esac", want: []string{"*syntax.File", stmt,
			"*syntax.CaseClause", "*syntax.Word", "CaseItem ;;", "*syntax.Word",
			"CaseItem ;&", "*syntax.Word"}},
		{src: "echo $(a) `b`", want: []string{"*syntax.File", stmt, "*syntax.CallExpr",
			"*syntax.Word", "*syntax.Word", "*syntax.CmdSubst", stmt, "*syntax.CallExpr",
			"*syntax.Word", "*syntax.Word", "*syntax.CmdSubst", stmt, "*syntax.CallExpr",
			"*syntax.Word"}},
	} {
		t.Run(snippet(tc.src), func(t *testing.T) {
			assert.Equal(t, tc.want, shellStructure(parse(t, tc.src), nil))
		})
	}

	t.Run("substitution sites are skipped", func(t *testing.T) {
		tmpl := "echo ${X} ${Y}"
		substs := []bodySubst{{start: 5, end: 9}}
		assert.Equal(t, []string{"*syntax.File", stmt, "*syntax.CallExpr",
			"*syntax.Word", "*syntax.Word", "*syntax.Word", "ParamExp Y"},
			shellStructure(parse(t, tmpl), substs))
		assert.Equal(t, shellStructure(parse(t, "echo 'v' ${Y}"), nil),
			shellStructure(parse(t, tmpl), substs))
	})

	t.Run("a parameter expansion without a name", func(t *testing.T) {
		file := &syntax.File{Stmts: []*syntax.Stmt{{Cmd: &syntax.CallExpr{
			Args: []*syntax.Word{{Parts: []syntax.WordPart{&syntax.ParamExp{}}}},
		}}}}
		assert.Equal(t, []string{"*syntax.File", stmt, "*syntax.CallExpr",
			"*syntax.Word", "ParamExp "}, shellStructure(file, nil))
	})

	for _, tc := range []struct{ a, b string }{
		{"echo a", "echo a b"},
		{"echo a", "echo a; touch b"},
		{"echo a", "echo a > f"},
		{"echo a", "echo a &"},
		{"echo a", "! echo a"},
		{"echo a", "echo $a"},
		{"echo a", "echo $(a)"},
		{"echo a", "echo `a`"},
		{"echo a", "echo $(( a ))"},
		{"a | b", "a || b"},
		{"[[ a == b ]]", "[[ a != b ]]"},
		{"cat <<EOF\na\nEOF", "cat <<EOF\na\nEOF\nb"},
	} {
		t.Run(tc.a+" vs "+tc.b, func(t *testing.T) {
			assert.NotEqual(t, shellStructure(parse(t, tc.a), nil),
				shellStructure(parse(t, tc.b), nil))
		})
	}
}

func TestExpandBodyRefusals(t *testing.T) {
	src := Source(func(name string) (string, bool) {
		switch name {
		case "X":
			return "plain", true
		case "EOF":
			return "EOF", true
		}
		return "", false
	})
	for _, tc := range []struct {
		name, body string
		wantErr    []string
	}{
		{name: "invalid shell syntax", body: "<$X>",
			wantErr: []string{`parse "<$X>"`, "must be followed by a word"}},
		{name: "expansion as heredoc delimiter", body: "cat <<$X\nhi\nEOF",
			wantErr: []string{"parse", "heredoc"}},
		{name: "expansion as function name", body: "$X() { :; }",
			wantErr: []string{"parse", "func name"}},
		{name: "value cannot be substituted", body: "echo $(( $X ))",
			wantErr: []string{"substitute ${X}", "non-negative integer", `"plain"`}},
		{name: "value into an array subscript", body: "a[$X]=1",
			wantErr: []string{"substitute ${X}", "array subscript"}},
		{name: "value that ends the here-document", body: "cat <<EOF\n$EOF\nEOF",
			wantErr: []string{"refusing to run", "change its structure"}},
		{name: "value after the process id", body: "echo $$$X",
			wantErr: []string{"refusing to run"}},
		{name: "value that becomes an assignment", body: `$X=1`,
			wantErr: []string{"refusing to run"}},
		{name: "value that becomes an assignment prefix", body: `$X=1 printf hi`,
			wantErr: []string{"refusing to run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, err := ExpandBody(context.Background(), tc.body, src)
			require.Error(t, err)
			assert.Empty(t, line)
			for _, want := range tc.wantErr {
				assert.ErrorContains(t, err, want)
			}
		})
	}
}

func TestExpandBodyMixedContexts(t *testing.T) {
	src := Source(func(name string) (string, bool) {
		switch name {
		case "1":
			return "a b", true
		case "2":
			return "it's", true
		case "N":
			return "41", true
		case "V":
			return "x", true
		case "CMD":
			return "printf", true
		case "GT":
			return ">", true
		}
		return "", false
	})
	for _, tc := range []struct {
		body, want, out string
	}{
		{
			body: `printf '%s|%s|%s|%s|%s' $1 "$1" '$1' $(( $N )) "$(printf '%s' "$2")"`,
			want: `printf '%s|%s|%s|%s|%s' 'a b' ""'a b'"" '''a b''' $(( 41 )) "$(printf '%s' """it's""")"`,
			out:  "a b|a b|a b|41|it's",
		},
		{
			body: "y=`printf '%s' \"<$2>\"`; [[ -v $V ]]; printf '%s' \"$y\"",
			want: "y=`printf '%s' \"<\"\"it's\"\">\"`; [[ -v x ]]; printf '%s' \"$y\"",
			out:  "<it's>",
		},
		{
			body: "cat <<EOF\n$1 $$RUNE_UNSET $UNKNOWN\nEOF",
			want: "cat <<EOF\na b $RUNE_UNSET $UNKNOWN\nEOF",
			out:  "a b  \n",
		},
		{
			body: "cat <<'EOF'\n$1 $$RUNE_UNSET $UNKNOWN\nEOF",
			want: "cat <<'EOF'\na b $RUNE_UNSET $UNKNOWN\nEOF",
			out:  "a b $RUNE_UNSET $UNKNOWN\n",
		},
		{
			body: `$CMD '%s' "$1"`,
			want: `printf '%s' ""'a b'""`,
			out:  "a b",
		},
		{
			body: `printf '%s' $GT file`,
			want: `printf '%s' '>' file`,
			out:  ">file",
		},
	} {
		t.Run(snippet(tc.body), func(t *testing.T) {
			line, err := ExpandBody(context.Background(), tc.body, src)
			require.NoError(t, err)
			assert.Equal(t, tc.want, line)
			out, started, err := runShell(line)
			require.NoError(t, err)
			assert.Empty(t, started)
			assert.Equal(t, tc.out, out)
		})
	}
}

// startsCommand reports whether running file in Rune's interpreter
// starts a command other than cat. A background job counts without
// running it: the interpreter races with its own background jobs.
func startsCommand(file *syntax.File) (started bool) {
	syntax.Walk(file, func(node syntax.Node) bool {
		if s, ok := node.(*syntax.Stmt); ok && s.Background {
			started = true
		}
		return !started
	})
	if started {
		return true
	}
	defer func() {
		// The interpreter panics on some lines, such as [[ -v '' ]];
		// those start nothing.
		_ = recover()
	}()
	var b strings.Builder
	if err := syntax.NewPrinter().Print(&b, file); err != nil {
		return false
	}
	_, cmds, _ := runShell(b.String())
	return len(cmds) > 0
}

// catOnlyExecutor runs cat in-process and records every other command
// it is asked to start without starting it.
type catOnlyExecutor struct {
	mu      sync.Mutex
	started [][]string
}

func (e *catOnlyExecutor) StartCommand(
	_ context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	var err error
	if cmd.Path == "cat" && len(cmd.Args) == 0 {
		_, err = io.Copy(cmd.Stdout, cmd.Stdin)
	} else {
		e.mu.Lock()
		e.started = append(e.started, append([]string{cmd.Path}, cmd.Args...))
		e.mu.Unlock()
		err = errors.New("command not allowed")
	}
	cmd.Watcher.WatchProcess() <- err
	return 1, nil
}

func (e *catOnlyExecutor) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }

func (e *catOnlyExecutor) Close() error { return nil }

func (e *catOnlyExecutor) commands() [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.started)
}

func TestQuoteRoundTripsThroughShellFields(t *testing.T) {
	cases := []string{
		"",
		"plain",
		"with space",
		"a$b",
		"a'b",
		`a"b`,
		"a`b",
		"a;b|c&d",
		"unicode-Ω-é",
		strings.Repeat("x", 4096),
	}
	for _, c := range cases {
		t.Run(snippet(c), func(t *testing.T) {
			fields, err := shell.Fields(
				Quote(c), func(string) string { return "" })
			require.NoError(t, err, "input %q", c)
			require.Len(t, fields, 1, "input %q", c)
			assert.Equal(t, c, fields[0], "input %q", c)
		})
	}
}

func TestQuoteNULBytesAreStripped(t *testing.T) {
	fields, err := shell.Fields(
		Quote("a\x00b"), func(string) string { return "" })
	require.NoError(t, err)
	require.Len(t, fields, 1)
	assert.Equal(t, "ab", fields[0],
		"shell.Fields currently strips NUL inside '...' — "+
			"adjust if mvdan/sh fixes this")
}

func TestQuoteANSICForms(t *testing.T) {
	cases := []struct {
		in, quoted string
	}{
		{"tab\there", `$'tab\there'`},
		{"line1\nline2", `$'line1\nline2'`},
		{"bell\x07", `$'bell\a'`},
		{"a\xffb", `$'a\xffb'`},
	}
	for _, c := range cases {
		t.Run(snippet(c.in), func(t *testing.T) {
			assert.Equal(t, c.quoted, Quote(c.in))
			fields, err := shell.Fields(
				c.quoted, func(string) string { return "" })
			require.NoError(t, err)
			require.Len(t, fields, 1)
			assert.Equal(t, c.in, fields[0])
		})
	}
}

func TestQuoteNonUTF8(t *testing.T) {
	in := "a\xffb"
	got := Quote(in)
	assert.Equal(t, `$'a\xffb'`, got)
}

func TestEscapeDoubleDollar(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"$", "$"},
		{"$$", `\$`},
		{"$$$$", `\$\$`},
		{"$$N", `\$N`},
		{"$N", "$N"},
		{"a$$b", `a\$b`},
		{`\$$`, `\\$`},
		{"$$$", `\$$`}, // first $$ consumed, trailing $ untouched
		{strings.Repeat("$$", 1024),
			strings.Repeat(`\$`, 1024)},
	}
	for _, c := range cases {
		t.Run(snippet(c.in), func(t *testing.T) {
			assert.Equal(t, c.want, EscapeDoubleDollar(c.in))
		})
	}
}

func TestQuoteForShellFields(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", `""`},
		{"plain", "plain", "plain"},
		{"space wraps", "with space", `"with space"`},
		{"dquote inside escaped", `has"q`, `"has\"q"`},
		{"squote inside is fine", "has'q", `"has'q"`},
		{"backtick escaped", "has`bt", "\"has\\`bt\""},
		{"dollar is preserved",
			"$VAR", "$VAR"},
		{"pipe quoted", "a|b", `"a|b"`},
		{"redirect quoted", "a>b", `"a>b"`},
		{"amp quoted", "a&b", `"a&b"`},
		{"glob quoted", "*.go", `"*.go"`},
		{"semicolon quoted", "a;b", `"a;b"`},
		{"equals quoted", "a=b", `"a=b"`},
		{"nul passes through",
			"a\x00b", "a\x00b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, QuoteForShellFields(c.in))
		})
	}
}

func TestQuoteArgsForShellFields(t *testing.T) {
	assert.Equal(t, []string{}, QuoteArgsForShellFields(nil))
	assert.Equal(t, []string{}, QuoteArgsForShellFields([]string{}))
	got := QuoteArgsForShellFields([]string{"", "a|b", "plain"})
	assert.Equal(t, []string{`""`, `"a|b"`, "plain"}, got)
}

type expandCase struct {
	name    string
	in      string
	want    string
	wantErr bool
}

var strictEnv = map[string]string{
	"NAME":   "world",
	"EMPTY":  "",
	"BIG":    strings.Repeat("x", 65536),
	"NUL":    "a\x00b",
	"UTF8":   "héllo",
	"NOUTF8": "a\xffb",
}

func strictExpandCases() []expandCase {
	return []expandCase{
		{name: "literal", in: "hello", want: "hello"},
		{name: "named var",
			in: "hi $NAME", want: "hi world"},
		{name: "braced var",
			in: "hi ${NAME}!", want: "hi world!"},
		{name: "default value",
			in: "${MISSING:-fallback}", want: "fallback"},
		{name: "default uses set var",
			in: "${NAME:-fallback}", want: "world"},
		{name: "unknown var is empty",
			in: "[$UNKNOWN]", want: "[]"},
		{name: "arithmetic",
			in: "$((1+2*3))", want: "7"},
		{name: "arithmetic with var",
			in: "$((100))", want: "100"},
		{name: "empty input", in: "", want: ""},
		{name: "empty var",
			in: "[${EMPTY}]", want: "[]"},
		{name: "very large var",
			in:   "<$BIG>",
			want: "<" + strings.Repeat("x", 65536) + ">"},
		{name: "embedded nul in var",
			in: "<$NUL>", want: "<a\x00b>"},
		{name: "utf8 var",
			in: "<$UTF8>", want: "<héllo>"},
		{name: "non-utf8 var",
			in: "<$NOUTF8>", want: "<a\xffb>"},
		{name: "backslash before dollar is literal",
			in: `\$NAME`, want: "$NAME"},
		{name: "cmdsubst rejected",
			in: "$(echo hi)", wantErr: true},
		{name: "backtick rejected",
			in: "`echo hi`", wantErr: true},
	}
}

var permissiveEnv = map[string]string{
	"NAME": "world",
}

func permissiveExpandCases() []expandCase {
	return []expandCase{
		{name: "no substitution",
			in: "plain text", want: "plain text"},
		{name: "var still expands",
			in: "hi $NAME", want: "hi world"},
		{name: "cmdsubst preserved at end",
			in: "echo $(date)", want: "echo $(date)"},
		{name: "cmdsubst preserved at start",
			in: "$(date) end", want: "$(date) end"},
		{name: "cmdsubst preserved in middle",
			in:   "before $(date) after",
			want: "before $(date) after"},
		{name: "two adjacent cmdsubsts",
			in:   "$(a)$(b)",
			want: "$(a)$(b)"},
		{name: "nested cmdsubst at depth 5",
			in:   "$(a $(b $(c $(d $(e)))))",
			want: "$(a $(b $(c $(d $(e)))))"},
		{name: "backtick preserved",
			in: "echo `date`", want: "echo `date`"},
		{name: "mixed var and cmdsubst",
			in:   "$NAME $(echo $NAME) $NAME",
			want: "world $(echo $NAME) world"},
		{name: "escaped dollar paren stays literal",
			in: `\$(date)`, want: "$(date)"},
		{name: "unclosed paren falls through to shell.Expand and errors",
			// findCmdSubstEnd returns -1; the remainder is handed
			// to shell.Expand which surfaces the unmatched paren.
			in:      "echo $(date",
			wantErr: true},
		{name: "unclosed backtick falls through to shell.Expand and errors",
			// findBacktickEnd returns -1; the remainder is handed
			// to shell.Expand which surfaces the unclosed quote.
			in:      "echo `date",
			wantErr: true},
		{name: "arithmetic looks like $( and is preserved verbatim",
			// findCmdSubstEnd cannot distinguish $((..)) from
			// $(..) without re-parsing, so arithmetic spans are
			// preserved alongside command substitutions. The
			// downstream shell evaluates either form.
			in:   "$((1+1))",
			want: "$((1+1))"},
		{name: "braced var still expands",
			in:   "<${NAME}>",
			want: "<world>"},
		{name: "cmdsubst body with quotes preserved",
			in:   `$(echo "); echo hi")`,
			want: `$(echo "); echo hi")`},
		{name: "cmdsubst body containing dollar paren preserved",
			in:   "$(echo $(echo nested))",
			want: "$(echo $(echo nested))"},
		{name: "many cmdsubsts on one line",
			in:   strings.Repeat("$(a)", 50),
			want: strings.Repeat("$(a)", 50)},
		{name: "escaped paren does not close cmdsubst",
			in:   `$(echo \) x)`,
			want: `$(echo \) x)`},
		{name: "bare paren group nested in cmdsubst",
			in:   "$(a (b) c)",
			want: "$(a (b) c)"},
		{name: "trailing backslash leaves cmdsubst unclosed",
			in:      `$(echo \`,
			wantErr: true},
		{name: "escaped backtick does not close backtick span",
			in:   "`echo \\` x`",
			want: "`echo \\` x`"},
		{name: "trailing backslash leaves backtick unclosed",
			in:      "`echo \\",
			wantErr: true},
		{name: "var between backtick spans expands",
			in:   "`a` <$NAME> `b`",
			want: "`a` <world> `b`"},
		{name: "expansion error before cmdsubst",
			in:      "${MISSING:?msg} $(date)",
			wantErr: true},
		{name: "expansion error before backtick",
			in:      "${MISSING:?msg} `date`",
			wantErr: true},
		{name: "parse error before cmdsubst",
			in:      "${NAME $(date)",
			wantErr: true},
		{name: "parse error before backtick",
			in:      "${NAME `date`",
			wantErr: true},
		{name: "parse error after last cmdsubst",
			in:      "$(date) ${NAME",
			wantErr: true},
	}
}

var bodyEnv = map[string]string{
	"NAME":   "world",
	"N":      "41",
	"BIGN":   "5555555555555555555555555570",
	"BRACES": "{a,b}",
	"1":      "first arg",
	"2":      "",
	"WITH$":  "dollar-key", // a key the parser cannot reach (no $$ in input)
	"NL":     "line1\nline2",
	"NUL":    "a\x00b",
	"NOUTF8": "a\xffb",
	"SQ":     "it's",
}

func expandBodyCases() []expandCase {
	return []expandCase{
		{name: "empty input", in: "", want: ""},
		{name: "plain text", in: "hello world", want: "hello world"},
		{name: "named var",
			in: "hi $NAME", want: "hi world"},
		{name: "braced var",
			in: "x${NAME}y", want: "xworldy"},
		{name: "invalid shell with a substitution is refused",
			in: "<${NAME}>", wantErr: true},
		{name: "invalid shell without a substitution is left to the shell",
			in: "<${UNKNOWN}>", want: "<${UNKNOWN}>"},
		{name: "unknown name with missing close brace",
			in: "${UNKNOWN", want: "${UNKNOWN"},
		{name: "integer in arithmetic",
			in: "echo $(( $N + 1 ))", want: "echo $(( 41 + 1 ))"},
		{name: "integer beyond int64 in arithmetic is refused",
			in: "echo $(( $BIGN + 1 ))", wantErr: true},
		{name: "string in arithmetic is refused",
			in: "echo $(( $NAME + 1 ))", wantErr: true},
		{name: "double quotes",
			in: `echo "<$1>"`, want: `echo "<"'first arg'">"`},
		{name: "double quotes with a value the shell would expand",
			in: `echo "<${BRACES}>"`, want: `echo "<"'{a,b}'">"`},
		{name: "single quotes",
			in: `echo '<$1>'`, want: `echo '<''first arg''>'`},
		{name: "substitution right after $$ is refused",
			in: "echo $$$NAME", wantErr: true},
		{name: "${NAME}suffix",
			in: "${NAME}suffix", want: "worldsuffix"},
		{name: "${} empty stays literal",
			in: "${}", want: "${}"},
		{name: "missing close brace still resolves the name",
			// ExpandBody's brace scan consumes until '}' or EOF;
			// when EOF is reached it still looks up the name and
			// emits the (quoted) value. This documents the actual
			// behavior rather than asserting an ideal one.
			in: "${NAME", want: "world"},
		{name: "positional $1 quoted",
			in: "echo $1", want: `echo 'first arg'`},
		{name: "positional $2 empty quoted with surroundings",
			in: "[$2]", want: "['']"},
		{name: "unknown positional passes through",
			in: "$9", want: "$9"},
		{name: "double dollar becomes literal dollar",
			in: "$$", want: "$"},
		{name: "double dollar before var name",
			in: "$$NAME", want: "$NAME"},
		{name: "escape preserves both bytes",
			in: `\$NAME`, want: `\$NAME`},
		{name: "cmdsubst preserved",
			in: "$(date)", want: "$(date)"},
		{name: "backtick preserved",
			in: "`date`", want: "`date`"},
		{name: "nested cmdsubst preserved",
			in:   "$(a $(b))",
			want: "$(a $(b))"},
		{name: "value with newline gets quoted",
			in: "$NL", want: `$'line1\nline2'`},
		{name: "value with nul gets quoted",
			in: "$NUL", want: "'a\x00b'"},
		{name: "value with non-utf8 gets quoted",
			in: "$NOUTF8", want: `$'a\xffb'`},
		{name: "value with single quote gets quoted",
			in: "$SQ", want: `"it's"`},
		{name: "value not re-expanded",
			// NAME is "world"; even though we then inject the
			// literal "$NAME" inside the value below, ExpandBody
			// must NOT walk it again.
			in: "$NAME$NAME", want: "worldworld"},
	}
}

func snippet(s string) string {
	const max = 32
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
