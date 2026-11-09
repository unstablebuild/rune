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

package ide

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/braille"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/glslshader"
	"unstable.build/rune/internal/component/shader/shaderloop"
	"unstable.build/rune/internal/handler/handlertest"
)

// fakeLoadingShader returns a deterministic, cheap shader factory
// usable in tests. We use Shine because it's pure CPU and avoids
// the heavier GL paths.
func fakeLoadingShader() func(term.Attributes) shader.Shader {
	return func(defaultAttr term.Attributes) shader.Shader {
		params := glslshader.DefaultShineParams()
		params.Color = term.NewRGBColor(255, 255, 255)
		return glslshader.Shine(params, defaultAttr)
	}
}

func newTestShaderRunner(loading, open loadingShaderConfig) *shaderRunner {
	r := new(shaderRunner)
	openCfg := openShaderConfig{fps: open.fps, duration: open.duration}
	if open.shader != nil {
		openCfg.shader = func(attr term.Attributes, _ shader.Snapshot) shader.Shader { return open.shader(attr) }
	}
	r.init(handler.Nop(), term.NopInterrupter(), term.Attributes{},
		nopShutdownShaderConfig(), loading, openCfg,
		component.FrameCharSetDefault())
	return r
}

func TestShaderRunnerLoadingNoOpWithoutConfig(t *testing.T) {
	r := newTestShaderRunner(loadingShaderConfig{}, loadingShaderConfig{})
	initial := r.shader

	r.startLoading()
	r.startLoading()
	assert.Same(t, initial, r.shader,
		"startLoading must not swap the shader when no loading config is set")

	r.stopLoading()
	r.stopLoading()
	assert.Same(t, initial, r.shader,
		"stopLoading must not swap the shader when no loading config is set")
}

func TestShaderRunnerStartLoadingRestarts(t *testing.T) {
	loading := loadingShaderConfig{shader: fakeLoadingShader(), fps: 30}
	r := newTestShaderRunner(loading, loadingShaderConfig{})
	initial := r.shader

	r.startLoading()
	first := r.shader
	assert.NotSame(t, initial, first,
		"first startLoading must swap in a loading shader")

	r.startLoading()
	second := r.shader
	assert.NotSame(t, first, second,
		"second startLoading must replace the previous loading shader")
}

func TestShaderRunnerStopLoadingFiresOpenShader(t *testing.T) {
	loading := loadingShaderConfig{shader: fakeLoadingShader(), fps: 30}
	open := loadingShaderConfig{shader: fakeLoadingShader(), fps: 30}
	r := newTestShaderRunner(loading, open)

	r.startLoading()
	loadingComp := r.shader

	r.stopLoading()
	assert.NotSame(t, loadingComp, r.shader,
		"stopLoading must swap in the open shader")
}

func TestShaderRunnerStopLoadingFallsBackToCancelWithoutOpen(t *testing.T) {
	loading := loadingShaderConfig{shader: fakeLoadingShader(), fps: 30}
	r := newTestShaderRunner(loading, loadingShaderConfig{})

	r.startLoading()
	loadingComp := r.shader

	r.stopLoading()
	assert.NotSame(t, loadingComp, r.shader,
		"stopLoading without an open shader must cancel the active one")
}

func TestShaderRunnerStartLoadingUsesConfiguredDuration(t *testing.T) {
	const (
		fps    = 30
		dur    = 4 * time.Second
		expect = int(dur) / (int(time.Second) / fps) // shader.Component.total
	)
	loading := loadingShaderConfig{
		shader:   fakeLoadingShader(),
		fps:      fps,
		duration: dur,
	}
	r := newTestShaderRunner(loading, loadingShaderConfig{})

	r.startLoading()
	assert.Equal(t, expect, r.shader.Total(),
		"startLoading must use the configured duration")
}

func TestShaderRunnerStopLoadingUsesConfiguredOpenDuration(t *testing.T) {
	const (
		fps    = 30
		dur    = 2500 * time.Millisecond
		expect = int(dur) / (int(time.Second) / fps)
	)
	loading := loadingShaderConfig{shader: fakeLoadingShader(), fps: fps}
	open := loadingShaderConfig{
		shader:   fakeLoadingShader(),
		fps:      fps,
		duration: dur,
	}
	r := newTestShaderRunner(loading, open)

	r.startLoading()
	r.stopLoading()
	assert.Equal(t, expect, r.shader.Total(),
		"stopLoading must use the configured open-shader duration")
}

func TestBuildNamedShaderRegistersEveryListedName(t *testing.T) {
	defAttr := term.Attributes{}
	fc := component.FrameCharSetDefault()
	for _, name := range namedShaderNames() {
		sh, ok := buildNamedShader(name, defAttr, fc, shader.Snapshot{}, defaultShaderFPS)
		assert.True(t, ok,
			"buildNamedShader must register %q (listed by namedShaderNames)",
			name)
		assert.NotNil(t, sh,
			"buildNamedShader(%q) must return a non-nil Shader", name)
	}
}

func TestBuildNamedShaderPlaysTheFireAtItsPace(t *testing.T) {
	const fps = defaultShaderFPS
	frame := time.Second / fps
	attr := term.Attributes{Fg: term.ColorWhite}
	fc := component.FrameCharSetDefault()
	shade := func(s shader.Shader, frame, total int) [][]term.Cell {
		out := makeScreensaverCanvas()
		s.Shade(frame, total, out)
		return out
	}
	for _, tc := range []struct {
		name  string
		cycle time.Duration
	}{
		{"blaze", shaderloop.BlazeCycle},
		{"blazeBlue", shaderloop.BlazeCycle},
		{"inferno", shaderloop.InfernoCycle},
		{"infernoBlue", shaderloop.InfernoCycle},
	} {
		bare, ok := namedShader(tc.name, attr, fc, shader.Snapshot{})
		require.True(t, ok)
		// The fire's cycle a frame at a time, at the pace it was tuned to.
		cycle := make([][][]term.Cell, int(tc.cycle/frame))
		for k := range cycle {
			cycle[k] = shade(bare, k, len(cycle))
		}
		// at returns the frames of the cycle that look like frame of a
		// run, which neighbouring frames may when the fire barely moves.
		at := func(s shader.Shader, frame, total int) []int {
			got := shade(s, frame, total)
			var ks []int
			for k, c := range cycle {
				if assert.ObjectsAreEqual(c, got) {
					ks = append(ks, k)
				}
			}
			return ks
		}
		for _, d := range []time.Duration{time.Second, 10 * time.Second, 5 * time.Minute} {
			t.Run(fmt.Sprintf("%s/%s", tc.name, d), func(t *testing.T) {
				s, ok := buildNamedShader(tc.name, attr, fc, shader.Snapshot{}, fps)
				require.True(t, ok)
				total := int(d / frame)
				// Midway through the run, clear of its fades.
				mid := total / 2
				start := at(s, mid, total)
				require.NotEmpty(t, start, "frame %d of the run is a frame of the fire's cycle", mid)
				for _, step := range []int{1, 2, total / 4} {
					later := at(s, mid+step, total)
					assert.True(t, slices.ContainsFunc(start, func(k int) bool {
						return slices.Contains(later, (k+step)%len(cycle))
					}), "%d frames later the fire is %d frames of its cycle further", step, step)
				}
			})
		}
	}
}

func TestShaderRunnerReplaysTheIntro(t *testing.T) {
	ctx := context.Background()
	r := newTestShaderRunner(loadingShaderConfig{}, loadingShaderConfig{})
	completions := func() []string {
		it, _, err := r.Complete(ctx, textapi.Command{Name: "shaderrun"})
		require.NoError(t, err)
		names, err := iterator.ToSlice(ctx, it)
		require.NoError(t, err)
		return names
	}
	intro := textapi.Command{Name: "shaderrun", Args: []string{"intro", "5s", "60"}}

	initial := r.shader
	assert.Error(t, r.HandleCommand(ctx, intro), "without an intro")
	assert.Same(t, initial, r.shader)
	assert.NotContains(t, completions(), "intro")

	var built []term.Attributes
	r.defAttr = term.Attributes{Fg: term.ColorWhite}
	r.initShaderCfg = initShaderConfig{
		shader: func(attr term.Attributes, _ component.FrameCharSet) shader.Shader {
			built = append(built, attr)
			return shader.Nop()
		},
		fps:      30,
		duration: 2 * time.Second,
	}
	require.NoError(t, r.HandleCommand(ctx, intro))
	assert.Equal(t, []term.Attributes{{Fg: term.ColorWhite}}, built)
	assert.Equal(t, 60, r.shader.Total(), "the intro keeps its own length")
	assert.Equal(t, "intro", completions()[0])
}

func TestNamedShadersShadeThePicturesOnScreenWithTheCells(t *testing.T) {
	const cols, rows, total = 20, 8, 100
	drawsItsPictures := map[string]bool{
		"nop": true, "grayFade": true, "burn": true, "beams": true, "binaryPath": true,
		"decrypt": true, "expand": true, "matrix": true, "middleOut": true,
		"pour": true, "print": true, "rain": true, "slide": true,
	}
	picture := func(c color.RGBA, pos term.Coordinates, width, height int) term.Image {
		src := image.NewRGBA(image.Rect(0, 0, 1, 1))
		src.SetRGBA(0, 0, c)
		return term.Image{Src: src, ID: term.NewImageID(), Pos: pos, Width: width, Height: height}
	}
	pictures := []term.Image{
		picture(color.RGBA{R: 30, G: 160, B: 220, A: 255}, term.Coordinates{X: 4, Y: 2}, 8, 4),
		// A dark wallpaper, some cells of which the fire leaves as they
		// were painted, on some frames and not others.
		picture(color.RGBA{A: 255}, term.Coordinates{}, cols, rows),
	}
	attr := term.Attributes{Fg: term.ColorWhite}
	for _, name := range namedShaderNames() {
		if drawsItsPictures[name] {
			continue
		}
		t.Run(name, func(t *testing.T) {
			sh, ok := buildNamedShader(name, attr, component.FrameCharSetDefault(), shader.Snapshot{}, defaultShaderFPS)
			require.True(t, ok)
			for _, picture := range pictures {
				// Every frame, as the shaders that fade in and out of
				// the screen do so over the first and last of them.
				for frame := range total {
					cells := make([][]term.Cell, rows)
					for y := range cells {
						cells[y] = make([]term.Cell, cols)
						for x := range cells[y] {
							cells[y][x] = term.NewCell('x', 1, attr)
						}
					}
					drawn := term.CloneCells(cells)
					got := shader.ShadeImages(sh, frame, total, cells, []term.Image{picture})
					for _, img := range got {
						r := img.Visible()
						for y := r.Min.Y; y < r.Max.Y; y++ {
							for x := r.Min.X; x < r.Max.X; x++ {
								require.Equal(t, drawn[y][x], cells[y][x],
									"frame %d: the picture is drawn over the effect at %d,%d", frame, x, y)
							}
						}
					}
				}
			}
		})
	}
}

func TestShaderRunnerOpenShaderStartsOnThePicturesOnScreen(t *testing.T) {
	const cols, rows = 40, 12
	ctx := cell.ContextWithPixelSize(context.Background(), cell.PixelSize{Width: 10, Height: 20})
	wallpaper := func(w, h int, c color.NRGBA) tui.Component {
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
		}
		return braille.NewComponent(img, func() (int, int) { return 10, 20 }, braille.Blocks).
			WithPicture(func() image.Image { return img })
	}
	screen := &swappable{Component: wallpaper(10, 20, color.NRGBA{R: 255, A: 255})}
	r := new(shaderRunner)
	r.init(handler.NopFromComponent(screen), term.NopInterrupter(), term.Attributes{},
		nopShutdownShaderConfig(),
		loadingShaderConfig{
			shader: func(attr term.Attributes) shader.Shader {
				return shader.GrayFade(shader.DefaultGrayFadeParams(), attr)
			},
			fps: 1, duration: 10 * time.Second,
		},
		openShaderConfig{
			shader: func(attr term.Attributes, from shader.Snapshot) shader.Shader {
				params := shader.DefaultBurnParams()
				params.From = from
				return shader.Burn(params, attr)
			},
			fps: 1, duration: 10 * time.Second,
		},
		component.FrameCharSetDefault())
	t.Cleanup(func() { _ = r.shader.Close() })
	r.Resize(cols, rows)
	draw := func() *cell.BufferWriter {
		buf := cell.NewBufferWriter(ctx, cols, rows)
		r.Draw(buf)
		return buf
	}

	shown := draw()
	require.Len(t, shown.Images(), 1)
	r.startLoading()
	draw()
	r.captureOpenShaderCells()
	screen.swap(wallpaper(40, 10, color.NRGBA{B: 255, A: 255}))
	r.stopLoading()
	first := draw()

	for y, row := range first.RawCells() {
		for x, c := range row {
			require.NotEqual(t, braille.Block, c.Ch, "cell %d,%d shows the fallback for writers without images", x, y)
		}
	}
	require.Len(t, first.Images(), 1, "only the picture that was on screen shows")
	want, got := shown.Images()[0], first.Images()[0]
	assert.Equal(t, want.Visible(), got.Visible())
	assert.Equal(t, want.Src.(*image.RGBA).Pix, got.Src.(*image.RGBA).Pix)
}

func TestShaderRunnerOpenShaderRevealsThePreviousScreen(t *testing.T) {
	const cols, rows = 40, 12
	for _, name := range []string{
		"burn", "beams", "binaryPath", "decrypt", "expand", "matrix", "middleOut",
		"pour", "print", "rain", "slide",
	} {
		t.Run(name, func(t *testing.T) {
			screen := &swappable{Component: &filled{ch: 'O'}}
			r := new(shaderRunner)
			r.init(handler.NopFromComponent(screen), term.NopInterrupter(), term.Attributes{},
				nopShutdownShaderConfig(),
				loadingShaderConfig{
					shader: func(attr term.Attributes) shader.Shader {
						return shader.GrayFade(shader.DefaultGrayFadeParams(), attr)
					},
					fps: 1, duration: 10 * time.Second,
				},
				openShaderConfig{
					shader: func(attr term.Attributes, from shader.Snapshot) shader.Shader {
						s, ok := buildNamedShader(name, attr, component.FrameCharSetDefault(), from, 1)
						require.True(t, ok)
						return s
					},
					fps: 1, duration: 10 * time.Second,
				},
				component.FrameCharSetDefault())
			t.Cleanup(func() { _ = r.shader.Close() })
			r.Resize(cols, rows)
			draw := func() [][]term.Cell {
				buf := cell.NewBufferWriter(context.Background(), cols, rows)
				r.Draw(buf)
				return buf.RawCells()
			}

			r.startLoading()
			draw()
			r.captureOpenShaderCells()
			screen.swap(&filled{ch: 'N'})
			r.stopLoading()
			old := 0
			for _, row := range draw() {
				for _, c := range row {
					if c.Ch == 'O' {
						old++
					}
				}
			}
			assert.GreaterOrEqual(t, old, cols*rows*3/4, "the first frame shows the screen the workspace took over")
		})
	}
}

// filled is a component every cell of which shows ch.
type filled struct {
	ch            rune
	width, height int
}

func (f *filled) Resize(width, height int) { f.width, f.height = width, height }

func (f *filled) Draw(w term.Writer) {
	for y := range f.height {
		for x := range f.width {
			w.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell(f.ch, 1, term.Attributes{Fg: term.ColorWhite}))
		}
	}
}

// swappable is a component whose content can be replaced, like the
// screen when a workspace opens.
type swappable struct {
	tui.Component
	width, height int
}

func (s *swappable) Resize(width, height int) {
	s.width, s.height = width, height
	s.Component.Resize(width, height)
}

func (s *swappable) swap(c tui.Component) {
	c.Resize(s.width, s.height)
	s.Component = c
}

const testIdleTimeout = 10 * time.Minute

// idleRunner is a shaderRunner with a screensaver whose clock, timers and
// event loop the test drives.
type idleRunner struct {
	*shaderRunner
	inner  *eventRecorder
	clock  time.Time
	timers []time.Duration
	fire   func()
}

func newIdleRunner(t *testing.T, shaders ...string) *idleRunner {
	ir := &idleRunner{
		shaderRunner: new(shaderRunner),
		inner:        &eventRecorder{Handler: handler.Nop()},
		clock:        time.Unix(1_000_000, 0),
	}
	ir.init(ir.inner, term.NopInterrupter(), term.Attributes{},
		nopShutdownShaderConfig(), loadingShaderConfig{}, openShaderConfig{},
		component.FrameCharSetDefault())
	t.Cleanup(func() { _ = ir.Close() })
	ir.now = func() time.Time { return ir.clock }
	ir.screensaverCfg = screensaverConfig{
		shaders:     shaders,
		idleTimeout: testIdleTimeout,
		duration:    time.Second,
		fps:         defaultShaderFPS,
		timeShader:  defaultScreensaverTimeShader,
	}
	ir.Resize(10, 4)
	ir.enableScreensaver(func(d time.Duration, fn func()) *time.Timer {
		ir.timers = append(ir.timers, d)
		ir.fire = fn
		return nil
	}, func(fn func()) bool {
		fn()
		return true
	})
	return ir
}

// settle waits for the shader playing to finish, like the intro does.
func (ir *idleRunner) settle(t *testing.T) {
	t.Helper()
	require.NoError(t, ir.shader.Close())
	require.Eventually(t, ir.shader.Done, time.Second, time.Millisecond)
}

// startScreensaver lets the idle timeout pass and fires the timer.
func (ir *idleRunner) startScreensaver(t *testing.T) {
	t.Helper()
	ir.settle(t)
	ir.clock = ir.clock.Add(testIdleTimeout)
	ir.fire()
	require.NotNil(t, ir.screensaver)
	require.Same(t, ir.screensaver, ir.shader)
}

type eventRecorder struct {
	tui.Handler
	events []term.Event
}

func (e *eventRecorder) Handle(ev term.Event) (exit, handled bool) {
	e.events = append(e.events, ev)
	return false, true
}

func TestShaderRunnerScreensaverWithoutShaders(t *testing.T) {
	ir := newIdleRunner(t)
	initial := ir.shader
	key := term.Event{Type: term.EventKey, Ch: 'a'}

	_, handled := ir.Handle(key)

	assert.True(t, handled)
	assert.Equal(t, []term.Event{key}, ir.inner.events)
	assert.Empty(t, ir.timers)
	assert.Same(t, initial, ir.shader)
}

func TestShaderRunnerScreensaverWaitsForTheIdleTimeout(t *testing.T) {
	ir := newIdleRunner(t, "nop")
	require.Equal(t, []time.Duration{testIdleTimeout}, ir.timers)
	ir.settle(t)
	initial := ir.shader

	ir.clock = ir.clock.Add(4 * time.Minute)
	ir.fire()

	assert.Same(t, initial, ir.shader)
	assert.Nil(t, ir.screensaver)
	assert.Equal(t, []time.Duration{testIdleTimeout, 6 * time.Minute}, ir.timers,
		"checks again once the rest of the timeout passed")
}

func TestShaderRunnerScreensaverStartsOnceIdle(t *testing.T) {
	ir := newIdleRunner(t, "nop", "grayFade")
	initial := ir.shader

	ir.startScreensaver(t)

	assert.NotSame(t, initial, ir.shader)
	assert.Equal(t, int(shaderloop.Duration/(time.Second/defaultShaderFPS)), ir.shader.Total())
	assert.Len(t, ir.timers, 1, "no timer runs while the screensaver plays")
	assert.NotPanics(t, func() { ir.Draw(cell.NewBufferWriter(context.Background(), 10, 4)) })
}

func TestShaderRunnerScreensaversTakeTurns(t *testing.T) {
	ir := newIdleRunner(t, "nop", "grayFade", "burn")
	key := term.Event{Type: term.EventKey, Ch: 'a'}

	var played []int
	for range 4 {
		played = append(played, ir.screensaverNext)
		ir.startScreensaver(t)
		ir.Handle(key)
		ir.fire()
	}

	assert.Equal(t, []int{0, 1, 2, 0}, played)
}

func TestScreensaverConfigPlaysPassesThroughTheTimeShader(t *testing.T) {
	const fps = 10
	total := int(shaderloop.Duration / (time.Second / fps))
	// The start and middle of the first two passes of a second.
	frames := []int{0, 5, 10, 15}
	for _, tc := range []struct {
		timeShader string
		want       []float64
	}{
		{timeShader: "loop", want: []float64{0, 0.5, 0, 0.5}},
		{timeShader: "ping_pong", want: []float64{0, 1, 0, 1}},
		{timeShader: "reverse", want: []float64{1, 0.5, 1, 0.5}},
		{timeShader: "accelerate", want: []float64{0, 0.25, 0, 0.25}},
		{timeShader: "decelerate", want: []float64{0, 0.75, 0, 0.75}},
		{timeShader: "boomerang", want: []float64{0, 1, 0, 1}},
	} {
		t.Run(tc.timeShader, func(t *testing.T) {
			cfg := screensaverConfig{duration: time.Second, fps: fps, timeShader: tc.timeShader}
			rec := new(frameRecorder)
			s := cfg.play(rec, 0, 0, term.Attributes{})
			var got []float64
			for _, frame := range frames {
				s.Shade(frame, total, nil)
				require.Equal(t, total, rec.total)
				got = append(got, math.Round(100*float64(rec.frame)/float64(total))/100)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestScreensaverConfigPlaysAFireWholeCyclesAPass(t *testing.T) {
	const fps = 10
	const cycle = 4 * time.Second
	total := int(shaderloop.Duration / (time.Second / fps))
	for _, tc := range []struct {
		name     string
		duration time.Duration
		// want is how far through its cycle the fire is at each frame.
		want map[int]float64
	}{
		{
			name:     "a pass shorter than the cycle lasts the cycle",
			duration: time.Second,
			want:     map[int]float64{0: 0, 10: 0.25, 20: 0.5, 39: 0.975, 40: 0, 60: 0.5},
		},
		{
			name:     "a pass longer than the cycle plays it over and over",
			duration: 10 * time.Second,
			want:     map[int]float64{0: 0, 20: 0.5, 40: 0, 100: 0.5, 119: 0.975, 120: 0, 140: 0.5},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := screensaverConfig{duration: tc.duration, fps: fps, timeShader: "loop"}
			rec := new(frameRecorder)
			s := cfg.play(rec, cycle, 0, term.Attributes{})
			for frame, want := range tc.want {
				s.Shade(frame, total, nil)
				assert.InDelta(t, want, float64(rec.frame)/float64(rec.total), 0.001, "frame %d", frame)
			}
		})
	}
}

func TestShaderRunnerScreensaverFadesInOnlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cycle   time.Duration
		fadesIn bool
	}{
		{"blaze", shaderloop.BlazeCycle, true},
		{"inferno", shaderloop.InfernoCycle, true},
		{"flames", 0, false},
		{"shine", 0, false},
	} {
		name := tc.name
		t.Run(name, func(t *testing.T) {
			ir := newIdleRunner(t, name)
			cfg := ir.screensaverCfg
			screensaver := ir.newScreensaver()
			bare, ok := namedShader(name, ir.defAttr, ir.fc, shader.Snapshot{})
			require.True(t, ok)
			frames := func(d time.Duration) int { return int(d / (time.Second / time.Duration(cfg.fps))) }
			total := frames(shaderloop.Duration)
			// A pass of a fire lasts its cycle, longer than the second of cfg.
			pass := frames(max(cfg.duration, tc.cycle))
			looped := cfg.play(bare, tc.cycle, 0, ir.defAttr)
			in := makeScreensaverCanvas()
			shade := func(s shader.Shader, frame int) [][]term.Cell {
				out := term.CloneCells(in)
				s.Shade(frame, total, out)
				return out
			}

			fading := false
			for frame := range frames(cfg.duration) / 2 {
				fading = fading || !assert.ObjectsAreEqual(shade(looped, frame), shade(screensaver, frame))
			}
			assert.Equal(t, tc.fadesIn, fading, "the first pass fades in")
			// Every later pass, across its seams, is the shader itself.
			for _, frame := range []int{pass - 1, pass, pass + 1, 2*pass - 1, 2 * pass, 5 * pass, total/2 - 1, total / 2} {
				require.Equal(t, shade(looped, frame), shade(screensaver, frame), "frame %d", frame)
			}
		})
	}
}

// makeScreensaverCanvas returns a small canvas of text.
func makeScreensaverCanvas() [][]term.Cell {
	out := make([][]term.Cell, 4)
	for y := range out {
		out[y] = make([]term.Cell, 10)
		for x := range out[y] {
			out[y][x] = term.NewCell(rune('a'+x+y), 1, term.Attributes{Fg: term.ColorWhite})
		}
	}
	return out
}

// frameRecorder records the frame and total it last shaded.
type frameRecorder struct{ frame, total int }

func (r *frameRecorder) Shade(frame, total int, _ [][]term.Cell) {
	r.frame, r.total = frame, total
}

func TestShaderRunnerScreensaverWaitsForThePlayingShader(t *testing.T) {
	ir := newIdleRunner(t, "nop")
	ir.runShader(shader.Nop(), defaultShaderFPS, time.Hour)
	intro := ir.shader

	ir.clock = ir.clock.Add(testIdleTimeout)
	ir.fire()

	assert.Same(t, intro, ir.shader)
	assert.Nil(t, ir.screensaver)
	assert.Equal(t, []time.Duration{testIdleTimeout, testIdleTimeout}, ir.timers)
}

func TestShaderRunnerScreensaverEvents(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ev    term.Event
		wakes bool
	}{
		{name: "key", ev: term.Event{Type: term.EventKey, Ch: 'a'}, wakes: true},
		{name: "mouse motion", ev: term.Event{Type: term.EventMouse, MouseX: 3, MouseY: 2}, wakes: true},
		{name: "mouse click", ev: term.Event{Type: term.EventMouse, Key: term.MouseLeft}, wakes: true},
		{name: "paste start", ev: term.Event{Type: term.EventPasteStart}, wakes: true},
		{name: "paste end", ev: term.Event{Type: term.EventPasteEnd}, wakes: true},
		{name: "interrupt", ev: term.Event{Type: term.EventInterrupt}},
		{name: "focus", ev: term.Event{Type: term.EventFocus}},
		{name: "unfocus", ev: term.Event{Type: term.EventUnfocus}},
		{name: "resize", ev: term.Event{Type: term.EventResize}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ir := newIdleRunner(t, "nop")
			ir.startScreensaver(t)
			screensaver := ir.shader
			idleSince := ir.lastInput
			ir.clock = ir.clock.Add(time.Minute)

			_, handled := ir.Handle(tc.ev)

			assert.True(t, handled)
			assert.Equal(t, []term.Event{tc.ev}, ir.inner.events,
				"the event reaches the IDE whether or not it wakes the screensaver")
			if !tc.wakes {
				assert.Same(t, screensaver, ir.shader)
				assert.Equal(t, idleSince, ir.lastInput)
				assert.Len(t, ir.timers, 1)
				return
			}
			assert.NotSame(t, screensaver, ir.shader)
			assert.Nil(t, ir.screensaver)
			assert.Equal(t, ir.clock, ir.lastInput)
			assert.Equal(t, []time.Duration{testIdleTimeout, testIdleTimeout}, ir.timers)
		})
	}
}

func TestShaderRunnerScreensaverInputWhileArmed(t *testing.T) {
	ir := newIdleRunner(t, "nop")
	ir.settle(t)
	key := term.Event{Type: term.EventKey, Ch: 'a'}

	for range 3 {
		ir.clock = ir.clock.Add(time.Minute)
		ir.Handle(key)
	}
	assert.Len(t, ir.timers, 1, "input only records when it happened")

	ir.clock = ir.clock.Add(7 * time.Minute)
	ir.fire()
	assert.Nil(t, ir.screensaver)
	assert.Equal(t, []time.Duration{testIdleTimeout, 3 * time.Minute}, ir.timers)
}

func TestShaderRunnerPointerLeaves(t *testing.T) {
	t.Parallel()
	for _, size := range []term.Coordinates{{X: 100, Y: 30}, {X: 2, Y: 2}} {
		ir := newIdleRunner(t, "nop")
		ir.startScreensaver(t)
		handlertest.RunPointerLeaves(t, ir, size.X, size.Y)
	}
}

func TestShaderRunnerScreensaverAfterClose(t *testing.T) {
	ir := newIdleRunner(t, "nop")
	ir.settle(t)
	initial := ir.shader
	ir.clock = ir.clock.Add(testIdleTimeout)

	require.NoError(t, ir.Close())
	ir.fire()

	assert.Same(t, initial, ir.shader)
	assert.Nil(t, ir.screensaver)
	assert.Len(t, ir.timers, 1)
}
