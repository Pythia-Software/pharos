import { zonedDayBounds } from "./concurrency-data";

export type WindowChoice = number | "today";
export const localTimezone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
export function todayBounds(clock = new Date(), timezone = localTimezone()): [string, string] {
  const day = new Intl.DateTimeFormat("en-CA", { timeZone: timezone }).format(clock);
  return [new Date(zonedDayBounds(day, timezone)[0]).toISOString(), clock.toISOString()];
}
export const windowLabel = (choice: WindowChoice) => choice === "today" ? "Today" : choice === 1 ? "Last 24 hours" : choice === 365 ? "Last year" : `Last ${choice} days`;
export const readWindowChoice = (value: string): WindowChoice => value === "today" ? value : Number(value);

// One calendar clock for open pages. Recheck on waking/focusing as timers can
// be suspended, and include the zone so travel changes the calendar boundary.
export function startDayClock(target: Window = window): () => void {
  const identity = () => `${localTimezone()}:${todayBounds()[0]}`;
  let previous = identity(), timer: ReturnType<typeof setTimeout>;
  const check = (event?: Event) => {
    clearTimeout(timer);
    const current = identity();
    if (current !== previous) { previous = current; target.dispatchEvent(new Event("pharos:day-change")); target.dispatchEvent(new Event("pharos:usage-refresh")); }
    else if (event?.type === "focus" || event?.type === "visibilitychange" && target.document.visibilityState === "visible") target.dispatchEvent(new Event("pharos:usage-refresh"));
    const now = new Date(), tomorrow = new Date(now); tomorrow.setHours(24, 0, 0, 0);
    timer = setTimeout(check, Math.max(1, tomorrow.getTime() - now.getTime()));
  };
  check();
  target.addEventListener("focus", check); target.document.addEventListener("visibilitychange", check);
  return () => { clearTimeout(timer); target.removeEventListener("focus", check); target.document.removeEventListener("visibilitychange", check); };
}
