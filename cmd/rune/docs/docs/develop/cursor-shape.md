# Cursor Shape

The unit of Rune's graphical layout is a cell, an area on the screen that shows a single rune (a character, an emoji, a symbol, or a Kitty graphic).
The gui package works as a whole to display the appropriate mouse cursor shape when it hovers over a cell.

CursorShapeArbiter knows the graphical layout at an abstract level, more precisely, it knows of the graphical components that send messages to it. It has an idea of what component is under the cursor (it calls these ObjectUnderCursors), as well as whether the cursor is currently right next to a component (also ObjectUnderCursor, but where ComponentA would be an object, it calls the neighboring cell a ComponentANeighbor).
The Neighbor notation is used to deconflict cursor shapes: a graphical component that sets a cursor shape when a cursor hovers over it would probably reset the cursor shape when the mouse moves away from it, onto the Neighbor cell. Two neighboring components are where conflicts arise: when the mouse moves from one component to another, the first component would want to reset the cursor shape, while the second component would want to set a certain cursor shape. Arbitration is needed when the cursor shapes are different; without it, one of the shapes would be displayed instead of the other depending on external factors such as function evaluation order or code execution order.

No one owns CursorShapeArbiter, it is a singleton that gets instantiated on first GetCursorShapeArbiter and returns the same instance on every subsequent call by any component.

The currently known cursor shape conflicts happen at:

1. Armed link (armed meaning the meta-key is held down) next to the resize border of a window.

2. Scroll bar next to the resize border of a window.

3. Armed link next to a scroll bar.

Accordingly, the CursorShapeArbiter has this conceptual knowledge of the layout:

Legend:
- `[  ]` Empty cell
  `[  ]`
- `[L ]` Link
  `[  ]`
- `[LN]` Cell neighboring a link
  `[  ]`
- `[S ]` Scroll bar
  `[  ]`
- `[SN]` Cell neighboring a scroll bar
  `[  ]`
- `[R ]` Resize border
  `[  ]`
- `[RN]` Cell neighboring a resize border
  `[  ]`
- `[A ]` Component A at this cell, and the cell is also a neighbor of component B
  `[BN]`
- `[AN]` The cell is a neighbor of component A, and the cell is also a neighbor of component B
  `[BN]`

Here is an example of the cells in a graphical layout:
TODO not sure if object's corners are its neighbors, the diagram looks cleaner if they are not
```
Window:
[  ][  ][  ][  ][  ][  ][  ][SN][S ][  ]
[  ][  ][  ][  ][  ][  ][  ][  ][  ][  ]
[  ][  ][  ][  ][  ][  ][  ][SN][S ][  ]
[  ][  ][  ][  ][  ][  ][  ][  ][  ][  ]
[  ][  ][  ][  ][  ][  ][  ][SN][S ][  ]
[  ][  ][  ][  ][  ][  ][  ][  ][  ][  ]
[  ][  ][  ][  ][  ][  ][  ][SN][S ][  ]
[  ][  ][  ][  ][  ][  ][  ][  ][RN][  ]
[  ][  ][  ][  ][  ][  ][  ][RN][R ][  ]
[  ][  ][  ][  ][  ][  ][  ][  ][SN][  ]
[  ][  ][  ][  ][  ][  ][  ][RN][R ][  ]
[  ][  ][  ][  ][  ][  ][  ][  ][  ][  ]
[  ][  ][  ][  ][  ][  ][  ][RN][R ][  ]
[  ][  ][  ][  ][  ][  ][  ][  ][  ][  ]
[  ][  ][  ][  ][  ][  ][  ][RN][R ][  ]
[  ][  ][  ][  ][  ][  ][  ][  ][  ][  ]
[  ][  ][  ][  ][  ][  ][  ][RN][R ][  ]
[  ][  ][  ][  ][  ][  ][  ][  ][  ][  ]
[  ][LN][LN][LN][LN][LN][LN][LN][R ][  ]
[  ][  ][  ][  ][  ][  ][  ][RN][  ][  ]
[  ][LN][L ][L ][L ][L ][L ][L ][R ][  ]
[  ][  ][  ][  ][  ][  ][  ][RN][LN][  ]
[  ][LN][LN][LN][LN][LN][LN][LN][R ][  ]
[  ][  ][  ][  ][  ][  ][  ][RN][  ][  ]
---------------
```

Cursor shape is arbitrated by the CursorShapeArbiter, of which there can only be one instance.

TODO generate the rest
