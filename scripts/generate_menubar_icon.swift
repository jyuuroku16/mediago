import AppKit

// Draw the full app icon as an 18 pt black-and-transparent template.
// Outline the white outer tile, fill the blue inner tile, and cut out its white glyph.
// Inner tile and glyph geometry matches scripts/build_production_icon.py.
// Run: swift scripts/generate_menubar_icon.swift
let output = URL(fileURLWithPath: "internal/ui/resources/menubar-download.png")
let size = 36
let bitmap = NSBitmapImageRep(
    bitmapDataPlanes: nil, pixelsWide: size, pixelsHigh: size,
    bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
    isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: size * 4, bitsPerPixel: 32
)!
let context = NSGraphicsContext(bitmapImageRep: bitmap)!
NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = context
context.cgContext.clear(CGRect(x: 0, y: 0, width: size, height: size))
context.cgContext.scaleBy(x: 2, y: 2)
// The outer contour distinguishes the original white tile from transparency.
NSColor.black.setStroke()
let outer = NSBezierPath(roundedRect: NSRect(x: 0.75, y: 0.75, width: 16.5, height: 16.5), xRadius: 4.5, yRadius: 4.5)
outer.lineWidth = 0.8
outer.stroke()
// Preserve the complete icon proportions, centered inside the outer contour.
context.cgContext.translateBy(x: 1, y: 17)
context.cgContext.scaleBy(x: 16.0 / 1024.0, y: -16.0 / 1024.0)
NSColor.black.setFill()
NSBezierPath(roundedRect: NSRect(x: 100, y: 100, width: 824, height: 824), xRadius: 185, yRadius: 185).fill()
// Transparent negative space replaces the white arrow and baseline.
context.cgContext.setBlendMode(.clear)
NSBezierPath(roundedRect: NSRect(x: 466, y: 270, width: 92, height: 230), xRadius: 24, yRadius: 24).fill()
let arrow = NSBezierPath()
arrow.move(to: NSPoint(x: 320, y: 480))
arrow.curve(to: NSPoint(x: 308, y: 510), controlPoint1: NSPoint(x: 304, y: 480), controlPoint2: NSPoint(x: 296, y: 498))
arrow.line(to: NSPoint(x: 498, y: 700))
arrow.curve(to: NSPoint(x: 526, y: 700), controlPoint1: NSPoint(x: 506, y: 708), controlPoint2: NSPoint(x: 518, y: 708))
arrow.line(to: NSPoint(x: 716, y: 510))
arrow.curve(to: NSPoint(x: 704, y: 480), controlPoint1: NSPoint(x: 728, y: 498), controlPoint2: NSPoint(x: 720, y: 480))
arrow.close()
arrow.fill()
NSBezierPath(roundedRect: NSRect(x: 300, y: 746, width: 424, height: 42), xRadius: 21, yRadius: 21).fill()
NSGraphicsContext.restoreGraphicsState()
try bitmap.representation(using: .png, properties: [:])!.write(to: output)
print(output.path)
