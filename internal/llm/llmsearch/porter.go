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

package llmsearch

// porterMaxToken is FTS5_PORTER_MAX_TOKEN: longer tokens are not stemmed.
const porterMaxToken = 64

// porterRule is one suffix rule of fts5_tokenize.c's generated Porter
// steps. Within a step, the first rule whose suffix matches (leaving at
// least one byte of stem) is the only one tried, whether or not its
// condition holds: that is what the C switch over the second-to-last byte
// and its else-if chains amount to, since rules of different branches can
// never match the same word.
type porterRule struct {
	suffix string
	cond   func(stem []byte) bool
	output string
}

var (
	porterStep1B2 = []porterRule{{"at", nil, "ate"}, {"bl", nil, "ble"}, {"iz", nil, "ize"}}
	porterStep2   = []porterRule{
		{"ational", porterMGt0, "ate"}, {"tional", porterMGt0, "tion"},
		{"enci", porterMGt0, "ence"}, {"anci", porterMGt0, "ance"},
		{"izer", porterMGt0, "ize"},
		{"logi", porterMGt0, "log"},
		{"bli", porterMGt0, "ble"}, {"alli", porterMGt0, "al"}, {"entli", porterMGt0, "ent"},
		{"eli", porterMGt0, "e"}, {"ousli", porterMGt0, "ous"},
		{"ization", porterMGt0, "ize"}, {"ation", porterMGt0, "ate"}, {"ator", porterMGt0, "ate"},
		{"alism", porterMGt0, "al"}, {"iveness", porterMGt0, "ive"}, {"fulness", porterMGt0, "ful"},
		{"ousness", porterMGt0, "ous"},
		{"aliti", porterMGt0, "al"}, {"iviti", porterMGt0, "ive"}, {"biliti", porterMGt0, "ble"},
	}
	porterStep3 = []porterRule{
		{"ical", porterMGt0, "ic"},
		{"ness", porterMGt0, ""},
		{"icate", porterMGt0, "ic"}, {"iciti", porterMGt0, "ic"},
		{"ful", porterMGt0, ""},
		{"ative", porterMGt0, ""},
		{"alize", porterMGt0, "al"},
	}
	porterStep4 = []porterRule{
		{"al", porterMGt1, ""},
		{"ance", porterMGt1, ""}, {"ence", porterMGt1, ""},
		{"er", porterMGt1, ""},
		{"ic", porterMGt1, ""},
		{"able", porterMGt1, ""}, {"ible", porterMGt1, ""},
		{"ant", porterMGt1, ""}, {"ement", porterMGt1, ""}, {"ment", porterMGt1, ""},
		{"ent", porterMGt1, ""},
		{"ion", porterMGt1SorT, ""}, {"ou", porterMGt1, ""},
		{"ism", porterMGt1, ""},
		{"ate", porterMGt1, ""}, {"iti", porterMGt1, ""},
		{"ous", porterMGt1, ""},
		{"ive", porterMGt1, ""},
		{"ize", porterMGt1, ""},
	}
)

// porterStem is fts5PorterCb: tok stemmed byte for byte as SQLite stems
// it. Non-ASCII bytes are consonants to it, as they are to SQLite. tok is
// not modified.
func porterStem(tok []byte) []byte {
	if len(tok) > porterMaxToken || len(tok) < 3 {
		return tok
	}
	b := make([]byte, len(tok), len(tok)+1)
	copy(b, tok)

	b = porterStep1A(b)
	var ok bool
	if b, ok = porterStep1B(b); ok {
		if b, ok = porterApply(b, porterStep1B2); !ok {
			if c := b[len(b)-1]; !porterIsVowel(c, false) && c != 'l' && c != 's' && c != 'z' &&
				len(b) >= 2 && c == b[len(b)-2] {
				b = b[:len(b)-1]
			} else if porterMEq1(b) && porterOStar(b) {
				b = append(b, 'e')
			}
		}
	}

	if b[len(b)-1] == 'y' && porterVowel(b[:len(b)-1]) {
		b[len(b)-1] = 'i'
	}

	b, _ = porterApply(b, porterStep2)
	b, _ = porterApply(b, porterStep3)
	b, _ = porterApply(b, porterStep4)

	if n := len(b); b[n-1] == 'e' && (porterMGt1(b[:n-1]) || (porterMEq1(b[:n-1]) && !porterOStar(b[:n-1]))) {
		b = b[:n-1]
	}
	if n := len(b); n > 1 && b[n-1] == 'l' && b[n-2] == 'l' && porterMGt1(b[:n-1]) {
		b = b[:n-1]
	}
	return b
}

// porterStep1B is fts5PorterStep1B. ok is true only when "ed" or "ing" was
// removed: rewriting "eed" to "ee" does not go on to the rest of step 1b.
func porterStep1B(b []byte) (_ []byte, ok bool) {
	n := len(b)
	switch {
	case n > 3 && string(b[n-3:]) == "eed":
		if porterMGt0(b[:n-3]) {
			return b[:n-1], false
		}
	case n > 2 && string(b[n-2:]) == "ed":
		if porterVowel(b[:n-2]) {
			return b[:n-2], true
		}
	case n > 3 && string(b[n-3:]) == "ing":
		if porterVowel(b[:n-3]) {
			return b[:n-3], true
		}
	}
	return b, false
}

func porterStep1A(b []byte) []byte {
	n := len(b)
	if b[n-1] != 's' {
		return b
	}
	switch {
	case b[n-2] == 'e':
		if (n > 4 && b[n-4] == 's' && b[n-3] == 's') || (n > 3 && b[n-3] == 'i') {
			return b[:n-2]
		}
		return b[:n-1]
	case b[n-2] != 's':
		return b[:n-1]
	}
	return b
}

// porterApply applies the first rule of rules whose suffix b ends in. ok
// reports whether the rule's condition held and its output replaced the
// suffix.
func porterApply(b []byte, rules []porterRule) ([]byte, bool) {
	n := len(b)
	for _, r := range rules {
		if n <= len(r.suffix) || string(b[n-len(r.suffix):]) != r.suffix {
			continue
		}
		stem := b[:n-len(r.suffix)]
		if r.cond != nil && !r.cond(stem) {
			return b, false
		}
		return append(stem, r.output...), true
	}
	return b, false
}

func porterIsVowel(c byte, yIsVowel bool) bool {
	return c == 'a' || c == 'e' || c == 'i' || c == 'o' || c == 'u' || (yIsVowel && c == 'y')
}

// porterGobbleVC is fts5PorterGobbleVC: the length of stem's first
// vowel-consonant sequence, or 0 when it has none.
func porterGobbleVC(stem []byte, prevCons bool) int {
	cons := prevCons
	i := 0
	for ; i < len(stem); i++ {
		if cons = !porterIsVowel(stem[i], cons); !cons {
			break
		}
	}
	for i++; i < len(stem); i++ {
		if cons = !porterIsVowel(stem[i], cons); cons {
			return i + 1
		}
	}
	return 0
}

// porterMGt0 is the condition (m > 0).
func porterMGt0(stem []byte) bool { return porterGobbleVC(stem, false) != 0 }

// porterMGt1 is the condition (m > 1).
func porterMGt1(stem []byte) bool {
	n := porterGobbleVC(stem, false)
	return n != 0 && porterGobbleVC(stem[n:], true) != 0
}

// porterMEq1 is the condition (m = 1).
func porterMEq1(stem []byte) bool {
	n := porterGobbleVC(stem, false)
	return n != 0 && porterGobbleVC(stem[n:], true) == 0
}

// porterOStar is the condition (*o): stem ends consonant-vowel-consonant,
// the last not w, x or y.
func porterOStar(stem []byte) bool {
	n := len(stem)
	if n == 0 || stem[n-1] == 'w' || stem[n-1] == 'x' || stem[n-1] == 'y' {
		return false
	}
	mask, cons := 0, false
	for _, c := range stem {
		cons = !porterIsVowel(c, cons)
		bit := 0
		if cons {
			bit = 1
		}
		mask = (mask<<1 + bit) & 0x0007
	}
	return mask == 0x0005
}

// porterMGt1SorT is the condition (m > 1 and (*S or *T)).
func porterMGt1SorT(stem []byte) bool {
	n := len(stem)
	return (stem[n-1] == 's' || stem[n-1] == 't') && porterMGt1(stem)
}

// porterVowel is fts5Porter_Vowel, the condition (*v*). A y counts as a
// vowel anywhere but first, as in SQLite (not only after a consonant).
func porterVowel(stem []byte) bool {
	for i, c := range stem {
		if porterIsVowel(c, i > 0) {
			return true
		}
	}
	return false
}
