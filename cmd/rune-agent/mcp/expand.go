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

package mcp

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// Expansion reports the environment variables a server config refers
// to.
type Expansion struct {
	// Referenced lists every variable the config names, sorted.
	Referenced []string
	// Missing lists the referenced variables that are unset and have no
	// default, sorted.
	Missing []string
}

// ExpandConfig expands ${VAR} and ${VAR:-default} references in the
// command, args, env values, url, header values and OAuth client secret
// of cfg, resolving variables through getenv. Command substitution is
// an error. Rune's own credential variables always expand to empty,
// whatever default the reference gives, so a workspace config cannot
// smuggle them to a server.
func ExpandConfig(
	cfg ServerConfig, getenv func(string) (string, bool),
) (ServerConfig, Expansion, error) {
	e := &expander{
		getenv:     getenv,
		referenced: make(map[string]bool),
		missing:    make(map[string]bool),
	}
	out := cfg
	var err error
	field := func(name string, v *string) {
		if err != nil {
			return
		}
		var expanded string
		if expanded, err = e.expand(*v); err != nil {
			err = fmt.Errorf("expand %s: %w", name, err)
			return
		}
		*v = expanded
	}
	field("command", &out.Command)
	field("url", &out.URL)
	out.Args = slices.Clone(cfg.Args)
	for i := range out.Args {
		field(fmt.Sprintf("args[%d]", i), &out.Args[i])
	}
	out.Env = expandMap(cfg.Env, "env", field)
	out.Headers = expandMap(cfg.Headers, "headers", field)
	if cfg.OAuth != nil {
		oauth := *cfg.OAuth
		field("oauth.clientSecret", &oauth.ClientSecret)
		out.OAuth = &oauth
	}
	if err != nil {
		return ServerConfig{}, Expansion{}, err
	}
	return out, Expansion{
		Referenced: slices.Sorted(maps.Keys(e.referenced)),
		Missing:    slices.Sorted(maps.Keys(e.missing)),
	}, nil
}

func expandMap(
	m map[string]string, name string, field func(string, *string),
) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		v := m[k]
		field(name+"."+k, &v)
		out[k] = v
	}
	return out
}

type expander struct {
	getenv     func(string) (string, bool)
	referenced map[string]bool
	missing    map[string]bool
}

var errCommandSubstitution = errors.New("command substitution is not supported")

func (e *expander) expand(s string) (string, error) {
	// Without a '$' nothing expands, and the value is kept verbatim
	// rather than having its backslashes interpreted.
	if !strings.ContainsRune(s, '$') {
		return s, nil
	}
	word, err := syntax.NewParser().Document(strings.NewReader(s))
	if err != nil {
		return "", err
	}
	var walkErr error
	syntax.Walk(word, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			walkErr = errCommandSubstitution
			return false
		case *syntax.ParamExp:
			if n.Param == nil || !syntax.ValidName(n.Param.Value) {
				return true
			}
			name := n.Param.Value
			e.referenced[name] = true
			if slices.Contains(credentialEnvKeys, name) {
				// Drop any operator, so a default cannot stand in
				// for the credential either.
				*n = syntax.ParamExp{Dollar: n.Dollar, Rbrace: n.Rbrace, Param: n.Param}
				return false
			}
			if _, ok := e.getenv(name); !ok && !hasFallback(n) {
				e.missing[name] = true
			}
		}
		return true
	})
	if walkErr != nil {
		return "", walkErr
	}
	return expand.Document(&expand.Config{Env: e}, word)
}

// hasFallback reports whether p supplies a value for an unset variable.
func hasFallback(p *syntax.ParamExp) bool {
	if p.Exp == nil {
		return false
	}
	switch p.Exp.Op {
	case syntax.DefaultUnset, syntax.DefaultUnsetOrNull,
		syntax.AlternateUnset, syntax.AlternateUnsetOrNull:
		return true
	}
	return false
}

// Get implements expand.Environ.
func (e *expander) Get(name string) expand.Variable {
	if slices.Contains(credentialEnvKeys, name) {
		return expand.Variable{Set: true, Kind: expand.String}
	}
	v, ok := e.getenv(name)
	if !ok {
		return expand.Variable{}
	}
	return expand.Variable{Set: true, Kind: expand.String, Str: v}
}

// Each implements expand.Environ. Expansion never enumerates the
// environment.
func (e *expander) Each(func(string, expand.Variable) bool) {}
