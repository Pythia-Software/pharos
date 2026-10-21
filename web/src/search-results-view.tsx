import React from "react";
import type { SearchResultsView } from "./search-results-view-model";

export function SearchResultsDisplay({ view, grouped, onChange }: {
  view: SearchResultsView;
  grouped: boolean;
  onChange: (view: SearchResultsView) => void;
}) {
  return <div className="qt-qb-row search-results-display">
    <div className="qt-qb-section-header"><span className="qt-qb-kw">display as</span></div>
    <div className="search-results-display-controls">
      <div role="group" aria-label="Display results as" className="search-results-display-options">
        <button type="button" className={`qt-btn${view === "table" ? " qt-btn--active" : ""}`} aria-pressed={view === "table"} onClick={() => onChange("table")}>Table</button>
        <button type="button" className={`qt-btn${view === "conversation" ? " qt-btn--active" : ""}`} aria-pressed={view === "conversation"} onClick={() => onChange("conversation")}>Conversations</button>
      </div>
      <span className="search-results-display-hint">{grouped ? "Grouped rows. Conversations returns to individual workspaces."
        : view === "conversation" ? "Read prompts and replies. Selected columns are kept for Table."
        : "Workspaces in rows and columns."}</span>
    </div>
  </div>;
}
