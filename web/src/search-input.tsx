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
