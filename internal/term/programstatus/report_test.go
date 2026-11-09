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

package programstatus

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func rawb64(s string) string { return base64.RawStdEncoding.EncodeToString([]byte(s)) }

func TestParse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want Report
		err  error
	}{
		// spec examples
		{
			name: "spec terraform blocked",
			body: "state=blocked:kind=permission:app=terraform:msg=QXBwbHkgMyB0byBhZGQsIDEgdG8gY2hhbmdlLCAwIHRvIGRlc3Ryb3k/",
			want: Report{State: Blocked, Kind: KindPermission, Progress: -1, App: "terraform",
				Msg: "Apply 3 to add, 1 to change, 0 to destroy?"},
		},
		{
			name: "spec brew working",
			body: "state=working:app=brew:msg=SW5zdGFsbGluZyB1cGRhdGVz",
			want: Report{State: Working, Progress: -1, App: "brew", Msg: "Installing updates"},
		},
		{
			name: "spec brew auth",
			body: "state=blocked:kind=auth:app=brew:msg=UGFzc3dvcmQgcmVxdWlyZWQgdG8gaW5zdGFsbCB1cGRhdGVz",
			want: Report{State: Blocked, Kind: KindAuth, Progress: -1, App: "brew",
				Msg: "Password required to install updates"},
		},
		{
			name: "spec brew done padded",
			body: "state=done:app=brew:msg=VXBncmFkZWQgMTIgcGFja2FnZXM=",
			want: Report{State: Done, Progress: -1, App: "brew", Msg: "Upgraded 12 packages"},
		},
		{
			name: "spec deploy root",
			body: "state=working:app=deploy:msg=RGVwbG95aW5nIHYyLjQuMQ==",
			want: Report{State: Working, Progress: -1, App: "deploy", Msg: "Deploying v2.4.1"},
		},
		{
			name: "spec us-east progress",
			body: "state=working:id=us-east:title=VVMgRWFzdA==:progress=40:msg=UHVzaGluZyBpbWFnZQ==",
			want: Report{ID: "us-east", State: Working, Progress: 40, Title: "US East", Msg: "Pushing image"},
		},
		{
			name: "spec eu-west blocked",
			body: "state=blocked:kind=permission:id=eu-west:title=RVUgV2VzdA==:msg=QXBwcm92ZSBkZXBsb3kgdG8gZXUtd2VzdCAocHJvZHVjdGlvbik/",
			want: Report{ID: "eu-west", State: Blocked, Kind: KindPermission, Progress: -1, Title: "EU West",
				Msg: "Approve deploy to eu-west (production)?"},
		},
		{
			name: "spec clear id",
			body: "state=clear:id=us-east",
			want: Report{ID: "us-east", State: Clear, Progress: -1},
		},
		{
			name: "spec shell function",
			body: "state=working:msg=" + b64("Syncing photos"),
			want: Report{State: Working, Progress: -1, Msg: "Syncing photos"},
		},
		// grammar
		{
			name: "idle bare",
			body: "state=idle",
			want: Report{State: Idle, Progress: -1},
		},
		{
			name: "error",
			body: "state=error:msg=" + b64("rsync failed"),
			want: Report{State: Error, Progress: -1, Msg: "rsync failed"},
		},
		{
			name: "clear root",
			body: "state=clear",
			want: Report{State: Clear, Progress: -1},
		},
		{
			name: "clear ignores other keys",
			body: "state=clear:kind=auth:progress=5:app=x:msg=" + b64("hi"),
			want: Report{State: Clear, Progress: -1},
		},
		{
			name: "whitespace trimmed",
			body: " state = working : app = brew ",
			want: Report{State: Working, Progress: -1, App: "brew"},
		},
		{
			name: "last value wins",
			body: "state=working:state=done",
			want: Report{State: Done, Progress: -1},
		},
		{
			name: "unknown key ignored",
			body: "state=working:future=abc",
			want: Report{State: Working, Progress: -1},
		},
		{
			name: "malformed pair without equals skipped",
			body: "garbage:state=working",
			want: Report{State: Working, Progress: -1},
		},
		{
			name: "malformed pair empty key skipped",
			body: "=x:state=working",
			want: Report{State: Working, Progress: -1},
		},
		{
			name: "malformed pair bad value byte skipped",
			body: "app=br ew:state=working",
			want: Report{State: Working, Progress: -1},
		},
		{
			name: "malformed pair uppercase key skipped",
			body: "State=done:state=working",
			want: Report{State: Working, Progress: -1},
		},
		{
			name: "empty pairs skipped",
			body: "::state=working::",
			want: Report{State: Working, Progress: -1},
		},
		{
			name: "state with bad value byte skipped so missing",
			body: "state=wor king",
			err:  errNoState,
		},
		{
			name: "missing state",
			body: "app=brew",
			err:  errNoState,
		},
		{
			name: "empty body",
			body: "",
			err:  errNoState,
		},
		{
			name: "unknown state rejects whole report",
			body: "state=paused",
			err:  errBadState,
		},
		{
			name: "unknown state even when followed by known",
			body: "state=paused:state=working",
			err:  errBadState,
		},
		{
			name: "key limit",
			body: "state=working:abcdefghijklmnopq=1",
			err:  errKeyLimit,
		},
		{
			name: "key at limit",
			body: "state=working:abcdefghijklmnop=1",
			want: Report{State: Working, Progress: -1},
		},
		// kind
		{
			name: "kind question",
			body: "state=blocked:kind=question",
			want: Report{State: Blocked, Kind: KindQuestion, Progress: -1},
		},
		{
			name: "kind unknown absent",
			body: "state=blocked:kind=coffee",
			want: Report{State: Blocked, Progress: -1},
		},
		{
			name: "kind ignored unless blocked",
			body: "state=working:kind=auth",
			want: Report{State: Working, Progress: -1},
		},
		// progress
		{
			name: "progress zero",
			body: "state=working:progress=0",
			want: Report{State: Working, Progress: 0},
		},
		{
			name: "progress hundred",
			body: "state=blocked:progress=100",
			want: Report{State: Blocked, Progress: 100},
		},
		{
			name: "progress over hundred absent",
			body: "state=working:progress=101",
			want: Report{State: Working, Progress: -1},
		},
		{
			name: "progress non numeric absent",
			body: "state=working:progress=abc",
			want: Report{State: Working, Progress: -1},
		},
		{
			name: "progress signed absent",
			body: "state=working:progress=+5",
			want: Report{State: Working, Progress: -1},
		},
		{
			name: "progress ignored unless working or blocked",
			body: "state=done:progress=50",
			want: Report{State: Done, Progress: -1},
		},
		// id
		{
			name: "id nested",
			body: "state=working:id=build/test/unit",
			want: Report{ID: "build/test/unit", State: Working, Progress: -1},
		},
		{
			name: "id max depth",
			body: "state=working:id=a/b/c/d/e/f/g/h",
			want: Report{ID: "a/b/c/d/e/f/g/h", State: Working, Progress: -1},
		},
		{
			name: "id too deep",
			body: "state=working:id=a/b/c/d/e/f/g/h/i",
			err:  errBadID,
		},
		{
			name: "id segment at limit",
			body: "state=working:id=" + strings.Repeat("s", 32),
			want: Report{ID: strings.Repeat("s", 32), State: Working, Progress: -1},
		},
		{
			name: "id segment too long",
			body: "state=working:id=" + strings.Repeat("s", 33),
			err:  errBadID,
		},
		{
			name: "id total too long",
			body: "state=working:id=" + strings.Repeat(strings.Repeat("s", 32)+"/", 3) + strings.Repeat("s", 32),
			err:  errBadID,
		},
		{
			name: "id empty segment",
			body: "state=working:id=a//b",
			err:  errBadID,
		},
		{
			name: "id leading slash",
			body: "state=working:id=/a",
			err:  errBadID,
		},
		{
			name: "id empty",
			body: "state=working:id=",
			err:  errBadID,
		},
		{
			name: "id bad char",
			body: "state=working:id=a,b",
			err:  errBadID,
		},
		// app
		{
			name: "app at limit",
			body: "state=working:app=" + strings.Repeat("a", 32),
			want: Report{State: Working, Progress: -1, App: strings.Repeat("a", 32)},
		},
		{
			name: "app too long absent",
			body: "state=working:app=" + strings.Repeat("a", 33),
			want: Report{State: Working, Progress: -1},
		},
		{
			name: "app bad char absent",
			body: "state=working:app=claude,code",
			want: Report{State: Working, Progress: -1},
		},
		{
			name: "app empty absent",
			body: "state=working:app=",
			want: Report{State: Working, Progress: -1},
		},
		// text
		{
			name: "msg unpadded",
			body: "state=done:msg=" + rawb64("Upgraded 12 packages"),
			want: Report{State: Done, Progress: -1, Msg: "Upgraded 12 packages"},
		},
		{
			name: "msg single pad",
			body: "state=done:msg=" + rawb64("ab") + "=",
			want: Report{State: Done, Progress: -1, Msg: "ab"},
		},
		{
			name: "msg over padded",
			body: "state=done:msg=" + rawb64("a") + "====",
			want: Report{State: Done, Progress: -1, Msg: "a"},
		},
		{
			name: "msg empty",
			body: "state=done:msg=",
			want: Report{State: Done, Progress: -1},
		},
		{
			name: "msg unicode",
			body: "state=done:msg=" + b64("déployé ✓"),
			want: Report{State: Done, Progress: -1, Msg: "déployé ✓"},
		},
		{
			name: "msg bad base64",
			body: "state=done:msg=a",
			err:  errBadText,
		},
		{
			name: "msg pad in middle",
			body: "state=done:msg=YQ==YQ==",
			err:  errBadText,
		},
		{
			name: "msg invalid utf8",
			body: "state=done:msg=" + base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe}),
			err:  errBadText,
		},
		{
			name: "msg control char",
			body: "state=done:msg=" + b64("a\x1bb"),
			err:  errControl,
		},
		{
			name: "msg newline",
			body: "state=done:msg=" + b64("a\nb"),
			err:  errControl,
		},
		{
			name: "msg del",
			body: "state=done:msg=" + b64("a\x7fb"),
			err:  errControl,
		},
		{
			name: "msg c1 control",
			body: "state=done:msg=" + b64("a\u0085b"),
			err:  errControl,
		},
		{
			name: "msg bidi stripped",
			body: "state=done:msg=" + b64("a\u202eb\u200bc\u2066d"),
			want: Report{State: Done, Progress: -1, Msg: "abcd"},
		},
		{
			name: "msg decoded at limit",
			body: "state=done:msg=" + rawb64(strings.Repeat("m", 2048)),
			want: Report{State: Done, Progress: -1, Msg: strings.Repeat("m", 2048)},
		},
		{
			name: "msg decoded over limit",
			body: "state=done:msg=" + rawb64(strings.Repeat("m", 2049)),
			err:  errTooLong,
		},
		{
			name: "msg encoded over limit",
			body: "state=done:msg=" + strings.Repeat("A", 2733),
			err:  errTooLong,
		},
		{
			name: "title decoded at limit",
			body: "state=done:title=" + rawb64(strings.Repeat("t", 192)),
			want: Report{State: Done, Progress: -1, Title: strings.Repeat("t", 192)},
		},
		{
			name: "title decoded over limit",
			body: "state=done:title=" + rawb64(strings.Repeat("t", 193)),
			err:  errTooLong,
		},
		{
			name: "title encoded over limit",
			body: "state=done:title=" + strings.Repeat("A", 257),
			err:  errTooLong,
		},
		{
			name: "title control",
			body: "state=done:title=" + b64("\t"),
			err:  errControl,
		},
		{
			name: "rejected report applies nothing",
			body: "state=done:app=brew:msg=" + b64("ok") + ":title=a",
			err:  errBadText,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse([]byte(tc.body))
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				assert.Equal(t, Report{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestReportParent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id     string
		parent string
		ok     bool
	}{
		{id: "", parent: "", ok: false},
		{id: "build", parent: "", ok: true},
		{id: "build/test", parent: "build", ok: true},
		{id: "a/b/c", parent: "a/b", ok: true},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			parent, ok := Report{ID: tc.id}.Parent()
			assert.Equal(t, tc.parent, parent)
			assert.Equal(t, tc.ok, ok)
		})
	}
}

func TestStateString(t *testing.T) {
	t.Parallel()
	for name, st := range stateNames {
		assert.Equal(t, name, st.String())
	}
	assert.Equal(t, "state(42)", State(42).String())
	for name, k := range kindNames {
		assert.Equal(t, name, k.String())
	}
	assert.Equal(t, "", KindNone.String())
	assert.Equal(t, "kind(42)", Kind(42).String())
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   Report
		want Report
		err  error
	}{
		{
			name: "zero value is a root idle report",
			in:   Report{},
			want: Report{Progress: -1},
		},
		{
			name: "every field kept",
			in: Report{ID: "deploy/eu-west", State: Blocked, Kind: KindQuestion, Progress: 40,
				App: "deploy", Title: "EU West", Msg: "Approve?"},
			want: Report{ID: "deploy/eu-west", State: Blocked, Kind: KindQuestion, Progress: 40,
				App: "deploy", Title: "EU West", Msg: "Approve?"},
		},
		{
			name: "clear keeps only the id",
			in:   Report{ID: "job", State: Clear, Kind: KindAuth, Progress: 5, App: "x", Title: "t", Msg: "m"},
			want: Report{ID: "job", State: Clear, Progress: -1},
		},
		{
			name: "clear at the root",
			in:   Report{State: Clear},
			want: Report{State: Clear, Progress: -1},
		},
		{
			name: "unknown state rejects",
			in:   Report{State: Clear + 1},
			err:  errBadState,
		},
		{
			name: "invalid id rejects",
			in:   Report{ID: "a//b", State: Working, Progress: -1},
			err:  errBadID,
		},
		{
			name: "id at limits",
			in:   Report{ID: strings.Repeat("s", 32) + "/" + strings.Repeat("s", 32) + "/" + strings.Repeat("s", 32) + "/" + strings.Repeat("s", 29), State: Working, Progress: -1},
			want: Report{ID: strings.Repeat("s", 32) + "/" + strings.Repeat("s", 32) + "/" + strings.Repeat("s", 32) + "/" + strings.Repeat("s", 29), State: Working, Progress: -1},
		},
		{
			name: "id over the total limit rejects",
			in:   Report{ID: strings.Repeat("s", 32) + "/" + strings.Repeat("s", 32) + "/" + strings.Repeat("s", 32) + "/" + strings.Repeat("s", 30), State: Working, Progress: -1},
			err:  errBadID,
		},
		{
			name: "kind dropped unless blocked",
			in:   Report{State: Working, Kind: KindPermission, Progress: -1},
			want: Report{State: Working, Progress: -1},
		},
		{
			name: "unknown kind is none",
			in:   Report{State: Blocked, Kind: KindAuth + 1, Progress: -1},
			want: Report{State: Blocked, Progress: -1},
		},
		{
			name: "progress dropped unless working or blocked",
			in:   Report{State: Done, Progress: 50},
			want: Report{State: Done, Progress: -1},
		},
		{
			name: "progress zero kept",
			in:   Report{State: Working, Progress: 0},
			want: Report{State: Working, Progress: 0},
		},
		{
			name: "progress full kept",
			in:   Report{State: Blocked, Progress: 100},
			want: Report{State: Blocked, Progress: 100},
		},
		{
			name: "progress over range is indeterminate",
			in:   Report{State: Working, Progress: 101},
			want: Report{State: Working, Progress: -1},
		},
		{
			name: "progress under range is indeterminate",
			in:   Report{State: Working, Progress: -7},
			want: Report{State: Working, Progress: -1},
		},
		{
			name: "app outside the charset dropped",
			in:   Report{State: Working, App: "claude code", Progress: -1},
			want: Report{State: Working, Progress: -1},
		},
		{
			name: "app over the limit dropped",
			in:   Report{State: Working, App: strings.Repeat("a", 33), Progress: -1},
			want: Report{State: Working, Progress: -1},
		},
		{
			name: "title at the limit",
			in:   Report{State: Working, Title: strings.Repeat("t", 192), Progress: -1},
			want: Report{State: Working, Progress: -1, Title: strings.Repeat("t", 192)},
		},
		{
			name: "title over the limit rejects",
			in:   Report{State: Working, Title: strings.Repeat("t", 193), Progress: -1},
			err:  errTooLong,
		},
		{
			name: "msg at the limit",
			in:   Report{State: Working, Msg: strings.Repeat("m", 2048), Progress: -1},
			want: Report{State: Working, Progress: -1, Msg: strings.Repeat("m", 2048)},
		},
		{
			name: "msg over the limit rejects",
			in:   Report{State: Working, Msg: strings.Repeat("m", 2049), Progress: -1},
			err:  errTooLong,
		},
		{
			name: "control character in title rejects",
			in:   Report{State: Working, Title: "a\x1bb", Progress: -1},
			err:  errControl,
		},
		{
			name: "control character in msg rejects",
			in:   Report{State: Working, Msg: "line\nline", Progress: -1},
			err:  errControl,
		},
		{
			name: "invalid utf8 rejects",
			in:   Report{State: Working, Msg: "a\xffb", Progress: -1},
			err:  errBadText,
		},
		{
			name: "invisible formatting stripped",
			in:   Report{State: Working, Title: "\u202etitle\u202c", Msg: "a\u200bb", Progress: -1},
			want: Report{State: Working, Progress: -1, Title: "title", Msg: "ab"},
		},
		{
			name: "wide characters kept",
			in:   Report{State: Done, Msg: "構築 🚀"},
			want: Report{State: Done, Progress: -1, Msg: "構築 🚀"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Normalize(tc.in)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				assert.Equal(t, Report{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
