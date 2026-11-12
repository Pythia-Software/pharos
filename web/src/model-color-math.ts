export const linear = (value: number) => value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4;
export function colorLuminance(color: string): number {
  return [1, 3, 5].reduce((sum, at, index) => sum + linear(parseInt(color.slice(at, at + 2), 16) / 255) * [.2126, .7152, .0722][index], 0);
}
export function perceptualColor(color: string): number[] {
  const [r, g, b] = [1, 3, 5].map(at => linear(parseInt(color.slice(at, at + 2), 16) / 255));
  const l = Math.cbrt(.4122214708 * r + .5363325363 * g + .0514459929 * b);
  const m = Math.cbrt(.2119034982 * r + .6806995451 * g + .1073969566 * b);
  const s = Math.cbrt(.0883024619 * r + .2817188376 * g + .6299787005 * b);
  return [.2104542553 * l + .793617785 * m - .0040720468 * s, 1.9779984951 * l - 2.428592205 * m + .4505937099 * s, .0259040371 * l + .7827717662 * m - .808675766 * s];
}
export function colorDistance(first: string, second: string): number {
  const a = perceptualColor(first), b = perceptualColor(second);
  return Math.hypot(...a.map((value, index) => value - b[index]));
}
