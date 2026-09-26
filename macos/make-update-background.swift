import AppKit
import Foundation

guard CommandLine.arguments.count == 3 else {
    fputs("Usage: swift make-update-background.swift OUTPUT.png VERSION\n", stderr)
    exit(2)
}

let output = URL(fileURLWithPath: CommandLine.arguments[1])
let version = CommandLine.arguments[2]
let width = 900, height = 560, scale = 2
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
    paragraph.lineSpacing = 3
    let attributes: [NSAttributedString.Key: Any] = [
        .font: NSFont.systemFont(ofSize: size, weight: weight),
        .foregroundColor: foreground,
        .paragraphStyle: paragraph,
    ]
    let measured = (value as NSString).boundingRect(with: NSSize(width: width, height: 140),
                                                     options: [.usesLineFragmentOrigin], attributes: attributes)
    (value as NSString).draw(in: NSRect(x: x, y: y, width: width, height: ceil(measured.height) + 2),
                             withAttributes: attributes)
}

func polygon(_ points: [CGPoint], fill: NSColor) {
    let path = NSBezierPath()
    path.move(to: points[0])
    for point in points.dropFirst() { path.line(to: point) }
    path.close()
    fill.setFill()
    path.fill()
}

func line(from start: CGPoint, to end: CGPoint, color: NSColor, width: CGFloat = 1) {
    let path = NSBezierPath()
    path.move(to: start)
    path.line(to: end)
    path.lineWidth = width
    color.setStroke()
    path.stroke()
}

NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = context
context.cgContext.scaleBy(x: CGFloat(scale), y: CGFloat(scale))

// The forest-green gradient and lighthouse echo the app icon without reducing text contrast.
NSGradient(colorsAndLocations:
    (color(60, 111, 80), 0), (color(28, 72, 55), 0.45), (dark, 1)
)!.draw(in: NSRect(x: 0, y: 0, width: width, height: height), angle: 90)

let glowColors = [color(246, 209, 130, alpha: 0.22).cgColor,
                  color(246, 209, 130, alpha: 0).cgColor] as CFArray
let glow = CGGradient(colorsSpace: CGColorSpaceCreateDeviceRGB(), colors: glowColors,
                      locations: [0, 1])!
context.cgContext.drawRadialGradient(glow, startCenter: CGPoint(x: 150, y: 214), startRadius: 3,
                                     endCenter: CGPoint(x: 150, y: 214), endRadius: 230,
                                     options: [])
polygon([CGPoint(x: 146, y: 208), CGPoint(x: 154, y: 220),
         CGPoint(x: 300, y: 262), CGPoint(x: 300, y: 224)],
        fill: color(246, 209, 130, alpha: 0.065))

// A simplified version of the icon's lantern, tapered tower, and gold trim.
polygon([CGPoint(x: 107, y: 54), CGPoint(x: 193, y: 54),
         CGPoint(x: 176, y: 165), CGPoint(x: 124, y: 165)],
        fill: color(231, 240, 219, alpha: 0.16))
polygon([CGPoint(x: 121, y: 165), CGPoint(x: 179, y: 165),
         CGPoint(x: 171, y: 203), CGPoint(x: 129, y: 203)],
        fill: color(231, 240, 219, alpha: 0.20))
color(243, 214, 150, alpha: 0.32).setFill()
NSRect(x: 117, y: 163, width: 66, height: 5).fill()
NSRect(x: 127, y: 201, width: 46, height: 5).fill()
NSRect(x: 137, y: 210, width: 26, height: 23).fill()
polygon([CGPoint(x: 132, y: 234), CGPoint(x: 168, y: 234), CGPoint(x: 150, y: 249)],
        fill: color(243, 214, 150, alpha: 0.45))
color(243, 214, 150, alpha: 0.23).setFill()
NSRect(x: 102, y: 51, width: 96, height: 4).fill()

line(from: CGPoint(x: 281, y: 46), to: CGPoint(x: 281, y: 513),
     color: color(240, 246, 222, alpha: 0.19))

text("PHAROS  \(version)", x: 58, y: 495, width: 190, size: 13,
     weight: .semibold, foreground: soft)
text("DRAG THIS APP", x: 83, y: 284, width: 150, size: 13,
     weight: .semibold, foreground: cream)

let left: CGFloat = 320
let contentWidth: CGFloat = 536
text("First time install", x: left, y: 470, width: contentWidth, size: 31, weight: .bold)

text("SSD · portable library", x: left, y: 423, width: contentWidth, size: 17,
     weight: .semibold, foreground: gold)
text("Make a Pharos folder on your SSD. Drag the app there, open it, and choose “Create Library Beside App.” App and data stay together.",
     x: left, y: 357, width: contentWidth, size: 16, foreground: soft)

text("Applications · this Mac", x: left, y: 310, width: contentWidth, size: 17,
     weight: .semibold, foreground: gold)
text("Drag the app into Applications. Open it and choose “Set Up on This Mac.” Data goes in ~/Library/Application Support/Pharos.",
     x: left, y: 244, width: contentWidth, size: 16, foreground: soft)

line(from: CGPoint(x: left, y: 225), to: CGPoint(x: 856, y: 225),
     color: color(240, 246, 222, alpha: 0.29))
text("Updating?", x: left, y: 175, width: contentWidth, size: 31, weight: .bold)
text("Quit Pharos. Drag this app over your installed copy (SSD or Applications), choose Replace, then reopen and approve the access prompts.",
     x: left, y: 111, width: contentWidth, size: 16, foreground: soft)
text("macOS blocked it? Privacy & Security → Open Anyway.",
     x: left, y: 58, width: contentWidth, size: 14, foreground: cream)

NSGraphicsContext.restoreGraphicsState()

guard let data = bitmap.representation(using: .png, properties: [:]) else { fatalError("Could not encode install guide") }
try data.write(to: output)
