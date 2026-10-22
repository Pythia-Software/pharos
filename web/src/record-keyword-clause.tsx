import React, { useRef, useState } from "react";
import type { FieldDef } from "@pythia-software/query-table-core";
import { ModalSurface } from "@pythia-software/query-table-ui";
import { TextSearchEditor } from "./text-search-editor";
import { SearchSelect } from "./search-select";
import { conditionSummary } from "./text-search-model";
import { freshRecordKeywordSearch, recordKeywordFields, type RecordKeywordSearch } from "./record-keyword-model";
import { Icon } from "./icons";
import "./explore-search.css";

type RecordKeywordContext = { label: string; field: string; searchLabel: string };
function targetLabel(context: RecordKeywordContext, field: string, fields: FieldDef[]) {
  return field === context.field ? context.searchLabel : `${context.label} · ${fields.find(choice => choice.name === field)?.label ?? field}`;
}

export function recordKeywordSummary(search: RecordKeywordSearch, fields: FieldDef[], context: RecordKeywordContext) {
  const label = targetLabel(context, search.field, fields);
  const draft = search.draft;
  return `${label}: ${draft.editor === "syntax" ? draft.text : draft.match === "rules" ? conditionSummary(draft.root) : `${draft.match}: ${draft.text}`}${draft.caseSensitive ? " · case sensitive" : ""}${draft.separators ? " · exact phrase spacing" : ""}`;
}

export function RecordKeywordClause({ fields, context, value, error, onChange }: { fields: FieldDef[]; context: RecordKeywordContext; value: RecordKeywordSearch | null; error?: string; onChange: (next: RecordKeywordSearch | null) => void }) {
  const choices = recordKeywordFields(fields);
  const defaultField = choices.find(field => field.name === context.field)?.name ?? choices[0]?.name ?? "";
  const fresh = () => freshRecordKeywordSearch(defaultField);
  const label = targetLabel(context, value?.field ?? defaultField, choices);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState(fresh);
  const invoker = useRef<HTMLButtonElement | null>(null);
  if (!choices.length) return null;
  const close = () => setOpen(false);
  return <>
    <div className="qt-qb-row explore-content-row">
      <div className="qt-qb-section-header"><span className="qt-qb-kw">search in</span></div>
      <div className="explore-content-controls">
        <button type="button" className="qt-btn explore-content-chip" onClick={event => { invoker.current = event.currentTarget; setEditing(value ?? fresh()); setOpen(true); }} aria-label={`${value || error ? "Edit" : "Add"} keyword filter for ${label}`}>
          <Icon name="search" /><span>{error ? `${label}: keyword filter needs attention` : value ? recordKeywordSummary(value, choices, context) : `${label}…`}</span>
        </button>
        {value || error ? <button type="button" className="qt-link-btn" aria-label={`Remove keyword filter for ${label}`} onClick={() => onChange(null)}><Icon name="close" /></button> : null}
      </div>
    </div>
    {open ? <ModalSurface title={`Search ${targetLabel(context, editing.field, choices)}`} onClose={close} returnFocus={invoker.current} className="qt-modal explore-search-modal" initialFocus="textarea, input:not([type=checkbox])">
      <div className="qt-overlay-body"><TextSearchEditor compact sentence={false} textOnly value={editing.draft} active={!!value} initialError={error} kind="text" onKind={() => {}}
        recordTarget={<SearchSelect aria-label="Text field to search" value={editing.field} onChange={event => setEditing(previous => ({ ...previous, field: event.target.value }))}>{choices.map(field => <option key={field.name} value={field.name}>{field.label}</option>)}</SearchSelect>}
        context={<p className="explore-search-context">Search the selected field of each source record. Field filters still apply. Grouped rows and metrics use the matching records.</p>}
        onApply={draft => { onChange({ field: editing.field, draft }); close(); }} onClear={() => { onChange(null); close(); }}
        file={{ text: "", caseSensitive: false, active: false, dirty: false, onText() {}, onCase() {}, onApply() {}, onClear() {} }}
        workspaceScope={{ filters: [] }} orderControl={null} /></div>
    </ModalSurface> : null}
  </>;
}
