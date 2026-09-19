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

//go:build windows

package wingfx

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const supported = true

const (
	dpiAwarenessContextPerMonitorAwareV2 = ^uintptr(3) // (DPI_AWARENESS_CONTEXT)-4

	dwmwaUseImmersiveDarkMode = 20
	dwmwaWindowCornerPref     = 33
	dwmwaSystemBackdropType   = 38
	dwmwcpRound               = 2
	dwmsbtNone                = 1
	dwmsbtMainWindow          = 2
	dwmsbtTransientWindow     = 3

	accentEnableBlurbehind  = 3
	accentEnableAcrylicblur = 4
	wcaAccentPolicy         = 19
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	dwmapi = windows.NewLazySystemDLL("dwmapi.dll")
	shcore = windows.NewLazySystemDLL("shcore.dll")

	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDpiAwareness        = shcore.NewProc("SetProcessDpiAwareness")
	procFindWindowW                   = user32.NewProc("FindWindowW")
	procGetForegroundWindow           = user32.NewProc("GetForegroundWindow")
	procGetActiveWindow               = user32.NewProc("GetActiveWindow")
	procGetWindowTextW                = user32.NewProc("GetWindowTextW")
	procDwmSetWindowAttribute         = dwmapi.NewProc("DwmSetWindowAttribute")
	procSetWindowCompositionAttribute = user32.NewProc("SetWindowCompositionAttribute")
)

func enableProcessDPI() {
	if procSetProcessDpiAwarenessContext.Find() == nil {
		_, _, _ = procSetProcessDpiAwarenessContext.Call(dpiAwarenessContextPerMonitorAwareV2)
		return
	}
	if procSetProcessDpiAwareness.Find() == nil {
		_, _, _ = procSetProcessDpiAwareness.Call(2)
	}
}

func applyWindowEffects(title string, fx Effects) bool {
	hwnd := resolveHWND(title)
	if hwnd == 0 {
		return false
	}

	dark := uint32(1)
	_, _, _ = procDwmSetWindowAttribute.Call(hwnd, uintptr(dwmwaUseImmersiveDarkMode), uintptr(unsafe.Pointer(&dark)), unsafe.Sizeof(dark))

	corners := uint32(dwmwcpRound)
	_, _, _ = procDwmSetWindowAttribute.Call(hwnd, uintptr(dwmwaWindowCornerPref), uintptr(unsafe.Pointer(&corners)), unsafe.Sizeof(corners))

	if !fx.Transparent && fx.BlurRadius <= 0 {
		none := uint32(dwmsbtNone)
		_, _, _ = procDwmSetWindowAttribute.Call(hwnd, uintptr(dwmwaSystemBackdropType), uintptr(unsafe.Pointer(&none)), unsafe.Sizeof(none))
		return true
	}

	backdrop := uint32(dwmsbtMainWindow)
	if fx.BlurRadius > 0 {
		backdrop = dwmsbtTransientWindow
	}
	r, _, _ := procDwmSetWindowAttribute.Call(hwnd, uintptr(dwmwaSystemBackdropType), uintptr(unsafe.Pointer(&backdrop)), unsafe.Sizeof(backdrop))
	if r == 0 {
		return true
	}
	applyAcrylic(hwnd, fx.BlurRadius)
	return true
}

type accentPolicy struct {
	AccentState   uint32
	AccentFlags   uint32
	GradientColor uint32
	AnimationId   uint32
}

type windowCompositionAttribData struct {
	Attrib uint32
	Data   uintptr
	SizeOf uint32
}

func applyAcrylic(hwnd uintptr, blurRadius int) {
	if procSetWindowCompositionAttribute.Find() != nil {
		return
	}
	state := uint32(accentEnableBlurbehind)
	if blurRadius > 0 {
		state = accentEnableAcrylicblur
	}
	policy := accentPolicy{
		AccentState:   state,
		AccentFlags:   2,
		GradientColor: 0x99000000,
	}
	data := windowCompositionAttribData{
		Attrib: wcaAccentPolicy,
		Data:   uintptr(unsafe.Pointer(&policy)),
		SizeOf: uint32(unsafe.Sizeof(policy)),
	}
	_, _, _ = procSetWindowCompositionAttribute.Call(hwnd, uintptr(unsafe.Pointer(&data)))
}

func resolveHWND(title string) uintptr {
	if title != "" {
		ptr, err := windows.UTF16PtrFromString(title)
		if err == nil && procFindWindowW.Find() == nil {
			hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(ptr)))
			if hwnd != 0 {
				return hwnd
			}
		}
	}
	if procGetActiveWindow.Find() == nil {
		hwnd, _, _ := procGetActiveWindow.Call()
		if hwnd != 0 && titleMatches(hwnd, title) {
			return hwnd
		}
		if hwnd != 0 && title == "" {
			return hwnd
		}
	}
	if procGetForegroundWindow.Find() == nil {
		hwnd, _, _ := procGetForegroundWindow.Call()
		if hwnd != 0 {
			return hwnd
		}
	}
	return 0
}

func titleMatches(hwnd uintptr, want string) bool {
	if want == "" {
		return true
	}
	var buf [256]uint16
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return false
	}
	return windows.UTF16ToString(buf[:n]) == want
}
