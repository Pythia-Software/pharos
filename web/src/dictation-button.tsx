import React, { useEffect, useRef, type RefObject } from "react";
import { Icon } from "./icons";

// The page shell's dictation.js owns the microphone button's behaviour, so
// React text boxes and classic ones dictate the same way. The button stays
// hidden unless the Mac app can transcribe on this Mac.
declare global {
  interface Window {
    pharosDictation?: { attach(options: { button: HTMLButtonElement; field: HTMLTextAreaElement | HTMLInputElement; onError?: (message: string) => void }): () => void };
  }
}

/** A microphone button for `field`; place it inside a `.dictation-field` wrapper with the field. */
export function DictationButton({ field, onError }: { field: RefObject<HTMLTextAreaElement | HTMLInputElement | null>; onError?: (message: string) => void }) {
  const button = useRef<HTMLButtonElement>(null);
  const reportError = useRef(onError);
  reportError.current = onError;
  useEffect(() => {
    if (!button.current || !field.current || !window.pharosDictation) return;
    return window.pharosDictation.attach({ button: button.current, field: field.current, onError: message => reportError.current?.(message) });
  }, [field]);
  return <button ref={button} type="button" className="dictation-button" aria-label="Dictate" title="Dictate" aria-pressed="false" hidden><Icon name="mic" /></button>;
}
