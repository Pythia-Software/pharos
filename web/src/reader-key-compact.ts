// The conversation reader's Key can be shown as icons only. The choice is a
// preference saved with the library (window.pharosPrefs), not view state: it
// holds for every conversation and is never part of a URL. The classic reader
// (ui.py, transcriptFilterControl) owns the toggle and announces a change;
// the Key's React parts follow it here.
import { useSyncExternalStore } from "react";
import { preferences } from "./preferences";

export const READER_KEY_COMPACT_PREF = "pharos-reader-key-compact", READER_KEY_COMPACT_EVENT = "pharos:reader-key-compact";
const read = () => preferences().get<boolean>(READER_KEY_COMPACT_PREF, false) === true;
const subscribe = (notify: () => void) => { window.addEventListener(READER_KEY_COMPACT_EVENT, notify); return () => window.removeEventListener(READER_KEY_COMPACT_EVENT, notify); };
export function useReaderKeyCompact() { return useSyncExternalStore(subscribe, read, () => false); }
