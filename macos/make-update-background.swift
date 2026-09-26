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

// Keep the illustration quiet so the installation steps stay in focus.
NSGradient(colorsAndLocations:
    (color(60, 111, 80), 0), (color(28, 72, 55), 0.45), (dark, 1)
)!.draw(in: NSRect(x: 0, y: 0, width: width, height: height), angle: 90)

let glowColors = [color(246, 209, 130, alpha: 0.13).cgColor,
                  color(246, 209, 130, alpha: 0).cgColor] as CFArray
let glow = CGGradient(colorsSpace: CGColorSpaceCreateDeviceRGB(), colors: glowColors,
                      locations: [0, 1])!
context.cgContext.drawRadialGradient(glow, startCenter: CGPoint(x: 150, y: 151), startRadius: 0,
                                     endCenter: CGPoint(x: 150, y: 151), endRadius: 170,
                                     options: [])

// Two speech bubbles use the same cream and brass tones as the app icon.
polygon([CGPoint(x: 79, y: 169), CGPoint(x: 59, y: 137), CGPoint(x: 106, y: 162)],
        fill: color(231, 240, 219, alpha: 0.23))
let firstBubble = NSBezierPath(roundedRect: NSRect(x: 58, y: 163, width: 150, height: 83),
                               xRadius: 19, yRadius: 19)
color(231, 240, 219, alpha: 0.23).setFill()
firstBubble.fill()
firstBubble.lineWidth = 1.5
color(231, 240, 219, alpha: 0.37).setStroke()
firstBubble.stroke()
color(231, 240, 219, alpha: 0.56).setFill()
NSBezierPath(roundedRect: NSRect(x: 79, y: 212, width: 96, height: 5), xRadius: 2.5, yRadius: 2.5).fill()
NSBezierPath(roundedRect: NSRect(x: 79, y: 192, width: 69, height: 5), xRadius: 2.5, yRadius: 2.5).fill()

polygon([CGPoint(x: 207, y: 93), CGPoint(x: 237, y: 65), CGPoint(x: 227, y: 105)],
        fill: color(243, 214, 150, alpha: 0.32))
let replyBubble = NSBezierPath(roundedRect: NSRect(x: 93, y: 88, width: 149, height: 85),
                               xRadius: 19, yRadius: 19)
color(243, 214, 150, alpha: 0.32).setFill()
replyBubble.fill()
replyBubble.lineWidth = 1.5
color(243, 214, 150, alpha: 0.55).setStroke()
replyBubble.stroke()
color(248, 245, 229, alpha: 0.72).setFill()
NSBezierPath(roundedRect: NSRect(x: 114, y: 138, width: 104, height: 5), xRadius: 2.5, yRadius: 2.5).fill()
NSBezierPath(roundedRect: NSRect(x: 114, y: 118, width: 75, height: 5), xRadius: 2.5, yRadius: 2.5).fill()

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
