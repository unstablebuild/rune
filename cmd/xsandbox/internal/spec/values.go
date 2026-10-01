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
	"fmt"

	"go.starlark.net/starlark"
	"unstable.build/rune/cmd/xsandbox/internal/record"
	"unstable.build/rune/internal/ide/starlarkconfig"
)

// predicateValue wraps a record.Predicate as an opaque Starlark value
// usable inside expect_rpc where dicts.
type predicateValue struct {
	p record.Predicate
}

var _ starlark.Value = predicateValue{}

func (v predicateValue) String() string        { return v.p.String() }
func (v predicateValue) Type() string          { return "matcher" }
func (v predicateValue) Freeze()               {}
func (v predicateValue) Truth() starlark.Bool  { return starlark.True }
func (v predicateValue) Hash() (uint32, error) { return starlark.String(v.p.String()).Hash() }

func predicateBuiltin(kind string, wantsArg bool) func(
	*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(
		_ *starlark.Thread, b *starlark.Builtin,
		args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		var arg string
		if wantsArg {
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "value", &arg); err != nil {
				return nil, err
			}
		} else if err := starlark.UnpackArgs(b.Name(), args, kwargs); err != nil {
			return nil, err
		}
		return predicateValue{p: record.Predicate{Kind: kind, Arg: arg}}, nil
	}
}

// commandHandle is returned by expect_command / expect_repl_command
// so subsequent invoke calls can reference the registration.
type commandHandle struct {
	name string
	repl bool
}

var _ starlark.Value = commandHandle{}

func (h commandHandle) String() string {
	if h.repl {
		return fmt.Sprintf("<repl command %q>", h.name)
	}
	return fmt.Sprintf("<command %q>", h.name)
}
func (h commandHandle) Type() string          { return "command" }
func (h commandHandle) Freeze()               {}
func (h commandHandle) Truth() starlark.Bool  { return starlark.True }
func (h commandHandle) Hash() (uint32, error) { return starlark.String(h.name).Hash() }

// resourceOpenerHandle is returned by expect_resource_opener so
// open_resource can reference the registration.
type resourceOpenerHandle struct {
	scheme string
}

var _ starlark.Value = resourceOpenerHandle{}

func (h resourceOpenerHandle) String() string {
	return fmt.Sprintf("<resource opener %q>", h.scheme)
}
func (h resourceOpenerHandle) Type() string          { return "resource_opener" }
func (h resourceOpenerHandle) Freeze()               {}
func (h resourceOpenerHandle) Truth() starlark.Bool  { return starlark.True }
func (h resourceOpenerHandle) Hash() (uint32, error) { return starlark.String(h.scheme).Hash() }

// windowHandle is returned by expect_window so render and send_key can
// reference the installed handler.
type windowHandle struct {
	id     string
	method string
}

var _ starlark.Value = windowHandle{}

func (h windowHandle) String() string        { return fmt.Sprintf("<window %q>", h.method) }
func (h windowHandle) Type() string          { return "window" }
func (h windowHandle) Freeze()               {}
func (h windowHandle) Truth() starlark.Bool  { return starlark.True }
func (h windowHandle) Hash() (uint32, error) { return starlark.String(h.id).Hash() }

// windowHandleID extracts the handle id from a value returned by
// expect_window.
func windowHandleID(v starlark.Value) (string, error) {
	h, ok := v.(windowHandle)
	if !ok {
		return "", fmt.Errorf("window must be a handle from expect_window, got %s", v.Type())
	}
	return h.id, nil
}

// matcherFromDict converts a where dict to a record.Matcher. Values
// may be plain Starlark values (compared exactly) or predicate values
// from present()/contains()/regex(), including nested inside dicts.
func matcherFromDict(d *starlark.Dict) (*record.Matcher, error) {
	if d == nil {
		return nil, nil
	}
	fields := make(map[string]any, d.Len())
	for _, item := range d.Items() {
		key, ok := item[0].(starlark.String)
		if !ok {
			return nil, fmt.Errorf("keys must be strings, got %s", item[0].Type())
		}
		v, err := matcherValue(item[1])
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", string(key), err)
		}
		fields[string(key)] = v
	}
	return record.NewMatcher(fields), nil
}

func matcherValue(v starlark.Value) (any, error) {
	switch t := v.(type) {
	case predicateValue:
		return t.p, nil
	case *starlark.Dict:
		out := make(map[string]any, t.Len())
		for _, item := range t.Items() {
			key, ok := item[0].(starlark.String)
			if !ok {
				return nil, fmt.Errorf("keys must be strings, got %s", item[0].Type())
			}
			nested, err := matcherValue(item[1])
			if err != nil {
				return nil, err
			}
			out[string(key)] = nested
		}
		return out, nil
	default:
		return starlarkconfig.ToGo(v)
	}
}
