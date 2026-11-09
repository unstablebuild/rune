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

package vteparser

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/term/vte/vtescanner"
)

type programStatusHandler struct {
	nopHandler
	reports []programstatus.Report
	queries []string
	prompts int
}

func (h *programStatusHandler) ProgramStatus(r programstatus.Report) {
	h.reports = append(h.reports, r)
}

func (h *programStatusHandler) ProgramStatusQuery(terminator string) {
	h.queries = append(h.queries, terminator)
}

func (h *programStatusHandler) PromptStart() { h.prompts++ }

type pointerShapeHandler struct {
	nopHandler
	calls []string
}

func (h *pointerShapeHandler) SetPointerShape(shape PointerShape) {
	h.calls = append(h.calls, "set("+string(shape)+")")
}

func (h *pointerShapeHandler) PushPointerShape(shape PointerShape) {
	h.calls = append(h.calls, "push("+string(shape)+")")
}

func (h *pointerShapeHandler) PopPointerShape() {
	h.calls = append(h.calls, "pop()")
}

func (h *pointerShapeHandler) PointerShapeQuery(names []string, terminator string) {
	h.calls = append(h.calls, "query("+strings.Join(names, ",")+")"+terminator)
}

func TestOSCDispatchPointerShape(t *testing.T) {
	t.Parallel()
	const st, bel = "\x1b\\", "\x07"
	osc := func(body string) string { return "\x1b]22;" + body + st }
	cases := []struct {
		name   string
		stream string
		want   []string
	}{
		{"name", osc("pointer"), []string{"set(pointer)"}},
		{"BEL terminated", "\x1b]22;text" + bel, []string{"set(text)"}},
		{"case insensitive", osc("Col-Resize"), []string{"set(col-resize)"}},
		{"name without a shape of its own", osc("wait"), []string{"set(wait)"}},
		{"x11 name", osc("xterm"), []string{"set(text)"}},
		{"x11 open hand", osc("hand1"), []string{"set(grab)"}},
		{"set operation", osc("=move"), []string{"set(move)"}},
		{"set applies every name in order", osc("=wait,pointer"),
			[]string{"set(wait)", "set(pointer)"}},
		{"set stops at the first unknown name", osc("wait,nope,pointer"), []string{"set(wait)"}},
		{"empty name lets the terminal choose", osc(""), []string{"set()"}},
		{"missing name lets the terminal choose", "\x1b]22" + st, []string{"set()"}},
		{"set operation without a name", osc("="), []string{"set()"}},
		{"push operation", osc(">ns-resize"), []string{"push(ns-resize)"}},
		{"push applies every name in order", osc(">wait,pointer"),
			[]string{"push(wait)", "push(pointer)"}},
		{"push skips empty names", osc(">,text,"), []string{"push(text)"}},
		{"push without a name", osc(">"), nil},
		{"pop", osc("<"), []string{"pop()"}},
		{"pop ignores names", osc("<pointer,text"), []string{"pop()"}},
		{"query", osc("?pointer,nope,__current__"), []string{"query(pointer,nope,__current__)" + st}},
		{"BEL terminated query", "\x1b]22;?text" + bel, []string{"query(text)" + bel}},
		{"unknown names are ignored", osc("nope"), nil},
		{"extra parameters are ignored", osc("pointer;text"), nil},
		{"sequences apply in order", osc("text") + osc(""), []string{"set(text)", "set()"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := new(pointerShapeHandler)
			NewParser(h).AdvanceBytes([]byte(tc.stream))
			assert.Equal(t, tc.want, h.calls)
		})
	}
}

func TestOSCDispatchProgramStatus(t *testing.T) {
	t.Parallel()
	const (
		st  = "\x1b\\"
		bel = "\x07"
		// introducer is what precedes the body on the wire: ESC ] 7501 ;
		introducer = "\x1b]7501;"
	)
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	encBytes := func(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
	osc := func(body string) string { return introducer + body + st }
	rep := func(state programstatus.State) programstatus.Report {
		return programstatus.Report{State: state, Progress: -1}
	}
	working := rep(programstatus.Working)
	workingWith := func(f func(*programstatus.Report)) programstatus.Report {
		r := working
		f(&r)
		return r
	}
	withMsg := func(msg string) programstatus.Report {
		return workingWith(func(r *programstatus.Report) { r.Msg = msg })
	}
	withTitle := func(title string) programstatus.Report {
		return workingWith(func(r *programstatus.Report) { r.Title = title })
	}
	withID := func(id string) programstatus.Report {
		return workingWith(func(r *programstatus.Report) { r.ID = id })
	}
	withApp := func(app string) programstatus.Report {
		return workingWith(func(r *programstatus.Report) { r.App = app })
	}
	withProgress := func(p int) programstatus.Report {
		return workingWith(func(r *programstatus.Report) { r.Progress = p })
	}
	blockedKind := func(k programstatus.Kind) programstatus.Report {
		r := rep(programstatus.Blocked)
		r.Kind = k
		return r
	}
	seg := func(n int) string { return strings.Repeat("s", n) }
	cjk := "構築中…完了まで待機"
	emoji := "🚀 deploying 🎉"

	cases := []struct {
		name        string
		stream      string
		wantReports []programstatus.Report
		wantQueries []string
		wantPrompts int
	}{
		// --- states and terminators ---
		{
			name:        "idle with ST",
			stream:      osc("state=idle"),
			wantReports: []programstatus.Report{rep(programstatus.Idle)},
		},
		{
			name:        "working with BEL",
			stream:      introducer + "state=working" + bel,
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "done with ST",
			stream:      osc("state=done"),
			wantReports: []programstatus.Report{rep(programstatus.Done)},
		},
		{
			name:        "blocked with BEL",
			stream:      introducer + "state=blocked" + bel,
			wantReports: []programstatus.Report{rep(programstatus.Blocked)},
		},
		{
			name:        "error with ST",
			stream:      osc("state=error"),
			wantReports: []programstatus.Report{rep(programstatus.Error)},
		},
		{
			name:        "clear with ST",
			stream:      osc("state=clear"),
			wantReports: []programstatus.Report{rep(programstatus.Clear)},
		},
		{
			name:   "clear keeps only the id",
			stream: osc("state=clear:id=job:kind=permission:progress=50:app=x:title=" + enc("T") + ":msg=" + enc("M")),
			wantReports: []programstatus.Report{{
				ID: "job", State: programstatus.Clear, Progress: -1,
			}},
		},
		{
			name:   "clear still validates text",
			stream: osc("state=clear:msg=AAAAA"),
		},
		{
			name:   "state is case sensitive",
			stream: osc("state=Working"),
		},
		{
			name:   "state empty",
			stream: osc("state="),
		},
		{
			name:        "state padded with whitespace",
			stream:      osc("  state =  working  "),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "state repeated last wins",
			stream:      osc("state=working:state=done"),
			wantReports: []programstatus.Report{rep(programstatus.Done)},
		},
		{
			name:   "state repeated first unknown rejects",
			stream: osc("state=paused:state=done"),
		},
		{
			name:   "state unknown",
			stream: osc("state=paused"),
		},
		{
			name:   "state missing",
			stream: osc("app=brew:msg=" + enc("hi")),
		},
		{
			name:   "empty body",
			stream: osc(""),
		},
		{
			name:   "missing body",
			stream: "\x1b]7501" + st,
		},
		{
			name:   "body of separators",
			stream: osc(":::"),
		},
		{
			name:   "body of whitespace",
			stream: osc("   "),
		},
		{
			name:        "rejected report does not affect the next",
			stream:      osc("state=paused") + osc("state=idle"),
			wantReports: []programstatus.Report{rep(programstatus.Idle)},
		},

		// --- query ---
		{
			name:        "query echoes terminator ST",
			stream:      osc("?"),
			wantQueries: []string{st},
		},
		{
			name:        "query echoes terminator BEL",
			stream:      introducer + "?" + bel,
			wantQueries: []string{bel},
		},
		{
			name:        "query with surrounding whitespace",
			stream:      introducer + " ? " + st,
			wantQueries: []string{st},
		},
		{
			name:   "double question mark is neither query nor report",
			stream: osc("??"),
		},
		{
			name:        "question mark pair inside a report is a malformed pair",
			stream:      osc("?:state=working"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "question mark after a report is a malformed pair",
			stream:      osc("state=working:?"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "query and report in one stream",
			stream:      introducer + "?" + bel + osc("state=working"),
			wantReports: []programstatus.Report{working},
			wantQueries: []string{bel},
		},

		// --- pairs ---
		{
			name: "every key",
			stream: osc("state=blocked:kind=question:id=deploy/eu-west:progress=40:app=deploy:title=" +
				enc("EU West") + ":msg=" + enc("Approve?")),
			wantReports: []programstatus.Report{{
				ID: "deploy/eu-west", State: programstatus.Blocked, Kind: programstatus.KindQuestion,
				Progress: 40, App: "deploy", Title: "EU West", Msg: "Approve?",
			}},
		},
		{
			name: "keys in any order",
			stream: osc("msg=" + enc("Approve?") + ":title=" + enc("EU West") + ":app=deploy:progress=40" +
				":id=deploy/eu-west:kind=question:state=blocked"),
			wantReports: []programstatus.Report{{
				ID: "deploy/eu-west", State: programstatus.Blocked, Kind: programstatus.KindQuestion,
				Progress: 40, App: "deploy", Title: "EU West", Msg: "Approve?",
			}},
		},
		{
			name:        "unknown keys ignored",
			stream:      osc("state=working:future=1:zz=y:x="),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "unknown key at the key limit ignored",
			stream:      osc("abcdefghijklmnop=1:state=working"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:   "key over the limit rejects",
			stream: osc("state=working:abcdefghijklmnopq=1"),
		},
		{
			name:   "key uppercase is a malformed pair",
			stream: osc("State=working"),
		},
		{
			name:        "known key uppercase is a malformed pair",
			stream:      osc("state=working:App=brew:MSG=" + enc("x")),
			wantReports: []programstatus.Report{working},
		},
		{
			name:   "key with digit is a malformed pair",
			stream: osc("state2=working"),
		},
		{
			name:   "key with underscore is a malformed pair",
			stream: osc("sta_te=working"),
		},
		{
			name:        "malformed pairs skipped",
			stream:      osc("state=working:noequals:=orphan:="),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "empty pairs skipped",
			stream:      osc("::state=working::"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "equals inside a value stays in the value",
			stream:      osc("state=working:app=a=b"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "shell metacharacters in a value skip the pair",
			stream:      osc("state=working:app=$(rm -rf /):msg=`id`:title=a|b&c"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:   "shell metacharacters in state reject",
			stream: osc("state=$(working)"),
		},
		{
			name:        "space inside a value skips the pair",
			stream:      osc("state=working:app=my app"),
			wantReports: []programstatus.Report{working},
		},

		// --- kind ---
		{
			name:        "kind permission",
			stream:      osc("state=blocked:kind=permission"),
			wantReports: []programstatus.Report{blockedKind(programstatus.KindPermission)},
		},
		{
			name:        "kind question",
			stream:      osc("state=blocked:kind=question"),
			wantReports: []programstatus.Report{blockedKind(programstatus.KindQuestion)},
		},
		{
			name:        "kind auth",
			stream:      osc("state=blocked:kind=auth"),
			wantReports: []programstatus.Report{blockedKind(programstatus.KindAuth)},
		},
		{
			name:        "kind unknown is none",
			stream:      osc("state=blocked:kind=sudo"),
			wantReports: []programstatus.Report{blockedKind(programstatus.KindNone)},
		},
		{
			name:        "kind is case sensitive",
			stream:      osc("state=blocked:kind=Permission"),
			wantReports: []programstatus.Report{blockedKind(programstatus.KindNone)},
		},
		{
			name:        "kind ignored unless blocked",
			stream:      osc("state=working:kind=permission") + osc("state=done:kind=auth") + osc("state=error:kind=question"),
			wantReports: []programstatus.Report{working, rep(programstatus.Done), rep(programstatus.Error)},
		},
		{
			name:        "kind repeated last wins",
			stream:      osc("state=blocked:kind=auth:kind=question"),
			wantReports: []programstatus.Report{blockedKind(programstatus.KindQuestion)},
		},

		// --- progress ---
		{
			name:        "progress zero",
			stream:      osc("state=working:progress=0"),
			wantReports: []programstatus.Report{withProgress(0)},
		},
		{
			name:        "progress full",
			stream:      osc("state=working:progress=100"),
			wantReports: []programstatus.Report{withProgress(100)},
		},
		{
			name:        "progress leading zeros",
			stream:      osc("state=working:progress=007"),
			wantReports: []programstatus.Report{withProgress(7)},
		},
		{
			name:        "progress over 100 indeterminate",
			stream:      osc("state=working:progress=101"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "progress four digits indeterminate",
			stream:      osc("state=working:progress=1000"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "progress negative indeterminate",
			stream:      osc("state=working:progress=-1"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "progress signed indeterminate",
			stream:      osc("state=working:progress=+5"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "progress decimal indeterminate",
			stream:      osc("state=working:progress=5.5"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "progress non-numeric indeterminate",
			stream:      osc("state=working:progress=abc"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "progress empty indeterminate",
			stream:      osc("state=working:progress="),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "progress percent sign skips the pair",
			stream:      osc("state=working:progress=50%"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:   "progress on blocked",
			stream: osc("state=blocked:progress=50"),
			wantReports: []programstatus.Report{{
				State: programstatus.Blocked, Progress: 50,
			}},
		},
		{
			name:   "progress ignored unless working or blocked",
			stream: osc("state=done:progress=50") + osc("state=error:progress=50") + osc("state=idle:progress=50"),
			wantReports: []programstatus.Report{
				rep(programstatus.Done), rep(programstatus.Error), rep(programstatus.Idle),
			},
		},
		{
			name:        "progress repeated last wins",
			stream:      osc("state=working:progress=10:progress=20"),
			wantReports: []programstatus.Report{withProgress(20)},
		},

		// --- app ---
		{
			name:        "app full charset",
			stream:      osc("state=working:app=Claude_Code.v2+beta-1"),
			wantReports: []programstatus.Report{withApp("Claude_Code.v2+beta-1")},
		},
		{
			name:        "app at limit",
			stream:      osc("state=working:app=" + strings.Repeat("a", 32)),
			wantReports: []programstatus.Report{withApp(strings.Repeat("a", 32))},
		},
		{
			name:        "app over limit absent",
			stream:      osc("state=working:app=" + strings.Repeat("a", 33)),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "app empty absent",
			stream:      osc("state=working:app="),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "app with slash absent",
			stream:      osc("state=working:app=usr/bin/brew"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "app with comma absent",
			stream:      osc("state=working:app=brew,cask"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "app repeated last wins",
			stream:      osc("state=working:app=brew:app=cargo"),
			wantReports: []programstatus.Report{withApp("cargo")},
		},

		// --- id ---
		{
			name:        "id nested",
			stream:      osc("state=working:id=build/test/unit"),
			wantReports: []programstatus.Report{withID("build/test/unit")},
		},
		{
			name:        "id full charset",
			stream:      osc("state=working:id=A-z_0.9+x"),
			wantReports: []programstatus.Report{withID("A-z_0.9+x")},
		},
		{
			name:        "id at max depth",
			stream:      osc("state=working:id=a/b/c/d/e/f/g/h"),
			wantReports: []programstatus.Report{withID("a/b/c/d/e/f/g/h")},
		},
		{
			name:   "id over max depth",
			stream: osc("state=working:id=a/b/c/d/e/f/g/h/i"),
		},
		{
			name:        "id segment at limit",
			stream:      osc("state=working:id=" + seg(32)),
			wantReports: []programstatus.Report{withID(seg(32))},
		},
		{
			name:   "id segment over limit",
			stream: osc("state=working:id=" + seg(33)),
		},
		{
			name:        "id at total limit",
			stream:      osc("state=working:id=" + seg(32) + "/" + seg(32) + "/" + seg(32) + "/" + seg(29)),
			wantReports: []programstatus.Report{withID(seg(32) + "/" + seg(32) + "/" + seg(32) + "/" + seg(29))},
		},
		{
			name:   "id over total limit",
			stream: osc("state=working:id=" + seg(32) + "/" + seg(32) + "/" + seg(32) + "/" + seg(30)),
		},
		{
			name:   "id empty",
			stream: osc("state=working:id="),
		},
		{
			name:   "id leading slash",
			stream: osc("state=working:id=/a"),
		},
		{
			name:   "id trailing slash",
			stream: osc("state=working:id=a/"),
		},
		{
			name:   "id empty segment",
			stream: osc("state=working:id=a//b"),
		},
		{
			name:   "id with comma",
			stream: osc("state=working:id=a,b"),
		},
		{
			name:   "id with equals",
			stream: osc("state=working:id=a=b"),
		},
		{
			name:        "id dot segments are opaque",
			stream:      osc("state=working:id=../../etc/passwd"),
			wantReports: []programstatus.Report{withID("../../etc/passwd")},
		},
		{
			name:        "id with space is a malformed pair addressing the root",
			stream:      osc("state=working:id=my job"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "id repeated last wins",
			stream:      osc("state=working:id=a:id=b"),
			wantReports: []programstatus.Report{withID("b")},
		},
		{
			name:   "id repeated first invalid rejects",
			stream: osc("state=working:id=/a:id=b"),
		},

		// --- title and msg ---
		{
			name:        "msg padded",
			stream:      osc("state=working:msg=" + enc("Installing updates")),
			wantReports: []programstatus.Report{withMsg("Installing updates")},
		},
		{
			name:        "msg double padded",
			stream:      osc("state=working:msg=" + enc("Upgraded")),
			wantReports: []programstatus.Report{withMsg("Upgraded")},
		},
		{
			name:        "msg unpadded",
			stream:      osc("state=working:msg=" + strings.TrimRight(enc("Upgraded"), "=")),
			wantReports: []programstatus.Report{withMsg("Upgraded")},
		},
		{
			name:        "msg over padded",
			stream:      osc("state=working:msg=" + enc("ABC") + "===="),
			wantReports: []programstatus.Report{withMsg("ABC")},
		},
		{
			name:        "msg only padding is empty",
			stream:      osc("state=working:msg===="),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "msg empty",
			stream:      osc("state=working:msg="),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "msg and title",
			stream:      osc("state=working:title=" + enc("Brew") + ":msg=" + enc("Installing")),
			wantReports: []programstatus.Report{workingWith(func(r *programstatus.Report) { r.Title, r.Msg = "Brew", "Installing" })},
		},
		{
			name:        "msg cjk",
			stream:      osc("state=working:msg=" + enc(cjk)),
			wantReports: []programstatus.Report{withMsg(cjk)},
		},
		{
			name:        "msg emoji",
			stream:      osc("state=working:msg=" + enc(emoji)),
			wantReports: []programstatus.Report{withMsg(emoji)},
		},
		{
			name:        "msg combining marks",
			stream:      osc("state=working:msg=" + enc("e\u0301 n\u0303")),
			wantReports: []programstatus.Report{withMsg("e\u0301 n\u0303")},
		},
		{
			name:        "msg right to left script",
			stream:      osc("state=working:msg=" + enc("שלום עולם")),
			wantReports: []programstatus.Report{withMsg("שלום עולם")},
		},
		{
			name:        "msg fullwidth and symbols",
			stream:      osc("state=working:msg=" + enc("ＡＢＣ ✓ → ∑")),
			wantReports: []programstatus.Report{withMsg("ＡＢＣ ✓ → ∑")},
		},
		{
			name:        "msg replacement character kept",
			stream:      osc("state=working:msg=" + enc("a\ufffdb")),
			wantReports: []programstatus.Report{withMsg("a\ufffdb")},
		},
		{
			name:        "title cjk",
			stream:      osc("state=working:title=" + enc(cjk)),
			wantReports: []programstatus.Report{withTitle(cjk)},
		},
		{
			name:        "title emoji",
			stream:      osc("state=working:title=" + enc(emoji)),
			wantReports: []programstatus.Report{withTitle(emoji)},
		},
		{
			name:        "msg zero width characters stripped",
			stream:      osc("state=working:msg=" + enc("a\u200bb\u200cc\u200dd\u200ee\u200ff")),
			wantReports: []programstatus.Report{withMsg("abcdef")},
		},
		{
			name:        "msg bidi overrides stripped",
			stream:      osc("state=working:msg=" + enc("\u202aa\u202bb\u202cc\u202dd\u202ee")),
			wantReports: []programstatus.Report{withMsg("abcde")},
		},
		{
			name:        "msg bidi isolates stripped",
			stream:      osc("state=working:msg=" + enc("\u2066a\u2067b\u2068c\u2069")),
			wantReports: []programstatus.Report{withMsg("abc")},
		},
		{
			name:        "msg made only of invisible formatting is empty",
			stream:      osc("state=working:msg=" + enc("\u200b\u202e\u2066")),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "title invisible formatting stripped",
			stream:      osc("state=working:title=" + enc("\u202etitle\u202c")),
			wantReports: []programstatus.Report{withTitle("title")},
		},
		{
			name:        "msg byte order mark kept",
			stream:      osc("state=working:msg=" + enc("a\ufeffb")),
			wantReports: []programstatus.Report{withMsg("a\ufeffb")},
		},
		{
			name:   "msg with NUL rejects",
			stream: osc("state=working:msg=" + enc("a\x00b")),
		},
		{
			name:   "msg with escape rejects",
			stream: osc("state=working:msg=" + enc("a\x1bb")),
		},
		{
			name:   "msg with embedded OSC rejects",
			stream: osc("state=working:msg=" + enc("\x1b]0;pwned\x07")),
		},
		{
			name:   "msg with embedded status report rejects",
			stream: osc("state=working:msg=" + enc("\x1b]7501;state=done\x1b\\")),
		},
		{
			name:   "msg with embedded CSI rejects",
			stream: osc("state=working:msg=" + enc("\x1b[2J")),
		},
		{
			name:   "msg with newline rejects",
			stream: osc("state=working:msg=" + enc("line\r\nline")),
		},
		{
			name:   "msg with tab rejects",
			stream: osc("state=working:msg=" + enc("a\tb")),
		},
		{
			name:   "msg with DEL rejects",
			stream: osc("state=working:msg=" + enc("a\x7fb")),
		},
		{
			name:   "msg with C1 control rejects",
			stream: osc("state=working:msg=" + enc("a\u0085b")),
		},
		{
			name:   "msg with C1 CSI rejects",
			stream: osc("state=working:msg=" + enc("a\u009b2Jb")),
		},
		{
			name:   "title with NUL rejects",
			stream: osc("state=working:title=" + enc("\x00")),
		},
		{
			name:   "title with escape rejects",
			stream: osc("state=working:title=" + enc("\x1bc")),
		},
		{
			name:   "msg invalid utf8 rejects",
			stream: osc("state=working:msg=" + encBytes([]byte{0xff, 0xfe})),
		},
		{
			name:   "msg overlong utf8 rejects",
			stream: osc("state=working:msg=" + encBytes([]byte{0xc0, 0x80})),
		},
		{
			name:   "msg surrogate utf8 rejects",
			stream: osc("state=working:msg=" + encBytes([]byte{0xed, 0xa0, 0x80})),
		},
		{
			name:   "msg truncated utf8 rejects",
			stream: osc("state=working:msg=" + encBytes([]byte{0xe6, 0x9c})),
		},
		{
			name:   "title invalid utf8 rejects",
			stream: osc("state=working:title=" + encBytes([]byte{0x80})),
		},
		{
			name:   "msg base64 length one rejects",
			stream: osc("state=working:msg=A"),
		},
		{
			name:   "msg base64 length five rejects",
			stream: osc("state=working:msg=AAAAA"),
		},
		{
			name:   "msg base64 padding inside rejects",
			stream: osc("state=working:msg=QU=JD"),
		},
		{
			name:   "msg base64 url alphabet rejects",
			stream: osc("state=working:msg=QU-D"),
		},
		{
			name:   "msg base64 with dot rejects",
			stream: osc("state=working:msg=QU.D"),
		},
		{
			name:        "msg base64 with space skips the pair",
			stream:      osc("state=working:msg=QU JD"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:   "title bad base64 rejects",
			stream: osc("state=working:title=A"),
		},
		{
			name:   "msg bad base64 rejects even with valid title",
			stream: osc("state=working:title=" + enc("ok") + ":msg=A"),
		},
		{
			name:        "msg repeated only the last is validated",
			stream:      osc("state=working:msg=AAAAA:msg=" + enc("last")),
			wantReports: []programstatus.Report{withMsg("last")},
		},
		{
			name:   "msg repeated last invalid rejects",
			stream: osc("state=working:msg=" + enc("first") + ":msg=AAAAA"),
		},
		{
			name:        "msg at decoded limit",
			stream:      osc("state=working:msg=" + enc(strings.Repeat("m", 2048))),
			wantReports: []programstatus.Report{withMsg(strings.Repeat("m", 2048))},
		},
		{
			name:   "msg over decoded limit",
			stream: osc("state=working:msg=" + enc(strings.Repeat("m", 2049))),
		},
		{
			name:   "msg over encoded limit",
			stream: osc("state=working:msg=" + strings.Repeat("QUJD", 683) + "A"),
		},
		{
			name:   "msg far over encoded limit",
			stream: osc("state=working:msg=" + strings.Repeat("QUJD", 700)),
		},
		{
			name:        "msg at decoded limit in wide characters",
			stream:      osc("state=working:msg=" + enc(strings.Repeat("構", 2048/3))),
			wantReports: []programstatus.Report{withMsg(strings.Repeat("構", 2048/3))},
		},
		{
			name:   "msg over decoded limit in wide characters",
			stream: osc("state=working:msg=" + enc(strings.Repeat("構", 2048/3+1))),
		},
		{
			name:        "msg at decoded limit in emoji",
			stream:      osc("state=working:msg=" + enc(strings.Repeat("🚀", 2048/4))),
			wantReports: []programstatus.Report{withMsg(strings.Repeat("🚀", 2048/4))},
		},
		{
			name:        "title at decoded limit",
			stream:      osc("state=working:title=" + enc(strings.Repeat("t", 192))),
			wantReports: []programstatus.Report{withTitle(strings.Repeat("t", 192))},
		},
		{
			name:   "title over decoded limit",
			stream: osc("state=working:title=" + enc(strings.Repeat("t", 193))),
		},
		{
			name:   "title over encoded limit",
			stream: osc("state=working:title=" + strings.Repeat("QUJD", 64) + "A"),
		},
		{
			name:   "title at msg limit rejects",
			stream: osc("state=working:title=" + enc(strings.Repeat("t", 2048))),
		},
		{
			name:        "title and msg both at limit",
			stream:      osc("state=working:title=" + enc(strings.Repeat("t", 192)) + ":msg=" + enc(strings.Repeat("m", 2048))),
			wantReports: []programstatus.Report{workingWith(func(r *programstatus.Report) { r.Title, r.Msg = strings.Repeat("t", 192), strings.Repeat("m", 2048) })},
		},

		// --- whole-sequence limit ---
		{
			name:        "sequence at limit with ST",
			stream:      osc("state=working:future=" + strings.Repeat("A", programstatus.MaxSequence-len(introducer)-len(st)-len("state=working:future="))),
			wantReports: []programstatus.Report{working},
		},
		{
			name:   "sequence over limit with ST",
			stream: osc("state=working:future=" + strings.Repeat("A", programstatus.MaxSequence-len(introducer)-len(st)-len("state=working:future=")+1)),
		},
		{
			name:        "sequence at limit with BEL",
			stream:      introducer + "state=working:future=" + strings.Repeat("A", programstatus.MaxSequence-len(introducer)-len(bel)-len("state=working:future=")) + bel,
			wantReports: []programstatus.Report{working},
		},
		{
			name:   "sequence over limit with BEL",
			stream: introducer + "state=working:future=" + strings.Repeat("A", programstatus.MaxSequence-len(introducer)-len(bel)-len("state=working:future=")+1) + bel,
		},
		{
			name:   "sequence over limit in whitespace",
			stream: osc("state=working" + strings.Repeat(" ", programstatus.MaxSequence)),
		},
		{
			name:        "oversized sequence does not affect the next",
			stream:      osc("state=working:msg="+strings.Repeat("A", programstatus.MaxSequence)) + osc("state=done"),
			wantReports: []programstatus.Report{rep(programstatus.Done)},
		},
		{
			name:        "sequence over 64 KiB dropped",
			stream:      osc("state=working:msg="+strings.Repeat("QUJD", 16389)) + osc("state=done"),
			wantReports: []programstatus.Report{rep(programstatus.Done)},
		},
		{
			name:        "sequence over 64 KiB with index wrap below the body dropped",
			stream:      osc("state=working:msg="+strings.Repeat("QUJD", 16379)) + osc("state=done"),
			wantReports: []programstatus.Report{rep(programstatus.Done)},
		},
		{
			name:        "sequence over the scanner limit dropped",
			stream:      osc("state=working:msg="+strings.Repeat("A", vtescanner.MaxOSCLen+1)) + osc("state=done"),
			wantReports: []programstatus.Report{rep(programstatus.Done)},
		},

		// --- parameters ---
		{
			name:   "semicolon splits the body",
			stream: osc("state=working;app=brew"),
		},
		{
			name:   "trailing semicolon",
			stream: osc("state=working;"),
		},
		{
			name:   "leading semicolon",
			stream: osc(";state=working"),
		},
		{
			name:   "semicolon inside base64",
			stream: osc("state=working:msg=QU;JD"),
		},
		{
			name:        "report after a split one",
			stream:      osc("state=working;app=brew") + osc("state=working"),
			wantReports: []programstatus.Report{working},
		},

		// --- byte level ---
		{
			name:        "NUL bytes inside the body are discarded",
			stream:      osc("sta\x00te=wor\x00king\x00"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:        "C0 controls inside the body are discarded",
			stream:      osc("state=\x01\x02wo\x0e\x0frking\x1c\x1f:app=\x08brew"),
			wantReports: []programstatus.Report{withApp("brew")},
		},
		{
			name:        "newlines inside the body are discarded",
			stream:      osc("state=working\r\n:app=brew\n"),
			wantReports: []programstatus.Report{withApp("brew")},
		},
		{
			name:        "tab inside the body is discarded",
			stream:      osc("state=\tworking"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:   "DEL inside a value skips the pair",
			stream: osc("state=working\x7f"),
		},
		{
			name:        "raw utf8 in msg skips the pair",
			stream:      osc("state=working:msg=héllo"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:   "raw utf8 in state rejects",
			stream: osc("state=wörking"),
		},
		{
			name:        "raw utf8 in id addresses the root",
			stream:      osc("state=working:id=ジョブ"),
			wantReports: []programstatus.Report{working},
		},
		{
			name:   "raw C1 byte in a value rejects",
			stream: osc("state=working\x9c"),
		},
		{
			name:   "raw invalid utf8 in a value rejects",
			stream: osc("state=working\xff"),
		},
		{
			name:        "BEL inside the body ends the report early",
			stream:      introducer + "state=working:msg=" + enc("ABC") + bel + "trailing" + st,
			wantReports: []programstatus.Report{withMsg("ABC")},
		},
		{
			name:        "escape inside the body starts the next sequence",
			stream:      introducer + "state=working" + introducer + "state=done" + st,
			wantReports: []programstatus.Report{working, rep(programstatus.Done)},
		},
		{
			name:        "escape inside base64 ends the report early",
			stream:      introducer + "state=working:msg=QUJD" + introducer + "state=done" + st,
			wantReports: []programstatus.Report{withMsg("ABC"), rep(programstatus.Done)},
		},
		{
			name:        "CAN ends the report with what was collected",
			stream:      introducer + "state=working:msg=QUJD\x18RE" + osc("state=done"),
			wantReports: []programstatus.Report{withMsg("ABC"), rep(programstatus.Done)},
		},
		{
			name:        "SUB ends the report with what was collected",
			stream:      introducer + "state=working\x1a:app=brew" + osc("state=done"),
			wantReports: []programstatus.Report{working, rep(programstatus.Done)},
		},
		{
			name:        "unterminated report is held until the terminator",
			stream:      introducer + "state=working:msg=" + enc("still going") + "\x1b" + "\\",
			wantReports: []programstatus.Report{withMsg("still going")},
		},
		{
			name:   "unterminated report never dispatches",
			stream: introducer + "state=working",
		},
		{
			name:        "other OSCs between reports",
			stream:      "\x1b]0;title\x07" + osc("state=working") + "\x1b]52;c;QUJD\x07" + osc("state=done"),
			wantReports: []programstatus.Report{working, rep(programstatus.Done)},
		},
		{
			name:        "report between printable text",
			stream:      "before " + osc("state=working") + " after",
			wantReports: []programstatus.Report{working},
		},
		{
			name:   "wrong OSC number",
			stream: "\x1b]7502;state=working" + st + "\x1b]751;state=working" + st + "\x1b]07501;state=working" + st,
		},
		{
			name:   "OSC number with whitespace",
			stream: "\x1b]7501 ;state=working" + st + "\x1b] 7501;state=working" + st,
		},

		// --- shell integration ---
		{
			name:        "prompt start",
			stream:      "\x1b]133;A" + st + "\x1b]133;A;special_key=1" + bel,
			wantPrompts: 2,
		},
		{
			name:   "other shell integration marks ignored",
			stream: "\x1b]133;B" + st + "\x1b]133;C" + st + "\x1b]133;D;0" + st + "\x1b]133" + st + "\x1b]133;" + st + "\x1b]133;a" + st,
		},
		{
			name:        "prompt start between reports",
			stream:      osc("state=done") + "\x1b]133;A" + bel + osc("state=working"),
			wantReports: []programstatus.Report{rep(programstatus.Done), working},
			wantPrompts: 1,
		},
	}
	feeds := []struct {
		name string
		feed func(p *Parser, stream string)
	}{
		{"whole", func(p *Parser, stream string) { p.AdvanceBytes([]byte(stream)) }},
		{"byte at a time", func(p *Parser, stream string) {
			for i := range len(stream) {
				p.AdvanceBytes([]byte(stream[i : i+1]))
			}
		}},
	}
	for _, feed := range feeds {
		t.Run(feed.name, func(t *testing.T) {
			t.Parallel()
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					h := new(programStatusHandler)
					p := NewParser(h)
					feed.feed(p, tc.stream)
					assert.Equal(t, tc.wantReports, h.reports)
					assert.Equal(t, tc.wantQueries, h.queries)
					assert.Equal(t, tc.wantPrompts, h.prompts)
				})
			}
		})
	}
}
