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

/*
Package glslshader is a collection of shaders and utils that leverage graphics
math functions inspired by OpenGL API (used by GLSL).

It does not contain GPU code, but rather a client-side adaptation, hence don't
be misguided by the word GLSL.

Some of the shaders here will be pixel shaders (i.e. [glslshader.Blaze],
[glslshader.Inferno]), therefore we recommend reading out [Tutorial: Write a
Pixel Shader] if you don't know what a pixel shader is.

But also there are non-pixel shaders that are here because they simply use
GLSL-like functions (i.e. [glslshader.Flame], [glslshader.ProgressViz123]).

If you want to port from [Shadertoy] then you will likely want to satisfy the
cellRunner interface by defining the runCell() method on your shader struct and
calling shadeGLSL() from Shade() as illustrated below. This saves you from
performing any screen space transformation such as normalizing coords or
flipping vertical axis to match the pixel shader specification, it's all done
by shadeGLSL() and you only responsible for your runCell().

	func (s *myShader) Shade(frame, total int, in [][]term.Cell) {
		s.helper.shadeGLSL(frame, total, in, s) // only works if myShader.runCell defined
	}

	func (s *myShader) runCell(
		frame, total int,
		cellCoords term.Coordinates,
		fragCoordX, fragCoordY int,
		resolutionX, resolutionY int,
		inChar rune, inFg, inBg term.Color,
	) (char rune, fg, bg term.Color) {
		phase := float(frame) / float(total)
		// generate your art based on those parameters or pass through by
	    // piping the input values to the out return values.
		char = inChar
		fg = inFg
		bg = inBg
	}

Derive time from frame/total alone, as phase is above, rather than from
a frame rate or a duration: a shader then plays the same whatever the
frame rate, length and frame order it is run at, and time shaders can
remap its progress. An effect that can go on forever, such as fire,
should also end where it starts, so that time shaders can loop it: make
what turns over the run turn a whole number of times, and what drifts
drift through noise that repeats by as much (see [Blaze]).

Check out [Tutorial: Write a Pixel Shader] for a complete guide on the topic.

# Design & Workflow

We recommend using [ShaderToy] or [KodeLife] for shader look development, and
when you are done port over the GLSL code into Go to avoid having to recompile
Go as you are exploring the parameter space or writing new shader code.

If you, Unstable Build developer, are given the task of updating the aesthetic
of a shader you don't need to write it directly in Go. For complex shaders we
store [KodeLife] project and the .glsl file under Unstable Build's
engineering/Shaders Google Drive folder. Either open up the [KodeLife] project
or adapt the .glsl code within [ShaderToy] for your development iteration
cycles, and when you are done translate to Go into the project.

[Shadertoy]: https://www.shadertoy.com
[KodeLife]: https://hexler.net/kodelife
[Tutorial: Write a Pixel Shader]: https://x.unstable.build/docs/tutorials/ox/pixel_shader
*/
package glslshader
