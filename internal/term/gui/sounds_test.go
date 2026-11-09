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

package gui

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/sounds"
)

func TestDefaultSounds(t *testing.T) {
	t.Parallel()
	const nearZero = math.MaxInt16 / 100
	for _, name := range []string{sounds.Error, sounds.Warning, sounds.Info, sounds.Success} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pcm := defaultSounds()[name]
			require.NotEmpty(t, pcm)
			require.Zero(t, len(pcm)%soundFrameBytes, "frame aligned")

			sample := func(frame int) int16 {
				left := int16(binary.LittleEndian.Uint16(pcm[frame*soundFrameBytes:]))
				right := int16(binary.LittleEndian.Uint16(pcm[frame*soundFrameBytes+2:]))
				require.Equal(t, left, right, "frame %d", frame)
				return left
			}
			frames := len(pcm) / soundFrameBytes
			var peak int
			for i := range frames {
				peak = max(peak, abs(int(sample(i))))
			}
			assert.Less(t, peak, math.MaxInt16, "below full scale")
			assert.Greater(t, peak, math.MaxInt16/10, "audible")
			assert.LessOrEqual(t, abs(int(sample(0))), nearZero, "starts near zero")
			assert.LessOrEqual(t, abs(int(sample(frames-1))), nearZero, "ends near zero")
		})
	}
}

func TestDefaultErrorSoundIsShort(t *testing.T) {
	t.Parallel()
	// Refused keystrokes play it, so it must not outlast typing.
	const maxSeconds = 0.2
	frames := len(defaultSounds()[sounds.Error]) / soundFrameBytes
	assert.LessOrEqual(t, float64(frames)/soundSampleRate, maxSeconds)
}

func TestSoundCachePCM(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	want := make([]byte, 64*soundFrameBytes)
	for i := range want {
		want[i] = byte(i)
	}
	wavPath := filepath.Join(dir, "ding.wav")
	require.NoError(t, os.WriteFile(wavPath, wavFile(want), 0o600))
	textPath := filepath.Join(dir, "ding.txt")
	require.NoError(t, os.WriteFile(textPath, []byte("not audio"), 0o600))
	corruptPath := filepath.Join(dir, "corrupt.wav")
	require.NoError(t, os.WriteFile(corruptPath, []byte("not audio"), 0o600))
	missingPath := filepath.Join(dir, "missing.wav")

	warning := defaultSounds()[sounds.Warning]
	tests := []struct {
		desc  string
		sound sounds.Sound
		want  []byte
	}{
		{desc: "built-in tone", sound: sounds.Sound{BuiltIn: sounds.Warning}, want: warning},
		{desc: "wav file", sound: sounds.Sound{Path: wavPath, BuiltIn: sounds.Warning}, want: want},
		{desc: "wav file without a built-in", sound: sounds.Sound{Path: wavPath}, want: want},
		{desc: "missing file falls back",
			sound: sounds.Sound{Path: missingPath, BuiltIn: sounds.Warning}, want: warning},
		{desc: "unsupported format falls back",
			sound: sounds.Sound{Path: textPath, BuiltIn: sounds.Warning}, want: warning},
		{desc: "corrupt file falls back",
			sound: sounds.Sound{Path: corruptPath, BuiltIn: sounds.Warning}, want: warning},
		{desc: "missing file without a built-in is silent", sound: sounds.Sound{Path: missingPath}},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, test.want, make(soundCache).pcm(test.sound))
		})
	}
}

func TestSoundCacheRedecodesChangedFiles(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "ding.wav")
	first := make([]byte, 16*soundFrameBytes)
	for i := range first {
		first[i] = byte(i + 1)
	}
	require.NoError(t, os.WriteFile(path, wavFile(first), 0o600))

	cache := make(soundCache)
	sound := sounds.Sound{Path: path}
	assert.Equal(t, first, cache.pcm(sound))

	second := make([]byte, 32*soundFrameBytes)
	for i := range second {
		second[i] = byte(i + 2)
	}
	require.NoError(t, os.WriteFile(path, wavFile(second), 0o600))
	assert.Equal(t, second, cache.pcm(sound), "a rewritten file is decoded again")

	require.NoError(t, os.Remove(path))
	assert.Nil(t, cache.pcm(sound), "a removed file is not played from the cache")
}

func TestSoundOutputSetVolume(t *testing.T) {
	t.Parallel()
	tests := []struct {
		volume float64
		want   float64
	}{
		{volume: 0, want: 0},
		{volume: 0.25, want: 0.25},
		{volume: 1, want: 1},
		{volume: -1, want: 0},
		{volume: 2, want: 1},
		{volume: math.NaN(), want: 0},
		{volume: math.Inf(1), want: 1},
	}
	p := NewSounds()
	assert.Equal(t, 1.0, p.Volume())
	for _, test := range tests {
		p.SetVolume(test.volume)
		assert.Equal(t, test.want, p.Volume(), "SetVolume(%v)", test.volume)
	}
}

func TestSoundOutputStepVolume(t *testing.T) {
	t.Parallel()
	p := NewSounds()
	p.IncreaseVolume()
	assert.Equal(t, 1.0, p.Volume(), "clamped at full volume")

	for range 3 {
		p.DecreaseVolume()
	}
	assert.InDelta(t, 0.7, p.Volume(), 1e-9)

	p.SetVolume(0.34)
	p.IncreaseVolume()
	assert.InDelta(t, 0.4, p.Volume(), 1e-9, "snapped to the step grid")

	for range 20 {
		p.DecreaseVolume()
	}
	assert.Equal(t, 0.0, p.Volume(), "clamped at silence")
}

func TestSoundOutputMute(t *testing.T) {
	t.Parallel()
	p := NewSounds()
	p.SetVolume(0.6)
	p.SetMuted(true)
	assert.True(t, p.Muted())
	assert.Equal(t, 0.6, p.Volume(), "muting keeps the volume")
	p.SetMuted(false)
	assert.False(t, p.Muted())

	for _, step := range []func(){p.IncreaseVolume, p.DecreaseVolume} {
		p.SetMuted(true)
		step()
		assert.False(t, p.Muted(), "changing the volume unmutes")
	}
}

func TestSoundOutputSilentPlayIsNoop(t *testing.T) {
	t.Parallel()
	tests := []struct {
		desc   string
		volume float64
		muted  bool
	}{
		{desc: "zero volume", volume: 0},
		{desc: "muted", volume: 1, muted: true},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			t.Parallel()
			p := NewSounds()
			p.SetVolume(test.volume)
			p.SetMuted(test.muted)
			p.Play(sounds.Sound{Name: sounds.Warning, BuiltIn: sounds.Warning})
			assert.Empty(t, p.queue)
		})
	}
}

// wavFile returns a 48 kHz stereo signed 16-bit WAV file holding pcm.
func wavFile(pcm []byte) []byte {
	const channels, bits = 2, 16
	le := binary.LittleEndian
	ret := make([]byte, 0, 44+len(pcm))
	ret = append(ret, "RIFF"...)
	ret = le.AppendUint32(ret, uint32(36+len(pcm)))
	ret = append(ret, "WAVEfmt "...)
	ret = le.AppendUint32(ret, 16)
	ret = le.AppendUint16(ret, 1) // PCM
	ret = le.AppendUint16(ret, channels)
	ret = le.AppendUint32(ret, soundSampleRate)
	ret = le.AppendUint32(ret, soundSampleRate*channels*bits/8)
	ret = le.AppendUint16(ret, channels*bits/8)
	ret = le.AppendUint16(ret, bits)
	ret = append(ret, "data"...)
	ret = le.AppendUint32(ret, uint32(len(pcm)))
	return append(ret, pcm...)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
