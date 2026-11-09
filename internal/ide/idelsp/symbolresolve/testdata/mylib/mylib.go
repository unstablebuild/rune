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

// Package mylib exercises qualified-type and selector phases.
package mylib

type MyType struct {
	Value string
}

func (m MyType) String() string { return m.Value }

func (m *MyType) Set(v string) { m.Value = v }

// Store's methods have no receiver declaration; they must still be
// addressable as mylib.Store.Method.
type Store interface {
	Load(key string) string
	save(key, v string)
}

func MyFunc(s string) string { return "mylib:" + s }

func unexportedHelper() string { return "secret" }

// New is intentionally homonymous with iter.New to exercise
// cross-package isolation.
func New(v string) MyType { return MyType{Value: v} }
