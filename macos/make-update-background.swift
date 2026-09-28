import AppKit
import Foundation

guard CommandLine.arguments.count == 3 else {
    fputs("Usage: swift make-update-background.swift OUTPUT.png VERSION\n", stderr)
    exit(2)
}

let output = URL(fileURLWithPath: CommandLine.arguments[1])
let version = CommandLine.arguments[2]
// Finder uses points for its window and icon positions. Draw at 2x for Retina.
let width = 760, height = 440, scale = 2
guard let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: width * scale,
                                  pixelsHigh: height * scale, bitsPerSample: 8,
                                  samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                                  colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0),
      let context = NSGraphicsContext(bitmapImageRep: bitmap) else { fatalError("Could not draw install guide") }

func color(_ red: Int, _ green: Int, _ blue: Int, alpha: CGFloat = 1) -> NSColor {
    NSColor(srgbRed: CGFloat(red) / 255, green: CGFloat(green) / 255,
            blue: CGFloat(blue) / 255, alpha: alpha)
}

let dark = color(12, 35, 29)
let cream = color(248, 245, 229)
let soft = color(222, 236, 217)
let gold = color(245, 205, 126)

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

NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = context
context.cgContext.scaleBy(x: CGFloat(scale), y: CGFloat(scale))

NSGradient(colorsAndLocations:
    (color(48, 94, 69), 0), (color(26, 69, 53), 0.5), (dark, 1)
)!.draw(in: NSRect(x: 0, y: 0, width: width, height: height), angle: 90)

text("PHAROS  \(version)", x: 34, y: 393, width: 230, size: 15,
     weight: .semibold, foreground: soft)
text("Drag to install", x: 43, y: 305, width: 200, size: 19, weight: .bold)
// The actual Finder icons sit above this simple directional cue.
text("→", x: 131, y: 221, width: 35, size: 23, foreground: gold)
text("For a portable library, drag Pharos.app to a folder on your SSD instead.",
     x: 39, y: 96, width: 212, size: 13, foreground: soft)
line(from: CGPoint(x: 276, y: 34), to: CGPoint(x: 276, y: 406),
     color: color(240, 246, 222, alpha: 0.25))

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
try data.write(to: output)
