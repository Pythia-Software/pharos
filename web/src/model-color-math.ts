export const linear = (value: number) => value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4;
export function colorLuminance(color: string): number {
  return [1, 3, 5].reduce((sum, at, index) => sum + linear(parseInt(color.slice(at, at + 2), 16) / 255) * [.2126, .7152, .0722][index], 0);
}
// Choose the stronger black/white contrast for text and icons on a swatch.
export const colorForeground = (color: string): string => colorLuminance(color) > .179 ? "#000000" : "#ffffff";
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

export function lch(color: string): [number, number, number] {
  const [l, a, b] = perceptualColor(color);
  return [l, Math.hypot(a, b), (Math.atan2(b, a) * 180 / Math.PI + 360) % 360];
}
function rgb(l: number, c: number, h: number): number[] {
  const a = c * Math.cos(h * Math.PI / 180), b = c * Math.sin(h * Math.PI / 180);
  const x = (l + .3963377774 * a + .2158037573 * b) ** 3;
  const y = (l - .1055613458 * a - .0638541728 * b) ** 3;
  const z = (l - .0894841775 * a - 1.2914855480 * b) ** 3;
  return [4.0767416621 * x - 3.3077115913 * y + .2309699292 * z, -1.2684380046 * x + 2.6097574011 * y - .3413193965 * z, -.0041960863 * x - .7034186147 * y + 1.7076147010 * z];
}
// Reduce chroma at fixed lightness/hue until the color is inside sRGB, rather
// than clipping RGB channels and silently changing the requested hue.
export function gamutChroma(l: number, h: number): number {
  let low = 0, high = .4;
  for (let i = 0; i < 24; i++) {
    const middle = (low + high) / 2;
    if (rgb(l, middle, h).every(value => value >= 0 && value <= 1)) low = middle;
    else high = middle;
  }
  return low;
}
// A model's shade for an effort step: step 0 is the model's own color, each
// step below is lighter and a little greyer, each step above darker and a
// little richer. Hue never moves, so every step still reads as its model.
export function effortShade(color: string, step: number): string {
  if (!step) return color;
  const [l, c, h] = lch(color), lower = step < 0, size = Math.abs(step);
  return fromLCH(Math.max(.08, Math.min(.97, l + (lower ? .06 : -.045) * size)), c * Math.max(0, 1 + (lower ? -.08 : .08) * size), h);
}
export function fromLCH(l: number, c: number, h: number): string {
  return "#" + rgb(l, Math.min(c, gamutChroma(l, h)), h).map(value => {
    const encoded = value <= .0031308 ? value * 12.92 : 1.055 * value ** (1 / 2.4) - .055;
    return Math.round(255 * Math.max(0, Math.min(1, encoded))).toString(16).padStart(2, "0");
  }).join("");
}
