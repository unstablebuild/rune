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

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Normalize returns text the way keyword search keeps and matches it, so
// that a word is found however it was typed: NFKC (presentation forms and
// ligatures become plain letters, full-width becomes ASCII), invisible
// formatting characters dropped, a zero-width space made a space, and
// Arabic as people type it (no tatweel, no short-vowel marks, one alef).
func Normalize(text string) string {
	compatible := norm.NFKC.String(text)
	var b strings.Builder
	b.Grow(len(compatible))
	for _, r := range compatible {
		switch {
		case r == 0x200B:
			b.WriteByte(' ')
		case r == 0x00AD, r == 0x061C, r >= 0x200C && r <= 0x200F, r >= 0x202A && r <= 0x202E,
			r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x2069, r == 0xFEFF:
		case r == 0x0640, r >= 0x064B && r <= 0x065F, r == 0x0670:
		case r == 0x0622, r == 0x0623, r == 0x0625, r == 0x0671:
			b.WriteRune(0x0627)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Inflects reports whether word is in a script that attaches endings to
// words (Indic scripts, Arabic): keyword search matches such words as
// prefixes, so "কুমির" finds "কুমিরও".
func Inflects(word string) bool {
	for _, r := range word {
		if (r >= 0x0900 && r <= 0x0DFF) || (r >= 0x0600 && r <= 0x06FF) {
			return true
		}
	}
	return false
}

// HasWords reports whether text says something in words: letters (marks
// included) are at least a fifth of what is not space. A table of numbers
// gets keyword search only.
func HasWords(text string) bool {
	letters, others := 0, 0
	for _, r := range text {
		switch {
		case unicode.IsSpace(r):
		case unicode.IsLetter(r) || unicode.IsMark(r):
			letters++
		default:
			others++
		}
	}
	return letters > 0 && letters*4 >= others
}

// isAlphanumeric is Foundation's CharacterSet.alphanumerics: L*, M* and N*.
func isAlphanumeric(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r)
}

var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range []string{
		"a", "an", "the", "of", "in", "on", "at", "to", "for", "with", "and", "or", "my", "me", "that", "this",
		"these", "those", "from", "about", "where", "what", "which", "who", "when", "is", "was", "are", "were", "be",
		"it", "its", "some", "any", "find", "show", "file", "files", "photo", "photos", "picture", "image", "images",
		"screenshot", "screenshots", "video", "videos", "pdf", "document", "documents", "recording",
		"এবং", "ও", "এই", "সেই", "একটি", "আমার", "থেকে", "জন্য", "সাথে", "মধ্যে", "কোথায়", "কোন", "কী", "কি", "যে",
		"ছবি", "স্ক্রিনশট", "ভিডিও", "ফাইল", "পিডিএফ",
		"في", "من", "على", "الى", "عن", "مع", "هذا", "هذه", "ذلك", "التي", "الذي", "اين", "ما",
		"صورة", "صور", "لقطة", "شاشة", "فيديو", "ملف", "مستند",
	} {
		m[w] = true
	}
	return m
}()

// Terms returns the content words of a query, normalized as keyword search
// keeps its text: words of two characters or more (a Bengali letter with its
// vowel sign is two), stopwords dropped, each once, in query order.
func Terms(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(Normalize(text)), func(r rune) bool { return !isAlphanumeric(r) }) {
		if utf8.RuneCountInString(w) < 2 || stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// IsLookup reports whether terms are codes or IDs ("K7Q2LM", "TRX50") or
// long numbers: every term has a digit and a letter, or four characters or
// more. Such a query weighs words double and meaning half.
func IsLookup(terms []string) bool {
	if len(terms) == 0 {
		return false
	}
	for _, t := range terms {
		if !strings.ContainsFunc(t, unicode.IsNumber) ||
			!(strings.ContainsFunc(t, unicode.IsLetter) || utf8.RuneCountInString(t) >= 4) {
			return false
		}
	}
	return true
}

// Patterns returns one phrase per term: a prefix for the last term while it
// is being typed (prefixLast) and for words of scripts that attach endings.
func Patterns(terms []string, prefixLast bool) []phrase {
	out := make([]phrase, len(terms))
	for i, t := range terms {
		out[i] = newPhrase(t, (prefixLast && i == len(terms)-1) || Inflects(t))
	}
	return out
}

// matchFold folds text for matching as Foundation's case- and
// diacritic-insensitive search does: lower case, Latin combining marks
// dropped. Indic vowel signs are not diacritics and stay.
func matchFold(text string) string {
	decomposed := norm.NFD.String(strings.ToLower(text))
	var b strings.Builder
	for _, r := range decomposed {
		if (r >= 0x0300 && r <= 0x036F) || (r >= 0x1AB0 && r <= 0x1AFF) || (r >= 0x1DC0 && r <= 0x1DFF) ||
			(r >= 0x20D0 && r <= 0x20FF) || (r >= 0xFE20 && r <= 0xFE2F) {
			continue
		}
		b.WriteRune(r)
	}
	return norm.NFC.String(b.String())
}

// characters splits text into user-perceived characters, approximated as a
// rune and the marks and joiners that follow it.
func characters(text string) []string {
	var out []string
	for i := 0; i < len(text); {
		_, n := utf8.DecodeRuneInString(text[i:])
		j := i + n
		for j < len(text) {
			r, m := utf8.DecodeRuneInString(text[j:])
			if !unicode.IsMark(r) && r != 0x200C && r != 0x200D && !unicode.Is(unicode.Variation_Selector, r) {
				break
			}
			j += m
		}
		out = append(out, text[i:j])
		i = j
	}
	return out
}

func isWordCharacter(c string) bool { return strings.ContainsFunc(c, isAlphanumeric) }

func isWhitespaceCharacter(c string) bool {
	r, _ := utf8.DecodeRuneInString(c)
	return unicode.IsSpace(r)
}

// charRange is a range of characters, [start, end).
type charRange struct{ start, end int }

// matches returns where word is in chars as keyword search would find it
// (normalized, ignoring case and accents), as ranges of the characters as
// written: whole words, and for a word of a script that attaches endings,
// the whole word it starts.
func matches(word string, chars []string) []charRange {
	needle := matchFold(Normalize(word))
	if needle == "" {
		return nil
	}
	var folded strings.Builder
	var origin []int
	for i, c := range chars {
		piece := matchFold(Normalize(c))
		folded.WriteString(piece)
		for range len(piece) {
			origin = append(origin, i)
		}
	}
	haystack := folded.String()
	prefix := Inflects(word)
	var found []charRange
	for from := 0; len(found) < 500; {
		at := strings.Index(haystack[from:], needle)
		if at < 0 {
			break
		}
		lo, hi := from+at, from+at+len(needle)
		from = hi
		r := charRange{origin[lo], origin[hi-1] + 1}
		if r.start > 0 && isWordCharacter(chars[r.start-1]) {
			continue
		}
		if r.end < len(chars) && isWordCharacter(chars[r.end]) {
			if !prefix {
				continue
			}
			for r.end < len(chars) && isWordCharacter(chars[r.end]) {
				r.end++
			}
		}
		if n := len(found); n > 0 && found[n-1].end > r.start {
			continue
		}
		found = append(found, r)
	}
	return found
}

// Snippet returns about words words of text around the first of terms it
// has, terms marked « » as SQLite's snippet() marks them, quoting the text
// as written. ok is false when none of the terms is in text.
func Snippet(text string, terms []string, words int) (_ string, ok bool) {
	chars := characters(text)
	var marks []charRange
	for _, t := range terms {
		marks = append(marks, matches(t, chars)...)
	}
	if len(marks) == 0 {
		return "", false
	}
	slices.SortStableFunc(marks, func(a, b charRange) int { return a.start - b.start })
	first := marks[0]

	start, before := first.start, 0
	for start > 0 && before < words/3 {
		start--
		if isWhitespaceCharacter(chars[start]) && start < first.start && !isWhitespaceCharacter(chars[start+1]) {
			before++
		}
	}
	if start > 0 || isWhitespaceCharacter(chars[start]) {
		start++
	}
	end, after := first.end, 0
	for end < len(chars) && after < words-before {
		if isWhitespaceCharacter(chars[end]) {
			after++
		}
		end++
	}

	var out strings.Builder
	if start > 0 {
		out.WriteString("…")
	}
	cursor := start
	for _, m := range marks {
		if m.start < start || m.end > end || m.start < cursor {
			continue
		}
		out.WriteString(strings.Join(chars[cursor:m.start], ""))
		out.WriteString("«")
		out.WriteString(strings.Join(chars[m.start:m.end], ""))
		out.WriteString("»")
		cursor = m.end
	}
	out.WriteString(strings.Join(chars[cursor:end], ""))
	if end < len(chars) {
		out.WriteString("…")
	}
	return strings.Join(strings.Fields(out.String()), " "), true
}
