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

// Draws text whose first line starts at `top`, in points from the top of the design.
func text(_ value: String, x: CGFloat, top: CGFloat, width: CGFloat, size: CGFloat,
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
    let height = ceil(measured.height) + 2
    (value as NSString).draw(in: NSRect(x: x, y: CGFloat(designHeight) - top - height, width: width, height: height),
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

    // Finder icon centers are in points from the top left (see dmg-settings.py);
    // flip them into the design's bottom-left coordinates.
    func flip(_ y: CGFloat) -> CGFloat { CGFloat(designHeight) - y }

    text("PHAROS  \(version)", x: 34, top: 28, width: 230, size: 15,
         weight: .semibold, foreground: moss)

    // One row per place Pharos can live. The middle row is the Applications
    // link at (210, 180); the others name a place the user opens in Finder.
    let rows: [CGFloat] = [96, 180, 304]
    for divider: CGFloat in [140, 256, 362] {
        line(from: CGPoint(x: 150, y: flip(divider)), to: CGPoint(x: 276, y: flip(divider)),
             color: moss.withAlphaComponent(0.22))
        line(from: CGPoint(x: 307, y: flip(divider)), to: CGPoint(x: 725, y: flip(divider)),
             color: color(240, 246, 222, alpha: 0.25))
    }

    // Arrows fork from the Pharos icon at (87, 180) to each row's destination.
    let trunk: CGFloat = 140, tip: CGFloat = 168, radius: CGFloat = 12
    let fork = NSBezierPath()
    fork.move(to: CGPoint(x: 126, y: flip(180)))
    fork.line(to: CGPoint(x: tip, y: flip(180)))
    for row in [rows[0], rows[2]] {
        let turn: CGFloat = row < 180 ? radius : -radius
        fork.move(to: CGPoint(x: trunk, y: flip(180)))
        fork.line(to: CGPoint(x: trunk, y: flip(row + turn)))
        fork.curve(to: CGPoint(x: trunk + radius, y: flip(row)),
                   controlPoint1: CGPoint(x: trunk, y: flip(row)),
                   controlPoint2: CGPoint(x: trunk, y: flip(row)))
        fork.line(to: CGPoint(x: tip, y: flip(row)))
    }
    fork.lineWidth = 2
    fork.lineCapStyle = .round
    fork.lineJoinStyle = .round
    amber.setStroke()
    fork.stroke()
    amber.setFill()
    for row in rows {
        let head = NSBezierPath()
        head.move(to: CGPoint(x: tip + 4, y: flip(row)))
        head.line(to: CGPoint(x: tip - 5, y: flip(row) + 5))
        head.line(to: CGPoint(x: tip - 5, y: flip(row) - 5))
        head.close()
        head.fill()
    }

    // Drawn destinations for the rows without a Finder icon, sized to match one.
    func label(_ value: String, center: CGFloat) {
        let attributes: [NSAttributedString.Key: Any] = [
            .font: NSFont.systemFont(ofSize: 12, weight: .medium), .foregroundColor: moss,
        ]
        let measured = (value as NSString).size(withAttributes: attributes)
        (value as NSString).draw(at: CGPoint(x: 210 - measured.width / 2, y: flip(center) - 34),
                                 withAttributes: attributes)
    }
    let configuration = NSImage.SymbolConfiguration(pointSize: 30, weight: .regular)
        .applying(NSImage.SymbolConfiguration(hierarchicalColor: moss))
    if let drive = NSImage(systemSymbolName: "externaldrive", accessibilityDescription: nil)?
        .withSymbolConfiguration(configuration) {
        let size = drive.size
        drive.draw(in: NSRect(x: 210 - size.width / 2, y: flip(rows[0]) - size.height / 2 + 6,
                              width: size.width, height: size.height))
    }
    label("Your SSD", center: rows[0])

    // An outline of the Pharos lighthouse, drawn on a 24-point grid from the top
    // left and stroked to match the weight of the drive symbol above.
    let unit: CGFloat = 1.85
    func grid(_ x: CGFloat, _ y: CGFloat) -> CGPoint {
        CGPoint(x: 210 + (x - 12) * unit, y: flip(rows[2]) + 7 - (y - 12) * unit)
    }
    let lighthouse = NSBezierPath()
    lighthouse.move(to: grid(7, 21.5))
    lighthouse.line(to: grid(8.8, 12))
    lighthouse.line(to: grid(15.2, 12))
    lighthouse.line(to: grid(17, 21.5))
    lighthouse.close()
    lighthouse.move(to: grid(7.4, 12))
    lighthouse.line(to: grid(16.6, 12))
    lighthouse.appendRect(NSRect(x: grid(9.6, 12).x, y: grid(9.6, 12).y,
                                 width: 4.8 * unit, height: 4.4 * unit))
    lighthouse.move(to: grid(9, 7.6))
    lighthouse.curve(to: grid(12, 4), controlPoint1: grid(9, 5.4), controlPoint2: grid(10.4, 4.3))
    lighthouse.curve(to: grid(15, 7.6), controlPoint1: grid(13.6, 4.3), controlPoint2: grid(15, 5.4))
    lighthouse.close()
    lighthouse.move(to: grid(10.8, 21.5))
    lighthouse.line(to: grid(10.8, 19))
    lighthouse.curve(to: grid(13.2, 19), controlPoint1: grid(10.8, 17.4), controlPoint2: grid(13.2, 17.4))
    lighthouse.line(to: grid(13.2, 21.5))
    for (from, to) in [((6.6, 8.6), (2.6, 7.4)), ((17.4, 8.6), (21.4, 7.4)), ((17.4, 10.4), (20.6, 11.8))] {
        lighthouse.move(to: grid(from.0, from.1))
        lighthouse.line(to: grid(to.0, to.1))
    }
    lighthouse.lineWidth = 2
    lighthouse.lineCapStyle = .round
    lighthouse.lineJoinStyle = .round
    moss.setStroke()
    lighthouse.stroke()
    label("Installed copy", center: rows[2])

    let left: CGFloat = 307
    let contentWidth: CGFloat = 418
    text("Drag Pharos to one place", x: left, top: 22, width: contentWidth, size: 21, weight: .bold)
    text("Portable · a folder on your SSD", x: left, top: 70, width: contentWidth, size: 15,
         weight: .semibold, foreground: gold)
    text("Open your SSD in a new Finder window and drag Pharos there. Open it and choose “Create Library Beside App.”",
         x: left, top: 94, width: contentWidth, size: 13, foreground: soft)
    text("This Mac · Applications", x: left, top: 162, width: contentWidth, size: 15,
         weight: .semibold, foreground: gold)
    text("Drag Pharos onto Applications. Open it and choose “Set Up on This Mac.”",
         x: left, top: 186, width: contentWidth, size: 13, foreground: soft)
    text("Updating · wherever Pharos lives now", x: left, top: 270, width: contentWidth, size: 15,
         weight: .semibold, foreground: gold)
    text("Quit Pharos. Drag this app over your installed copy, on the SSD or in Applications, and choose Replace. Then reopen it and approve access prompts.",
         x: left, top: 294, width: contentWidth, size: 13, foreground: soft)
    text("Already installed? Replace it. Don’t add a second copy.",
         x: left, top: 376, width: contentWidth, size: 12, weight: .semibold, foreground: cream)
    text("Blocked by macOS? Privacy & Security → Open Anyway.",
         x: left, top: 398, width: contentWidth, size: 12, foreground: cream)

    NSGraphicsContext.restoreGraphicsState()
    guard let data = bitmap.representation(using: .png, properties: [:]) else { fatalError("Could not encode install guide") }
    return data
}

try render(scale: 1).write(to: URL(fileURLWithPath: prefix + ".png"))
try render(scale: 2).write(to: URL(fileURLWithPath: prefix + "@2x.png"))
