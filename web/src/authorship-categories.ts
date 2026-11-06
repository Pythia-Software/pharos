// Categories in the order the page lists them. The second category of a group
// shares its color, striped, so the chart's five groups still read.
export const authorshipCategories = [
  { key: "typed", label: "Typed", color: "var(--sea)", description: "Written or dictated by you: no other rule claimed it." },
  { key: "pasted", label: "Likely pasted", color: "var(--heat-1)", description: "Looks like code, logs, tables, or agent formatting, or arrived faster than anyone types. Inferred from how the text looks." },
  { key: "quoted", label: "Copied", color: "var(--gold)", description: "Matches agent output or authored file content from the preceding seven days, or is explicitly quoted." },
  { key: "resent", label: "Re-sent", color: "var(--gold)", striped: true, description: "Matches something you sent before." },
  { key: "template", label: "Template", color: "var(--heat-2)", description: "A one-click prompt or saved prompt sent again and again, or a slash command." },
  { key: "attachment", label: "Attachment", color: "var(--heat-2)", striped: true, description: "An attachment reference or image placeholder." },
  { key: "harness", label: "Harness", color: "var(--muted)", description: "Generated prompt context, question forms, and feedback metadata." },
  { key: "automated", label: "Automated", color: "var(--muted)", striped: true, description: "The whole message came from a script or another agent." },
];
