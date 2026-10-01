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

package spec

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/cmd/xsandbox/internal/record"
)

type hostCall struct {
	name string
	args []any
}

type mockHost struct {
	calls []hostCall
	fail  map[string]error
}

func (m *mockHost) record(name string, args ...any) error {
	m.calls = append(m.calls, hostCall{name: name, args: args})
	if m.fail != nil {
		return m.fail[name]
	}
	return nil
}

func (m *mockHost) ExpectMetadata(id string, permissions []string) error {
	return m.record("expect_metadata", id, permissions)
}

func (m *mockHost) SetConfig(cfg map[string]any) error {
	return m.record("config", cfg)
}

func (m *mockHost) FSWrite(path, content string) error {
	return m.record("fs.write", path, content)
}

func (m *mockHost) StubRPC(method string, where *record.Matcher, resp *record.Response) error {
	return m.record("stub_rpc", method, where, resp)
}

func (m *mockHost) ExpectRPC(exp RPCExpectation) error {
	return m.record("expect_rpc", exp)
}

func (m *mockHost) ExpectCommand(name string, repl bool, timeout time.Duration) error {
	return m.record("expect_command", name, repl, timeout)
}

func (m *mockHost) InvokeCommand(name string, args []string, repl bool, timeout time.Duration) error {
	return m.record("invoke_command", name, args, repl, timeout)
}

func (m *mockHost) ExpectResourceOpener(scheme string, timeout time.Duration) error {
	return m.record("expect_resource_opener", scheme, timeout)
}

func (m *mockHost) OpenResource(scheme, uri string, timeout time.Duration) error {
	return m.record("open_resource", scheme, uri, timeout)
}

func (m *mockHost) PublishEvent(evType, uri, content string) error {
	return m.record("publish_event", evType, uri, content)
}

func (m *mockHost) WaitIdle(d time.Duration) error {
	return m.record("wait_idle", d)
}

func (m *mockHost) AssertNoUnexpectedRPCs(ignore []string) error {
	return m.record("assert_no_unexpected_rpcs", ignore)
}

func (m *mockHost) ExpectWindow(method string, timeout time.Duration) (string, error) {
	err := m.record("expect_window", method, timeout)
	return method, err
}

func (m *mockHost) RenderWindow(handle string, width, height int) (string, error) {
	err := m.record("render", handle, width, height)
	return "", err
}

func (m *mockHost) SendKey(handle, keys string) (bool, bool, error) {
	err := m.record("send_key", handle, keys)
	return false, false, err
}

func runSpec(t *testing.T, src string, host Host) error {
	t.Helper()
	return Run([]byte(src), host, Options{DefaultTimeout: 7 * time.Second})
}

func TestSpecRunHappyPath(t *testing.T) {
	t.Parallel()

	src := `
expect_metadata(id = "com.example.hello", permissions = ["permcmd", "permnoti"])
config({"greeting": "hello", "count": 3})
fs.write("README.md", "# readme\n")

expect_rpc("text.Editor/SubscribeCommand", timeout = "10s")
h = expect_command("hello")
invoke_command(h, args = ["world"])
expect_rpc(
    "browser.Notifications/Notify",
    where = {"msg": "hello world", "level": present()},
    timeout = "5s",
)
expect_rpc("workspace.Files/Read", respond = {"data": "canned"})
stub_rpc("config.Config/Get", respond_error = {"code": "unavailable", "message": "down"})
wait_idle("1s")
assert_no_unexpected_rpcs(ignore = ["config.Config/Get"])
`
	host := &mockHost{}
	require.NoError(t, runSpec(t, src, host))

	names := make([]string, 0, len(host.calls))
	for _, c := range host.calls {
		names = append(names, c.name)
	}
	assert.Equal(t, []string{
		"expect_metadata", "config", "fs.write",
		"expect_rpc", "expect_command", "invoke_command",
		"expect_rpc", "expect_rpc", "stub_rpc",
		"wait_idle", "assert_no_unexpected_rpcs",
	}, names)

	assert.Equal(t, []any{"com.example.hello", []string{"permcmd", "permnoti"}},
		host.calls[0].args)
	assert.Equal(t, []any{map[string]any{"greeting": "hello", "count": 3}},
		host.calls[1].args)
	assert.Equal(t, []any{"README.md", "# readme\n"}, host.calls[2].args)

	first := host.calls[3].args[0].(RPCExpectation)
	assert.Equal(t, "text.Editor/SubscribeCommand", first.Method)
	assert.Nil(t, first.Where)
	assert.Nil(t, first.Respond)
	assert.Equal(t, 10*time.Second, first.Timeout)

	assert.Equal(t, []any{"hello", false, 7 * time.Second}, host.calls[4].args)
	assert.Equal(t, []any{"hello", []string{"world"}, false, 7 * time.Second},
		host.calls[5].args)

	notify := host.calls[6].args[0].(RPCExpectation)
	assert.Equal(t, 5*time.Second, notify.Timeout)
	require.NotNil(t, notify.Where)
	assert.Equal(t, "hello world", notify.Where.Fields["msg"])
	assert.Equal(t, record.Predicate{Kind: "present"}, notify.Where.Fields["level"])

	read := host.calls[7].args[0].(RPCExpectation)
	require.NotNil(t, read.Respond)
	assert.Equal(t, map[string]any{"data": "canned"}, read.Respond.Body)

	assert.Equal(t, "config.Config/Get", host.calls[8].args[0])
	stub := host.calls[8].args[2].(*record.Response)
	assert.Equal(t, "unavailable", stub.ErrCode)
	assert.Equal(t, "down", stub.ErrMsg)

	assert.Equal(t, []any{time.Second}, host.calls[9].args)
	assert.Equal(t, []any{[]string{"config.Config/Get"}}, host.calls[10].args)
}

func TestSpecRunPredicatesAndEvents(t *testing.T) {
	t.Parallel()

	src := `
expect_rpc("x.Y/Z", where = {
    "a": contains("boo"),
    "b": regex("^h"),
    "nested": {"c": present()},
})
publish_event("open", uri = "file:///tmp/x", content = "data")
r = expect_repl_command("shell")
invoke_repl_command(r, args = [])
`
	host := &mockHost{}
	require.NoError(t, runSpec(t, src, host))

	exp := host.calls[0].args[0].(RPCExpectation)
	assert.Equal(t, record.Predicate{Kind: "contains", Arg: "boo"}, exp.Where.Fields["a"])
	assert.Equal(t, record.Predicate{Kind: "regex", Arg: "^h"}, exp.Where.Fields["b"])
	assert.Equal(t, map[string]any{"c": record.Predicate{Kind: "present"}},
		exp.Where.Fields["nested"])

	assert.Equal(t, hostCall{
		name: "publish_event", args: []any{"open", "file:///tmp/x", "data"},
	}, host.calls[1])
	assert.Equal(t, []any{"shell", true, 7 * time.Second}, host.calls[2].args)
	assert.Equal(t, []any{"shell", []string{}, true, 7 * time.Second},
		host.calls[3].args)
}

func TestSpecRunInvokeByName(t *testing.T) {
	t.Parallel()

	host := &mockHost{}
	require.NoError(t, runSpec(t, `invoke_command("hello", args = ["a"])`, host))
	assert.Equal(t, []any{"hello", []string{"a"}, false, 7 * time.Second},
		host.calls[0].args)
}

func TestSpecRunResourceOpener(t *testing.T) {
	t.Parallel()

	src := `
o = expect_resource_opener("fake", timeout = "3s")
open_resource(o, "fake://host/a")
open_resource(o, uri = "fake://host/b", timeout = "2s")
`
	host := &mockHost{}
	require.NoError(t, runSpec(t, src, host))
	assert.Equal(t, []hostCall{
		{name: "expect_resource_opener", args: []any{"fake", 3 * time.Second}},
		{name: "open_resource", args: []any{"fake", "fake://host/a", 7 * time.Second}},
		{name: "open_resource", args: []any{"fake", "fake://host/b", 2 * time.Second}},
	}, host.calls)
}

func TestSpecRunErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		src     string
		wantErr string
	}{
		{
			name:    "syntax error",
			src:     "expect_rpc(",
			wantErr: "spec.star",
		},
		{
			name:    "load forbidden",
			src:     `load("other.star", "x")`,
			wantErr: "load() is not allowed",
		},
		{
			name:    "unknown builtin",
			src:     `frobnicate()`,
			wantErr: "undefined: frobnicate",
		},
		{
			name:    "empty metadata id",
			src:     `expect_metadata(id = "")`,
			wantErr: "id must not be empty",
		},
		{
			name:    "bad timeout",
			src:     `expect_rpc("x.Y/Z", timeout = "banana")`,
			wantErr: "invalid timeout",
		},
		{
			name:    "respond and respond_error together",
			src:     `expect_rpc("x.Y/Z", respond = {}, respond_error = {"message": "m"})`,
			wantErr: "mutually exclusive",
		},
		{
			name:    "stub_rpc requires a response",
			src:     `stub_rpc("x.Y/Z")`,
			wantErr: "exactly one of respond or respond_error",
		},
		{
			name:    "respond_error unknown key",
			src:     `expect_rpc("x.Y/Z", respond_error = {"status": "x"})`,
			wantErr: "unknown key",
		},
		{
			name:    "respond_error empty",
			src:     `expect_rpc("x.Y/Z", respond_error = {})`,
			wantErr: "must set code and/or message",
		},
		{
			name:    "non-string permissions",
			src:     `expect_metadata(id = "x", permissions = [1])`,
			wantErr: "must be a string",
		},
		{
			name:    "non-string where key",
			src:     `expect_rpc("x.Y/Z", where = {1: "a"})`,
			wantErr: "keys must be strings",
		},
		{
			name:    "bad wait_idle duration",
			src:     `wait_idle("nope")`,
			wantErr: "invalid duration",
		},
		{
			name:    "invoke with wrong handle kind",
			src:     "h = expect_command(\"x\")\ninvoke_repl_command(h)",
			wantErr: "registered with repl=false",
		},
		{
			name:    "invoke with non-command value",
			src:     `invoke_command(42)`,
			wantErr: "must be a string or a handle",
		},
		{
			name:    "unknown event type is host error",
			src:     `publish_event("open")`,
			wantErr: "publish_event failed",
		},
		{
			name:    "expect_resource_opener with empty scheme",
			src:     `expect_resource_opener("")`,
			wantErr: "scheme must not be empty",
		},
		{
			name:    "open_resource with a scheme instead of a handle",
			src:     `open_resource("fake", "fake://host/a")`,
			wantErr: "must be a handle from expect_resource_opener",
		},
		{
			name:    "open_resource without uri",
			src:     "o = expect_resource_opener(\"fake\")\nopen_resource(o)",
			wantErr: "missing argument for uri",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			host := &mockHost{fail: map[string]error{
				"publish_event": errors.New("publish_event failed"),
			}}
			err := runSpec(t, tc.src, host)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestSpecRunHostErrorCarriesPosition(t *testing.T) {
	t.Parallel()

	host := &mockHost{fail: map[string]error{
		"expect_rpc": fmt.Errorf("expectation timed out"),
	}}
	err := Run([]byte("\n\nexpect_rpc(\"x.Y/Z\")\n"), host, Options{
		Filename: "myspec.star",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "myspec.star:3")
	assert.Contains(t, err.Error(), "expectation timed out")
}

func TestSpecRunTopLevelControlFlow(t *testing.T) {
	t.Parallel()

	src := `
for name in ["a", "b"]:
    fs.write(name + ".txt", name)
`
	host := &mockHost{}
	require.NoError(t, runSpec(t, src, host))
	require.Len(t, host.calls, 2)
	assert.Equal(t, []any{"a.txt", "a"}, host.calls[0].args)
	assert.Equal(t, []any{"b.txt", "b"}, host.calls[1].args)
}

func TestSpecRunPrint(t *testing.T) {
	t.Parallel()

	var printed []string
	err := Run([]byte(`print("hello from spec")`), &mockHost{}, Options{
		Print: func(msg string) { printed = append(printed, msg) },
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"hello from spec"}, printed)
}
