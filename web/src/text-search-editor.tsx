import React, { createContext, useContext, useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon } from "./icons";
import { SearchSelect } from "./search-select";
import { SearchInput, WrappingInput } from "./search-input";
import { chooseScope, compileRules, newGroup, newRule, optionScope, optionsInEffect, querySyntax, scopeChoice, scopes, searchRequest, searchSentence, sourceLabel, visualFromQuery, type SentencePart, type RuleKind, type SearchIssue, type TextCondition, type TextGroup, type TextQuery, type TextScope, type TextSearchDraft } from "./text-search-model";
import "./text-search-editor.css";

const EditorDOMPrefix = createContext("");
const shortRuleLabels: Record<RuleKind, string> = { words: "All words", anyWords: "Any word", phrase: "Phrase", prefix: "Word starts with", near: "Phrases near", pre: "Phrases in order" };
const ruleHelp: Record<RuleKind, string> = {
  words: "Each word must appear somewhere in the same searched text. Type ordinary words; AND, OR, quotes, and parentheses are not commands here.",
  anyWords: "At least one of these words must appear in the searched text.",
  phrase: "These words stay together, in this order. No quotation marks needed.",
  prefix: "Matches word beginnings, such as migrat matching migration. No * symbol needed.",
  near: "Both phrases must appear within the chosen number of words, in either order.",
  pre: "Both phrases must appear within the chosen number of words, with the first phrase before the second.",
};
// Matching options live together in one menu, each explained in place.
function MatchOption({ label, help, checked, change, disabled = false }: { label: string; help: string; checked: boolean; change: (value: boolean) => void; disabled?: boolean }) {
  const id = useId();
  return <label className="text-option"><input type="checkbox" aria-label={label} aria-describedby={id} checked={checked} disabled={disabled} onChange={event => change(event.target.checked)} /><span>{label}</span><small id={id}>{help}</small></label>;
}
export function SearchPopover({ label, title, className, popupClassName = "", triggerContent, children }: { label: string; title?: string; className: string; popupClassName?: string; triggerContent: React.ReactNode; children: (close: () => void) => React.ReactNode }) {
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState({ left: 8, top: 8 });
  const trigger = useRef<HTMLButtonElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  const id = useId();
  useEffect(() => {
    if (!open) return;
    const place = () => {
      const rect = trigger.current!.getBoundingClientRect();
      const width = popup.current?.offsetWidth ?? 280, height = popup.current?.offsetHeight ?? 180;
      // Open toward the free side: under the trigger's start on the left half, under its end on the right.
      const start = rect.left < window.innerWidth / 2 ? rect.left : rect.right - width;
      setPosition({ left: Math.max(8, Math.min(start, window.innerWidth - width - 8)), top: rect.bottom + height + 16 < window.innerHeight ? rect.bottom + 6 : Math.max(8, rect.top - height - 6) });
    };
    const outside = (event: Event) => { if (!trigger.current?.contains(event.target as Node) && !popup.current?.contains(event.target as Node)) setOpen(false); };
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); setOpen(false); trigger.current?.focus(); } };
    place();
    // Explicit tab stops keep menu buttons reachable with Safari's default
    // keyboard settings. Preserve any deliberate tabindex supplied by a child.
    popup.current?.querySelectorAll("button:not([tabindex])").forEach(button => { (button as HTMLButtonElement).tabIndex = 0; });
    popup.current?.querySelector<HTMLElement>("select, button, input")?.focus();
    document.addEventListener("pointerdown", outside);
    document.addEventListener("focusin", outside);
    // Dismiss this menu before a containing modal handles Escape.
    document.addEventListener("keydown", escape, true);
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => { document.removeEventListener("pointerdown", outside); document.removeEventListener("focusin", outside); document.removeEventListener("keydown", escape, true); window.removeEventListener("resize", place); window.removeEventListener("scroll", place, true); };
  }, [open]);
  return <>
    <button ref={trigger} type="button" tabIndex={0} className={className} aria-label={label} title={title} aria-haspopup="dialog" aria-expanded={open} aria-controls={open ? id : undefined} onClick={() => setOpen(!open)}>{triggerContent}</button>
    {open ? createPortal(<div ref={popup} id={id} role="dialog" aria-label={label} className={`text-condition-popup ${popupClassName}`} style={position} onMouseDown={event => {
      const element = event.target as Element;
      const control = element.closest<HTMLElement>('button, input[type="checkbox"]') ?? element.closest("label")?.querySelector<HTMLInputElement>('input[type="checkbox"]');
      // Safari blurs the active control when clicking buttons or checkboxes.
      // Keep focus in the menu before the modal redirects it and dismisses it.
      if (event.button === 0 && control) { event.preventDefault(); control.focus(); }
    }}>{children(() => setOpen(false))}</div>, trigger.current?.closest('dialog, [role="dialog"][aria-modal="true"]') ?? document.body) : null}
  </>;
}
function ConditionOptions({ node, path, scopeLabel, onExclude }: { node: TextCondition; path: string; scopeLabel: string; onExclude: (exclude: boolean) => void }) {
  const prefix = useContext(EditorDOMPrefix);
  const type = node.kind === "group" ? "group" : "rule";
  return <SearchPopover label={`Options for ${type} ${path}`} title={`${type === "group" ? "Group" : "Condition"} options and help`} className="text-condition-options" triggerContent={<Icon name="more" />}>{close => <>
    <p>{node.kind === "group" ? "A group keeps its conditions together. AND NOT rejects a match only inside the enclosing group." : ruleHelp[node.kind]}</p>
    <label>Use in {scopeLabel}<SearchSelect aria-label={`Include or exclude ${type} ${path}`} value={node.exclude ? "exclude" : "include"} onChange={event => {
      close(); onExclude(event.target.value === "exclude");
      requestAnimationFrame(() => (document.getElementById(`${prefix}${node.id}-${node.kind === "group" ? "join" : "text"}`) ?? document.getElementById(`${prefix}${firstRule(node).id}-text`))?.focus());
    }}><option value="include">Match</option><option value="exclude">AND NOT</option></SearchSelect></label>
  </>}</SearchPopover>;
}
// The search read as English. Connecting words stay quiet; chosen values carry the weight.
export function SearchSentence({ parts, empty = false }: { parts: SentencePart[]; empty?: boolean }) {
  return <span className="search-sentence">{parts.map((part, index) => typeof part === "string" ? part : <b key={index}>{part.value}</b>)}{empty ? "…" : null}</span>;
}
// Heads a narrow composer; beside the conditions when there is room.
export function SearchReading({ parts, empty }: { parts: SentencePart[]; empty: boolean }) {
  return <p className={`text-editor-sentence${empty ? " is-empty" : ""}`}><Icon name="search" /><SearchSentence parts={parts} empty={empty} /></p>;
}
const firstRule = (node: TextCondition): TextCondition => node.kind === "group" && node.children.length ? firstRule(node.children[0]) : node;
function RemoveButton({ label, onRemove }: { label: string; onRemove: () => void }) {
  return <button type="button" className="text-remove" aria-label={label} title="Remove" onClick={onRemove}><Icon name="close" /></button>;
}

// Every add menu offers the same text conditions; the editor's own menu also
// lists message and workspace properties alongside them, without distinction.
function useAddCondition(group: TextGroup, onChange: (group: TextGroup) => void) {
  const prefix = useContext(EditorDOMPrefix);
  return (node: TextCondition) => {
    onChange({ ...group, children: [...group.children, node] });
    const first = node.kind === "group" ? node.children[0] : node;
    requestAnimationFrame(() => document.getElementById(`${prefix}${first.id}-text`)?.focus());
  };
}
function TextChoices({ add, close }: { add: (node: TextCondition) => void; close: () => void }) {
  return <>
    <button type="button" onClick={() => { close(); add(newRule()); }}>Words or phrase</button>
    <button type="button" onClick={() => { close(); add({ ...newRule(), exclude: true }); }}>Words to exclude</button>
  </>;
}
function GroupChoices({ add, close }: { add: (node: TextCondition) => void; close: () => void }) {
  return <>
    <button type="button" onClick={() => { close(); add(newGroup()); }}>Group of conditions</button>
    <button type="button" onClick={() => { close(); add({ ...newGroup(), exclude: true }); }}>Excluded group</button>
  </>;
}
function AddToGroup({ group, onChange, path, depth }: { group: TextGroup; onChange: (group: TextGroup) => void; path: string; depth: number }) {
  const add = useAddCondition(group, onChange);
  return <SearchPopover label={`Add condition to group ${path}`} className="text-add-condition" popupClassName="text-add-popup" triggerContent={<><Icon name="plus" />Add condition</>}>{close => <>
    <TextChoices add={add} close={close} />
    {depth < 3 ? <><hr /><GroupChoices add={add} close={close} /></> : null}
  </>}</SearchPopover>;
}

// The query reads down a left column of grammar words: "with" leads the first
// rule, later rows say how they join, and the second row's word chooses AND or
// OR for its group. Only explicit nested groups get a box; OR alternatives and
// AND NOT exclusions are marked by a rail so filters visibly sit outside them.
function GroupEditor({ group, onChange, issues, heading, lead, path = "1", number = "", depth = 0, parentLabel = "entire search", onRemove }: { group: TextGroup; onChange: (group: TextGroup) => void; issues: SearchIssue[]; heading?: React.ReactNode; lead?: string; path?: string; number?: string; depth?: number; parentLabel?: string; onRemove?: () => void }) {
  const prefix = useContext(EditorDOMPrefix);
  const edit = (index: number, node: TextCondition) => onChange({ ...group, children: group.children.map((child, i) => i === index ? node : child) });
  // The whole search always keeps one positive text condition to type into.
  const remove = (index: number) => {
    const children = group.children.filter((_, i) => i !== index);
    onChange({ ...group, children: depth || children.some(child => !child.exclude) ? children : [newRule(), ...children] });
  };
  const groupIssue = issues.find(issue => issue.id === group.id);
  const groupLabel = depth ? `Group ${number}` : "Entire search";
  const children = group.children.map((child, index) => ({ child, index }));
  const included = children.filter(({ child }) => !child.exclude), excluded = children.filter(({ child }) => child.exclude);
  const joinChoice = <SearchSelect id={`${prefix}${group.id}-join`} aria-label={`Match rules in group ${path}`} value={group.join} onChange={e => onChange({ ...group, join: e.target.value as "all" | "any" })}><option value="all">and</option><option value="any">or</option></SearchSelect>;
  const renderConditions = (nodes: typeof children, join: "all" | "any", { lead, choice = false }: { lead?: string; choice?: boolean } = {}) => <div className="text-group-children">
    {nodes.map(({ child, index }, displayIndex) => {
      const childPath = `${path}.${index + 1}`;
      const word = displayIndex ? join === "all" ? "and" : "or" : lead ?? "";
      const connector = displayIndex === 1 && choice ? <span className="text-join">{joinChoice}</span> : word || lead !== undefined || nodes.length > 1 ? <span className="text-join" aria-hidden={word ? undefined : true}>{word}</span> : null;
      if (child.kind === "group") {
        const groupNumber = group.children.slice(0, index + 1).filter(c => c.kind === "group").length;
        return <div key={child.id} className="text-condition text-condition-group">{connector}<GroupEditor group={child} path={childPath} number={`${number ? `${number}.` : ""}${groupNumber}`} depth={depth + 1} parentLabel={groupLabel} onChange={node => edit(index, node)} onRemove={() => remove(index)} issues={issues} /></div>;
      }
      const error = issues.find(issue => issue.id === child.id);
      const proximity = child.kind === "near" || child.kind === "pre";
      return <div key={child.id} className="text-condition">{connector}<div className="text-rule">
        <div className="text-rule-controls">
          <SearchSelect aria-label={`Match type for rule ${childPath}`} value={child.kind} onChange={e => edit(index, { ...child, kind: e.target.value as RuleKind })}>{Object.entries(shortRuleLabels).map(([key, label]) => <option value={key} key={key}>{label}</option>)}</SearchSelect>
          <WrappingInput id={`${prefix}${child.id}-text`} aria-label={`Text for rule ${childPath}`} aria-invalid={!!error} aria-describedby={error ? `${prefix}${child.id}-error` : undefined} value={child.text} onValue={text => edit(index, { ...child, text })} placeholder={child.kind === "prefix" ? "Start of a word, e.g. migrat" : proximity ? "First phrase" : child.kind === "phrase" ? "Exact phrase, e.g. race condition" : child.kind === "anyWords" ? "Any of these words" : "Words to find"} />
          <ConditionOptions node={child} path={childPath} scopeLabel={groupLabel} onExclude={exclude => edit(index, { ...child, exclude })} />
          <RemoveButton label={`Remove rule ${childPath}`} onRemove={() => remove(index)} />
        </div>
        {proximity ? <div className="text-proximity">
          <span>{child.kind === "pre" ? "then" : "and"}</span>
          <SearchInput aria-label={`Second phrase for rule ${childPath}`} aria-invalid={!!error} aria-describedby={error ? `${prefix}${child.id}-error` : undefined} value={child.other} onChange={e => edit(index, { ...child, other: e.target.value })} placeholder="Second phrase" />
          <label>within <input type="number" min={0} max={100} aria-label={`Words between phrases in rule ${childPath}`} value={child.distance} onChange={e => edit(index, { ...child, distance: e.target.value })} /> words</label>
        </div> : null}
        {error ? <p id={`${prefix}${child.id}-error`} className="text-editor-error">{error.message}</p> : null}
      </div></div>;
    })}
  </div>;
  const bracketed = group.join === "any" && included.length > 1 && (!depth || excluded.length > 0);
  const matches = <div className={`text-group-matches${depth ? "" : " text-root-matches"}${bracketed ? " text-bracketed-matches" : ""}`} role="group" aria-label={`${groupLabel} matching conditions`}>
    {renderConditions(included, group.join, { lead: bracketed ? undefined : depth ? excluded.length ? "" : undefined : lead, choice: true })}
  </div>;
  const content = <>
    {depth ? <div className="text-group-heading">
      <strong>{groupLabel}</strong>
      <AddToGroup group={group} onChange={onChange} path={path} depth={depth} />
      <ConditionOptions node={group} path={path} scopeLabel={parentLabel} onExclude={exclude => onChange({ ...group, exclude })} />
      {onRemove ? <RemoveButton label={`Remove group ${path}`} onRemove={onRemove} /> : null}
    </div> : heading}
    {bracketed ? <div className="text-condition"><span className="text-join" aria-hidden={depth ? true : undefined}>{depth ? "" : lead}</span>{matches}</div> : matches}
    {excluded.length ? <div className="text-negative-clause">
      <span className="text-join text-not"><span className="sr-only">and </span>not</span>
      <div className={`text-negative-group${excluded.length === 1 ? " text-negative-single" : ""}`} role="group" aria-label={`AND NOT in ${groupLabel}`}>
        {renderConditions(excluded, "any")}
      </div>
    </div> : null}
    {groupIssue ? <p className="text-editor-error">{groupIssue.message}</p> : null}
  </>;
  return depth ? <fieldset className="text-rule-group text-nested-group"><legend className="sr-only">Rule group {path}</legend>{content}</fieldset> : content;
}

async function validate(request: Record<string, unknown>, signal: AbortSignal): Promise<TextQuery> {
  const response = await fetch("/api/search/text/validate", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(request), signal });
  const result = await response.json();
  if (!response.ok) throw new Error(result.error ?? "Could not validate the search. Please try again.");
  return result.where;
}
const modes = [["advanced", "Boolean query syntax"], ["terms", "All words, with operators treated as words"], ["phrase", "Whole query as a phrase"], ["literal", "Exact text, including punctuation"], ["regex", "Regular expression (Go / RE2)"]];
const dateInputType = (value: string) => !value || /^\d{4}-\d{2}-\d{2}$/.test(value) ? "date" : /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$/.test(value) ? "datetime-local" : "text";
const messageFields = [
  { key: "provider", label: "Provider", operator: "is", placeholder: "Provider name" },
  { key: "source", label: "Source", operator: "is", placeholder: "Source name" },
  { key: "repository", label: "Repository", operator: "contains", placeholder: "Repository name" },
  { key: "from", label: "Date", operator: "on or after", placeholder: "YYYY-MM-DD", menu: "Date on or after" },
  { key: "to", label: "Date", operator: "on or before", placeholder: "YYYY-MM-DD", menu: "Date on or before" },
  { key: "conversation_id", label: "Conversation ID", operator: "is", placeholder: "Conversation ID" },
  { key: "workspace_id", label: "Workspace ID", operator: "is", placeholder: "Workspace ID" },
] as const;
type MessageField = typeof messageFields[number]["key"];
export type LibrarySearchKind = "text" | "file" | "url";
type FileSearch = { text: string; caseSensitive: boolean; active: boolean; dirty: boolean; onText: (text: string) => void; onCase: (checked: boolean) => void; onApply: () => void; onClear: () => void };
export function TextSearchEditor({ value, active, initialError, onApply, onClear, kind, onKind, file, workspaceScope, orderControl, onDraft, context, recordTarget, textOnly = false, fixedContext = false, filterError, allowedScopes, compact = false, sentence = true }: { onDraft?: (draft: TextSearchDraft) => void; context?: React.ReactNode; recordTarget?: React.ReactNode; textOnly?: boolean; fixedContext?: boolean; filterError?: string; allowedScopes?: string[]; compact?: boolean; sentence?: boolean; value: TextSearchDraft; active: boolean; initialError?: string; onApply: (value: TextSearchDraft) => void; onClear: () => void; kind: LibrarySearchKind; onKind: (kind: LibrarySearchKind) => void; file: FileSearch; workspaceScope: { filters: string[]; reading?: SentencePart[][]; editor?: React.ReactNode; inline?: React.ReactNode; add?: React.ReactNode; caption?: string; sentenceLead?: string }; orderControl: React.ReactNode }) {
  const domPrefix = useId();
  const [draft, setDraft] = useState(value);
  const [addedFields, setAddedFields] = useState<MessageField[]>([]);
  const [workspaceOpen, setWorkspaceOpen] = useState(false);
  const filterID = useId();
  const workspaceID = useId();
  const [showIssues, setShowIssues] = useState(false);
  const [error, setError] = useState(initialError ?? "");
  const [busy, setBusy] = useState(false);
  const [choices, setChoices] = useState<{ providers: string[]; sources: string[] }>({ providers: [], sources: [] });
  const [choicesError, setChoicesError] = useState(false);
  const pending = useRef<AbortController>();
  const previousRules = useRef<{ visual: Pick<TextSearchDraft, "editor" | "root" | "match" | "text">; text: string; mode: string; separators: boolean }>();
  const form = useRef<HTMLFormElement>(null);
  const baseline = JSON.stringify(value);
  useEffect(() => { if (value === draft && !initialError) return; pending.current?.abort(); setBusy(false); setDraft(value); if (value.root.id !== draft.root.id) setAddedFields([]); setError(initialError ?? ""); setShowIssues(false); }, [value, initialError]);
  useEffect(() => {
    if (recordTarget) return () => pending.current?.abort();
    const controller = new AbortController();
    fetch("/api/search/text/options", { signal: controller.signal }).then(async response => { if (!response.ok) throw new Error(); return response.json(); }).then(setChoices).catch(() => { if (!controller.signal.aborted) setChoicesError(true); });
    return () => { controller.abort(); pending.current?.abort(); };
  }, []);
  const dirty = JSON.stringify(draft) !== baseline;
  const patch = (change: Partial<TextSearchDraft>) => { pending.current?.abort(); setBusy(false); setError(""); setShowIssues(true); const next = { ...draft, ...change }; setDraft(next); onDraft?.(next); };
  const scope = (change: Partial<TextScope>) => patch({ scope: { ...draft.scope, ...change } });
  const compiled = searchRequest(draft);
  const selectedScope = scopeChoice(draft.scope);
  const issueList = showIssues ? compiled.issues : [];
  const run = async () => {
    setShowIssues(true); setError("");
    if (filterError) return;
    if (!compiled.request) { form.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus(); return; }
    const controller = new AbortController(); pending.current?.abort(); pending.current = controller; setBusy(true);
    try {
      await validate(compiled.request, controller.signal);
      if (controller.signal.aborted) return;
      onApply(draft);
    } catch (cause) { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : String(cause)); }
    finally { if (!controller.signal.aborted) setBusy(false); }
  };
  const editSyntax = () => {
    if (optionsInEffect(draft).fuzzy) { setError("Turn off “Include word variations” in Options before switching to query syntax, so the converted query keeps the same matching rules."); return; }
    const node = draft.match === "rules" ? compileRules(draft.root).where : { op: draft.match, text: draft.text } as TextQuery;
    try {
      const text = node ? querySyntax(node, draft.separators) : "";
      const mode = draft.match === "rules" ? "advanced" : draft.match;
      previousRules.current = { visual: { editor: "visual", root: draft.root, match: draft.match, text: draft.text }, text, mode, separators: draft.separators };
      patch({ editor: "syntax", text, mode });
    }
    catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
  };
  const editRules = async () => {
    const previous = previousRules.current;
    if (previous && previous.text === draft.text && previous.mode === draft.mode && previous.separators === draft.separators) {
      patch(previous.visual); return;
    }
    if (!draft.text.trim()) {
      patch({ editor: "visual", root: newGroup(), match: draft.mode === "literal" || draft.mode === "regex" ? draft.mode : "rules", text: "" }); return;
    }
    // Editor navigation only parses the text. Unfinished workspace or date
    // filters must not prevent returning to Rules.
    const controller = new AbortController(); pending.current?.abort(); pending.current = controller; setBusy(true); setError("");
    try {
      const node = await validate({ query: draft.text, mode: draft.mode, separators: draft.separators, case_sensitive: draft.caseSensitive }, controller.signal);
      if (!controller.signal.aborted) patch(visualFromQuery(node, draft.separators));
    } catch (cause) { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : String(cause)); }
    finally { if (!controller.signal.aborted) setBusy(false); }
  };
  const textKind = kind === "text";
  const visualRules = textKind && draft.editor === "visual" && draft.match === "rules";
  const availableFields = recordTarget ? [] : messageFields.filter(field => !fixedContext || !["conversation_id", "workspace_id"].includes(field.key));
  const visibleFields = availableFields.filter(field => draft.scope[field.key] || addedFields.includes(field.key));
  const targetDescription = textKind ? selectedScope?.description ?? `Saved message scope: ${draft.scope.surface}. Its role and message-type restrictions appear below.` : kind === "file" ? "Recorded paths and names of modified files, including old paths of renamed files. File contents are not searched." : "URLs recorded in tool activity, by URL or hostname. Page contents are not searched.";
  // The usual target needs no explanation; any other choice says what it covers.
  const defaultScope = allowedScopes?.[0] ?? "prose";
  const sameSpan = textKind ? scopes.slice(8).some(s => s.id === selectedScope?.id) ? "All conditions apply within the same classified span." : "All text conditions apply within the same message." : "";
  const targetNote = recordTarget ? "All conditions match within the selected field of the same source record." : textKind && selectedScope?.id === defaultScope ? "" : `${targetDescription} ${sameSpan}`.trim();
  const heading = <div className="text-group-heading text-query-heading">
    <span className="text-join">Find</span>
    <span className="text-query-subject">
      {recordTarget ?? <SearchSelect aria-label="Search target" title={targetDescription} aria-describedby={`${filterID}-target`} value={textKind ? selectedScope?.id ?? "custom" : kind} onChange={event => {
        const target = event.target.value;
        if (target === "file" || target === "url") onKind(target);
        else { patch({ scope: chooseScope(draft.scope, target) }); onKind("text"); }
      }}>
        {!selectedScope ? <option value="custom">Custom message scope</option> : null}
        <optgroup label="Messages">{scopes.slice(0, 8).filter(s => !allowedScopes || allowedScopes.includes(s.id)).map(s => <option key={s.id} value={s.id}>{s.label}</option>)}</optgroup>
        <optgroup label="Parts of user messages">{scopes.slice(8).filter(s => !allowedScopes || allowedScopes.includes(s.id)).map(s => <option key={s.id} value={s.id}>{s.label}</option>)}</optgroup>
        {!textOnly ? <optgroup label="Files and links"><option value="file">Modified files</option><option value="url">Tool URLs</option></optgroup> : null}
      </SearchSelect>}
    </span>
    {textKind ? <div className="text-editor-tabs" role="group" aria-label="Search editor">
      <button type="button" aria-label="Visual editor" aria-pressed={draft.editor === "visual"} disabled={busy} onClick={() => { if (draft.editor !== "visual") void editRules(); }}>Rules</button>
      <button type="button" aria-label="Query syntax" aria-pressed={draft.editor === "syntax"} disabled={busy} onClick={() => { if (draft.editor !== "syntax") editSyntax(); }}>Syntax</button>
    </div> : null}
    {targetNote ? <p className="text-target-note" id={`${filterID}-target`}>{targetNote}</p> : <span className="sr-only" id={`${filterID}-target`}>{targetDescription} {sameSpan}</span>}
  </div>;
  const filters = <div className="text-query-filters" role="group" aria-label="Additional query filters">
    {textKind ? visibleFields.map(field => {
      const name = "menu" in field ? field.menu : field.label;
      const id = `${filterID}-${field.key}`;
      const value = draft.scope[field.key];
      const options = field.key === "provider" ? choices.providers : field.key === "source" ? choices.sources : undefined;
      const date = field.key === "from" || field.key === "to";
      const inputType = dateInputType(value);
      const utcNote = inputType === "date" ? "Dates include the whole day in UTC." : "Timestamps use UTC.";
      return <div className="text-filter-condition" key={field.key}>
        <span className="text-join">and</span>
        <div className="text-filter-clause">
          <label htmlFor={id}>{field.label} <span className="text-filter-operator">{field.operator}</span></label>
          {options && !choicesError ? <SearchSelect id={id} aria-label={name} value={value} onChange={e => scope({ [field.key]: e.target.value })}>
            <option value="">Any {field.label.toLowerCase()}</option>
            {Array.from(new Set([...options, ...(value ? [value] : [])])).map(v => <option key={v} value={v}>{sourceLabel(v)}</option>)}
          </SearchSelect> : date && inputType !== "text" ? <input id={id} aria-label={name} type={inputType} step="any" value={inputType === "datetime-local" ? value.slice(0, -1) : value} aria-invalid={issueList.some(i => i.id === "dates")} aria-describedby={`${filterID}-dates ${id}-utc`} onChange={e => scope({ [field.key]: e.target.value && inputType === "datetime-local" ? new Date(`${e.target.value}Z`).toISOString() : e.target.value })} />
            : <WrappingInput id={id} aria-label={name} value={value} placeholder={field.placeholder} aria-invalid={date && issueList.some(i => i.id === "dates")} aria-describedby={date ? `${filterID}-dates ${id}-utc` : undefined} onValue={text => scope({ [field.key]: text })} />}
          {date ? <span className="text-filter-note" id={`${id}-utc`} title={utcNote}>UTC<span className="sr-only">: {utcNote}</span></span> : null}
          {field.key === "from" && !draft.scope.to ? <button type="button" aria-label="Search all time" onClick={() => { scope({ from: "" }); setAddedFields(fields => fields.filter(key => key !== "from")); }}>All time<Icon name="close" /></button>
            : <RemoveButton label={`Remove ${name.toLowerCase()} filter`} onRemove={() => { scope({ [field.key]: "" }); setAddedFields(fields => fields.filter(key => key !== field.key)); }} />}
        </div>
      </div>;
    }) : null}
    {textKind && !selectedScope ? (["roles", "kinds"] as const).filter(key => draft.scope[key].length).map(key => <div className="text-filter-condition" key={key}>
      <span className="text-join">and</span><div className="text-filter-clause"><span>Message {key === "roles" ? "role" : "type"} is</span>
        {draft.scope[key].map((value, index) => <React.Fragment key={value}>{index ? <span className="text-filter-operator">or</span> : null}<span className="text-saved-filter-value">{value}<button type="button" className="text-remove" aria-label={`Remove ${key === "roles" ? "role" : "message type"} ${value}`} onClick={() => scope({ [key]: draft.scope[key].filter(v => v !== value) })}><Icon name="close" /></button></span></React.Fragment>)}
      </div>
    </div>) : null}
    {workspaceScope.inline}
    {!workspaceScope.inline && (workspaceScope.filters.length || workspaceOpen) ? <div className="text-filter-condition text-workspace-condition">
      <span className="text-join">and</span>
      <button type="button" className="text-filter-clause text-workspace-filter" aria-label="Edit workspace filters" aria-expanded={workspaceOpen} aria-controls={workspaceID} onClick={() => setWorkspaceOpen(!workspaceOpen)}>
        <span className="text-filter-subject">Workspace</span> <span className="text-filter-operator">matches</span>
        <span className="text-workspace-expression">{workspaceScope.filters.length ? workspaceScope.filters.map((label, index) => <React.Fragment key={index}>{index ? <span className="text-filter-operator">and</span> : null}<span>{label}</span></React.Fragment>) : "all workspaces"}</span>
        <Icon name="chevron-down" />
      </button>
    </div> : null}
  </div>;
  const addToRoot = useAddCondition(draft.root, root => patch({ root }));
  const addField = (key: MessageField) => { setAddedFields(fields => [...fields, key]); requestAnimationFrame(() => document.getElementById(`${filterID}-${key}`)?.focus()); };
  const unusedFields = textKind ? availableFields.filter(field => !visibleFields.includes(field)) : [];
  const addCondition = <SearchPopover label="Add condition" className="text-add-condition" popupClassName="text-add-popup text-filter-popup" triggerContent={<><Icon name="plus" />Add condition</>}>{close => <>
      {visualRules ? <TextChoices add={addToRoot} close={close} /> : null}
      {visualRules && unusedFields.length ? <hr /> : null}
      {unusedFields.map(field => <button key={field.key} type="button" onClick={() => { close(); addField(field.key); }}>{"menu" in field ? field.menu : field.label}</button>)}
      {visualRules ? <><hr /><GroupChoices add={addToRoot} close={close} /></> : null}
      {workspaceScope.add || workspaceScope.editor ? <p className="text-popup-caption">{workspaceScope.caption ?? "Workspace"}</p> : null}
      {workspaceScope.add}
      {workspaceScope.editor ? <button type="button" onClick={() => { close(); setWorkspaceOpen(true); requestAnimationFrame(() => document.getElementById(workspaceID)?.focus()); }}>Workspace properties…</button> : null}
    </>}</SearchPopover>;
  // How text is matched is secondary to what is searched: one menu holds it,
  // and the trigger counts any choice that differs from the default.
  const applies = optionScope(draft), effect = optionsInEffect(draft);
  const optionCount = textKind ? [effect.caseSensitive, effect.separators, effect.fuzzy, draft.editor === "visual" ? draft.match !== "rules" : draft.mode !== "advanced"].filter(Boolean).length : Number(file.caseSensitive);
  const matchOptions = <SearchPopover label={`Match options${optionCount ? `, ${optionCount} changed` : ""}`} className="text-options-trigger" popupClassName="text-options-popup" triggerContent={<><Icon name="settings" />Options{optionCount ? <span className="text-options-count">{optionCount}</span> : null}</>}>{() => textKind ? <>
    <label className="text-option-mode">{draft.editor === "visual" ? <>Match using<SearchSelect value={draft.match} aria-label="Match using" onChange={e => patch({ match: e.target.value as TextSearchDraft["match"] })}><option value="rules">Words & phrases</option><option value="literal">Exact text</option><option value="regex">Regular expression</option></SearchSelect></> : <>Query format<SearchSelect aria-label="Query format" value={draft.mode} onChange={e => patch({ mode: e.target.value })}>{modes.map(([mode, label]) => <option key={mode} value={mode}>{label}</option>)}</SearchSelect></>}</label>
    <MatchOption label="Match letter case" help="Uppercase and lowercase must match, so Cache won't match cache." checked={draft.caseSensitive} change={caseSensitive => patch({ caseSensitive })} />
    <MatchOption label="Keep phrase punctuation and spacing" help="Phrases keep the punctuation and spacing between their words. Exact text and regular expressions always do." checked={draft.separators} change={separators => patch({ separators })} disabled={!applies.separators} />
    {!recordTarget ? <MatchOption label="Include word variations" help="Also match related word forms and close spellings. Applies to word rules only." checked={draft.fuzzy} change={fuzzy => patch({ fuzzy })} disabled={!applies.fuzzy} /> : null}
  </> : <MatchOption label="Match letter case" help="Uppercase and lowercase must match exactly in file paths or URLs." checked={file.caseSensitive} change={file.onCase} />}</SearchPopover>;
  const patternCaption = draft.editor === "syntax" ? `${modes.find(([mode]) => mode === draft.mode)?.[1] ?? "Query syntax"}. Choose Rules to edit a supported query as conditions.` : draft.match === "literal" ? "Exact text, kept as typed: no quotes or escaping needed" : "Regular expression (Go / RE2), checked before searching"
  const reading = searchSentence(draft, { kind, file: file.text, fileCase: file.caseSensitive, workspace: workspaceScope.reading ?? workspaceScope.filters, workspaceLead: workspaceScope.sentenceLead, fixedContext });
  return <EditorDOMPrefix.Provider value={domPrefix}><section className={`text-search-editor${compact ? " text-search-compact" : ""}`} aria-label="Search composer">
    {context}
    <form ref={form} onSubmit={event => { event.preventDefault(); if (textKind) void run(); else if (file.text.trim()) file.onApply(); }} aria-label={textKind ? "Build a text search" : "Build a file or URL search"}>
      {sentence ? <SearchReading parts={reading.parts} empty={reading.empty} /> : null}
      <div className="text-query-frame" role="group" aria-label="Query conditions and ordering">
        <div className="text-query-conditions" role="group" aria-label="Query conditions">
          {visualRules ? <GroupEditor heading={heading} lead="with" group={draft.root} issues={issueList} onChange={root => patch({ root })} /> : <>
            {heading}
            <div className="text-condition">
              <span className="text-join">with</span>
              {textKind ? <label className="text-pattern-input">
                {draft.editor === "visual"
                  ? <textarea aria-label={draft.match === "literal" ? "Text to find exactly" : "Regular expression"} value={draft.text} aria-invalid={issueList.some(i => i.id === "text")} onChange={e => patch({ text: e.target.value })} placeholder={draft.match === "literal" ? "e.g. foo::bar" : "e.g. error[0-9]+"} rows={2} />
                  : <textarea aria-label="Search query" value={draft.text} onChange={e => patch({ text: e.target.value })} rows={2} placeholder='(cache OR buffer) AND "race condition"' />}
                <span className="text-pattern-caption">{patternCaption}</span>
              </label> : <div className="text-file-input"><WrappingInput value={file.text} onValue={file.onText} placeholder={kind === "file" ? "File path or name" : "URL or host"} aria-label="Search library" /></div>}
            </div>
          </>}
          {filters}
        </div>
        <div id={`${filterID}-dates`}>{textKind ? issueList.filter(i => i.id === "dates").map(i => <p role="alert" className="text-editor-error" key={i.id}>{i.message}</p>) : null}</div>
        {textKind && choicesError && visibleFields.some(f => f.key === "provider" || f.key === "source") ? <p className="text-editor-scope-note">Suggestions could not be loaded. Enter the provider or source name directly.</p> : null}
        <div className="text-query-tools">
          {addCondition}
          {workspaceScope.editor ? <button type="button" className="text-layout-link" aria-expanded={workspaceOpen} aria-controls={workspaceID} onClick={() => setWorkspaceOpen(!workspaceOpen)}>Result layout</button> : null}
        </div>
      </div>
      <div className="text-editor-messages">
        {textKind ? <>
          {issueList.filter(issue => issue.id === "text").map(issue => <p className="text-editor-error" key={issue.id}>{issue.message}</p>)}
          {(draft.editor === "visual" ? draft.match !== "rules" : ["literal", "regex"].includes(draft.mode)) && ["all", "tools"].includes(draft.scope.surface) && !draft.scope.conversation_id && !draft.scope.workspace_id ? <p className="text-editor-scope-note">For this search across instructions or tools, add a Conversation ID filter or use workspace properties to select at most 100 workspaces.</p> : null}
          {error ? <div className="text-editor-error text-editor-alert" role="alert"><strong>Search not applied.</strong> {error} {active ? "Your previous results are still shown." : "No search has been applied."}</div> : null}
        </> : null}
        {filterError ? <p role="alert" className="text-editor-error">{filterError}</p> : null}
      </div>
      <div className="text-editor-footer">
        {orderControl}
        {matchOptions}
        <div className="text-submit-actions">
          {(textKind ? dirty : file.dirty) ? <p className="text-draft-status" role="status">Changes haven't been searched yet.</p> : null}
          {(textKind ? active : file.active) ? <button type="button" onClick={() => { pending.current?.abort(); if (textKind) onClear(); else file.onClear(); }}>Clear</button> : null}
          <button className="text-search-submit" type="submit" disabled={!!filterError || (textKind ? busy || !compiled.request : !file.text.trim())}><Icon name="search" />{textKind ? busy ? "Checking…" : recordTarget ? "Apply keyword filter" : "Search messages" : "Search"}</button>
        </div>
      </div>
    </form>
    <div id={workspaceID} hidden={!workspaceOpen} tabIndex={-1} className="text-workspace-controls" role="region" aria-label="Workspace properties and result layout">
      <div className="text-workspace-editor-heading"><strong>Workspace properties & result layout</strong><button type="button" onClick={() => setWorkspaceOpen(false)}>Done</button></div>
      <p>Workspace filters select the containers to search. Filters and layout changes apply immediately.</p>
      {workspaceScope.editor}
    </div>
  </section></EditorDOMPrefix.Provider>;
}
