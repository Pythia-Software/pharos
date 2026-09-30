// Interface preferences and saved views live in the library, so they follow it
// between Macs. window.pharosPrefs (internal/archive/assets/ui.py) keeps them
// there, or in the web view's storage on a shared page with no library.
export interface Preferences {
  get<T>(key: string, fallback: T): T;
  set(key: string, value: unknown): void;
  remove(key: string): void;
  keys(prefix: string): string[];
}

declare global {
  interface Window { pharosPrefs?: Preferences }
}

const unavailable: Preferences = { get: (_key, fallback) => fallback, set() {}, remove() {}, keys: () => [] };

export function preferences(): Preferences {
  return window.pharosPrefs ?? unavailable;
}
