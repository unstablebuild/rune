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

package text

import (
	"math"
	"sort"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
)

// LocationStore manages LocationLists and priorities and provides
// convenience methods to set, retrieve and sort locations.
type LocationStore struct {
	locs             map[string]LocationList // used by cursor moves
	drawLocations    map[string]*LocationSet // used to draw
	locsSliceTemp    []*LocationSet
	locsSlice        []textapi.Location
	messages         map[term.Coordinates][]message
	locsAtCoordsTemp []textapi.Location
}

// NewLocationStore allocates storage for a new LocationStore and initializes it.
func NewLocationStore() *LocationStore {
	ret := new(LocationStore)
	ret.Init()
	return ret
}

// Init initializes this LocationStore.
func (c *LocationStore) Init() {
	c.locs = make(map[string]LocationList)
	c.drawLocations = make(map[string]*LocationSet)
	c.messages = make(map[term.Coordinates][]message)
}

// LocationList returns the location list identified by ID, set previously via SetLocationList,
// or nil and false, if no location list is currently set with the given ID.
func (c *LocationStore) LocationList(ID string) (LocationList, bool) {
	l, ok := c.locs[ID]
	return l, ok
}

// SortedLocations returns all the locations sorted by priority level. If two
// location lists have the same priority level, then the location list ID is used
// to disambiguate order.
func (c *LocationStore) SortedLocations() []textapi.Location {
	// re-use previous allocation; clear element slots first so the strings
	// inside textapi.Location (Message, Icon) and *LocationSet pointers do
	// not stay reachable past [:0] until subsequent appends overwrite them.
	clear(c.locsSlice)
	c.locsSlice = c.locsSlice[:0]
	clear(c.locsSliceTemp)
	c.locsSliceTemp = c.locsSliceTemp[:0]
	for _, list := range c.drawLocations {
		c.locsSliceTemp = append(c.locsSliceTemp, list)
	}

	sort.Slice(c.locsSliceTemp, func(i, j int) bool {
		return c.locsSliceTemp[i].Priority <
			c.locsSliceTemp[j].Priority ||
			(c.locsSliceTemp[i].Priority == c.locsSliceTemp[j].Priority &&
				c.locsSliceTemp[i].ID < c.locsSliceTemp[j].ID)
	})

	for _, list := range c.locsSliceTemp {
		c.locsSlice = append(c.locsSlice, list.Locations...)
	}
	return c.locsSlice
}

// LocationLists returns a map of location list IDs to their
// respective locations.
func (c *LocationStore) LocationLists() (ret []LocationSet) {
	for _, list := range c.drawLocations {
		set := *list
		// clone slice
		set.Locations = make([]textapi.Location, len(list.Locations))
		copy(set.Locations, list.Locations)
		ret = append(ret, set)
	}
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Priority < ret[j].Priority ||
			(ret[i].Priority == ret[j].Priority && ret[i].ID < ret[j].ID)
	})
	return
}

// SetLocationList sets a location list on this cursor. It substitutes and returns
// the previous location list with the same ID, if there was any.
// Any calls to Insert on the underlying Writer will reset all location lists.
func (c *LocationStore) SetLocationList(
	pri textapi.LocationPriority, ID string, l LocationList,
) LocationList {
	prev, ok := c.locs[ID]
	if ok {
		c.clearMessages(ID)
		// Clear before truncating: Location holds Message and Icon strings.
		clear(c.drawLocations[ID].Locations)
		c.drawLocations[ID].Locations = c.drawLocations[ID].Locations[:0]
		c.drawLocations[ID].Priority = pri
	} else {
		c.drawLocations[ID] = &LocationSet{ID: ID, Priority: pri}
	}

	if l == nil {
		delete(c.locs, ID)
	} else {
		c.locs[ID] = l
		c.processList(ID, l)
	}

	return prev
}

// LocationsAtCoordinates returns the set of locations by location list ID set by SetLocationList,
// at the given coordinates, if there's any. The returned slice is only valid until
// this method is called again.
func (c *LocationStore) LocationsAtCoordinates(pos term.Coordinates) (
	[]textapi.Location, bool,
) {
	msgs, ok := c.messages[pos]
	if !ok {
		return nil, false
	}
	if len(msgs) == 0 {
		return nil, false
	}
	clear(c.locsAtCoordsTemp)
	c.locsAtCoordsTemp = c.locsAtCoordsTemp[:0]
	for _, msg := range msgs {
		c.locsAtCoordsTemp = append(c.locsAtCoordsTemp, msg.location)
	}
	return c.locsAtCoordsTemp, true
}

// DrawLocations draws the given locations using the given writer.
// This is intended to be used alongside Scroll's Draw method.
func DrawLocations(locations []textapi.Location, scroll *component.Scroll, w term.Writer) {
	if scroll.Width() == 0 {
		return // avoid division by 0 in ScrollToWindowCoordinates
	}

	offset := scroll.Offset()
	if scroll.InvertOffset {
		offset.Y = max(0, scroll.MaxOffset().Y-offset.Y)
	}
	height := scroll.SizeHeight()
	buffer := scroll.Buffer()

	minY := scroll.WindowToScrollCoordinates(term.Coordinates{}).Y
	// maxY is the line on the row just below the scroll. When wrapping,
	// that row can continue the last visible line, so maxY itself may
	// still be partly shown.
	maxY := scroll.WindowToScrollCoordinates(term.Coordinates{Y: height}).Y
	for _, loc := range locations {
		fromAtScroll := loc.From
		toAtScroll := loc.To
		if fromAtScroll.Y > maxY || toAtScroll.Y < minY {
			continue
		}

		fromAtScrollX := fromAtScroll.X
		if fromAtScroll.Y < minY {
			fromAtScroll.Y = minY
			fromAtScrollX = 0
		}
		toAtScroll.Y = int(math.Min(float64(buffer.Rows()-1), float64(toAtScroll.Y)))
		// Rows past maxY map below the scroll, where Less superimposes
		// its message and search bar.
		for y := fromAtScroll.Y; y < toAtScroll.Y && y <= maxY; y++ {
			toX := buffer.Columns(y)
			for x := fromAtScrollX; x < toX; x++ {
				posAtScreen, ok := scroll.ScrollToWindowCoordinates(term.Coordinates{Y: y, X: x})
				if posAtScreen.X >= scroll.Width() || posAtScreen.Y >= height {
					break
				}
				if !ok || posAtScreen.X < 0 {
					if !ok && loc.Message != "" {
						drawHiddenMessage(w, buffer, scroll, posAtScreen, loc)
					}
					continue
				}
				w.UnionAttributes(posAtScreen, loc.Attr)
			}
			fromAtScrollX = 0
		}

		toX := toAtScroll.X
		for x := fromAtScrollX; x < toX; x++ {
			posAtScreen, ok := scroll.ScrollToWindowCoordinates(term.Coordinates{Y: toAtScroll.Y, X: x})
			if posAtScreen.X >= scroll.Width() || posAtScreen.Y >= height {
				break
			}
			if !ok || posAtScreen.X < 0 {
				if !ok && loc.Message != "" {
					drawHiddenMessage(w, buffer, scroll, posAtScreen, loc)
				}
				continue
			}
			w.UnionAttributes(posAtScreen, loc.Attr)
		}
	}
}

// show cue for messages that are hidden inside hidden lines;
// this is a best effort and the last location scanned "wins".
func drawHiddenMessage(
	w term.Writer, buffer *cell.Buffer, scroll *component.Scroll,
	posAtScreen term.Coordinates, loc textapi.Location,
) {
	hiddenLinesStartBlock := scroll.WindowToScrollCoordinates(posAtScreen)
	cols := buffer.Columns(hiddenLinesStartBlock.Y)
	posAtScreen.X = max(0, cols-1)
	w.UnionAttributes(posAtScreen, loc.Attr)
}

// LocationSet is a static view over a textapi.LocationList.
type LocationSet struct {
	ID        string
	Priority  textapi.LocationPriority
	Locations []textapi.Location
}

func (c *LocationStore) clearMessages(ID string) {
	for from, msgs := range c.messages {
		var stay []message
		for _, msg := range msgs {
			if msg.listID == ID {
				continue
			}
			stay = append(stay, msg)
		}
		c.messages[from] = stay
	}
}

func (c *LocationStore) processList(ID string, l LocationList) {
	scrollStartList(l)
	for n, ok := l.Current(); ok; n, ok = l.Next() {
		c.drawLocations[ID].Locations = append(c.drawLocations[ID].Locations, n)
		if n.Message == "" {
			continue
		}
		from, to := term.CoordinatesSort(n.From, n.To)
		for {
			msgs, ok := c.messages[from]
			if !ok {
				msgs = make([]message, 0, 1)
				c.messages[from] = msgs
			}
			c.messages[from] = append(msgs, message{
				listID:   ID,
				location: n,
			})
			if from.Y == to.Y && from.X == to.X {
				break
			}
			if from.Y == to.Y {
				from.X++
				continue
			}

			from.Y++
			from.X = 0
		}
	}
}

func scrollStartList(l LocationList) {
	for ok := true; ok; _, ok = l.Prev() {
	}
}
