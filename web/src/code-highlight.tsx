import React from "react";

// Source text in the reader's highlight runs (highlightCode in
// reader-model.js, which loads before the bundle): comments, strings,
// numbers and keywords by a file's extension, each a tok-* span styled by
// the page. Where the reader's model is absent the text stays plain.
export type CodeRun = { kind: string; text: string };
declare function highlightCode(text: string, name: string): CodeRun[];
declare function highlightCodeLines(text: string, name: string): CodeRun[][];

const available = () => typeof highlightCode === "function";

export function codeRuns(text: string, name: string): CodeRun[] {
  return available() ? highlightCode(text, name) : [{ kind: "", text }];
}

/** The text's runs one list per line, read whole so a comment spanning lines keeps its colour. */
export function codeLines(text: string, name: string): CodeRun[][] {
  return available() ? highlightCodeLines(text, name) : text.split("\n").map(line => line ? [{ kind: "", text: line }] : []);
}

export function CodeRuns({ runs }: { runs: CodeRun[] }) {
  return <>{runs.map((run, index) => run.kind ? <span key={index} className={`tok-${run.kind}`}>{run.text}</span> : <React.Fragment key={index}>{run.text}</React.Fragment>)}</>;
}

/** Lines drawn as one text, each line's runs followed by a line break. */
export function CodeLines({ lines }: { lines: CodeRun[][] }) {
  return <>{lines.map((line, index) => <React.Fragment key={index}>{index ? "\n" : null}<CodeRuns runs={line} /></React.Fragment>)}</>;
}
