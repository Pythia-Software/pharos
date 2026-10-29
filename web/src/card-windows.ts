// Cards whose dashboard draws a trailing total over the card's own window
// ({days}): their destination's rollingPeriods follows the card's window,
// when the card is opened and when its window is edited.
const windowed = new Set(["carbon-trend", "human-word-total"]);
export function windowedChart<T extends { rollingPeriods?: number }>(cardId: string | null | undefined, chart: T, days: number | null | undefined): T {
  return cardId && windowed.has(cardId) && days ? { ...chart, rollingPeriods: days } : chart;
}
