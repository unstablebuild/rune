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

// Package markdown provides a terminal-based markdown rendering component.
//
// The component parses markdown content using goldmark and renders it
// with proper formatting for terminal display, supporting headers, paragraphs,
// lists (ordered, unordered, task), code blocks, blockquotes, tables,
// and horizontal rules.
//
// # Images
//
// A component given an ImagesConfig shows the pictures of a paragraph
// made only of images at GitHub's size relative to the text, each
// captioned with its title. An attribute block right after an image
// sizes it in cells instead:
//
//	![Acme](logo.png "Acme"){cols=16 rows=8 maintain_aspect_ratio=false}
//
// With one of cols and rows, the other follows the picture's aspect
// ratio. With both, the picture fits in the box, or fills it when
// maintain_aspect_ratio is false. Sizes are whole numbers from 1 to
// 1000, and a box wider than the component shrinks to it.
//
// # Basic Usage
//
//	md := markdown.New("# Hello World\n\nThis is a **bold** statement.")
//	md.Resize(80, 24)
//	md.Draw(writer)
//
// # Scrolling
//
// The component implements component.ScrollableFloating, allowing it to be
// scrolled when content exceeds the viewport:
//
//	md.SeekDown() // scroll down one line
//	md.SeekUp()   // scroll up one line
//
// # Customization
//
// Use NewWithConfig to customize colors and styling:
//
//	cfg := markdown.DefaultConfig()
//	cfg.H1 = term.Attributes{Fg: term.ColorRed, Attrs: term.AttrBold}
//	md := markdown.NewWithConfig(content, cfg)
package markdown
