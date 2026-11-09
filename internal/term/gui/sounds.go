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
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ebitengine/oto/v3"
	"github.com/hajimehoshi/ebiten/v2/audio/mp3"
	"github.com/hajimehoshi/ebiten/v2/audio/vorbis"
	"github.com/hajimehoshi/ebiten/v2/audio/wav"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/sounds"
)

const (
	soundSampleRate = 48_000
	// soundFrameBytes is the size of one stereo signed 16-bit frame.
	soundFrameBytes = 4
	// maxSoundBytes caps the PCM decoded from a configured sound file so
	// a long track cannot hold a large buffer or play for minutes.
	maxSoundBytes = 10 * soundSampleRate * soundFrameBytes
	// soundQueueSize bounds the sounds waiting to start. Play drops a
	// sound rather than block when the queue is full.
	soundQueueSize = 16
	// toneAmplitude is the peak of a built-in tone relative to full scale.
	toneAmplitude = 0.35
	// volumeStep is how much IncreaseVolume and DecreaseVolume change
	// the volume.
	volumeStep = 0.1
)

// Sounds plays resolved sounds through the system's default audio
// output. The output device is opened on the first audible sound; if
// that fails, the output stays silent for the rest of the process.
type Sounds struct {
	queue  chan sounds.Sound
	start  sync.Once
	volume atomic.Uint64
	muted  atomic.Bool
}

var _ sounds.Backend = (*Sounds)(nil)

// NewSounds returns an unmuted Sounds at full volume.
func NewSounds() *Sounds {
	o := &Sounds{queue: make(chan sounds.Sound, soundQueueSize)}
	o.volume.Store(math.Float64bits(1))
	return o
}

// SetVolume sets the volume of sounds started afterwards, from 0 (mute)
// to 1 (full volume). Values outside that range are clamped. It is safe
// for concurrent use.
func (o *Sounds) SetVolume(volume float64) {
	switch {
	case !(volume > 0):
		volume = 0
	case volume > 1:
		volume = 1
	}
	o.volume.Store(math.Float64bits(volume))
}

// Volume returns the volume set by SetVolume. It is unaffected by
// SetMuted.
func (o *Sounds) Volume() float64 {
	return math.Float64frombits(o.volume.Load())
}

// IncreaseVolume raises the volume by one step, up to 1, and unmutes
// the output.
func (o *Sounds) IncreaseVolume() {
	o.stepVolume(volumeStep)
}

// DecreaseVolume lowers the volume by one step, down to 0, and unmutes
// the output.
func (o *Sounds) DecreaseVolume() {
	o.stepVolume(-volumeStep)
}

func (o *Sounds) stepVolume(delta float64) {
	o.SetMuted(false)
	// Snap to the step grid so repeated steps land exactly on 0 and 1.
	o.SetVolume(math.Round((o.Volume()+delta)/volumeStep) * volumeStep)
}

// SetMuted silences every sound while muted is true, keeping the
// volume to restore when it is set back to false. It is safe for
// concurrent use.
func (o *Sounds) SetMuted(muted bool) {
	o.muted.Store(muted)
}

// Muted reports whether the output is muted by SetMuted.
func (o *Sounds) Muted() bool {
	return o.muted.Load()
}

func (o *Sounds) audibleVolume() float64 {
	if o.Muted() {
		return 0
	}
	return o.Volume()
}

// Play starts s unless the output is muted, its volume is 0, or the same
// sound is still playing. A file is decoded when it is first played and
// again after it changes; if it cannot be played, the built-in tone of
// s.BuiltIn plays instead. Play does not block.
func (o *Sounds) Play(s sounds.Sound) {
	if o.audibleVolume() == 0 {
		return
	}
	o.start.Do(func() {
		go debug.CapturePanicReport(o.run)
	})
	select {
	case o.queue <- s:
	default:
	}
}

func (o *Sounds) run() {
	var (
		ctx     *oto.Context
		failed  bool
		cache   = make(soundCache)
		playing = make(map[sounds.Sound]*oto.Player)
	)
	for s := range o.queue {
		for playingSound, player := range playing {
			if !player.IsPlaying() {
				delete(playing, playingSound)
			}
		}
		volume := o.audibleVolume()
		if failed || volume == 0 || playing[s] != nil {
			continue
		}
		pcm := cache.pcm(s)
		if len(pcm) == 0 {
			continue
		}
		if ctx == nil {
			var err error
			if ctx, err = newSoundContext(); err != nil {
				failed = true
				soundLog().WithError(err).Warn("audio output unavailable; sounds are disabled")
				continue
			}
		}
		player := ctx.NewPlayer(bytes.NewReader(pcm))
		player.SetVolume(volume)
		player.Play()
		playing[s] = player
	}
}

func newSoundContext() (*oto.Context, error) {
	ctx, ready, err := oto.NewContext(&oto.NewContextOptions{
		SampleRate:   soundSampleRate,
		ChannelCount: 2,
		Format:       oto.FormatSignedInt16LE,
	})
	if err != nil {
		return nil, err
	}
	<-ready
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return ctx, nil
}

// soundCache holds the decoded PCM of sound files by path.
type soundCache map[string]cachedSound

type cachedSound struct {
	// modTime and size identify the version of the file pcm was decoded
	// from; size is -1 when the file could not be read.
	modTime time.Time
	size    int64
	// pcm is nil when the file could not be played.
	pcm []byte
}

// pcm returns the stereo signed 16-bit PCM of s at soundSampleRate,
// falling back to the built-in tone of s.BuiltIn when s.Path is empty or
// cannot be played. It returns nil when there is nothing to play.
func (c soundCache) pcm(s sounds.Sound) []byte {
	if s.Path != "" {
		if pcm := c.file(s.Path); pcm != nil {
			return pcm
		}
	}
	return defaultSounds()[s.BuiltIn]
}

// file decodes the file at path, or returns the PCM decoded before if the
// file has not changed since. A file that cannot be played is reported
// once per version of it.
func (c soundCache) file(path string) []byte {
	version := cachedSound{size: -1}
	info, statErr := os.Stat(path)
	if statErr == nil {
		version = cachedSound{modTime: info.ModTime(), size: info.Size()}
	}
	if cached, ok := c[path]; ok &&
		cached.modTime.Equal(version.modTime) && cached.size == version.size {
		return cached.pcm
	}
	err := statErr
	if err == nil {
		version.pcm, err = decodeSoundFile(path)
	}
	if err != nil {
		soundLog().WithError(err).Warn("cannot play sound file")
	}
	c[path] = version
	return version.pcm
}

func decodeSoundFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var stream io.Reader
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".wav":
		stream, err = wav.DecodeWithSampleRate(soundSampleRate, f)
	case ".mp3":
		stream, err = mp3.DecodeWithSampleRate(soundSampleRate, f)
	case ".ogg", ".oga":
		stream, err = vorbis.DecodeWithSampleRate(soundSampleRate, f)
	default:
		return nil, fmt.Errorf("%s: unsupported format %q", path, ext)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	pcm, err := io.ReadAll(io.LimitReader(stream, maxSoundBytes))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	pcm = pcm[:len(pcm)-len(pcm)%soundFrameBytes]
	if len(pcm) == 0 {
		return nil, fmt.Errorf("%s: no audio", path)
	}
	return pcm, nil
}

func soundLog() *log.Entry {
	return log.WithFields(log.Fields{logging.KeyClass: "gui.sounds"})
}

// note is one sine note of a built-in tone. Times are in seconds.
type note struct {
	freq  float64
	start float64
	dur   float64
	// decay is the time for the note to fall to 1/e of its peak.
	decay float64
}

var defaultTones = map[string][]note{
	sounds.Error: {
		{freq: 293.66, dur: 0.06, decay: 0.04},
		{freq: 220.00, start: 0.06, dur: 0.09, decay: 0.04},
	},
	sounds.Warning: {{freq: 1318.51, dur: 0.4, decay: 0.12}},
	sounds.Info: {
		{freq: 783.99, dur: 0.12, decay: 0.08},
		{freq: 1046.50, start: 0.11, dur: 0.3, decay: 0.1},
	},
	sounds.Success: {
		{freq: 523.25, dur: 0.12, decay: 0.08},
		{freq: 659.25, start: 0.09, dur: 0.12, decay: 0.08},
		{freq: 783.99, start: 0.18, dur: 0.3, decay: 0.12},
	},
}

var defaultSounds = sync.OnceValue(func() map[string][]byte {
	ret := make(map[string][]byte, len(defaultTones))
	for s, notes := range defaultTones {
		ret[s] = synthesize(notes)
	}
	return ret
})

// synthesize renders notes as stereo signed 16-bit PCM at soundSampleRate
// with a peak of toneAmplitude.
func synthesize(notes []note) []byte {
	var end float64
	for _, n := range notes {
		end = max(end, n.start+n.dur)
	}
	samples := make([]float64, int(math.Ceil(end*soundSampleRate)))
	for _, n := range notes {
		first := int(n.start * soundSampleRate)
		count := int(n.dur * soundSampleRate)
		for i := 0; i < count && first+i < len(samples); i++ {
			t := float64(i) / soundSampleRate
			phase := 2 * math.Pi * n.freq * t
			// A quiet octave overtone keeps pure sines from sounding thin.
			wave := math.Sin(phase) + 0.2*math.Sin(2*phase)
			samples[first+i] += envelope(t, n.dur, n.decay) * wave
		}
	}

	var peak float64
	for _, v := range samples {
		peak = max(peak, math.Abs(v))
	}
	scale := toneAmplitude * math.MaxInt16 / peak
	pcm := make([]byte, len(samples)*soundFrameBytes)
	for i, v := range samples {
		sample := uint16(int16(math.Round(v * scale)))
		binary.LittleEndian.PutUint16(pcm[i*soundFrameBytes:], sample)
		binary.LittleEndian.PutUint16(pcm[i*soundFrameBytes+2:], sample)
	}
	return pcm
}

// envelope shapes a note of length dur at time t: a short attack and a
// final release reach zero at both ends so the note does not click.
func envelope(t, dur, decay float64) float64 {
	const attack = 0.004
	release := min(0.02, dur/4)
	return min(t/attack, 1) * min((dur-t)/release, 1) * math.Exp(-t/decay)
}
