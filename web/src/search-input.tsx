import React from "react";

// A hidden copy gives text its natural width, including Unicode and spaces.
// CSS caps the field at its container; the native input scrolls once it is full.
// This also works in the desktop webview without field-sizing support.
export function SearchInput({ value, placeholder, minChars = 10, ...props }: Omit<React.InputHTMLAttributes<HTMLInputElement>, "value"> & { value: string; minChars?: number }) {
  return <span className="text-growing-input">
    <span className="text-input-measure" aria-hidden="true">{(value || placeholder || "").padEnd(minChars, "\u00a0")}{"\u00a0"}</span>
    <input {...props} value={value} placeholder={placeholder} size={1} />
  </span>;
}

// Long values wrap onto more lines instead of scrolling sideways. A hidden copy
// sizes the shared grid cell, so the field grows without measuring each
// keystroke. Values are single-line: Enter submits, as it would from a text
// field (not while Search is unavailable), and pasted breaks become spaces.
export function WrappingInput({ value, placeholder, onValue, onKeyDown, ...props }: Omit<React.TextareaHTMLAttributes<HTMLTextAreaElement>, "value" | "onChange"> & { value: string; onValue: (value: string) => void }) {
  return <span className="text-wrapping-input">
    <span className="text-input-measure" aria-hidden="true">{value || placeholder}{" "}</span>
    <textarea {...props} rows={1} value={value} placeholder={placeholder} onChange={event => onValue(event.target.value.replace(/[\r\n]+/g, " "))} onKeyDown={event => {
      onKeyDown?.(event);
      if (event.key !== "Enter" || event.nativeEvent.isComposing || event.defaultPrevented) return;
      event.preventDefault();
      const form = event.currentTarget.form;
      if (form && !form.querySelector<HTMLButtonElement>('button[type="submit"]')?.disabled) form.requestSubmit();
    }} />
  </span>;
}
