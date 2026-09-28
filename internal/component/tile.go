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

package component

import (
	"fmt"
	"unsafe"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

type splitDir uint8

const (
	noSplit splitDir = iota
	vertical
	horizontal
)

// TileTree represents the root node of a tree of TileNodes.
type TileTree struct {
	root TileNode
}

// TileNode represents a node in a tree of tiled components.
type TileNode struct {
	tree          *TileTree
	width, height int
	content       tui.Component
	children      []*component.Virtual[*TileNode]
	childSplit    splitDir
	parent        *TileNode
	fixedSize     int
	dirty         bool
}

// TileLayout is a serializable representation of a TileTree's structure.
// Nodes with children represent split stems; nodes without children represent
// leaf windows and carry the corresponding window ID.
type TileLayout struct {
	WindowID uint64
	Split    SplitOrientation
	Children []TileLayout
	Floating []FloatingLayout
}

// FloatingLayout is a serializable representation of a floating window's
// restored position and minimized state.
type FloatingLayout struct {
	WindowID           uint64
	Alignment          component.Alignment
	Offset             term.Coordinates
	MinimizedAlignment component.Alignment
	MinimizedPadding   int
	NoBar              bool
	Title              string
}

// SplitOrientation identifies how a TileLayout stem splits its children.
type SplitOrientation uint8

const (
	// SplitOrientationVertical is a vertical split.
	SplitOrientationVertical SplitOrientation = iota
	// SplitOrientationHorizontal is a horizontal split.
	SplitOrientationHorizontal
)

// Init initializes a TileTree or resets it if already initialied.
func (t *TileTree) Init(content tui.Component) (n *TileNode) {
	n = new(TileNode)
	t.root.childSplit = vertical
	t.root.children = []*component.Virtual[*TileNode]{{C: n}}
	t.root.tree = t
	n.initNode(content, &t.root)
	return
}

// NewTileTree allocates storage for a new TileTree and initializes it.
// It also returns the TileNode allocated to store the given content.
func NewTileTree(content tui.Component) (t *TileTree, n *TileNode) {
	t = new(TileTree)
	n = t.Init(content)
	return
}

// Resize resizes the contents of this TileTree.
func (t *TileTree) Resize(width, height int) {
	t.root.Resize(width, height)
}

// Draw draws the contents of this TileTree.
func (t *TileTree) Draw(w term.Writer) {
	t.root.Draw(w)
}

// DrawTile draws only node on w.
func (t *TileTree) DrawTile(node *TileNode, w term.Writer) {
	t.root.drawTile(node, w)
}

// Layout returns a tree of window IDs with the same shape and split
// orientations as the underlying tile tree.
func (t *TileTree) Layout() TileLayout {
	if len(t.root.children) == 1 {
		return t.root.children[0].C.layout()
	}
	return t.root.layout()
}

// RestoreLayout replaces the tree with layout and returns a map from old
// layout window IDs to the newly allocated tile nodes.
func (t *TileTree) RestoreLayout(
	layout TileLayout,
	content func(windowID uint64) tui.Component,
) map[uint64]*TileNode {
	ret := make(map[uint64]*TileNode)
	t.root = TileNode{tree: t, childSplit: splitDirFromLayout(layout.Split)}
	if len(layout.Children) == 0 {
		child := t.restoreLayoutNode(layout, &t.root, content, ret)
		t.root.childSplit = vertical
		t.root.children = []*component.Virtual[*TileNode]{{C: child}}
		return ret
	}
	t.root.children = make([]*component.Virtual[*TileNode], 0, len(layout.Children))
	for _, childLayout := range layout.Children {
		child := t.restoreLayoutNode(childLayout, &t.root, content, ret)
		t.root.children = append(t.root.children, &component.Virtual[*TileNode]{C: child})
	}
	return ret
}

func (t *TileTree) restoreLayoutNode(
	layout TileLayout,
	parent *TileNode,
	content func(windowID uint64) tui.Component,
	leafs map[uint64]*TileNode,
) *TileNode {
	node := new(TileNode)
	node.tree = t
	node.parent = parent
	if len(layout.Children) == 0 {
		node.content = content(layout.WindowID)
		if node.content == nil {
			node.content = component.Nop()
		}
		node.children = []*component.Virtual[*TileNode]{}
		if layout.WindowID != 0 {
			leafs[layout.WindowID] = node
		}
		return node
	}
	node.childSplit = splitDirFromLayout(layout.Split)
	node.children = make([]*component.Virtual[*TileNode], 0, len(layout.Children))
	for _, childLayout := range layout.Children {
		child := t.restoreLayoutNode(childLayout, node, content, leafs)
		node.children = append(node.children, &component.Virtual[*TileNode]{C: child})
	}
	return node
}

func splitDirFromLayout(split SplitOrientation) splitDir {
	switch split {
	case SplitOrientationHorizontal:
		return horizontal
	default:
		return vertical
	}
}

func (t *TileNode) layout() TileLayout {
	ret := TileLayout{WindowID: t.ID()}
	switch t.childSplit {
	case vertical:
		ret.Split = SplitOrientationVertical
	case horizontal:
		ret.Split = SplitOrientationHorizontal
	}
	if len(t.children) == 0 {
		return ret
	}
	ret.WindowID = 0
	ret.Children = make([]TileLayout, 0, len(t.children))
	for _, child := range t.children {
		ret.Children = append(ret.Children, child.C.layout())
	}
	return ret
}

func (t *TileNode) initNode(content tui.Component, parent *TileNode) {
	t.content = content
	t.children = []*component.Virtual[*TileNode]{}
	t.parent = parent
	t.tree = parent.tree
}

// Resize satisfies tui.Component.
func (t *TileNode) Resize(width, height int) {
	t.height = height
	t.width = width
	t.dirty = false

	if len(t.children) == 0 && t.content == nil {
		panic("corrupted node: non-empty children and content")
	}

	if t.content != nil {
		t.content.Resize(width, height)
		return
	}

	if t.childSplit == vertical {
		t.resizeChildren(width, width, height)
		return

	}

	t.resizeChildren(height, width, height)
}

// Draw satisfies tui.Component.
func (t *TileNode) Draw(w term.Writer) {
	if len(t.children) == 0 && t.content == nil {
		panic("corrupted node: non-empty children and content")
	}

	if t.dirty {
		t.Resize(t.width, t.height)
	}

	if t.content != nil {
		t.content.Draw(w)
		return
	}

	for _, ti := range t.children {
		ti.Draw(w)
	}
}

func (t *TileNode) drawTile(node *TileNode, w term.Writer) bool {
	if t.dirty {
		t.Resize(t.width, t.height)
	}

	if t == node {
		t.content.Draw(w)
		return true
	}

	for _, ti := range t.children {
		// call drawTile and use Virtual's position, width, height
		// to emulate Virtual.Draw via VirtualWriter
		vwriter := component.VirtualWriter{
			Writer: w, Offset: ti.Position(),
			Height: ti.Height(), Width: ti.Width(),
		}
		if ok := ti.C.drawTile(node, &vwriter); ok {
			return true
		}
	}
	return false
}

func (t *TileNode) childIdx(child *TileNode) int {
	for i, c := range t.children {
		if c.C == child {
			return i
		}
	}

	panic("tile is not a child of parent")
}

func (t *TileNode) addChildAtIdx(
	child *TileNode, content tui.Component, direction splitDir, idx int,
) {
	if idx > len(t.children) {
		panic(fmt.Errorf("trying to append child at index out of bounds: %d; len=%d",
			idx, len(t.children)))
	}

	v := &component.Virtual[*TileNode]{C: child}

	// transfer content to child at index 0 but do it in a way such that it
	// maintains mapping of content to TileNode.
	// This is because instances of TileNode are leaked outside of the
	// tree through various APIs.
	if len(t.children) == 0 {
		proxyNode := new(TileNode)
		proxyNode.initNode(nil, t.parent)
		proxyNode.childSplit = direction
		proxyNode.fixedSize = t.fixedSize
		proxyNode.children = append(proxyNode.children, &component.Virtual[*TileNode]{C: t}, v)

		idx := t.parent.childIdx(t)
		t.parent.children[idx] = &component.Virtual[*TileNode]{C: proxyNode}

		child.initNode(content, proxyNode)
		t.initNode(t.content, proxyNode)
		t.fixedSize = 0

		proxyNode.parent.Resize(proxyNode.parent.width, proxyNode.parent.height)
		return
	}

	child.initNode(content, t)

	t.children = append(t.children, nil)
	copy(t.children[idx+1:], t.children[idx:])
	t.children[idx] = v

	t.Resize(t.width, t.height)
}

func split(over *TileNode, direction splitDir, content tui.Component) (
	n *TileNode,
) {
	if content == nil {
		panic("empty content for tile")
	}
	if over == nil {
		panic("trying to split over a nil tile")
	}

	n = new(TileNode)

	parent := over.parent

	if parent.childSplit == direction {
		i := parent.childIdx(over)
		parent.addChildAtIdx(n, content, direction, i+1)
		return
	}

	over.addChildAtIdx(n, content, direction, 0)
	return
}

func removeChild(parent, child *TileNode) {
	i := parent.childIdx(child)
	copy(parent.children[i:], parent.children[i+1:])
	parent.children = parent.children[:len(parent.children)-1]

	child.parent = nil
	child.tree = nil
	child.children = nil

	// transfer last child to content but do it in a way such that it
	// maintains mapping of contents to TileNode.
	if len(parent.children) == 1 && parent.parent != nil {
		proxyNode := parent
		lastNode := parent.children[0]

		idx := proxyNode.parent.childIdx(proxyNode)
		proxyNode.parent.children[idx] = lastNode

		lastNode.C.parent = proxyNode.parent
		// The survivor takes the proxy's place, and with it the size the
		// proxy held along the grandparent's axis; its own was along the
		// proxy's axis, which is gone.
		lastNode.C.fixedSize = proxyNode.fixedSize

		proxyNode.parent.Resize(proxyNode.parent.width, proxyNode.parent.height)

		proxyNode.tree = nil
		proxyNode.parent = nil
		proxyNode.children = nil
		return
	}

	if parent.flexChildren() == 0 {
		parent.resetFixedSizes()
	}
	parent.Resize(parent.width, parent.height)
}

// Close removes this node from the tree. It panics if node is last node on the tree.
func (t *TileNode) Close() {
	if t.parent == nil {
		return
	}
	if t.parent == &t.tree.root && len(t.parent.children) == 1 {
		panic("unsupported: trying to close last node")
	}

	parent := t.parent
	t.parent = nil

	if len(parent.children) != 0 {
		removeChild(parent, t)
		return
	}

	parent.Close()
}

// SplitVertical splits the given node to incorporate new content. If direction of the
// given node's split is vertical, a new node with content will be added as a sibling of node.
// Otherwise, a new node with content will become a child of the given node so
// width will be divided in half so new node can be drawn next to it.
// It panics if node is a child of this TileTree.
func (t *TileTree) SplitVertical(
	node *TileNode, content tui.Component,
) *TileNode {
	return split(node, vertical, content)
}

// SplitHorizontal splits the given node to incorporate new content. If direction of the
// given node's split is horizontal, a new node with content will be added as a sibling of node.
// Otherwise, a new node will become a child of the given node so
// height will be divided in half so new node can be drawn next to it.
// It panics if node is a child of this TileTree.
func (t *TileTree) SplitHorizontal(
	node *TileNode, content tui.Component,
) *TileNode {
	return split(node, horizontal, content)
}

// SplitRoot inserts a new node with content as a top-level child of the root.
// This guarantees the new tile spans the full height of the tiled layout when
// direction is vertical, or the full width when direction is horizontal.
//
// idx is clamped to [0, len(root.children)].
func (t *TileTree) SplitRoot(direction splitDir, idx int, content tui.Component) *TileNode {
	if content == nil {
		panic("empty content for tile")
	}
	if direction == noSplit {
		panic("invalid root split direction")
	}
	root := &t.root
	// A root with at most one child has no meaningful orientation,
	// so we simply adopt the caller's direction.
	if len(root.children) <= 1 {
		root.childSplit = direction
	} else if root.childSplit != direction {
		// Root already has multiple children split in the opposite
		// direction. Wrap them in a proxy node that preserves the
		// existing orientation so the root itself can flip to the
		// caller's direction without disturbing the existing layout.
		proxy := new(TileNode)
		proxy.tree = t
		proxy.parent = root
		proxy.childSplit = root.childSplit
		proxy.children = root.children
		for _, c := range proxy.children {
			c.C.parent = proxy
		}
		root.childSplit = direction
		root.children = []*component.Virtual[*TileNode]{{C: proxy}}
	}
	if idx < 0 {
		idx = 0
	}
	if idx > len(root.children) {
		idx = len(root.children)
	}
	n := new(TileNode)
	root.addChildAtIdx(n, content, direction, idx)
	return n
}

// leftMostChild will return the left-most tile in the node
// if node's split is horizontal, or the top-most tile if the node's split is vertical
func (t *TileNode) leftMostChild() *TileNode {
	node := t.children[0].C
	if len(node.children) == 0 {
		return node
	}

	return node.leftMostChild()
}

// rightMostChild will return the right-most tile in the node
// if node's split is horizontal, or the bottom-most tile if the node's split is vertical
func (t *TileNode) rightMostChild() *TileNode {
	node := t.children[len(t.children)-1].C
	if len(node.children) == 0 {
		return node
	}

	return node.rightMostChild()
}

func getParentIdx(node *TileNode) (parent *TileNode, i int) {
	parent = node.parent
	if parent == nil {
		return nil, 0
	}
	i = parent.childIdx(node)
	return
}

func tileLeftDir(node *TileNode, direction splitDir) *TileNode {
	parent, i := getParentIdx(node)
	if parent == nil {
		return nil
	}
	if i == 0 || parent.childSplit != direction {
		return tileLeftDir(parent, direction)
	}

	link := parent.children[i-1].C
	if len(link.children) == 0 {
		return link
	}

	return link.rightMostChild()
}

func tileRightDir(node *TileNode, direction splitDir) *TileNode {
	parent, i := getParentIdx(node)
	if parent == nil {
		return nil
	}
	if i == len(parent.children)-1 || parent.childSplit != direction {
		return tileRightDir(parent, direction)
	}

	link := parent.children[i+1].C
	if len(link.children) == 0 {
		return link
	}

	return link.leftMostChild()
}

// TileLeft returns the tile left-adjacent to t or nil if t is the
// left-most tile in the tree.
func (t *TileNode) TileLeft() *TileNode {
	return tileLeftDir(t, vertical)
}

// TileRight returns the tile right-adjacent to t or nil if t is the
// right-most tile in the tree.
func (t *TileNode) TileRight() *TileNode {
	return tileRightDir(t, vertical)
}

// TileUp returns the tile on top of t or nil if t is the
// top-most tile in the tree.
func (t *TileNode) TileUp() *TileNode {
	return tileLeftDir(t, horizontal)
}

// TileDown returns the tile in the bottom of t or nil if t is the
// bottom-most tile in the tree.
func (t *TileNode) TileDown() *TileNode {
	return tileRightDir(t, horizontal)
}

// Size returns the total number of nodes under this TileNode.
func (t *TileNode) Size() (size int) {
	if len(t.children) == 0 {
		return 1
	}

	for _, c := range t.children {
		size += c.C.Size()
	}

	return
}

// Size returns the total number of nodes in this tree.
func (t *TileTree) Size() (size int) {
	return t.root.Size()
}

// Content returns the Component held by this TileNode in the TileTree.
func (t *TileNode) Content() tui.Component {
	if t.content == nil {
		panic("corrupted node: leaked proxy node outside of tree")
	}
	return t.content
}

func (t *TileNode) tileAt(tileOffset, pos term.Coordinates) *TileNode {
	if t.childSplit == noSplit {
		return t
	}

	switch t.childSplit {
	case vertical:
		for _, child := range t.children {
			childPos := child.Position()
			childPos.X += tileOffset.X
			childPos.Y += tileOffset.Y
			if pos.X >= childPos.X && pos.X < childPos.X+child.Width() {
				return child.C.tileAt(childPos, pos)
			}
		}
	case horizontal:
		for _, child := range t.children {
			childPos := child.Position()
			childPos.X += tileOffset.X
			childPos.Y += tileOffset.Y
			if pos.Y >= childPos.Y && pos.Y < childPos.Y+child.Height() {
				return child.C.tileAt(childPos, pos)
			}
		}
	}

	panic(fmt.Sprintf("could not find tile at %+v", pos))
}

func (t *TileNode) tilePosition(child *TileNode, currOffset term.Coordinates) (
	offset term.Coordinates, ok bool,
) {
	if t == child {
		panic("missed child on parent loop")
	}

	for _, c := range t.children {
		offset = c.Position()
		offset.X += currOffset.X
		offset.Y += currOffset.Y

		ok = (c.C == child)
		if ok {
			return
		}

		offset, ok = c.C.tilePosition(child, offset)
		if ok {
			return
		}
	}
	return
}

// TilePosition returns the given tile's position offset inside this TileTree.
// It panics if tile is not a member of this tree.
func (t *TileTree) TilePosition(tile *TileNode) term.Coordinates {
	offset, ok := t.root.tilePosition(tile, term.Coordinates{})
	if !ok {
		panic("tile does not belong to this tree")
	}
	return offset
}

// TileAt returns the tile at pos term.Coordinates.
func (t *TileTree) TileAt(pos term.Coordinates) *TileNode {
	if pos.X < 0 || pos.Y < 0 || pos.X >= t.root.width || pos.Y >= t.root.height {
		panic("Coordinates out of bounds")
	}
	return t.root.tileAt(term.Coordinates{}, pos)
}

// SetContentResize sets the content of a TileNode to c.
func (t *TileNode) SetContentResize(c tui.Component, resize bool) (prev tui.Component) {
	prev = t.content
	t.content = c
	if resize {
		t.content.Resize(t.width, t.height)
	}
	return
}

// SetFixedHeight sets the height of this node, or returns
// false if this node's height cannot be fixed.
//
// A node that spans its whole tree's height, and so cannot be fixed,
// reports true when asked for the height it already has.
//
// Calling this method with height=0 effectively resets the
// height to be automatically calculated based on the space available.
func (t *TileNode) SetFixedHeight(height int) bool {
	if t.parent == nil {
		panic("corrupted tile tree: exposed root node")
	}
	if t.parent.childSplit == horizontal && len(t.parent.children) > 1 {
		if height != 0 && t.fixedSize == height {
			return true
		}
		if !t.parent.fixChildSize(t, t.parent.height, height) {
			return false
		}
		t.parent.dirty = true
		t.dirty = true
		return true
	}
	if t.parent.parent == nil {
		return height != 0 && t.height == height
	}
	return t.parent.SetFixedHeight(height)
}

// SetFixedWidth sets the width of this node, or returns
// false if this node's width cannot be fixed.
//
// A node that spans its whole tree's width, and so cannot be fixed,
// reports true when asked for the width it already has.
//
// Calling this method with width=0 effectively resets the
// width to be automatically calculated based on the space available.
func (t *TileNode) SetFixedWidth(width int) bool {
	if t.parent == nil {
		panic("corrupted tile tree: exposed root node")
	}
	if t.parent.childSplit == vertical && len(t.parent.children) > 1 {
		if width != 0 && t.fixedSize == width {
			return true
		}
		if !t.parent.fixChildSize(t, t.parent.width, width) {
			return false
		}
		t.parent.dirty = true
		t.dirty = true
		return true
	}
	if t.parent.parent == nil {
		return width != 0 && t.width == width
	}
	return t.parent.SetFixedWidth(width)
}

// ID returns a unique identifier for this tile.
func (t *TileNode) ID() uint64 {
	return uint64(uintptr(unsafe.Pointer(t)))
}

// Height returns the height of this node.
func (t *TileNode) Height() int {
	return t.height
}

// Width returns the height of this node.
func (t *TileNode) Width() int {
	return t.width
}

// MaxWidth returns the max fixed width that this window can be set, based on the
// available space and siblings.
func (t *TileNode) MaxWidth() int {
	if t.parent == nil {
		panic("corrupted tile tree: exposed root node")
	}
	if t.parent.childSplit == vertical {
		return t.parent.width - (3 * (len(t.parent.children) - 1))
	}
	if t.parent.parent == nil {
		return t.parent.width
	}
	return t.parent.MaxWidth()
}

// MaxHeight returns the max fixed height that this window can be set, based on the
// available space and siblings.
func (t *TileNode) MaxHeight() int {
	if t.parent == nil {
		panic("corrupted tile tree: exposed root node")
	}
	if t.parent.childSplit == horizontal {
		return t.parent.height - (3 * (len(t.parent.children) - 1))
	}
	if t.parent.parent == nil {
		return t.parent.height
	}
	return t.parent.MaxHeight()
}

// Position returns the position of this tile node inside its TileTree.
func (t *TileNode) Position() term.Coordinates {
	return t.tree.TilePosition(t)
}

func (t *TileNode) iterate(op func(*TileNode)) {
	if t.content != nil {
		op(t)
		return
	}

	// op may close a visited tile, and TileNode.Close mutates this
	// node's children slice in place. Snapshot it so a close does not
	// shift an unvisited sibling out from under the loop and skip it.
	children := make([]*component.Virtual[*TileNode], len(t.children))
	copy(children, t.children)
	for _, ti := range children {
		ti.C.iterate(op)
	}
}

// Iterate applies op to the content of all nodes of this tree.
func (t *TileTree) Iterate(op func(*TileNode)) {
	t.root.iterate(op)
}

// Closed returns if this Window has been closed.
func (t *TileNode) Closed() bool {
	return t.parent == nil
}

func (t *TileNode) fixedSizeNodes() (totalFixedSize, fixedSizeNodes int) {
	for _, ti := range t.children {
		fixedSize := ti.C.fixedSize
		if fixedSize != 0 {
			totalFixedSize += fixedSize
			fixedSizeNodes++
		}
	}
	return
}

// resizeChildren lays t's children out along its split axis, whose
// length is total. Flexible children share what the fixed ones leave,
// the last ones taking the spare cells so a fixed child never absorbs
// (and loses) one. When the fixed sizes do not fit, or no flexible
// child is left to fill the gap, the fixed children are scaled to
// total and the flexible ones collapse: children never overlap, and
// fixedSize is untouched so they regain their size once there is room.
func (t *TileNode) resizeChildren(total, width, height int) {
	fixedTotal, fixedNodes := t.fixedSizeNodes()
	flexNodes := len(t.children) - fixedNodes
	scale := flexNodes == 0 || fixedTotal > total

	var flexSize, spareFrom int
	if !scale {
		flexSize = (total - fixedTotal) / flexNodes
		spareFrom = flexNodes - (total - fixedTotal - flexSize*flexNodes)
	}

	var offset, flexIdx, fixedIdx int
	for _, ti := range t.children {
		var size int
		switch fixed := ti.C.fixedSize; {
		case fixed == 0:
			if flexIdx >= spareFrom {
				size = flexSize + 1
			} else {
				size = flexSize
			}
			if scale {
				size = 0
			}
			flexIdx++
		case !scale:
			size = fixed
		default:
			fixedIdx++
			if fixedIdx == fixedNodes {
				size = total - offset
			} else {
				size = fixed * total / fixedTotal
			}
		}
		if t.childSplit == horizontal {
			ti.Move(term.Coordinates{Y: offset})
			ti.Resize(width, size)
		} else {
			ti.Move(term.Coordinates{X: offset})
			ti.Resize(size, height)
		}
		offset += size
	}
}

// minFlexSize is the least a flexible tile may be squeezed to by its
// fixed-size siblings.
const minFlexSize = 3

// fixChildSize fixes child's size along t's split axis, or resets it
// when size is 0. The other fixed children keep their sizes as long as
// a flexible child remains to absorb the rest; otherwise they yield,
// since a split needs at least one flexible tile. Returns false when
// the flexible tiles would be left with less than minFlexSize each.
func (t *TileNode) fixChildSize(child *TileNode, avail, size int) bool {
	if len(t.children) <= 1 {
		return false
	}
	if size == 0 {
		child.fixedSize = 0
		return true
	}
	fixed := size
	flex := len(t.children) - 1
	for _, c := range t.children {
		if c.C != child && c.C.fixedSize != 0 {
			fixed += c.C.fixedSize
			flex--
		}
	}
	if flex > 0 && (avail-fixed)/flex >= minFlexSize {
		child.fixedSize = size
		return true
	}
	if (avail-size)/(len(t.children)-1) < minFlexSize {
		return false
	}
	t.resetFixedSizes()
	child.fixedSize = size
	return true
}

// flexChildren counts the children whose size is not fixed.
func (t *TileNode) flexChildren() int {
	_, fixed := t.fixedSizeNodes()
	return len(t.children) - fixed
}

func (t *TileNode) resetFixedSizes() {
	for _, c := range t.children {
		c.C.fixedSize = 0
	}
}
