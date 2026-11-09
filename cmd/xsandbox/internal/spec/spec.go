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

// Package spec evaluates the xsandbox Starlark specification. The
// spec runs top to bottom: setup builtins (expect_metadata, config)
// describe the handshake, and run-phase builtins block the evaluation
// until the expectation is satisfied or times out. The Host interface
// decouples the DSL from the sandbox runtime so the DSL is unit
// testable.
package spec

import (
	"errors"
	"fmt"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"
	"unstable.build/rune/cmd/xsandbox/internal/record"
	"unstable.build/rune/internal/ide/starlarkconfig"
)

// RPCExpectation describes one expect_rpc invocation.
type RPCExpectation struct {
	Method  string
	Where   *record.Matcher
	Respond *record.Response
	Timeout time.Duration
}

// Host is the sandbox runtime driven by the spec. Every method may
// return an error, which aborts the evaluation with the spec source
// position attached.
type Host interface {
	// ExpectMetadata declares the handshake identity (setup phase).
	ExpectMetadata(id string, permissions []string) error
	// SetConfig sets the config served to the extension (setup phase).
	SetConfig(cfg map[string]any) error
	// FSWrite writes a file relative to the workspace root (any phase).
	FSWrite(path, content string) error
	// StubRPC installs a scripted response without waiting (any
	// phase). Installing before the first expectation guarantees the
	// response is in place before the extension is launched.
	StubRPC(method string, where *record.Matcher, resp *record.Response) error
	// ExpectRPC blocks until a matching RPC is observed (run phase).
	ExpectRPC(exp RPCExpectation) error
	// ExpectCommand blocks until the named (REPL) command is registered.
	ExpectCommand(name string, repl bool, timeout time.Duration) error
	// InvokeCommand dispatches a registered command to the extension
	// and waits for its completion. A REPL command's output is returned
	// rendered as a string, and fails the command when it ends in an
	// error.
	InvokeCommand(name string, args []string, repl bool, timeout time.Duration) (string, error)
	// CompleteCommand asks a registered (REPL) command for the
	// candidates that complete args, as Rune does when Tab is pressed in
	// the command prompt or a console.
	CompleteCommand(name string, args []string, repl bool, timeout time.Duration) ([]string, error)
	// REPLHelp asks a registered REPL command for its help on args and
	// returns it rendered as InvokeCommand renders REPL output.
	REPLHelp(name string, args []string, timeout time.Duration) (string, error)
	// ExpectResourceOpener blocks until a resource opener is registered
	// for scheme.
	ExpectResourceOpener(scheme string, timeout time.Duration) error
	// OpenResource asks the resource opener of scheme for the content of
	// uri and shows it as the tab of uri in the focused window.
	OpenResource(scheme, uri string, timeout time.Duration) error
	// CompleteResource asks the resource opener of scheme for the URIs
	// that complete prefix, as Rune does to complete view and edit.
	CompleteResource(scheme, prefix string, timeout time.Duration) ([]string, error)
	// PublishEvent dispatches an editor event to extension subscribers.
	PublishEvent(evType, uri, content string) error
	// WaitIdle blocks until no RPC activity is recorded for d.
	WaitIdle(d time.Duration) error
	// AssertNoUnexpectedRPCs fails when unconsumed RPCs remain.
	AssertNoUnexpectedRPCs(ignore []string) error
	// ExpectWindow blocks until an install RPC for method (Split, Bar,
	// Tab, Floating, Open) is observed and returns an opaque window
	// handle that render and send_key reference.
	ExpectWindow(method string, timeout time.Duration) (string, error)
	// RenderWindow resizes the headless browser to width x height,
	// draws it, and returns the composited output as a string.
	RenderWindow(handle string, width, height int) (string, error)
	// RenderGraphics draws the headless browser as RenderWindow does, in
	// frames of cellWidth x cellHeight pixel cells, which lets handlers
	// place images, and returns the cells and the images placed.
	RenderGraphics(handle string, width, height int, cellWidth, cellHeight float64) (Graphics, error)
	// SendKey delivers a handlertest-style key sequence to the focused
	// installed handler and returns whether the last event was handled
	// and whether it requested exit.
	SendKey(handle, keys string) (handled, quit bool, err error)
	// SendMouse delivers the mouse keys of a handlertest-style sequence,
	// such as <mouse-left><mouse-release>, at column x and row y of the
	// browser, and returns as SendKey does.
	SendMouse(handle, keys string, x, y int) (handled, quit bool, err error)
	// SendPaste delivers text as a bracketed paste, its keys between a
	// paste start and a paste end, and returns as SendKey does.
	SendPaste(handle, text string) (handled, quit bool, err error)
	// SendEvent delivers an event without a key, "focus", "unfocus" or
	// "interrupt", and returns as SendKey does.
	SendEvent(handle, eventType string) (handled, quit bool, err error)
	// Selection returns the selection of the focused installed handler,
	// which Rune copies, and whether it has one.
	Selection(handle string) (string, bool, error)
}

// Graphics is what a render with images drew.
type Graphics struct {
	// Text is the rendered grid, as RenderWindow returns it.
	Text string
	// Images are the images placed over the cells, in order.
	Images []PlacedImage
}

// PlacedImage is an image placed over the cells.
type PlacedImage struct {
	// X, Y, Width and Height are the cells it covers.
	X, Y, Width, Height int
	// PixelWidth and PixelHeight are the size of its pixels.
	PixelWidth, PixelHeight int
	// Colors is how many distinct colors a sample of its pixels holds,
	// which tells a picture from a blank raster.
	Colors int
}

// Options configure spec evaluation.
type Options struct {
	// Filename appears in error positions. Defaults to "spec.star".
	Filename string
	// DefaultTimeout applies to expectations without a timeout kwarg.
	DefaultTimeout time.Duration
	// Print receives print() output. Defaults to discarding.
	Print func(msg string)
}

// Run evaluates src against host. It returns an error when the spec
// is invalid or any expectation fails.
func Run(src []byte, host Host, opts Options) error {
	if opts.Filename == "" {
		opts.Filename = "spec.star"
	}
	if opts.DefaultTimeout <= 0 {
		opts.DefaultTimeout = 10 * time.Second
	}
	printFn := opts.Print
	if printFn == nil {
		printFn = func(string) {}
	}
	thread := &starlark.Thread{
		Name:  opts.Filename,
		Print: func(_ *starlark.Thread, msg string) { printFn(msg) },
		Load: func(_ *starlark.Thread, module string) (starlark.StringDict, error) {
			return nil, fmt.Errorf("load() is not allowed in %s: cannot load %q",
				opts.Filename, module)
		},
	}
	fileOpts := &syntax.FileOptions{
		TopLevelControl: true,
		GlobalReassign:  true,
		Recursion:       true,
	}
	env := &environment{host: host, defaultTimeout: opts.DefaultTimeout}
	_, err := starlark.ExecFileOptions(
		fileOpts, thread, opts.Filename, src, env.builtins())
	if err != nil {
		var evalErr *starlark.EvalError
		if errors.As(err, &evalErr) {
			return fmt.Errorf("%s", evalErr.Backtrace())
		}
		return err
	}
	return nil
}

type environment struct {
	host           Host
	defaultTimeout time.Duration
}

func (e *environment) builtins() starlark.StringDict {
	return starlark.StringDict{
		"expect_metadata":           starlark.NewBuiltin("expect_metadata", e.expectMetadata),
		"config":                    starlark.NewBuiltin("config", e.config),
		"expect_rpc":                starlark.NewBuiltin("expect_rpc", e.expectRPC),
		"stub_rpc":                  starlark.NewBuiltin("stub_rpc", e.stubRPC),
		"expect_command":            starlark.NewBuiltin("expect_command", e.expectCommand),
		"expect_repl_command":       starlark.NewBuiltin("expect_repl_command", e.expectREPLCommand),
		"invoke_command":            starlark.NewBuiltin("invoke_command", e.invokeCommand),
		"invoke_repl_command":       starlark.NewBuiltin("invoke_repl_command", e.invokeREPLCommand),
		"complete_command":          starlark.NewBuiltin("complete_command", e.completeCommand),
		"complete_repl_command":     starlark.NewBuiltin("complete_repl_command", e.completeREPLCommand),
		"repl_help":                 starlark.NewBuiltin("repl_help", e.replHelp),
		"expect_resource_opener":    starlark.NewBuiltin("expect_resource_opener", e.expectResourceOpener),
		"open_resource":             starlark.NewBuiltin("open_resource", e.openResource),
		"complete_resource":         starlark.NewBuiltin("complete_resource", e.completeResource),
		"publish_event":             starlark.NewBuiltin("publish_event", e.publishEvent),
		"wait_idle":                 starlark.NewBuiltin("wait_idle", e.waitIdle),
		"assert_no_unexpected_rpcs": starlark.NewBuiltin("assert_no_unexpected_rpcs", e.assertNoUnexpectedRPCs),
		"expect_window":             starlark.NewBuiltin("expect_window", e.expectWindow),
		"render":                    starlark.NewBuiltin("render", e.render),
		"render_graphics":           starlark.NewBuiltin("render_graphics", e.renderGraphics),
		"send_key":                  starlark.NewBuiltin("send_key", e.sendKey),
		"send_mouse":                starlark.NewBuiltin("send_mouse", e.sendMouse),
		"send_paste":                starlark.NewBuiltin("send_paste", e.sendPaste),
		"send_event":                starlark.NewBuiltin("send_event", e.sendEvent),
		"selection":                 starlark.NewBuiltin("selection", e.selection),
		"present":                   starlark.NewBuiltin("present", predicateBuiltin("present", false)),
		"contains":                  starlark.NewBuiltin("contains", predicateBuiltin("contains", true)),
		"regex":                     starlark.NewBuiltin("regex", predicateBuiltin("regex", true)),
		"fs": &starlarkstruct.Module{
			Name: "fs",
			Members: starlark.StringDict{
				"write": starlark.NewBuiltin("fs.write", e.fsWrite),
			},
		},
	}
}

func (e *environment) expectMetadata(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var id string
	var permissions *starlark.List
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"id", &id, "permissions?", &permissions); err != nil {
		return nil, err
	}
	perms, err := stringList(permissions)
	if err != nil {
		return nil, fmt.Errorf("%s: permissions: %w", b.Name(), err)
	}
	if id == "" {
		return nil, fmt.Errorf("%s: id must not be empty", b.Name())
	}
	return starlark.None, e.host.ExpectMetadata(id, perms)
}

func (e *environment) config(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var dict *starlark.Dict
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "config", &dict); err != nil {
		return nil, err
	}
	cfg, err := starlarkconfig.DictToMap(dict)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	return starlark.None, e.host.SetConfig(cfg)
}

func (e *environment) fsWrite(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var path, content string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"path", &path, "content", &content); err != nil {
		return nil, err
	}
	return starlark.None, e.host.FSWrite(path, content)
}

func (e *environment) expectRPC(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var method string
	var where, respond, respondError *starlark.Dict
	var timeout string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"method", &method, "where?", &where, "respond?", &respond,
		"respond_error?", &respondError, "timeout?", &timeout); err != nil {
		return nil, err
	}
	exp := RPCExpectation{Method: method}
	var err error
	exp.Timeout, err = e.timeout(timeout)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	exp.Where, err = matcherFromDict(where)
	if err != nil {
		return nil, fmt.Errorf("%s: where: %w", b.Name(), err)
	}
	if respond != nil && respondError != nil {
		return nil, fmt.Errorf("%s: respond and respond_error are mutually exclusive",
			b.Name())
	}
	if respond != nil {
		body, err := starlarkconfig.DictToMap(respond)
		if err != nil {
			return nil, fmt.Errorf("%s: respond: %w", b.Name(), err)
		}
		exp.Respond = &record.Response{Body: body}
	}
	if respondError != nil {
		resp, err := errorResponse(respondError)
		if err != nil {
			return nil, fmt.Errorf("%s: respond_error: %w", b.Name(), err)
		}
		exp.Respond = resp
	}
	return starlark.None, e.host.ExpectRPC(exp)
}

func (e *environment) stubRPC(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var method string
	var where, respond, respondError *starlark.Dict
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"method", &method, "where?", &where, "respond?", &respond,
		"respond_error?", &respondError); err != nil {
		return nil, err
	}
	matcher, err := matcherFromDict(where)
	if err != nil {
		return nil, fmt.Errorf("%s: where: %w", b.Name(), err)
	}
	if (respond == nil) == (respondError == nil) {
		return nil, fmt.Errorf("%s: exactly one of respond or respond_error is required",
			b.Name())
	}
	var resp *record.Response
	if respond != nil {
		body, err := starlarkconfig.DictToMap(respond)
		if err != nil {
			return nil, fmt.Errorf("%s: respond: %w", b.Name(), err)
		}
		resp = &record.Response{Body: body}
	} else {
		resp, err = errorResponse(respondError)
		if err != nil {
			return nil, fmt.Errorf("%s: respond_error: %w", b.Name(), err)
		}
	}
	return starlark.None, e.host.StubRPC(method, matcher, resp)
}

func errorResponse(d *starlark.Dict) (*record.Response, error) {
	m, err := starlarkconfig.DictToMap(d)
	if err != nil {
		return nil, err
	}
	resp := &record.Response{}
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%q must be a string", k)
		}
		switch k {
		case "code":
			resp.ErrCode = s
		case "message":
			resp.ErrMsg = s
		default:
			return nil, fmt.Errorf("unknown key %q (want code, message)", k)
		}
	}
	if resp.ErrCode == "" && resp.ErrMsg == "" {
		return nil, errors.New("must set code and/or message")
	}
	return resp, nil
}

func (e *environment) expectCommand(
	t *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	return e.expectCommandKind(b, args, kwargs, false)
}

func (e *environment) expectREPLCommand(
	t *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	return e.expectCommandKind(b, args, kwargs, true)
}

func (e *environment) expectCommandKind(
	b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple, repl bool,
) (starlark.Value, error) {
	var name, timeout string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"name", &name, "timeout?", &timeout); err != nil {
		return nil, err
	}
	d, err := e.timeout(timeout)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	if err := e.host.ExpectCommand(name, repl, d); err != nil {
		return nil, err
	}
	return commandHandle{name: name, repl: repl}, nil
}

func (e *environment) invokeCommand(
	t *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	return e.invokeCommandKind(b, args, kwargs, false)
}

func (e *environment) invokeREPLCommand(
	t *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	return e.invokeCommandKind(b, args, kwargs, true)
}

func (e *environment) invokeCommandKind(
	b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple, repl bool,
) (starlark.Value, error) {
	var cmd starlark.Value
	var cmdArgs *starlark.List
	var timeout string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"command", &cmd, "args?", &cmdArgs, "timeout?", &timeout); err != nil {
		return nil, err
	}
	name, err := commandName(cmd, repl)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	argv, err := stringList(cmdArgs)
	if err != nil {
		return nil, fmt.Errorf("%s: args: %w", b.Name(), err)
	}
	d, err := e.timeout(timeout)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	out, err := e.host.InvokeCommand(name, argv, repl, d)
	if err != nil {
		return nil, err
	}
	if !repl {
		return starlark.None, nil
	}
	return starlark.String(out), nil
}

func (e *environment) completeCommand(
	t *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	return e.completeCommandKind(b, args, kwargs, false)
}

func (e *environment) completeREPLCommand(
	t *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	return e.completeCommandKind(b, args, kwargs, true)
}

func (e *environment) completeCommandKind(
	b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple, repl bool,
) (starlark.Value, error) {
	name, argv, d, err := e.commandCall(b, args, kwargs, repl)
	if err != nil {
		return nil, err
	}
	values, err := e.host.CompleteCommand(name, argv, repl, d)
	if err != nil {
		return nil, err
	}
	return stringValues(values), nil
}

func (e *environment) replHelp(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	name, argv, d, err := e.commandCall(b, args, kwargs, true)
	if err != nil {
		return nil, err
	}
	out, err := e.host.REPLHelp(name, argv, d)
	if err != nil {
		return nil, err
	}
	return starlark.String(out), nil
}

// commandCall unpacks the command, args and timeout of a builtin that
// calls a registered (REPL) command.
func (e *environment) commandCall(
	b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple, repl bool,
) (string, []string, time.Duration, error) {
	var cmd starlark.Value
	var cmdArgs *starlark.List
	var timeout string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"command", &cmd, "args?", &cmdArgs, "timeout?", &timeout); err != nil {
		return "", nil, 0, err
	}
	name, err := commandName(cmd, repl)
	if err != nil {
		return "", nil, 0, fmt.Errorf("%s: %w", b.Name(), err)
	}
	argv, err := stringList(cmdArgs)
	if err != nil {
		return "", nil, 0, fmt.Errorf("%s: args: %w", b.Name(), err)
	}
	d, err := e.timeout(timeout)
	if err != nil {
		return "", nil, 0, fmt.Errorf("%s: %w", b.Name(), err)
	}
	return name, argv, d, nil
}

func stringValues(values []string) *starlark.List {
	list := make([]starlark.Value, len(values))
	for i, v := range values {
		list[i] = starlark.String(v)
	}
	return starlark.NewList(list)
}

func commandName(v starlark.Value, repl bool) (string, error) {
	switch t := v.(type) {
	case starlark.String:
		return string(t), nil
	case commandHandle:
		if t.repl != repl {
			return "", fmt.Errorf("command %q was registered with repl=%v", t.name, t.repl)
		}
		return t.name, nil
	default:
		return "", fmt.Errorf(
			"command must be a string or a handle from expect_command, got %s", v.Type())
	}
}

func (e *environment) expectResourceOpener(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var scheme, timeout string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"scheme", &scheme, "timeout?", &timeout); err != nil {
		return nil, err
	}
	if scheme == "" {
		return nil, fmt.Errorf("%s: scheme must not be empty", b.Name())
	}
	d, err := e.timeout(timeout)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	if err := e.host.ExpectResourceOpener(scheme, d); err != nil {
		return nil, err
	}
	return resourceOpenerHandle{scheme: scheme}, nil
}

func (e *environment) openResource(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var opener starlark.Value
	var uri, timeout string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"opener", &opener, "uri", &uri, "timeout?", &timeout); err != nil {
		return nil, err
	}
	h, ok := opener.(resourceOpenerHandle)
	if !ok {
		return nil, fmt.Errorf(
			"%s: opener must be a handle from expect_resource_opener, got %s",
			b.Name(), opener.Type())
	}
	d, err := e.timeout(timeout)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	return starlark.None, e.host.OpenResource(h.scheme, uri, d)
}

func (e *environment) completeResource(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var opener starlark.Value
	var prefix, timeout string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"opener", &opener, "prefix", &prefix, "timeout?", &timeout); err != nil {
		return nil, err
	}
	h, ok := opener.(resourceOpenerHandle)
	if !ok {
		return nil, fmt.Errorf(
			"%s: opener must be a handle from expect_resource_opener, got %s",
			b.Name(), opener.Type())
	}
	d, err := e.timeout(timeout)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	values, err := e.host.CompleteResource(h.scheme, prefix, d)
	if err != nil {
		return nil, err
	}
	return stringValues(values), nil
}

func (e *environment) publishEvent(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var evType, uri, content string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"type", &evType, "uri?", &uri, "content?", &content); err != nil {
		return nil, err
	}
	return starlark.None, e.host.PublishEvent(evType, uri, content)
}

func (e *environment) waitIdle(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var duration string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"duration", &duration); err != nil {
		return nil, err
	}
	d, err := time.ParseDuration(duration)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	return starlark.None, e.host.WaitIdle(d)
}

func (e *environment) assertNoUnexpectedRPCs(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var ignore *starlark.List
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"ignore?", &ignore); err != nil {
		return nil, err
	}
	patterns, err := stringList(ignore)
	if err != nil {
		return nil, fmt.Errorf("%s: ignore: %w", b.Name(), err)
	}
	return starlark.None, e.host.AssertNoUnexpectedRPCs(patterns)
}

func (e *environment) expectWindow(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var method, timeout string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"method", &method, "timeout?", &timeout); err != nil {
		return nil, err
	}
	d, err := e.timeout(timeout)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	handle, err := e.host.ExpectWindow(method, d)
	if err != nil {
		return nil, err
	}
	return windowHandle{id: handle, method: method}, nil
}

func (e *environment) render(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var win starlark.Value
	width, height := 80, 24
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"window", &win, "width?", &width, "height?", &height); err != nil {
		return nil, err
	}
	handle, err := windowHandleID(win)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	out, err := e.host.RenderWindow(handle, width, height)
	if err != nil {
		return nil, err
	}
	return starlark.String(out), nil
}

func (e *environment) renderGraphics(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var win starlark.Value
	width, height := 80, 24
	var cellWidthV, cellHeightV starlark.Value = starlark.Float(10), starlark.Float(20)
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"window", &win, "width?", &width, "height?", &height,
		"cell_width?", &cellWidthV, "cell_height?", &cellHeightV); err != nil {
		return nil, err
	}
	cellWidth, okWidth := starlark.AsFloat(cellWidthV)
	cellHeight, okHeight := starlark.AsFloat(cellHeightV)
	if !okWidth || !okHeight || !(cellWidth > 0 && cellHeight > 0) {
		return nil, fmt.Errorf("%s: cell_width and cell_height must be positive", b.Name())
	}
	handle, err := windowHandleID(win)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	out, err := e.host.RenderGraphics(handle, width, height, cellWidth, cellHeight)
	if err != nil {
		return nil, err
	}
	images := make([]starlark.Value, 0, len(out.Images))
	for _, img := range out.Images {
		d := starlark.NewDict(7)
		for k, v := range map[string]int{
			"x": img.X, "y": img.Y, "width": img.Width, "height": img.Height,
			"pixel_width": img.PixelWidth, "pixel_height": img.PixelHeight, "colors": img.Colors,
		} {
			_ = d.SetKey(starlark.String(k), starlark.MakeInt(v))
		}
		images = append(images, d)
	}
	ret := starlark.NewDict(2)
	_ = ret.SetKey(starlark.String("text"), starlark.String(out.Text))
	_ = ret.SetKey(starlark.String("images"), starlark.NewList(images))
	return ret, nil
}

func (e *environment) sendKey(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var win starlark.Value
	var keys string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"window", &win, "keys", &keys); err != nil {
		return nil, err
	}
	handle, err := windowHandleID(win)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	handled, quit, err := e.host.SendKey(handle, keys)
	return handledResult(handled, quit, err)
}

func (e *environment) sendMouse(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var win starlark.Value
	var keys string
	var x, y int
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"window", &win, "keys", &keys, "x", &x, "y", &y); err != nil {
		return nil, err
	}
	handle, err := windowHandleID(win)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	return handledResult(e.host.SendMouse(handle, keys, x, y))
}

func (e *environment) sendPaste(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var win starlark.Value
	var text string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"window", &win, "text", &text); err != nil {
		return nil, err
	}
	handle, err := windowHandleID(win)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	return handledResult(e.host.SendPaste(handle, text))
}

func (e *environment) sendEvent(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var win starlark.Value
	var eventType string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"window", &win, "type", &eventType); err != nil {
		return nil, err
	}
	handle, err := windowHandleID(win)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	return handledResult(e.host.SendEvent(handle, eventType))
}

func (e *environment) selection(
	_ *starlark.Thread, b *starlark.Builtin,
	args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var win starlark.Value
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "window", &win); err != nil {
		return nil, err
	}
	handle, err := windowHandleID(win)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	sel, ok, err := e.host.Selection(handle)
	if err != nil {
		return nil, err
	}
	if !ok {
		return starlark.None, nil
	}
	return starlark.String(sel), nil
}

// handledResult is the {"handled", "quit"} dict the send builtins return.
func handledResult(handled, quit bool, err error) (starlark.Value, error) {
	if err != nil {
		return nil, err
	}
	ret := starlark.NewDict(2)
	_ = ret.SetKey(starlark.String("handled"), starlark.Bool(handled))
	_ = ret.SetKey(starlark.String("quit"), starlark.Bool(quit))
	return ret, nil
}

func (e *environment) timeout(s string) (time.Duration, error) {
	if s == "" {
		return e.defaultTimeout, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid timeout %q: %w", s, err)
	}
	return d, nil
}

func stringList(l *starlark.List) ([]string, error) {
	if l == nil {
		return nil, nil
	}
	out := make([]string, 0, l.Len())
	for i := range l.Len() {
		s, ok := l.Index(i).(starlark.String)
		if !ok {
			return nil, fmt.Errorf("element %d must be a string, got %s",
				i, l.Index(i).Type())
		}
		out = append(out, string(s))
	}
	return out, nil
}
