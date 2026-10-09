# Cursor Shape

The unit of Rune's graphical layout is a cell, an area on the screen that shows a single rune (a character, an emoji, a symbol, or a Kitty graphic).
The gui package works as a whole to display the appropriate mouse cursor shape when it hovers over a cell.

CursorShapeArbiter knows the graphical layout at an abstract level, more precisely, it knows of the graphical components that send messages to it.

No one owns CursorShapeArbiter, it is a singleton that gets instantiated on the first call to CursorShapeArbiter() and returns the same instance on every subsequent call by any component.

TODO generate the rest
