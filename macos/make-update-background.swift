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

text("PHAROS  /  UPDATE GUIDE", x: 50, y: 506, width: 600, size: 14, weight: .semibold, foreground: brass)
text("Install Pharos \(version)", x: 48, y: 433, width: 800, size: 39, weight: .bold)
text("Keep your library. Replace only Pharos.app.", x: 50, y: 397, width: 800, size: 18, foreground: pale)

let iconArea = NSBezierPath(roundedRect: NSRect(x: 50, y: 96, width: 224, height: 252), xRadius: 18, yRadius: 18)
color(31, 60, 47).setFill()
iconArea.fill()
text("DRAG THIS APP", x: 75, y: 313, width: 175, size: 13, weight: .semibold, foreground: brass)
text("to the place you\ninstalled Pharos", x: 75, y: 118, width: 175, size: 17, weight: .medium)

let steps: [(String, String, String, CGFloat)] = [
    ("01", "Quit Pharos", "Close the app before replacing it.", 306),
    ("02", "Drag Pharos.app to its original location", "Your SSD library folder or Applications.", 226),
    ("03", "Choose Replace", "Confirm Finder's replacement prompt.", 146),
    ("04", "Open it and allow access", "If blocked: Privacy & Security → Open Anyway. Allow library access.", 66),
]
for (number, title, detail, y) in steps {
    text(number, x: 310, y: y, width: 50, size: 20, weight: .bold, foreground: brass)
    text(title, x: 360, y: y + 2, width: 480, size: 19, weight: .semibold)
    text(detail, x: 360, y: y - 27, width: 480, size: 15, foreground: pale)
}
NSGraphicsContext.restoreGraphicsState()

guard let data = bitmap.representation(using: .png, properties: [:]) else { fatalError("Could not encode update guide") }
try data.write(to: output)
