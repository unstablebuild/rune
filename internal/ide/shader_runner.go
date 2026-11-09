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
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/glslshader"
	"unstable.build/rune/internal/component/shader/shaderloop"
	"unstable.build/rune/internal/component/shader/timeshader"
	"unstable.build/rune/internal/debug"
)

const (
	defaultShaderFPS             = 30
	defaultShaderDuration        = 1 * time.Second
	defaultLoadingShaderDuration = 10 * time.Second
	defaultOpenShaderDuration    = 1 * time.Second

	defaultScreensaverIdleTimeout = 10 * time.Minute
	defaultScreensaverDuration    = time.Minute
	defaultScreensaverTimeShader  = "loop"

	// introShaderName plays the intro of the wallpaper from :shaderrun,
	// even when animations.intro keeps it from playing at startup.
	introShaderName = "intro"
)

type initShaderConfig struct {
	shader   func(term.Attributes, component.FrameCharSet) shader.Shader
	fps      int
	duration time.Duration
}

type shutdownShaderConfig struct {
	shader   func(term.Attributes) shader.Shader
	fps      int
	duration time.Duration
}

func nopShutdownShaderConfig() shutdownShaderConfig {
	return shutdownShaderConfig{
		shader:   func(_ term.Attributes) shader.Shader { return shader.Nop() },
		fps:      defaultShaderFPS,
		duration: 100 * time.Millisecond,
	}
}

type loadingShaderConfig struct {
	shader   func(term.Attributes) shader.Shader
	fps      int
	duration time.Duration
}

type openShaderConfig struct {
	shader   func(term.Attributes, shader.Snapshot) shader.Shader
	fps      int
	duration time.Duration
}

// screensaverConfig is the screensaver that starts once the IDE has had
// no input for idleTimeout. Each one plays the next of shaders, a pass of
// duration at a time through the time shader named by timeShader, until
// input stops it. There is no screensaver without shaders.
type screensaverConfig struct {
	shaders     []string
	idleTimeout time.Duration
	duration    time.Duration
	fps         int
	timeShader  string
}

// screensaverTimeShaders are the time shaders a pass of the screensaver
// can play through, by the name animations.screensaver.time_shader takes.
// Every pass is looped, so loop plays a pass as it is. Abs and Sine are
// left out: frames are never negative for Abs to change, and Sine turns
// half of them negative, which shaders draw nothing for.
var screensaverTimeShaders = map[string]func(shader.Shader) shader.Shader{
	"loop":       func(s shader.Shader) shader.Shader { return s },
	"ping_pong":  timeshader.PingPong,
	"reverse":    timeshader.Reverse,
	"accelerate": timeshader.Accelerate,
	"decelerate": timeshader.Decelerate,
	"boomerang":  timeshader.Boomerang,
}

// play returns s played through the time shader one pass of duration at
// a time, over and over for the shaderloop.Duration a screensaver lasts.
// Each pass sees the whole of the frames and total it is given, unless s
// plays a whole cycle of the given length a run, as the fires do: then a
// pass lasts the fewest whole cycles that cover duration, so that s
// plays at its pace and its passes join seamlessly. The screensaver
// fades in from the screen once, over fadeIn of duration, and the passes
// play as they are after it: a fade at every pass would flash the screen
// between them. defaultAttr are the default attributes of the screen.
func (c screensaverConfig) play(
	s shader.Shader, cycle time.Duration, fadeIn float64, defaultAttr term.Attributes,
) shader.Shader {
	frame := time.Second / time.Duration(c.fps)
	total := int(shaderloop.Duration / frame)
	duration := max(1, int(c.duration/frame))
	pass := duration
	if cycle > 0 {
		cycles := max(1, int((c.duration+cycle-1)/cycle))
		s = timeshader.Loop(s, cycles)
		pass = max(1, int(time.Duration(cycles)*cycle/frame))
	}
	passes := max(1, total/pass)
	looped := timeshader.Loop(screensaverTimeShaders[c.timeShader](s), passes)
	if fadeIn <= 0 {
		return looped
	}
	overlap := min(1, fadeIn*float64(duration)/float64(total))
	// Changing halfway through the overlap starts the fade on the first
	// frame, so the looped shader sees the frames and total it is given.
	return shader.TransitionCrossFade(
		shader.TransitionCrossFadeParams{ChangeAtPerc: overlap / 2, OverlapPerc: overlap},
		defaultAttr, shader.Nop(), looped,
	)
}

// used as the root tui.Handler to dynamically run shaders
type shaderRunner struct {
	tui.Handler
	interrupter       term.Interrupter
	shader            *shader.Component
	defAttr           term.Attributes
	fc                component.FrameCharSet
	width, height     int
	initShaderCfg     initShaderConfig
	shutdownShaderCfg shutdownShaderConfig
	loadingShaderCfg  loadingShaderConfig
	openShaderCfg     openShaderConfig
	openShaderCells   [][]term.Cell
	openShaderImages  []term.Image
	loadingShaderInst shader.Shader
	// pixelSize is the cell size of the writer last drawn into, which the
	// screen needs to draw its pictures.
	pixelSize cell.PixelSize

	screensaverCfg screensaverConfig
	afterFunc      func(time.Duration, func()) *time.Timer
	scheduleFn     func(func()) bool
	now            func() time.Time
	lastInput      time.Time
	idleTimer      *time.Timer
	// idleArmed is set from arming the idle timer until its check runs on
	// the event loop, so that input arms at most one timer at a time.
	idleArmed bool
	// screensaver is the shader started by the idle timer, nil once input
	// woke it.
	screensaver *shader.Component
	// screensaverNext indexes the shader the next screensaver plays.
	screensaverNext int
	closed          bool
}

func (r *shaderRunner) HandleCommand(ctx context.Context, cmd textapi.Command) (
	err error,
) {
	if len(cmd.Args) == 0 {
		err = errors.New("expected at least one argument with the shader name")
		return
	}

	if cmd.Args[0] == introShaderName {
		if !r.runInitShader() {
			err = errors.New("the wallpaper has no intro: " +
				"only the built-in logo has one")
		}
		return
	}

	d := defaultShaderDuration
	if len(cmd.Args) > 1 {
		d, err = time.ParseDuration(cmd.Args[1])
		if err != nil {
			err = fmt.Errorf("parse duration: %v", err)
			return
		}
	}

	fps := defaultShaderFPS
	if len(cmd.Args) > 2 {
		fps, err = strconv.Atoi(cmd.Args[2])
		if err != nil {
			err = fmt.Errorf("parse fps: %v", err)
			return
		}
	}

	s, ok := buildNamedShader(cmd.Args[0], r.defAttr, r.fc, shader.Snapshot{}, fps)
	if !ok {
		err = errors.New("expected one of the available shaders")
		return
	}

	r.runShader(s, fps, d)
	return
}

func (r *shaderRunner) Complete(ctx context.Context, cmd textapi.Command) (
	iterator.Iterator[string], string, error,
) {
	if len(cmd.Args) <= 1 {
		names := namedShaderNames()
		if r.initShaderCfg.shader != nil {
			names = append([]string{introShaderName}, names...)
		}
		return iterator.FromSlice(names), "", nil
	}
	return iterator.Empty[string](), "", nil
}

// namedShaderNames returns the names accepted by buildNamedShader, in
// the order shown by the :shaderrun completer. The order is preserved
// for showroom UI stability.
func namedShaderNames() []string {
	return []string{
		"blaze",
		"blazeBlue",
		"bomb",
		"burn",
		"embers",
		"incendium",
		"flames",
		"inferno",
		"infernoBlue",
		"nop",
		"pulse",
		"shine",
		"shineFrame",
		"radarFrame",
		"grayFade",
		"beams",
		"binaryPath",
		"crumble",
		"decrypt",
		"expand",
		"matrix",
		"middleOut",
		"pour",
		"print",
		"rain",
		"rings",
		"slide",
		"spotlights",
		"thunderstorm",
		"unstable",
		"vhsTape",
	}
}

// buildNamedShader returns the shader namedShader returns played once at
// fps, as :shaderrun and the open-workspace animation do: faded in from
// and out to the screen it plays over, by showroomFades, and at the pace
// of its showroomCycle, if it has one, however long it plays.
func buildNamedShader(
	name string, defAttr term.Attributes, fc component.FrameCharSet, from shader.Snapshot, fps int,
) (shader.Shader, bool) {
	s, ok := namedShader(name, defAttr, fc, from)
	if !ok {
		return nil, false
	}
	if cycle := showroomCycle(name); cycle > 0 {
		s = timeshader.Period(s, max(1, int(cycle*time.Duration(fps)/time.Second)))
	}
	in, out := showroomFades(name)
	if in == 0 && out == 0 {
		return s, true
	}
	return wrapShaderCrossFadeInOut(s, in, out, defAttr), true
}

// showroomCycle returns how long a cycle of the shader named name lasts
// at the pace it was tuned to, for the fires, which play a whole cycle a
// run however long the run is: played for less than a cycle they would
// rush through all of it. It returns 0 for the others.
func showroomCycle(name string) time.Duration {
	switch name {
	case "blaze", "blazeBlue":
		return shaderloop.BlazeCycle
	case "inferno", "infernoBlue":
		return shaderloop.InfernoCycle
	}
	return 0
}

// showroomFades returns the fractions of a run of the shader named name
// over which it fades in from the screen it plays over and out to it.
// The shaders that paint over the whole screen fade, so that they do not
// pop in and out of view, and the others need not.
func showroomFades(name string) (in, out float64) {
	switch name {
	case "flames":
		return 0, 0.1
	case "blaze", "blazeBlue", "embers", "incendium", "inferno", "infernoBlue",
		"shine", "shineFrame", "pulseFrame", "radarFrame", "spotlights", "thunderstorm":
		return 0.1, 0.1
	}
	return 0, 0
}

// namedShader returns the shader registered under name with the default
// parameters of the :shaderrun showroom, as it is, to be played once or
// over and over. The reveal shaders start from from rather than a blank
// screen, or the screen shaded for burn, when it has cells. Returns
// ok=false when name does not match any known shader.
func namedShader(
	name string, defAttr term.Attributes, fc component.FrameCharSet, from shader.Snapshot,
) (shader.Shader, bool) {
	switch name {
	case "blaze":
		return glslshader.Blaze(glslshader.DefaultBlazeParams()), true
	case "blazeBlue":
		params := glslshader.DefaultBlazeParams()
		params.SwapRedBlue = true
		return glslshader.Blaze(params), true
	case "bomb":
		return shader.Bomb(shader.DefaultBombParams(), defAttr), true
	case "burn":
		params := shader.DefaultBurnParams()
		params.From = from
		return shader.Burn(params, defAttr), true
	case "embers":
		return glslshader.Embers(glslshader.DefaultEmbersParams(), defAttr), true
	case "incendium":
		return glslshader.Incendium(glslshader.DefaultIncendiumParams(), defAttr), true
	case "flames":
		return glslshader.Flames(glslshader.DefaultFlamesParams(), defAttr), true
	case "inferno":
		return glslshader.Inferno(glslshader.DefaultInfernoParams()), true
	case "infernoBlue":
		params := glslshader.DefaultInfernoParams()
		params.SwapRedBlue = true
		return glslshader.Inferno(params), true
	case "nop":
		return shader.Nop(), true
	case "pulse":
		return shader.Pulse(shader.DefaultPulseParams(), defAttr), true
	case "shine":
		return glslshader.Shine(glslshader.DefaultShineParams(), defAttr), true
	case "shineFrame":
		return glslshader.ShineFrame(glslshader.DefaultShineFrameParams(fc), defAttr), true
	case "pulseFrame":
		return glslshader.PulseFrame(glslshader.DefaultPulseFrameParams(fc), defAttr), true
	case "radarFrame":
		return glslshader.RadarFrame(glslshader.DefaultRadarFrameParams(fc), defAttr), true
	case "grayFade":
		return shader.GrayFade(shader.DefaultGrayFadeParams(), defAttr), true
	case "beams":
		params := shader.DefaultBeamsParams()
		params.From = from
		return shader.Beams(params, defAttr), true
	case "binaryPath":
		params := shader.DefaultBinaryPathParams()
		params.From = from
		return shader.BinaryPath(params, defAttr), true
	case "crumble":
		return shader.Crumble(shader.DefaultCrumbleParams(), defAttr), true
	case "decrypt":
		params := shader.DefaultDecryptParams()
		params.From = from
		return shader.Decrypt(params, defAttr), true
	case "expand":
		params := shader.DefaultExpandParams()
		params.From = from
		return shader.Expand(params, defAttr), true
	case "matrix":
		params := shader.DefaultMatrixParams()
		params.From = from
		return shader.Matrix(params, defAttr), true
	case "middleOut":
		params := shader.DefaultMiddleOutParams()
		params.From = from
		return shader.MiddleOut(params, defAttr), true
	case "pour":
		params := shader.DefaultPourParams()
		params.From = from
		return shader.Pour(params, defAttr), true
	case "print":
		params := shader.DefaultPrintParams()
		params.From = from
		return shader.Print(params, defAttr), true
	case "rain":
		params := shader.DefaultRainParams()
		params.From = from
		return shader.Rain(params, defAttr), true
	case "rings":
		return shader.Rings(shader.DefaultRingsParams(), defAttr), true
	case "slide":
		params := shader.DefaultSlideParams()
		params.From = from
		return shader.Slide(params, defAttr), true
	case "spotlights":
		return shader.Spotlights(shader.DefaultSpotlightsParams(), defAttr), true
	case "thunderstorm":
		return shader.Thunderstorm(shader.DefaultThunderstormParams(), defAttr), true
	case "unstable":
		return shader.Unstable(shader.DefaultUnstableParams(), defAttr), true
	case "vhsTape":
		return shader.VHSTape(shader.DefaultVHSTapeParams(), defAttr), true
	}
	return nil, false
}

func (r *shaderRunner) init(
	root tui.Handler,
	interrupter term.Interrupter,
	defAttr term.Attributes,
	shutdownShaderCfg shutdownShaderConfig,
	loadingShaderCfg loadingShaderConfig,
	openShaderCfg openShaderConfig,
	fc component.FrameCharSet,
) {
	r.Handler = root
	r.interrupter = interrupter
	r.defAttr = defAttr
	r.shutdownShaderCfg = shutdownShaderCfg
	r.loadingShaderCfg = loadingShaderCfg
	r.openShaderCfg = openShaderCfg
	r.fc = fc
	r.now = time.Now

	// Initialize zero shader so we can treat field always as non-nil.
	//
	// Some duration > 0 is passed so the r.shader.done stays true,
	// that's something that happens after rendering any shader and
	// allows us to only use one variable (c.done) to determine if a
	// shader is running or not.
	r.shader = shader.New(r.Handler, shader.Nop(), r.interrupter, defaultShaderFPS, 100*time.Millisecond)
}

// enableScreensaver starts counting idle time toward the screensaver of
// screensaverCfg, if it has shaders. afterFunc runs a function after a
// duration, and scheduleFn runs one on the event loop.
func (r *shaderRunner) enableScreensaver(
	afterFunc func(time.Duration, func()) *time.Timer, scheduleFn func(func()) bool,
) {
	if len(r.screensaverCfg.shaders) == 0 {
		return
	}
	r.afterFunc = afterFunc
	r.scheduleFn = scheduleFn
	r.lastInput = r.now()
	r.armIdle(r.screensaverCfg.idleTimeout)
}

func (r *shaderRunner) screensaverEnabled() bool {
	return r.afterFunc != nil
}

func (r *shaderRunner) armIdle(d time.Duration) {
	r.idleArmed = true
	r.idleTimer = r.afterFunc(d, func() {
		debug.CapturePanicReport(func() { r.scheduleFn(r.checkIdle) })
	})
}

// checkIdle starts the screensaver if there was no input for the idle
// timeout and no other shader is playing, and otherwise checks again
// later. It runs on the event loop.
func (r *shaderRunner) checkIdle() {
	r.idleArmed = false
	if r.closed {
		return
	}
	cfg := r.screensaverCfg
	if idle := r.now().Sub(r.lastInput); idle < cfg.idleTimeout {
		r.armIdle(cfg.idleTimeout - idle)
		return
	}
	if !r.shader.Done() {
		r.armIdle(cfg.idleTimeout)
		return
	}
	r.runShader(r.newScreensaver(), cfg.fps, shaderloop.Duration)
	r.screensaver = r.shader
}

// newScreensaver returns the next of the configured shaders, so that
// screensavers take turns through them.
func (r *shaderRunner) newScreensaver() shader.Shader {
	cfg := r.screensaverCfg
	name := cfg.shaders[r.screensaverNext]
	r.screensaverNext = (r.screensaverNext + 1) % len(cfg.shaders)
	s, _ := namedShader(name, r.defAttr, r.fc, shader.Snapshot{})
	fadeIn, _ := showroomFades(name)
	return cfg.play(s, showroomCycle(name), fadeIn, r.defAttr)
}

// Handle satisfies tui.Handler. Input stops the screensaver and puts off
// the next one, and is then handled as usual.
func (r *shaderRunner) Handle(ev term.Event) (exit, handled bool) {
	if r.screensaverEnabled() && isUserInput(ev) {
		r.lastInput = r.now()
		if r.shader == r.screensaver {
			r.cancel()
		}
		r.screensaver = nil
		if !r.idleArmed {
			r.armIdle(r.screensaverCfg.idleTimeout)
		}
	}
	return r.Handler.Handle(ev)
}

func isUserInput(ev term.Event) bool {
	switch ev.Type {
	case term.EventKey, term.EventMouse, term.EventPasteStart, term.EventPasteEnd:
		return true
	}
	return false
}

func (r *shaderRunner) runShader(s shader.Shader, fps int, duration time.Duration) {
	_ = r.shader.Close()
	r.shader = shader.New(r.Handler, s, r.interrupter, fps, duration)
	r.shader.Resize(r.width, r.height)
}

func (r *shaderRunner) captureOpenShaderCells() {
	if r.width <= 0 || r.height <= 0 || r.Handler == nil {
		r.openShaderCells = nil
		r.openShaderImages = nil
		return
	}
	// Drawn as on screen, so that it draws its pictures rather than what
	// it draws for writers without images.
	ctx := cell.ContextWithPixelSize(context.Background(), r.pixelSize)
	buf := cell.NewBufferWriter(ctx, r.width, r.height)
	_ = buf.Clear(term.Attributes{})
	r.Handler.Draw(buf)
	r.openShaderCells = term.CloneCells(buf.RawCells())
	r.openShaderImages = buf.Images()
}

func (r *shaderRunner) cancel() {
	r.runShader(shader.Nop(), defaultShaderFPS, 100*time.Millisecond)
}

// runInitShader runs the init shader, reporting false when there is none.
func (r *shaderRunner) runInitShader() bool {
	if r.initShaderCfg.shader == nil {
		return false
	}
	r.runShader(r.initShaderCfg.shader(r.defAttr, r.fc), r.initShaderCfg.fps, r.initShaderCfg.duration)
	return true
}

func (r *shaderRunner) runShutdownShader() {
	r.runShader(
		r.shutdownShaderCfg.shader(r.defAttr),
		r.shutdownShaderCfg.fps,
		r.shutdownShaderCfg.duration)
}

func (r *shaderRunner) startLoading() {
	r.openShaderCells = nil
	r.openShaderImages = nil
	r.loadingShaderInst = nil
	if r.loadingShaderCfg.shader == nil {
		return
	}
	loading := r.loadingShaderCfg.shader(r.defAttr)
	r.loadingShaderInst = loading
	duration := r.loadingShaderCfg.duration
	if duration <= 0 {
		duration = defaultLoadingShaderDuration
	}
	r.runShader(loading, r.loadingShaderCfg.fps, duration)
}

func (r *shaderRunner) stopLoading() {
	openShaderCells := r.openShaderCells
	openShaderImages := r.openShaderImages
	r.openShaderCells = nil
	r.openShaderImages = nil
	loadingShaderInst := r.loadingShaderInst
	r.loadingShaderInst = nil
	if r.loadingShaderCfg.shader == nil {
		return
	}
	if r.openShaderCfg.shader == nil {
		r.cancel()
		return
	}
	from := shader.Snapshot{Cells: openShaderCells, Images: openShaderImages}
	if gf, ok := loadingShaderInst.(*shader.GrayFadeShader); ok {
		from.Desaturation = gf.Progress()
	}
	openShader := r.openShaderCfg.shader(r.defAttr, from)
	duration := r.openShaderCfg.duration
	if duration <= 0 {
		duration = defaultOpenShaderDuration
	}
	r.runShader(openShader, r.openShaderCfg.fps, duration)
}

func (r *shaderRunner) Draw(w term.Writer) {
	r.pixelSize, _ = cell.PixelSizeFromContext(w.Context())
	r.shader.Draw(w)
}

func (r *shaderRunner) Resize(width, height int) {
	r.width = width
	r.height = height
	r.shader.Resize(width, height)
}

func (r *shaderRunner) Close() error {
	r.closed = true
	if r.idleTimer != nil {
		r.idleTimer.Stop()
	}
	return r.shader.Close()
}

func wrapShaderCrossFadeInOut(
	sh shader.Shader, fadeInPerc, fadeOutPerc float64,
	defaultAttr term.Attributes,
) shader.Shader {
	return shader.TransitionCrossFade(
		shader.TransitionCrossFadeParams{
			ChangeAtPerc: fadeInPerc,
			OverlapPerc:  fadeInPerc,
		},
		defaultAttr,
		shader.Nop(),
		shader.TransitionCrossFade(
			shader.TransitionCrossFadeParams{
				ChangeAtPerc: 1.0 - fadeOutPerc,
				OverlapPerc:  fadeOutPerc,
			},
			defaultAttr,
			sh,
			shader.Nop(),
		),
	)
}
