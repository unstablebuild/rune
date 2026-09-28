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

package helix

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
)

// The incrementors behind C-a and C-x, from helix-core/src/increment.
// Each takes the text a range covers and the amount to add, and
// answers with the replacement or nothing when the text is not what
// it handles.

// incrementText is the incrementor chain increment_impl tries in
// order: an integer, then a date or time.
func incrementText(selected string, amount int64) (string, bool) {
	if s, ok := incrementInteger(selected, amount); ok {
		return s, true
	}
	return incrementDateTime(selected, amount)
}

const digitSeparator = '_'

// incrementInteger is increment::integer. Base 10 with no prefix, or
// 2, 8 and 16 with a 0b, 0o or 0x prefix; underscores between digits
// are separators and are kept where they were, counted from the
// right, with more added at the same spacing when the number grows.
// A base 10 number keeps its zero padding and its width across a sign
// change; the other bases cannot go negative and stop at zero.
func incrementInteger(selected string, amount int64) (string, bool) {
	if selected == "" || strings.HasPrefix(selected, "_") || strings.HasSuffix(selected, "_") {
		return "", false
	}
	radix := 10
	switch {
	case strings.HasPrefix(selected, "0x"):
		radix = 16
	case strings.HasPrefix(selected, "0o"):
		radix = 8
	case strings.HasPrefix(selected, "0b"):
		radix = 2
	}
	var separators []int
	for i := len(selected) - 1; i >= 0; i-- {
		if selected[i] == digitSeparator {
			separators = append(separators, len(selected)-1-i)
		}
	}
	word := strings.ReplaceAll(selected, string(digitSeparator), "")
	delta := big.NewInt(amount)

	var text string
	if radix == 10 {
		value, ok := new(big.Int).SetString(word, 10)
		if !ok || !isDecimal(word) {
			return "", false
		}
		next := new(big.Int).Add(value, delta)
		width := len(word) - len(separators)
		switch {
		case value.Sign() < 0 && next.Sign() >= 0:
			width--
		case value.Sign() >= 0 && next.Sign() < 0:
			width++
		}
		text = next.String()
		if strings.HasPrefix(word, "0") || strings.HasPrefix(word, "-0") {
			text = zeroPad(text, width)
		}
	} else {
		digits := word[2:]
		value, ok := new(big.Int).SetString(digits, radix)
		if !ok || digits == "" || strings.HasPrefix(digits, "-") || strings.HasPrefix(digits, "+") {
			return "", false
		}
		next := new(big.Int).Add(value, delta)
		if next.Sign() < 0 {
			next.SetInt64(0)
		}
		width := len(selected) - 2 - len(separators)
		switch radix {
		case 2:
			text = "0b" + zeroPad(next.Text(2), width)
		case 8:
			text = "0o" + zeroPad(next.Text(8), width)
		default:
			lower, upper := 0, 0
			for _, ch := range digits {
				switch {
				case ch >= 'a' && ch <= 'z':
					lower++
				case ch >= 'A' && ch <= 'Z':
					upper++
				}
			}
			hex := next.Text(16)
			if upper > lower {
				hex = strings.ToUpper(hex)
			}
			text = "0x" + zeroPad(hex, width)
		}
	}

	for _, rtl := range separators {
		if rtl < len(text) {
			if at := len(text) - rtl; at > 0 {
				text = text[:at] + string(digitSeparator) + text[at:]
			}
		}
	}
	if len(text) > len(selected) && len(separators) > 0 {
		spacing := separators[0]
		if n := len(separators); n >= 2 {
			spacing = separators[n-1] - separators[n-2] - 1
		}
		prefix := 0
		if radix != 10 {
			prefix = 2
		}
		if at := strings.IndexByte(text, digitSeparator); at >= 0 && spacing > 0 {
			for at-prefix > spacing {
				at -= spacing
				text = text[:at] + string(digitSeparator) + text[at:]
			}
		}
	}
	return text, true
}

// isDecimal reports whether s is an optionally signed run of digits,
// which is what big.Int accepts in base 10 apart from a leading plus.
func isDecimal(s string) bool {
	s = strings.TrimPrefix(s, "-")
	if s == "" {
		return false
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// zeroPad pads a formatted number with zeros after its sign until it
// is width wide, as Rust's {:0width$} does.
func zeroPad(s string, width int) string {
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	if pad := width - len(sign) - len(s); pad > 0 {
		s = strings.Repeat("0", pad) + s
	}
	return sign + s
}

// dateFormat is one of the date and time shapes increment::date_time
// recognizes: the anchored pattern the whole text must match, the Go
// layout that parses and prints it, and what the amount counts in.
type dateFormat struct {
	pattern *regexp.Regexp
	layout  string
	unit    time.Duration
}

var dateFormats = []dateFormat{
	{regexp.MustCompile(`^\d{4}-[0-1]\d-[0-3]\d [0-2]\d:[0-5]\d:[0-5]\d$`), "2006-01-02 15:04:05", time.Minute},
	{regexp.MustCompile(`^\d{4}/[0-1]\d/[0-3]\d [0-2]\d:[0-5]\d:[0-5]\d$`), "2006/01/02 15:04:05", time.Minute},
	{regexp.MustCompile(`^\d{4}-[0-1]\d-[0-3]\d [0-2]\d:[0-5]\d$`), "2006-01-02 15:04", time.Minute},
	{regexp.MustCompile(`^\d{4}/[0-1]\d/[0-3]\d [0-2]\d:[0-5]\d$`), "2006/01/02 15:04", time.Minute},
	{regexp.MustCompile(`^\d{4}-[0-1]\d-[0-3]\d$`), "2006-01-02", 24 * time.Hour},
	{regexp.MustCompile(`^\d{4}/[0-1]\d/[0-3]\d$`), "2006/01/02", 24 * time.Hour},
	{regexp.MustCompile(`^(?:Sun|Mon|Tue|Wed|Thu|Fri|Sat) (?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) [0-3]\d \d{4}$`), "Mon Jan 02 2006", 24 * time.Hour},
	{regexp.MustCompile(`^[0-3]\d-(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)-\d{4}$`), "02-Jan-2006", 24 * time.Hour},
	{regexp.MustCompile(`^\d{4} (?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) [0-3]\d$`), "2006 Jan 02", 24 * time.Hour},
	{regexp.MustCompile(`^(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) [0-3]\d, \d{4}$`), "Jan 02, 2006", 24 * time.Hour},
	{regexp.MustCompile(`^1?\d:[0-5]\d:[0-5]\d (?:am|pm)$`), "3:04:05 pm", time.Minute},
	{regexp.MustCompile(`^1?\d:[0-5]\d (?:am|pm)$`), "3:04 pm", time.Minute},
	{regexp.MustCompile(`^1?\d:[0-5]\d:[0-5]\d (?:AM|PM)$`), "3:04:05 PM", time.Minute},
	{regexp.MustCompile(`^1?\d:[0-5]\d (?:AM|PM)$`), "3:04 PM", time.Minute},
	{regexp.MustCompile(`^[0-2]\d:[0-5]\d:[0-5]\d$`), "15:04:05", time.Minute},
	{regexp.MustCompile(`^[0-2]\d:[0-5]\d$`), "15:04", time.Minute},
}

// incrementDateTime is increment::date_time: a date moves by amount
// days, a time or a date with a time by amount minutes, a time on its
// own wrapping around the day. The text has to be exactly one of the
// shapes above and a real calendar date.
func incrementDateTime(selected string, amount int64) (string, bool) {
	for _, f := range dateFormats {
		if !f.pattern.MatchString(selected) {
			continue
		}
		t, err := time.Parse(f.layout, selected)
		if err != nil {
			return "", false
		}
		if f.unit == time.Minute && amount > int64(time.Duration(1<<62)/time.Minute) {
			return "", false
		}
		return t.Add(time.Duration(amount) * f.unit).Format(f.layout), true
	}
	return "", false
}

// String satisfies fmt.Stringer for error messages in tests.
func (f dateFormat) String() string { return fmt.Sprintf("dateFormat(%s)", f.layout) }
