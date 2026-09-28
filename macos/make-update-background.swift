import AppKit
import Foundation

guard CommandLine.arguments.count == 3 else {
    fputs("Usage: swift make-update-background.swift OUTPUT_PREFIX VERSION\n", stderr)
    exit(2)
}

// Writes OUTPUT_PREFIX.png and OUTPUT_PREFIX@2x.png; tiffutil joins them so
// Finder lays out in points and draws the 2x image on Retina displays.
let prefix = CommandLine.arguments[1]
let version = CommandLine.arguments[2]
// The design fills 760 x 440 points. The canvas runs taller so that any
// difference in Finder's title bar height reveals more gradient, not a blank strip.
let width = 760, height = 480, designHeight = 440

func color(_ red: Int, _ green: Int, _ blue: Int, alpha: CGFloat = 1) -> NSColor {
    NSColor(srgbRed: CGFloat(red) / 255, green: CGFloat(green) / 255,
            blue: CGFloat(blue) / 255, alpha: alpha)
}

let dark = color(12, 35, 29)
let cream = color(248, 245, 229)
let soft = color(222, 236, 217)
let gold = color(245, 205, 126)
let paper = color(246, 243, 228)
let moss = color(58, 92, 74)
let amber = color(176, 124, 36)

func text(_ value: String, x: CGFloat, y: CGFloat, width: CGFloat, size: CGFloat,
          weight: NSFont.Weight = .regular, foreground: NSColor = cream) {
    let paragraph = NSMutableParagraphStyle()
    paragraph.lineSpacing = 2
    let attributes: [NSAttributedString.Key: Any] = [
        .font: NSFont.systemFont(ofSize: size, weight: weight),
        .foregroundColor: foreground,
        .paragraphStyle: paragraph,
    ]
    let measured = (value as NSString).boundingRect(with: NSSize(width: width, height: 100),
                                                     options: [.usesLineFragmentOrigin], attributes: attributes)
    (value as NSString).draw(in: NSRect(x: x, y: y, width: width, height: ceil(measured.height) + 2),
                             withAttributes: attributes)
}

func line(from start: CGPoint, to end: CGPoint, color: NSColor) {
    let path = NSBezierPath()
    path.move(to: start)
    path.line(to: end)
    path.lineWidth = 1
    color.setStroke()
    path.stroke()
}

func render(scale: Int) -> Data {
    // Finder ignores backgrounds with alpha on some releases, so draw opaque.
    guard let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: width * scale,
                                      pixelsHigh: height * scale, bitsPerSample: 8,
                                      samplesPerPixel: 3, hasAlpha: false, isPlanar: false,
                                      colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 32),
          let context = NSGraphicsContext(bitmapImageRep: bitmap) else { fatalError("Could not draw install guide") }
    // Record the point size so the 2x file carries 144 DPI, which tiffutil checks.
    bitmap.size = NSSize(width: width, height: height)

    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = context
    context.cgContext.scaleBy(x: CGFloat(scale), y: CGFloat(scale))

    NSGradient(colorsAndLocations:
        (color(48, 94, 69), 0), (color(26, 69, 53), 0.5), (dark, 1)
    )!.draw(in: NSRect(x: 0, y: 0, width: width, height: height), angle: 90)
    // Finder draws icon labels in black over any background picture, even in
    // Dark Mode, so the icons sit on a light panel.
    paper.setFill()
    NSRect(x: 0, y: 0, width: 276, height: height).fill()
    // Lay out the design against the top edge, where Finder anchors the picture.
    context.cgContext.translateBy(x: 0, y: CGFloat(height - designHeight))

    text("PHAROS  \(version)", x: 34, y: 393, width: 230, size: 15,
         weight: .semibold, foreground: moss)
    text("Drag to install", x: 43, y: 305, width: 200, size: 19, weight: .bold, foreground: dark)
    // Centered between the Finder icons at (87, 180) and (210, 180).
    text("→", x: 131, y: 246, width: 35, size: 23, foreground: amber)
    text("For a portable library, drag Pharos.app to a folder on your SSD instead.",
         x: 39, y: 96, width: 212, size: 13, foreground: moss)

    let left: CGFloat = 307
    let contentWidth: CGFloat = 418
    text("First time install", x: left, y: 380, width: contentWidth, size: 23, weight: .bold)
    text("Portable · SSD", x: left, y: 331, width: contentWidth, size: 15,
         weight: .semibold, foreground: gold)
    text("Make a Pharos folder on your SSD. Drag the app there, open it, and choose “Create Library Beside App.”",
         x: left, y: 275, width: contentWidth, size: 13, foreground: soft)
    text("This Mac · Applications", x: left, y: 237, width: contentWidth, size: 15,
         weight: .semibold, foreground: gold)
    text("Drag the app onto Applications. Open it and choose “Set Up on This Mac.”",
         x: left, y: 195, width: contentWidth, size: 13, foreground: soft)
    line(from: CGPoint(x: left, y: 163), to: CGPoint(x: 725, y: 163),
         color: color(240, 246, 222, alpha: 0.25))
    text("Updating?", x: left, y: 127, width: contentWidth, size: 19, weight: .bold)
    text("Quit Pharos. Drag this app over your installed copy and choose Replace. Then reopen it and approve access prompts.",
         x: left, y: 71, width: contentWidth, size: 13, foreground: soft)
    text("Blocked by macOS? Privacy & Security → Open Anyway.",
         x: left, y: 32, width: contentWidth, size: 12, foreground: cream)

    NSGraphicsContext.restoreGraphicsState()
    guard let data = bitmap.representation(using: .png, properties: [:]) else { fatalError("Could not encode install guide") }
    return data
}

try render(scale: 1).write(to: URL(fileURLWithPath: prefix + ".png"))
try render(scale: 2).write(to: URL(fileURLWithPath: prefix + "@2x.png"))
