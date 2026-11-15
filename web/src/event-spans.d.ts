// Types for the event merge rule shared with the conversation reader
// (internal/archive/assets/event-spans.js, a classic script the bundle imports).
declare module '*/event-spans.js' {
  export const EVENT_MERGE_OVERLAP: number;
  export function nearSameSpan(a: readonly [number, number] | number[], b: readonly [number, number] | number[]): boolean;
  export function eventSpanGroups(spans: (readonly [number, number] | number[] | null | undefined)[]): number[][];
}
