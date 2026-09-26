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
      let context = NSGraphicsContext(bitmapImageRep: bitmap) else { fatalError("Could not draw update guide") }

func color(_ red: Int, _ green: Int, _ blue: Int) -> NSColor {
    NSColor(srgbRed: CGFloat(red) / 255, green: CGFloat(green) / 255, blue: CGFloat(blue) / 255, alpha: 1)
}

let dark = color(16, 36, 27)
let green = color(43, 82, 65)
let brass = color(212, 168, 87)
let cream = color(243, 235, 220)
let pale = color(204, 219, 205)

func text(_ value: String, x: CGFloat, y: CGFloat, width: CGFloat, size: CGFloat,
          weight: NSFont.Weight = .regular, foreground: NSColor = cream) {
    let paragraph = NSMutableParagraphStyle()
    paragraph.lineSpacing = 4
    let attributes: [NSAttributedString.Key: Any] = [
        .font: NSFont.systemFont(ofSize: size, weight: weight),
        .foregroundColor: foreground,
        .paragraphStyle: paragraph,
    ]
    let measured = (value as NSString).boundingRect(with: NSSize(width: width, height: 120),
                                                     options: [.usesLineFragmentOrigin], attributes: attributes)
    (value as NSString).draw(in: NSRect(x: x, y: y, width: width, height: ceil(measured.height) + 2),
                             withAttributes: attributes)
}

NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = context
context.cgContext.scaleBy(x: CGFloat(scale), y: CGFloat(scale))
dark.setFill()
NSRect(x: 0, y: 0, width: width, height: height).fill()
green.setFill()
NSRect(x: 0, y: height - 170, width: width, height: 170).fill()
brass.setFill()
NSRect(x: 48, y: height - 184, width: width - 96, height: 3).fill()

text("PHAROS  /  INSTALL & UPDATE", x: 50, y: 506, width: 600, size: 14, weight: .semibold, foreground: brass)
text("Install Pharos \(version)", x: 48, y: 433, width: 800, size: 39, weight: .bold)
text("Drag the app to one of these destinations.", x: 50, y: 397, width: 800, size: 18, foreground: pale)

let iconArea = NSBezierPath(roundedRect: NSRect(x: 50, y: 105, width: 224, height: 243), xRadius: 18, yRadius: 18)
color(31, 60, 47).setFill()
iconArea.fill()
text("PHAROS.APP", x: 75, y: 313, width: 175, size: 13, weight: .semibold, foreground: brass)
text("DRAG FROM HERE", x: 75, y: 120, width: 175, size: 16, weight: .semibold)

text("01", x: 310, y: 310, width: 45, size: 20, weight: .bold, foreground: brass)
text("SSD / PORTABLE LIBRARY", x: 358, y: 311, width: 490, size: 19, weight: .semibold)
text("Make a Pharos folder on your SSD. Drag the app there, open it, then choose Create Library Beside App. Your data stays in that folder.",
     x: 358, y: 235, width: 490, size: 15, foreground: pale)

text("02", x: 310, y: 190, width: 45, size: 20, weight: .bold, foreground: brass)
text("APPLICATIONS / THIS MAC", x: 358, y: 191, width: 490, size: 19, weight: .semibold)
text("Drag the app to Applications. Open it, then choose Set Up on This Mac. Data goes in ~/Library/Application Support/Pharos, not Applications.",
     x: 358, y: 115, width: 490, size: 15, foreground: pale)

brass.setFill()
NSRect(x: 50, y: 88, width: 800, height: 1).fill()
text("UPDATING? Quit Pharos. Drag this app over the original. Choose Replace. Reopen and allow access.",
     x: 50, y: 52, width: 800, size: 15, weight: .medium)
text("If macOS blocks opening, use Privacy & Security → Open Anyway.",
     x: 50, y: 21, width: 800, size: 14, foreground: pale)
NSGraphicsContext.restoreGraphicsState()

guard let data = bitmap.representation(using: .png, properties: [:]) else { fatalError("Could not encode update guide") }
try data.write(to: output)
