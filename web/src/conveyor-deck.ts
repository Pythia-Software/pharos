import { cardKind, defaultCardCover } from "./card-cover";
import type { QueryPreview, QueryRecipe } from "./curiosity-data";
import { compactMetricPresentation } from "./card-presentation";

// Cards of one kind should not share a stretch of the belt: none within
// SPACING places of another, where the mix allows it.
export const SPACING = 3;

// Keep the anchor in place as previews arrive. A singleton fills the shorter
// side; ties go right, matching the extra reading slot in an even-sized deck.
export function growBelt<T>(items: T[], center: number, right: T, left?: T): { items: T[]; left?: T; right?: T } {
  if (left === undefined && center < items.length - center - 1) return { items: [right, ...items], left: right };
  return { items: left === undefined ? [...items, right] : [left, ...items, right], left, right };
}

// Render only the reading area, its neighbors and the path of a slide. The
// ready deck can already cycle while the remaining previews are still loading.
export function beltSlots(size: number, center: number, count: number, overscan: number, movement?: { at: number; offset: number }): { first: number; last: number; positions: number[] } {
  const first = -Math.floor((count - 1) / 2), last = Math.ceil((count - 1) / 2);
  const from = Math.min(movement?.at ?? 0, movement?.offset ?? 0), to = Math.max(movement?.at ?? 0, movement?.offset ?? 0);
  const positions = !size ? [] : movement || size > count
    ? Array.from({ length: to - from + count + 2 * overscan }, (_, index) => from + first - overscan + index)
    : Array.from({ length: size }, (_, index) => index - center);
  return { first, last, positions };
}

// The library's kind names the question; recipes without a source measure
// still draw a cover, so name them by that cover's shape.
export function beltKind(recipe: QueryRecipe): string {
  const kind = cardKind(recipe);
  if (kind !== "Text") return kind;
  const cover = defaultCardCover(recipe)?.kind ?? "";
  return ["pie", "donut"].includes(cover) ? "Composition" : cover === "box" ? "Distribution" : cover === "scatter" ? "Relationship" : recipe.exploration?.unit === "percent" ? "Rate" : "Ranking";
}

// A preview whose groups collapsed to one value shows a bare number, whatever
// chart its recipe asks for.
export function previewKind(recipe: QueryRecipe, preview?: QueryPreview): string {
  return preview?.state === "ready" && preview.metrics && compactMetricPresentation(preview)?.scalar ? "Number" : beltKind(recipe);
}

// Each match within `reach` places (nearest first) costs more the closer it is.
const crowding = (value: string, neighbors: string[], reach = SPACING) => neighbors.slice(0, reach).reduce((total, neighbor, distance) => total + (neighbor === value ? reach - distance : 0), 0);
const before = (score: number[], other: number[]) => { const index = score.findIndex((value, at) => value !== other[at]); return index >= 0 && score[index] < other[index]; };

// Picks the pending card that best fits beside `neighbors` (nearest first).
// Among the kinds clear of its neighbors, the one with the most cards waiting
// goes first, so no kind is left to clump at the end. Within that kind, a card
// on a different subject from its neighbors wins, then the earliest pending
// card, which keeps the deck's shuffled order. A kind too common to keep
// SPACING apart is held only as far apart as its share of the waiting cards
// allows; when every kind is crowded, the one repeated farthest away goes.
export function chooseNext<T extends QueryRecipe>(pending: T[], neighbors: T[], kindOf: (item: T) => string = beltKind): T | undefined {
  const kinds = neighbors.map(kindOf), categories = neighbors.map(item => item.category);
  const waiting = new Map<string, number>();
  for (const item of pending) waiting.set(kindOf(item), (waiting.get(kindOf(item)) ?? 0) + 1);
  const reach = (count: number) => { let spacing = SPACING; while (spacing > 1 && count > Math.ceil(pending.length / (spacing + 1))) spacing--; return spacing; };
  let best: { item: T; score: number[] } | undefined;
  for (const item of pending) {
    const kind = kindOf(item), count = waiting.get(kind)!;
    const score = [crowding(kind, kinds, reach(count)), -count, crowding(item.category, categories)];
    if (!best || before(score, best.score)) best = { item, score };
  }
  return best?.item;
}

// Orders a whole deck at once, as one side of the belt would if every card
// were ready.
export function spreadDeck<T extends QueryRecipe>(deck: T[]): T[] {
  const pending = [...deck], ordered: T[] = [];
  while (pending.length) {
    const item = chooseNext(pending, ordered.slice(-SPACING).reverse())!;
    ordered.push(item); pending.splice(pending.indexOf(item), 1);
  }
  return ordered;
}

// A lean that differs from its neighbors (nearest first): never three in a row
// leaning one way, and never within a visible margin of the next card's angle.
const MIN_LEAN = .6, MAX_LEAN = 2.1;
export function leanBeside(neighbors: number[], random = Math.random): number {
  const [near, far] = neighbors;
  const sign = near === undefined ? random() < .5 ? -1 : 1
    : far !== undefined && Math.sign(near) === Math.sign(far) || random() < .7 ? -Math.sign(near) || 1 : Math.sign(near);
  // Draw from the magnitudes clear of both neighbors. The nearer one alone
  // always leaves part of the range open if both cannot be avoided.
  const steps = Array.from({ length: 31 }, (_, step) => MIN_LEAN + step * (MAX_LEAN - MIN_LEAN) / 30);
  const clearOf = (magnitude: number, neighbor: number | undefined, margin: number) => neighbor === undefined || Math.abs(magnitude - Math.abs(neighbor)) >= margin;
  const both = steps.filter(magnitude => clearOf(magnitude, near, .45) && clearOf(magnitude, far, .25));
  const open = both.length ? both : steps.filter(magnitude => clearOf(magnitude, near, .45));
  const magnitude = open[Math.floor(random() * open.length)];
  return sign * Math.round(magnitude * 100) / 100;
}
