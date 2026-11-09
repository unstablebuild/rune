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

package shader_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestDecrypt_Reveal(t *testing.T) {
	testReveal(t, textStays, func(from shader.Snapshot, defaultAttr term.Attributes) shader.ImageShader {
		params := shader.DefaultDecryptParams()
		params.From = from
		return shader.Decrypt(params, defaultAttr)
	})
}

func TestDecrypt_Suite(t *testing.T) {
	shadertest.TestShader(t, shader.Decrypt(shader.DefaultDecryptParams(), term.Attributes{}))
}

func TestDecrypt_EdgeCases(t *testing.T) {
	testShaderEdgeCases(t, func(defaultAttr term.Attributes) shader.Shader {
		return shader.Decrypt(shader.DefaultDecryptParams(), defaultAttr)
	})
}

func TestDecrypt_TypesCiphertextThenDecrypts(t *testing.T) {
	params := shader.DefaultDecryptParams()
	params.CipherSymbols = []rune{'#'}
	params.TypingSymbols = []rune{'█'}
	sh := shader.Decrypt(params, term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	const total = 200
	in := makeCharCells(10, 10)

	typing := cloneCells(in)
	sh.Shade(int(params.TypingDuration*total)/2, total, typing)
	assert.NotZero(t, countChar(typing, '#'), "typed characters show as ciphertext")
	assert.NotZero(t, countChar(typing, ' '), "characters still to type are hidden")
	assert.Zero(t, countChar(typing, 'A'), "nothing is decrypted while typing")

	decrypting := cloneCells(in)
	sh.Shade(int((params.TypingDuration+params.FastDuration)*total)+10, total, decrypting)
	assert.Equal(t, 100, countChar(decrypting, '#')+countChar(decrypting, 'A'),
		"every character shows while decrypting")

	assertLastFrameMatchesInput(t, sh, in, total)
	assertFramesAfterTotalLeaveInput(t, sh, in, total)
}

func TestDecrypt_BackgroundTravels(t *testing.T) {
	sh := shader.Decrypt(shader.DefaultDecryptParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	assertBackgroundTravels(t, sh, 200, true)
}

func TestDecrypt_Params(t *testing.T) {
	type P = shader.DecryptParams
	d := shader.DefaultDecryptParams()
	tcases := []paramsCase[P]{
		accepted("defaults", d),
		{name: "zero values select the defaults", params: P{}, sameAs: same(d)},
		{name: "phases not fitting", params: with(d, func(p *P) { p.TypingDuration, p.FastDuration, p.DiscoverDuration = 0.4, 0.4, 0.2 })},
		{name: "tiny symbol period is capped", params: with(d, func(p *P) { p.SymbolPeriod = 1e-12 }), sameAs: same(with(d, func(p *P) { p.SymbolPeriod = 1e-6 }))},
	}
	tcases = append(tcases, invalidFloats(d, "TypingDuration", func(p *P, v float64) { p.TypingDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "FastDuration", func(p *P, v float64) { p.FastDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "DiscoverDuration", func(p *P, v float64) { p.DiscoverDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "SymbolPeriod", func(p *P, v float64) { p.SymbolPeriod = v }, -0.1, 1.1)...)
	tcases = append(tcases, invalidSymbols(d, "CipherSymbols", func(p *P, v []rune) { p.CipherSymbols = v })...)
	tcases = append(tcases, invalidSymbols(d, "TypingSymbols", func(p *P, v []rune) { p.TypingSymbols = v })...)
	testParams(t, shader.Decrypt, tcases)
}

func FuzzDecrypt(f *testing.F) {
	d := shader.DefaultDecryptParams()
	fuzzSeed(
		f,
		fuzzColorSeed(d.CiphertextColors), string(d.CipherSymbols),
		string(d.TypingSymbols), uint32(d.DiscoverColor), d.TypingDuration,
		d.FastDuration, d.DiscoverDuration, d.SymbolPeriod, d.Seed,
	)
	fuzzSeed(
		f,
		[]byte(nil), "", "", uint32(0), 0.0, 0.0, 0.0, 0.0, int64(0),
	)
	f.Fuzz(func(
		t *testing.T, data []byte, frame, total int, attrs uint64,
		ciphertextColors []byte,
		cipherSymbols, typingSymbols string,
		discoverColor uint32,
		typingDuration, fastDuration, discoverDuration, symbolPeriod float64,
		seed int64,
	) {
		sh, ok := fuzzBuild(t, func() shader.Shader {
			return shader.Decrypt(shader.DecryptParams{
				CiphertextColors: fuzzColors(ciphertextColors),
				CipherSymbols:    fuzzRunes(cipherSymbols),
				TypingSymbols:    fuzzRunes(typingSymbols),
				DiscoverColor:    term.Color(discoverColor),
				TypingDuration:   typingDuration,
				FastDuration:     fastDuration,
				DiscoverDuration: discoverDuration,
				SymbolPeriod:     symbolPeriod,
				Seed:             seed,
			}, fuzzAttrs(attrs))
		})
		if ok {
			fuzzShade(t, sh, data, frame, total)
		}
	})
}
