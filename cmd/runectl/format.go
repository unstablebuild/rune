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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"github.com/unstablebuild/rune-go-sdk/cli/cliformat"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

func printResult[T any](
	ctx context.Context,
	format string,
	val T,
	defaultPrint func(T),
	tableFields []string,
) error {
	if format == "" {
		defaultPrint(val)
		return nil
	}
	if format == "json" {
		return printJSONValue(val, tableFields)
	}
	wrapped := wrapForTable(val, tableFields)
	it := iterator.FromSlice(
		[]map[string]any{wrapped},
	)
	return printIterator(
		ctx, format, it, tableFields,
	)
}

func printJSONValue[T any](
	val T, tableFields []string,
) error {
	v := reflect.ValueOf(val)
	var m map[string]any
	if v.Kind() == reflect.Struct {
		raw, err := json.Marshal(val)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
	} else {
		m = make(map[string]any)
		if len(tableFields) > 0 {
			m[tableFields[0]] = val
		}
	}
	m["success"] = true
	return json.NewEncoder(os.Stdout).Encode(m)
}

func printIterator[T any](
	ctx context.Context,
	format string,
	it iterator.Iterator[T],
	tableFields []string,
) error {
	var f cliformat.IteratorFormatter[T]
	switch format {
	case "table":
		f = cliformat.Table[T](tableFields)
	case "json":
		f = cliformat.JSON[T]()
	default:
		var err error
		f, err = cliformat.Template[T](format)
		if err != nil {
			return err
		}
	}
	return f.Format(ctx, os.Stdout, it)
}

func printString(
	format, val string, tableFields []string,
) error {
	return printResult(
		context.Background(),
		format,
		val,
		func(v string) { fmt.Println(v) },
		tableFields,
	)
}

func printOK(format string) error {
	if format == "json" {
		_, err := fmt.Println(`{"success":true}`)
		return err
	}
	fmt.Println("OK")
	return nil
}

func formatError(format string, err error) error {
	if err == nil || format != "json" {
		return err
	}
	m := map[string]any{
		"success": false,
		"error":   err.Error(),
	}
	_ = json.NewEncoder(os.Stdout).Encode(m)
	return nil
}

func wrapForTable[T any](
	val T, fields []string,
) map[string]any {
	m := make(map[string]any)
	v := reflect.ValueOf(val)
	if v.Kind() == reflect.Struct {
		for _, f := range fields {
			fv := v.FieldByName(f)
			if fv.IsValid() && fv.CanInterface() {
				m[f] = fv.Interface()
			}
		}
	} else if len(fields) > 0 {
		m[fields[0]] = val
	}
	return m
}
