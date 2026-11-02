// Sidebar preferences predate the integer URL contract and may contain drag
// subpixels. Normalize both saved values and viewport-limited drag results.
export function sidebarWidth(value: number, maximum = 5000): number {
  const limit = Math.max(300, Math.min(5000, Math.floor(maximum)));
  return Math.max(300, Math.min(limit, Math.round(Number.isFinite(value) ? value : 400)));
}
