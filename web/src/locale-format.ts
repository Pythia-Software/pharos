// toLocaleString with options builds an Intl formatter on every call, which
// costs tens of microseconds; a chart formatting each mark's label and tooltip
// pays that thousands of times a render. These keep one formatter per distinct
// locale and options and otherwise match the built-ins, including the default
// fields each Date method adds.

const numbers = new Map<string, Intl.NumberFormat>(), dates = new Map<string, Intl.DateTimeFormat>();
// A formatter keeps the time zone and locale it was built with. Both can
// change while the page stays open, in the background.
const forget = () => { numbers.clear(); dates.clear(); };
if (typeof window !== "undefined") { window.addEventListener("focus", forget); window.addEventListener("languagechange", forget); }
if (typeof document !== "undefined") document.addEventListener("visibilitychange", forget);

export function localeNumber(value: number, options?: Intl.NumberFormatOptions, locale?: string): string {
  const key = `${locale ?? ""}|${options ? JSON.stringify(options) : ""}`;
  let format = numbers.get(key);
  if (!format) numbers.set(key, format = new Intl.NumberFormat(locale, options));
  return format.format(value);
}

const dateFields = ["weekday", "year", "month", "day"] as const, timeFields = ["dayPeriod", "hour", "minute", "second", "fractionalSecondDigits"] as const;
const dateDefaults = { year: "numeric", month: "numeric", day: "numeric" } as const, timeDefaults = { hour: "numeric", minute: "numeric", second: "numeric" } as const;
function dated(kind: "any" | "date" | "time", value: Date | number, options: Intl.DateTimeFormatOptions = {}, locale?: string): string {
  const date = typeof value === "number" ? new Date(value) : value;
  if (Number.isNaN(date.getTime())) return "Invalid Date";
  const key = `${kind}|${locale ?? ""}|${JSON.stringify(options)}`;
  let format = dates.get(key);
  if (!format) {
    const styled = options.dateStyle !== undefined || options.timeStyle !== undefined;
    const hasDate = dateFields.some(field => options[field] !== undefined), hasTime = timeFields.some(field => options[field] !== undefined);
    const needed = styled ? {} : kind === "date" ? hasDate ? {} : dateDefaults : kind === "time" ? hasTime ? {} : timeDefaults : hasDate || hasTime ? {} : { ...dateDefaults, ...timeDefaults };
    dates.set(key, format = new Intl.DateTimeFormat(locale, { ...options, ...needed }));
  }
  return format.format(date);
}
// Date.prototype.toLocaleString, toLocaleDateString and toLocaleTimeString.
export const localeDateTime = (value: Date | number, options?: Intl.DateTimeFormatOptions, locale?: string) => dated("any", value, options, locale);
export const localeDate = (value: Date | number, options?: Intl.DateTimeFormatOptions, locale?: string) => dated("date", value, options, locale);
export const localeTime = (value: Date | number, options?: Intl.DateTimeFormatOptions, locale?: string) => dated("time", value, options, locale);
