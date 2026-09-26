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

package searchbox

import "github.com/unstablebuild/rune-go-sdk/term"

// Floating is the floating search component owned by a Box.
type Floating = floating

// Button identifies one of the search box buttons.
type Button = button

const (
	// ButtonNone is the absence of a button.
	ButtonNone = buttonNone
	// ButtonUpgrade is the find-to-replace button.
	ButtonUpgrade = buttonUpgrade
	// ButtonNext is the replace-next button.
	ButtonNext = buttonNext
	// ButtonAll is the replace-all button.
	ButtonAll = buttonAll
	// PaddingX is the horizontal padding of the search box.
	PaddingX = paddingX
)

// Rect is the position and size of a search box element.
type Rect struct {
	X, Y, Width, Height int
}

func toRect(r rect) Rect { return Rect{X: r.x, Y: r.y, Width: r.width, Height: r.height} }

// NewFloating builds the floating component without a window manager.
func NewFloating(owner *Box, mode Mode, query string) *Floating {
	return newFloating(owner, mode, query)
}

// Floating returns the currently open floating component, if any.
func (b *Box) Floating() *Floating { return b.floating }

// QueryRect returns the query input geometry.
func (f *Floating) QueryRect() Rect { return toRect(f.layout.query) }

// ReplacementRect returns the replacement input geometry.
func (f *Floating) ReplacementRect() Rect { return toRect(f.layout.replacement) }

// UpgradeRect returns the find-to-replace button geometry.
func (f *Floating) UpgradeRect() Rect { return toRect(f.layout.upgrade) }

// NextRect returns the replace-next button geometry.
func (f *Floating) NextRect() Rect { return toRect(f.layout.next) }

// AllRect returns the replace-all button geometry.
func (f *Floating) AllRect() Rect { return toRect(f.layout.all) }

// ContentHeight returns the height the content wants.
func (f *Floating) ContentHeight() int { return f.layout.contentHeight }

// ViewportHeight returns the height the content was last resized to.
func (f *Floating) ViewportHeight() int { return f.layout.height }

// Hover returns the button currently under the pointer.
func (f *Floating) Hover() Button { return f.hover }

// SetHover marks button as hovered.
func (f *Floating) SetHover(b Button) { f.hover = b }

// ButtonAt returns the button at the given viewport coordinates.
func (f *Floating) ButtonAt(x, y int) Button { return f.buttonAt(x, y) }

// Mode returns whether the replacement input is shown.
func (f *Floating) Mode() Mode { return f.mode }

// ReplacementFocused reports whether the replacement input has focus.
func (f *Floating) ReplacementFocused() bool { return f.focus == focusReplacement }

// FocusReplacement moves focus to the replacement input.
func (f *Floating) FocusReplacement() { f.setFocus(focusReplacement) }

// Upgrade turns a find box into a find and replace box.
func (f *Floating) Upgrade() { f.upgrade() }

// QueryText returns the current query.
func (f *Floating) QueryText() string { return f.query.text() }

// HasReplacement reports whether the replacement input exists.
func (f *Floating) HasReplacement() bool { return f.replacement != nil }

// ReplacementText returns the current replacement.
func (f *Floating) ReplacementText() string {
	if f.replacement == nil {
		return ""
	}
	return f.replacement.text()
}

// HandleReplacement sends ev straight to the replacement input.
func (f *Floating) HandleReplacement(ev term.Event) {
	f.replacement.Handle(ev)
}

// SetCopyShortcutFor installs goos's copy chord and returns a function
// that restores the host's.
func SetCopyShortcutFor(goos string) (restore func()) {
	prev := copyShortcut
	copyShortcut = hostCopyShortcut(goos)
	return func() { copyShortcut = prev }
}
