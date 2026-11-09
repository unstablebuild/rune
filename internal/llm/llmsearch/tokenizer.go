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

// fts5MaxTokenSize is the longest token FTS5 stores or queries; a longer
// one is cut to this many bytes on both sides.
const fts5MaxTokenSize = 32768

// token is one term as the keyword index keeps it: its text folded (and,
// from ftsTokens, stemmed) the way DigUp's FTS5 table keeps it, and the
// bytes of the input it was read from.
type token struct {
	text       string
	start, end int
}

// ftsTokens tokenizes text as DigUp's FTS5 table does, with
// `porter unicode61 remove_diacritics 2 categories 'L* N* Co M*'`. Every
// token takes one position, including the rare empty one (a lone combining
// mark folds to nothing, as in SQLite), so positions and column sizes match
// FTS5's.
func ftsTokens(text string) []token {
	var out []token
	unicode61(text, func(tok []byte, start, end int) {
		stem := porterStem(tok)
		if len(stem) > fts5MaxTokenSize {
			stem = stem[:fts5MaxTokenSize]
		}
		out = append(out, token{text: string(stem), start: start, end: end})
	})
	return out
}

func tokenTexts(toks []token) []string {
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		out = append(out, t.text)
	}
	return out
}

// unicode61Categories are the unicode61 categories DigUp asks for (L*, N*,
// Co, M*), indexed by sqlite3Fts5UnicodeCategory's numbering. 0, the
// codepoints the table doesn't know, is set as sqlite3Fts5UnicodeCatParse
// always sets it.
var unicode61Categories = func() (c [32]bool) {
	for _, i := range []int{0, 5, 6, 7, 8, 9, 30, 13, 14, 15, 31, 10, 11, 12} {
		c[i] = true
	}
	return c
}()

// unicode61ASCII is sqlite3Fts5UnicodeAscii: which ASCII bytes are token
// characters.
var unicode61ASCII = func() (a [128]bool) {
	i, tbl := 0, 0
	for i < 128 {
		tokenChar := unicode61Categories[fts5UnicodeData[tbl]&0x1F]
		n := int(fts5UnicodeData[tbl]>>5) + i
		for ; i < 128 && i < n; i++ {
			a[i] = tokenChar
		}
		tbl++
	}
	a[0] = false
	return a
}()

// unicode61 is fts5UnicodeTokenize with DigUp's options: it calls fn with
// every token of text, folded (lower case, diacritics removed), and its byte
// range. tok is reused between calls.
func unicode61(text string, fn func(tok []byte, start, end int)) {
	var fold []byte
	i, n := 0, len(text)
	for {
		var start, end int
		var code uint32
		nonASCII := false
		for {
			if i >= n {
				return
			}
			if c := text[i]; c&0x80 != 0 {
				start = i
				code, i = fts5ReadUTF8(text, i)
				if unicode61Categories[fts5Category(code)] {
					nonASCII = true
					break
				}
			} else {
				if unicode61ASCII[c] {
					start = i
					break
				}
				i++
			}
		}
		fold = fold[:0]
		if nonASCII {
			fold = fts5AppendFolded(fold, code)
			end = i
		}
		for i < n {
			if c := text[i]; c&0x80 != 0 {
				// A separator read here is consumed, as in SQLite.
				code, i = fts5ReadUTF8(text, i)
				if !unicode61Categories[fts5Category(code)] && !fts5IsDiacritic(code) {
					break
				}
				fold = fts5AppendFolded(fold, code)
			} else if !unicode61ASCII[c] {
				break
			} else {
				if c >= 'A' && c <= 'Z' {
					c += 'a' - 'A'
				}
				fold = append(fold, c)
				i++
			}
			end = i
		}
		fn(fold, start, end)
	}
}

func fts5AppendFolded(out []byte, code uint32) []byte {
	if code = fts5Fold(code); code != 0 {
		out = fts5AppendUTF8(out, code)
	}
	return out
}

var fts5UTF8Trans1 = [...]byte{
	0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
	0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
	0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
	0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
	0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
	0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
	0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
	0x00, 0x01, 0x02, 0x03, 0x00, 0x01, 0x00, 0x00,
}

// fts5ReadUTF8 is FTS5_READ_UTF8: the codepoint at s[i] and the offset
// after it, reading malformed input the way SQLite does.
func fts5ReadUTF8(s string, i int) (uint32, int) {
	c := uint32(s[i])
	i++
	if c >= 0xC0 {
		c = uint32(fts5UTF8Trans1[c-0xC0])
		for i < len(s) && s[i]&0xC0 == 0x80 {
			c = c<<6 + uint32(s[i]&0x3F)
			i++
		}
		if c < 0x80 || c&0xFFFFF800 == 0xD800 || c&0xFFFFFFFE == 0xFFFE {
			c = 0xFFFD
		}
	}
	return c, i
}

// fts5AppendUTF8 is FTS5_WRITE_UTF8.
func fts5AppendUTF8(out []byte, c uint32) []byte {
	switch {
	case c < 0x80:
		return append(out, byte(c))
	case c < 0x800:
		return append(out, 0xC0+byte(c>>6&0x1F), 0x80+byte(c&0x3F))
	case c < 0x10000:
		return append(out, 0xE0+byte(c>>12&0x0F), 0x80+byte(c>>6&0x3F), 0x80+byte(c&0x3F))
	default:
		return append(out, 0xF0+byte(c>>18&0x07), 0x80+byte(c>>12&0x3F),
			0x80+byte(c>>6&0x3F), 0x80+byte(c&0x3F))
	}
}

// fts5Category is sqlite3Fts5UnicodeCategory.
func fts5Category(c uint32) int {
	if c >= 1<<20 {
		return 0
	}
	lo := int(fts5UnicodeBlock[c>>16])
	hi := int(fts5UnicodeBlock[1+c>>16])
	key := uint16(c & 0xFFFF)
	res := -1
	for hi > lo {
		t := (hi + lo) / 2
		if key >= fts5UnicodeMap[t] {
			res = t
			lo = t + 1
		} else {
			hi = t
		}
	}
	if res < 0 {
		return 0
	}
	if int(key) >= int(fts5UnicodeMap[res])+int(fts5UnicodeData[res]>>5) {
		return 0
	}
	ret := int(fts5UnicodeData[res] & 0x1F)
	if ret != 30 {
		return ret
	}
	if (key-fts5UnicodeMap[res])&0x01 != 0 {
		return 5
	}
	return 9
}

// fts5IsDiacritic is sqlite3Fts5UnicodeIsdiacritic.
func fts5IsDiacritic(c uint32) bool {
	const mask0, mask1 = 0x08029FDF, 0x000361F8
	if c < 768 || c > 817 {
		return false
	}
	if c < 768+32 {
		return mask0&(uint32(1)<<(c-768)) != 0
	}
	return mask1&(uint32(1)<<(c-768-32)) != 0
}

// fts5Fold is sqlite3Fts5UnicodeFold with remove_diacritics 2.
func fts5Fold(c uint32) uint32 {
	switch {
	case c < 128:
		if c >= 'A' && c <= 'Z' {
			return c + 'a' - 'A'
		}
		return c
	case c < 65536:
		lo, hi, res := 0, len(fts5FoldTable)-1, -1
		for hi >= lo {
			t := (hi + lo) / 2
			if int(c)-int(fts5FoldTable[t].code) >= 0 {
				res = t
				lo = t + 1
			} else {
				hi = t - 1
			}
		}
		ret := c
		p := fts5FoldTable[res]
		if c < uint32(p.code)+uint32(p.nRange) && 0x01&uint32(p.flags)&(uint32(p.code)^c) == 0 {
			ret = (c + uint32(fts5FoldOff[p.flags>>1])) & 0xFFFF
		}
		return fts5RemoveDiacritic(ret)
	case c >= 66560 && c < 66600:
		return c + 40
	default:
		return c
	}
}

// fts5RemoveDiacritic is fts5_remove_diacritic in complex mode: the ASCII
// letter a lowercase letter with diacritics is based on, 0 for a combining
// diacritic, else c.
func fts5RemoveDiacritic(c uint32) uint32 {
	key := c<<3 | 0x07
	res, lo, hi := 0, 0, len(fts5Dia)-1
	for hi >= lo {
		t := (hi + lo) / 2
		if key >= uint32(fts5Dia[t]) {
			res = t
			lo = t + 1
		} else {
			hi = t - 1
		}
	}
	if c > uint32(fts5Dia[res]>>3)+uint32(fts5Dia[res]&0x07) {
		return c
	}
	return uint32(fts5DiaChar[res] & 0x7F)
}
