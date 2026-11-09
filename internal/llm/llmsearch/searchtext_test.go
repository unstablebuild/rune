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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"harakat", "الْمَكْتَبَةَ", "المكتبة"},
		{"alef forms", "إلى الأردن", "الى الاردن"},
		{"tatweel", "مـــرحبا", "مرحبا"},
		{"lam-alef ligature", "\uFEFB", "\u0644\u0627"},
		{"fi ligature", "\uFB01le", "file"},
		{"zero-width joiner", "র\u200D্যাব", "র্যাব"},
		{"bengali o", "\u09C7\u09BE", "\u09CB"},
		{"zero-width space breaks words", "a\u200Bb", "a b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Normalize(tt.in))
		})
	}
}

func TestTermsAndPatterns(t *testing.T) {
	tests := []struct {
		query      string
		prefixLast bool
		terms      []string
		prefix     []bool
	}{
		{"কুমির", false, []string{"কুমির"}, []bool{true}},
		{"مدرسة الأطفال", false, []string{"مدرسة", "الاطفال"}, []bool{true, true}},
		{"মা", false, []string{"মা"}, []bool{true}},
		{"payment error", false, []string{"payment", "error"}, []bool{false, false}},
		{"payment err", true, []string{"payment", "err"}, []bool{false, true}},
		{"Find the photo of a Cat, the CAT!", false, []string{"cat"}, []bool{false}},
		{"x y", false, nil, nil},
		{"quantize_row_q8_0_ref", false, []string{"quantize", "row", "q8", "ref"}, []bool{false, false, false, false}},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			terms := Terms(tt.query)
			assert.Equal(t, tt.terms, terms)
			var prefix []bool
			for _, p := range Patterns(terms, tt.prefixLast) {
				prefix = append(prefix, p.prefix)
			}
			assert.Equal(t, tt.prefix, prefix)
		})
	}
}

func TestIsLookup(t *testing.T) {
	tests := []struct {
		terms []string
		want  bool
	}{
		{[]string{"k7q2lm"}, true},
		{[]string{"trx50", "4815162342"}, true},
		{[]string{"2025"}, true},
		{[]string{"42"}, false},
		{[]string{"retry", "k7q2lm"}, false},
		{nil, false},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.terms, " "), func(t *testing.T) {
			assert.Equal(t, tt.want, IsLookup(tt.terms))
		})
	}
}

func TestHasWords(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"retry a failed call", true},
		{"4815162342, 2025, 0042, 1,000,000", false},
		{"id: 1234567890", false},
		{"order id 12345", true},
		{"   ", false},
		{"মাসিক ২৫,০০০ টাকা", true},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			assert.Equal(t, tt.want, HasWords(tt.text))
		})
	}
}

func TestMatches(t *testing.T) {
	tests := []struct {
		name, word, text string
		want             []string
	}{
		{"hamza is kept as written", "الاردن", "زرنا مدينة البتراء الأثرية في جنوب الأردن.", []string{"الأردن"}},
		{"harakat are kept as written", "المكتبة", "إِنَّ الْمَكْتَبَةَ تُغْلَقُ", []string{"الْمَكْتَبَةَ"}},
		{"an inflecting word marks the whole word", "কুমির", "একটি বড় কুমিরের ছবি", []string{"কুমিরের"}},
		{"whole words only", "day", "Sunday trip: day two", []string{"day"}},
		{"after characters that fold longer", "kelime", strings.Repeat("İ", 60) + " son kelime", []string{"kelime"}},
		{"case and accents are ignored", "cafe", "Le Café, the CAFE", []string{"Café", "CAFE"}},
		{"not found", "zzz", "abc", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chars := characters(tt.text)
			var got []string
			for _, r := range matches(tt.word, chars) {
				got = append(got, strings.Join(chars[r.start:r.end], ""))
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSnippetQuotesTheTextAsWritten(t *testing.T) {
	text := "قَالَ مُحَمَّدٌ إِنَّ الْمَكْتَبَةَ الْعَامَّةَ تُغْلَقُ فِي السَّاعَةِ الثَّامِنَةِ مَسَاءً يَوْمَ الْخَمِيسِ"
	got, ok := Snippet(text, []string{"المكتبة"}, 12)
	assert.True(t, ok)
	assert.Contains(t, got, "«الْمَكْتَبَةَ»")

	_, ok = Snippet(text, []string{"كلمة"}, 12)
	assert.False(t, ok)

	long := "one two three four five six seven eight nine ten match eleven twelve thirteen fourteen fifteen " +
		"sixteen seventeen eighteen nineteen twenty"
	got, ok = Snippet(long, []string{"match"}, 12)
	assert.True(t, ok)
	assert.Equal(t, "…eight nine ten «match» eleven twelve thirteen fourteen fifteen sixteen seventeen …", got)
}
