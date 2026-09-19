# Windows graphics

Rune's GPU path on Windows is Direct3D 11 (via the
`github.com/unstablebuild/ebiten` fork) plus Desktop Window Manager for
the frame.

| OS | GPU API | compositor extras |
| --- | --- | --- |
| Linux | OpenGL | X11/Wayland window class |
| macOS | Metal | Cocoa glass bar, blur |
| Windows | Direct3D 11 | per-monitor DPI v2, dark title bar, rounded corners, Mica/Acrylic |

`internal/term/gui/wingfx` owns the compositor extras. It is a no-op on
non-Windows builds.

## Build

This branch includes a `go.work` that temporarily replaces:

- `github.com/hajimehoshi/ebiten/v2` -> `Bell8571/ebiten` @ `windows-inithint` ([unstablebuild/ebiten#1](https://github.com/unstablebuild/ebiten/pull/1))
- `github.com/unstablebuild/tcell/v3` -> `Bell8571/tcell` @ `windows-console-screen` ([unstablebuild/tcell#6](https://github.com/unstablebuild/tcell/pull/6))

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags=ebitensinglethread -o rune.exe ./cmd/rune
```

Ebiten on Windows does not need CGO. Drop `go.work` (or its two replace
lines) once those PRs are merged and tagged.

## Remaining upstream work

1. [unstablebuild/ebiten#1](https://github.com/unstablebuild/ebiten/pull/1) — `glfw.InitHint` stub on Windows
2. [unstablebuild/tcell#6](https://github.com/unstablebuild/tcell/pull/6) — missing `Screen` methods on `cScreen`

Transparent themes (`gui.window_opacity`, `gui.window_blur_radius`)
activate Mica on Windows 11 and Acrylic on Windows 10.
