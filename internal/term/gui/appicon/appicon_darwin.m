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

#import <Cocoa/Cocoa.h>

#include <string.h>

// runeSetApplicationIcon copies pix, width*height straight-alpha RGBA
// pixels, because the application keeps the icon after the call returns.
// A NULL pix restores the icon of the application bundle.
void runeSetApplicationIcon(const unsigned char *pix, int width, int height) {
  @autoreleasepool {
    if (pix == NULL || width <= 0 || height <= 0) {
      [NSApp setApplicationIconImage:nil];
      return;
    }
    NSBitmapImageRep *rep = [[NSBitmapImageRep alloc]
        initWithBitmapDataPlanes:NULL
                      pixelsWide:width
                      pixelsHigh:height
                   bitsPerSample:8
                 samplesPerPixel:4
                        hasAlpha:YES
                        isPlanar:NO
                  colorSpaceName:NSDeviceRGBColorSpace
                    bitmapFormat:NSBitmapFormatAlphaNonpremultiplied
                     bytesPerRow:(NSInteger)width * 4
                    bitsPerPixel:32];
    if (rep == nil) {
      return;
    }
    unsigned char *dst = [rep bitmapData];
    if (dst == NULL) {
      [rep release];
      return;
    }
    // AppKit may pad rows beyond the requested bytesPerRow.
    NSInteger stride = [rep bytesPerRow];
    for (int y = 0; y < height; y++) {
      memcpy(dst + (size_t)y * (size_t)stride,
             pix + (size_t)y * (size_t)width * 4, (size_t)width * 4);
    }
    NSImage *image = [[NSImage alloc] initWithSize:NSMakeSize(width, height)];
    [image addRepresentation:rep];
    [NSApp setApplicationIconImage:image];
    [image release];
    [rep release];
  }
}
