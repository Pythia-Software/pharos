import React, { createContext, useContext, useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon } from "./icons";
import { SearchHelp } from "./search-help";
import { SearchSelect } from "./search-select";
import { SearchInput } from "./search-input";
import { chooseScope, compileRules, conditionSummary, newGroup, newRule, querySyntax, scopeChoice, scopes, searchRequest, sourceLabel, visualFromQuery, type RuleKind, type SearchIssue, type TextCondition, type TextGroup, type TextQuery, type TextScope, type TextSearchDraft } from "./text-search-model";
import "./text-search-editor.css";

const EditorDOMPrefix = createContext("");
const shortRuleLabels: Record<RuleKind, string> = { words: "All words", anyWords: "Any word", phrase: "Phrase", prefix: "Word starts with", near: "Phrases near", pre: "Phrases in order" };
const ruleHelp: Record<RuleKind, string> = {
  words: "Each word must appear somewhere in the same message or chosen text span. Type ordinary words; AND, OR, quotes, and parentheses are not commands here.",
  anyWords: "At least one of these words must appear in the message or chosen text span.",
  phrase: "These words stay together, in this order. No quotation marks needed.",
  prefix: "Matches word beginnings, such as migrat matching migration. No * symbol needed.",
  near: "Both phrases must appear within the chosen number of words, in either order.",
  pre: "Both phrases must appear within the chosen number of words, with the first phrase before the second.",
};
function MatchToggle({ label, short, help, checked, change, disabled = false }: { label: string; short: string; help: string; checked: boolean; change: (value: boolean) => void; disabled?: boolean }) {
  return <span className={`text-option-chip${checked ? " is-active" : ""}`}><label><input type="checkbox" aria-label={label} checked={checked} disabled={disabled} onChange={event => change(event.target.checked)} />{short}</label><SearchHelp label={label}>{help}</SearchHelp></span>;
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
      setPosition({ left: Math.max(8, Math.min(rect.right - width, window.innerWidth - width - 8)), top: rect.bottom + height + 16 < window.innerHeight ? rect.bottom + 6 : Math.max(8, rect.top - height - 6) });
    };
    const outside = (event: Event) => { if (!trigger.current?.contains(event.target as Node) && !popup.current?.contains(event.target as Node)) setOpen(false); };
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); setOpen(false); trigger.current?.focus(); } };
    place();
    popup.current?.querySelector<HTMLElement>("select, button")?.focus();
    document.addEventListener("pointerdown", outside);
    document.addEventListener("focusin", outside);
    document.addEventListener("keydown", escape);
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => { document.removeEventListener("pointerdown", outside); document.removeEventListener("focusin", outside); document.removeEventListener("keydown", escape); window.removeEventListener("resize", place); window.removeEventListener("scroll", place, true); };
  }, [open]);
  return <>
    <button ref={trigger} type="button" className={className} aria-label={label} title={title} aria-haspopup="dialog" aria-expanded={open} aria-controls={open ? id : undefined} onClick={() => setOpen(!open)}>{triggerContent}</button>
    {open ? createPortal(<div ref={popup} id={id} role="dialog" aria-label={label} className={`text-condition-popup ${popupClassName}`} style={position}>{children(() => setOpen(false))}</div>, trigger.current?.closest("dialog") ?? document.body) : null}
  </>;
}
function ConditionOptions({ node, path, scopeLabel, onExclude, onRemove }: { node: TextCondition; path: string; scopeLabel: string; onExclude: (exclude: boolean) => void; onRemove: () => void }) {
  const prefix = useContext(EditorDOMPrefix);
  const type = node.kind === "group" ? "group" : "rule";
  return <SearchPopover label={`Options for ${type} ${path}`} title={`${type === "group" ? "Group" : "Rule"} options and help`} className="text-condition-options" triggerContent={<Icon name="settings" />}>{close => <>
    <p>{node.kind === "group" ? "A group keeps its conditions together. AND NOT rejects a match only inside the enclosing group." : ruleHelp[node.kind]}</p>
    <label>Use in {scopeLabel}<SearchSelect aria-label={`Include or exclude ${type} ${path}`} value={node.exclude ? "exclude" : "include"} onChange={event => {
      close(); onExclude(event.target.value === "exclude");
      requestAnimationFrame(() => document.getElementById(`${prefix}${node.id}-${node.kind === "group" ? "join" : "text"}`)?.focus());
    }}><option value="include">Match</option><option value="exclude">AND NOT</option></SearchSelect></label>
    <button type="button" onClick={onRemove} aria-label={`Remove ${type} ${path}`}><Icon name="close" />Remove {type}</button>
  </>}</SearchPopover>;
}

function AddClause({ group, onChange, path = "1", depth = 0 }: { group: TextGroup; onChange: (group: TextGroup) => void; path?: string; depth?: number }) {
  const prefix = useContext(EditorDOMPrefix);
  const add = (node: TextCondition) => {
    onChange({ ...group, children: [...group.children, node] });
    const first = node.kind === "group" ? node.children[0] : node;
    requestAnimationFrame(() => document.getElementById(`${prefix}${first.id}-text`)?.focus());
  };
  return <SearchPopover label={`Add clause to group ${path}`} className="text-add-condition" popupClassName="text-add-popup" triggerContent={<><Icon name="plus" />Add clause<Icon name="chevron-down" /></>}>{close => <>
    <button type="button" onClick={() => { close(); add(newRule()); }}>Matching condition</button>
    {depth < 3 ? <button type="button" onClick={() => { close(); add(newGroup()); }}>Nested group</button> : null}
    <button type="button" onClick={() => { close(); add({ ...newRule(), exclude: true }); }}>AND NOT condition</button>
    {depth < 3 ? <button type="button" onClick={() => { close(); add({ ...newGroup(), exclude: true }); }}>AND NOT group</button> : null}
  </>}</SearchPopover>;
}

function GroupEditor({ group, onChange, issues, subject, path = "1", number = "", depth = 0, parentLabel = "entire search", onRemove }: { group: TextGroup; onChange: (group: TextGroup) => void; issues: SearchIssue[]; subject?: React.ReactNode; path?: string; number?: string; depth?: number; parentLabel?: string; onRemove?: () => void }) {
  const prefix = useContext(EditorDOMPrefix);
  const edit = (index: number, node: TextCondition) => onChange({ ...group, children: group.children.map((child, i) => i === index ? node : child) });
  const remove = (index: number) => onChange({ ...group, children: group.children.filter((_, i) => i !== index) });
  const groupIssue = issues.find(issue => issue.id === group.id);
  const groupLabel = depth ? `Group ${number}` : "Entire search";
  const children = group.children.map((child, index) => ({ child, index }));
  const included = children.filter(({ child }) => !child.exclude), excluded = children.filter(({ child }) => child.exclude);
  const renderConditions = (nodes: typeof children, join: "all" | "any") => <div className="text-group-children">
    {nodes.map(({ child, index }, displayIndex) => {
      const childPath = `${path}.${index + 1}`;
      const connector = displayIndex ? <span className="text-join">{join === "all" ? "AND" : "OR"}</span> : null;
      if (child.kind === "group") {
        const groupNumber = group.children.slice(0, index + 1).filter(c => c.kind === "group").length;
        return <div key={child.id} className="text-condition text-condition-group">{connector}<GroupEditor group={child} path={childPath} number={`${number ? `${number}.` : ""}${groupNumber}`} depth={depth + 1} parentLabel={groupLabel} onChange={node => edit(index, node)} onRemove={() => remove(index)} issues={issues} /></div>;
      }
      const error = issues.find(issue => issue.id === child.id);
      const proximity = child.kind === "near" || child.kind === "pre";
      return <div key={child.id} className="text-condition">{connector}<div className="text-rule">
        <div className="text-rule-controls">
          <SearchSelect aria-label={`Match type for rule ${childPath}`} value={child.kind} onChange={e => edit(index, { ...child, kind: e.target.value as RuleKind })}>{Object.entries(shortRuleLabels).map(([key, label]) => <option value={key} key={key}>{label}</option>)}</SearchSelect>
          <SearchInput id={`${prefix}${child.id}-text`} aria-label={`Text for rule ${childPath}`} aria-invalid={!!error} aria-describedby={error ? `${prefix}${child.id}-error` : undefined} value={child.text} onChange={e => edit(index, { ...child, text: e.target.value })} placeholder={child.kind === "prefix" ? "e.g. migrat" : proximity ? "First phrase" : child.kind === "phrase" ? "e.g. race condition" : "Words to find…"} />
          <ConditionOptions node={child} path={childPath} scopeLabel={groupLabel} onExclude={exclude => edit(index, { ...child, exclude })} onRemove={() => remove(index)} />
        </div>
        {proximity ? <div className="text-proximity">
          <span>{child.kind === "pre" ? "then" : "and"}</span>
          <SearchInput aria-label={`Second phrase for rule ${childPath}`} aria-invalid={!!error} aria-describedby={error ? `${prefix}${child.id}-error` : undefined} value={child.other} onChange={e => edit(index, { ...child, other: e.target.value })} placeholder="Second phrase" />
          <label>within <input type="number" min={0} max={100} aria-label={`Words between phrases in rule ${childPath}`} value={child.distance} onChange={e => edit(index, { ...child, distance: e.target.value })} /> intervening words</label>
        </div> : null}
        {error ? <p id={`${prefix}${child.id}-error`} className="text-editor-error">{error.message}</p> : null}
      </div></div>;
    })}
  </div>;
  const content = <>
    <div className="text-group-heading">
      {depth ? <strong>{groupLabel}</strong> : subject}
      {group.children.length > 1 ? <>
        <label>{depth ? "match" : "must match"}<SearchSelect id={`${prefix}${group.id}-join`} aria-label={`Match rules in group ${path}`} value={group.join} onChange={e => onChange({ ...group, join: e.target.value as "all" | "any" })}><option value="all">{depth ? "all of these" : "all text clauses"}</option><option value="any">{depth ? "any of these" : "any text clause"}</option></SearchSelect></label>
        <SearchHelp label={`Matching group ${path}`}>{group.join === "all" ? "Every positive condition must match within the same message or chosen text span." : "At least one positive condition must match within the same message or chosen text span."} AND NOT rejects any match of its bracketed conditions, within {depth ? groupLabel : "the entire text group"} only.{depth ? "" : " Message and workspace filters always apply as additional AND conditions."}</SearchHelp>
      </> : null}
      {depth ? <AddClause group={group} onChange={onChange} path={path} depth={depth} /> : null}
      {onRemove ? <ConditionOptions node={group} path={path} scopeLabel={parentLabel} onExclude={exclude => onChange({ ...group, exclude })} onRemove={onRemove} /> : null}
    </div>
    <div className={`text-group-matches${depth ? "" : " text-root-matches"}${group.join === "any" && included.length > 1 && (!depth || excluded.length) ? " text-bracketed-matches" : ""}`} role="group" aria-label={`${groupLabel} matching conditions`}>{renderConditions(included, group.join)}</div>
    {excluded.length ? <div className="text-negative-clause">
      <span className="text-join text-not">AND NOT</span>
      <div className={`text-negative-group${excluded.length === 1 && excluded[0].child.kind === "group" ? " text-negative-single-group" : ""}`} role="group" aria-label={`AND NOT in ${groupLabel}`}>
        {renderConditions(excluded, "any")}
      </div>
    </div> : null}
    {groupIssue ? <p className="text-editor-error">{groupIssue.message}</p> : null}
  </>;
  // Only explicit nested groups get a container. Root clauses share the query
  // flow with metadata filters; OR alternatives retain their own boundary.
  return depth ? <fieldset className="text-rule-group text-nested-group"><legend className="sr-only">Rule group {path}</legend>{content}</fieldset> : content;
}

async function validate(request: Record<string, unknown>, signal: AbortSignal): Promise<TextQuery> {
  const response = await fetch("/api/search/text/validate", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(request), signal });
  const result = await response.json();
  if (!response.ok) throw new Error(result.error ?? "Could not validate the search. Please try again.");
  return result.where;
}
const modes = [["advanced", "Boolean query syntax"], ["terms", "All words, with operators treated as words"], ["phrase", "Whole query as a phrase"], ["literal", "Exact text, including punctuation"], ["regex", "Regular expression (Go / RE2)"]];
const dateInputType = (value: string) => !value || /^\d{4}-\d{2}-\d{2}$/.test(value) ? "date" : "text";
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
export function TextSearchEditor({ value, active, initialError, onApply, onClear, kind, onKind, file, workspaceScope, orderControl, onDraft, context, textOnly = false, fixedContext = false, filterError, allowedScopes, compact = false }: { onDraft?: (draft: TextSearchDraft) => void; context?: React.ReactNode; textOnly?: boolean; fixedContext?: boolean; filterError?: string; allowedScopes?: string[]; compact?: boolean; value: TextSearchDraft; active: boolean; initialError?: string; onApply: (value: TextSearchDraft) => void; onClear: () => void; kind: LibrarySearchKind; onKind: (kind: LibrarySearchKind) => void; file: FileSearch; workspaceScope: { filters: string[]; editor?: React.ReactNode; inline?: React.ReactNode; add?: React.ReactNode; caption?: string }; orderControl: React.ReactNode }) {
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
    if (draft.fuzzy) { setError("Turn off “Include word variations” before switching to query syntax, so the converted query keeps the same matching rules."); return; }
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
  const availableFields = messageFields.filter(field => !fixedContext || !["conversation_id", "workspace_id"].includes(field.key));
  const visibleFields = availableFields.filter(field => draft.scope[field.key] || addedFields.includes(field.key));
  const targetDescription = textKind ? selectedScope?.description ?? `Saved message scope: ${draft.scope.surface}. Its role and message-type restrictions appear below.` : kind === "file" ? "Search recorded modified file paths and names, including old paths for renamed files. This does not search file contents." : "Search URLs recorded in tool activity, by URL or hostname. This does not search the contents of web pages.";
  const subject = <span className="text-query-subject">
    <SearchSelect aria-label="Search target" value={textKind ? selectedScope?.id ?? "custom" : kind} onChange={event => {
      const target = event.target.value;
      if (target === "file" || target === "url") onKind(target);
      else { patch({ scope: chooseScope(draft.scope, target) }); onKind("text"); }
    }}>
      {!selectedScope ? <option value="custom">Custom message scope</option> : null}
      <optgroup label="Messages">{scopes.slice(0, 8).filter(s => !allowedScopes || allowedScopes.includes(s.id)).map(s => <option key={s.id} value={s.id}>{s.label}</option>)}</optgroup>
      <optgroup label="Parts of user messages">{scopes.slice(8).filter(s => !allowedScopes || allowedScopes.includes(s.id)).map(s => <option key={s.id} value={s.id}>{s.label}</option>)}</optgroup>
      {!textOnly ? <optgroup label="Files and links"><option value="file">Modified files</option><option value="url">Tool URLs</option></optgroup> : null}
    </SearchSelect>
    <SearchHelp label="Search target">{targetDescription}{textKind ? scopes.slice(8).some(s => s.id === selectedScope?.id) ? " All text rules apply within the same classified span." : " All text rules apply within the same message." : ""}</SearchHelp>
  </span>;
  const filters = <div className="text-query-filters" role="group" aria-label="Additional query filters">
    {textKind ? visibleFields.map(field => {
      const name = "menu" in field ? field.menu : field.label;
      const id = `${filterID}-${field.key}`;
      const value = draft.scope[field.key];
      const options = field.key === "provider" ? choices.providers : field.key === "source" ? choices.sources : undefined;
      const date = field.key === "from" || field.key === "to";
      const Input = date && dateInputType(value) === "date" ? "input" : SearchInput;
      return <div className="text-filter-condition" key={field.key}>
        <span className="text-join">AND</span>
        <div className="text-filter-clause">
          <label htmlFor={id}><span className="text-filter-subject">Message</span> {field.label.toLowerCase()} <span className="text-filter-operator">{field.operator}</span></label>
          {options && !choicesError ? <SearchSelect id={id} aria-label={name} value={value} onChange={e => scope({ [field.key]: e.target.value })}>
            <option value="">Any {field.label.toLowerCase()}</option>
            {Array.from(new Set([...options, ...(value ? [value] : [])])).map(v => <option key={v} value={v}>{sourceLabel(v)}</option>)}
          </SearchSelect> : <Input id={id} aria-label={name} type={date ? dateInputType(value) : "text"} value={value} placeholder={field.placeholder} aria-invalid={date && issueList.some(i => i.id === "dates")} aria-describedby={date ? `${filterID}-dates` : undefined} onChange={e => scope({ [field.key]: e.target.value })} />}
          {date ? <SearchHelp label={`${name} UTC`}>Dates include the whole day in UTC.</SearchHelp> : null}
          <button type="button" className="text-remove" aria-label={`Remove ${name.toLowerCase()} filter`} onClick={() => { scope({ [field.key]: "" }); setAddedFields(fields => fields.filter(key => key !== field.key)); }}><Icon name="close" /></button>
        </div>
      </div>;
    }) : null}
    {textKind && !selectedScope ? (["roles", "kinds"] as const).filter(key => draft.scope[key].length).map(key => <div className="text-filter-condition" key={key}>
      <span className="text-join">AND</span><div className="text-filter-clause"><span>Message {key === "roles" ? "role" : "type"} is</span>
        {draft.scope[key].map((value, index) => <React.Fragment key={value}>{index ? <span className="text-join">OR</span> : null}<span className="text-saved-filter-value">{value}<button type="button" className="text-remove" aria-label={`Remove ${key === "roles" ? "role" : "message type"} ${value}`} onClick={() => scope({ [key]: draft.scope[key].filter(v => v !== value) })}><Icon name="close" /></button></span></React.Fragment>)}
      </div>
    </div>) : null}
    {workspaceScope.inline}
    {!workspaceScope.inline && (workspaceScope.filters.length || workspaceOpen) ? <div className="text-filter-condition text-workspace-condition">
      <span className="text-join">AND</span>
      <button type="button" className="text-filter-clause text-workspace-filter" aria-label="Edit workspace filters" aria-expanded={workspaceOpen} aria-controls={workspaceID} onClick={() => setWorkspaceOpen(!workspaceOpen)}>
        <span className="text-filter-subject">Workspace</span> matches
        <span className="text-workspace-expression">{workspaceScope.filters.length ? workspaceScope.filters.map((label, index) => <React.Fragment key={index}>{index ? <span className="text-join">AND</span> : null}<span>{label}</span></React.Fragment>) : "all workspaces"}</span>
        <Icon name="chevron-down" />
      </button>
      <SearchHelp label="Workspace filter">Choose the workspaces containing the matching content. These filters apply in addition to the text group and message filters. Workspace filters update results immediately.</SearchHelp>
    </div> : null}
  </div>;
  const addFilter = <SearchPopover label="Add filter to query" className="text-add-filter" popupClassName="text-add-popup text-filter-popup" triggerContent={<><Icon name="plus" />Add filter<Icon name="chevron-down" /></>}>{close => <>
      {textKind ? <><p className="text-popup-caption">Matching message</p>{availableFields.filter(field => !visibleFields.includes(field)).map(field => <button key={field.key} type="button" onClick={() => {
        close(); setAddedFields(fields => [...fields, field.key]);
        requestAnimationFrame(() => document.getElementById(`${filterID}-${field.key}`)?.focus());
      }}>{"menu" in field ? field.menu : field.label}</button>)}</> : null}
      {workspaceScope.add || workspaceScope.editor ? <p className="text-popup-caption">{workspaceScope.caption ?? "Containing workspace"}</p> : null}
      {workspaceScope.add}
      {workspaceScope.editor ? <button type="button" onClick={() => { close(); setWorkspaceOpen(true); requestAnimationFrame(() => document.getElementById(workspaceID)?.focus()); }}>Workspace properties…</button> : null}
    </>}</SearchPopover>;
  return <EditorDOMPrefix.Provider value={domPrefix}><section className={`text-search-editor${compact ? " text-search-compact" : ""}`} aria-label="Search composer">
    {context}
    <div className="text-editor-toolbar">
      <span className="text-editor-caption">Query</span>
      <div className="text-editor-tabs" role="group" aria-label="Search editor">
        <button type="button" aria-label="Visual editor" aria-pressed={!textKind || draft.editor === "visual"} disabled={busy} onClick={() => { if (textKind && draft.editor !== "visual") void editRules(); }}><Icon name="edit" />Rules</button>
        <button type="button" aria-label="Query syntax" aria-pressed={textKind && draft.editor === "syntax"} disabled={busy || !textKind} onClick={() => { if (draft.editor !== "syntax") editSyntax(); }}>Syntax</button>
      </div>
      <SearchHelp label="Editor mode">{textKind ? "Rules and Syntax are two ways to edit the text group. The search target, additional AND filters, and result order apply to both editors. Ordinary text in Rules is never interpreted as query syntax." : "File and URL searches use plain text. Choose a message search target to use query syntax."}</SearchHelp>
      {textKind ? draft.editor === "visual" ? <label className="text-match-style">Using<SearchSelect value={draft.match} aria-label="Match using" onChange={e => patch({ match: e.target.value as TextSearchDraft["match"] })}><option value="rules">Words & phrases</option><option value="literal">Exact text</option><option value="regex">Regular expression</option></SearchSelect></label> : <label className="text-match-style">Using<SearchSelect aria-label="Query format" value={draft.mode} onChange={e => patch({ mode: e.target.value })}>{modes.map(([mode, label]) => <option key={mode} value={mode}>{label}</option>)}</SearchSelect></label> : <span className="text-match-style">Plain text</span>}
    </div>
    <form ref={form} onSubmit={event => { event.preventDefault(); if (textKind) void run(); else file.onApply(); }} aria-label={textKind ? "Build a text search" : "Build a file or URL search"}>
      <div className="text-query-frame" role="group" aria-label="Query conditions and ordering">
        <div className="text-query-conditions" role="group" aria-label="Query conditions">
          {visualRules ? <GroupEditor subject={subject} group={draft.root} issues={issueList} onChange={root => patch({ root })} /> : <>
            <div className="text-group-heading">{subject}<span>{textKind ? "must match" : "must contain"}</span></div>
            {textKind ? draft.editor === "visual" ? <div className="text-pattern-input">
              <label>{draft.match === "literal" ? "Text to find exactly" : "Regular expression"}<textarea aria-label={draft.match === "literal" ? "Text to find exactly" : "Regular expression"} value={draft.text} aria-invalid={issueList.some(i => i.id === "text")} onChange={e => patch({ text: e.target.value })} placeholder={draft.match === "literal" ? "e.g. foo::bar" : "e.g. error[0-9]+"} rows={2} /></label>
              <SearchHelp label={draft.match === "literal" ? "Exact text" : "Regular expression"}>{draft.match === "literal" ? "Spaces and punctuation are preserved. No quotation marks or escaping needed." : "Uses Go / RE2. The pattern is checked before searching; unsupported syntax is shown as an error."}</SearchHelp>
            </div> : <div className="text-pattern-input text-syntax-input">
              <label>Search query<textarea aria-label="Search query" value={draft.text} onChange={e => patch({ text: e.target.value })} rows={2} placeholder='(cache OR buffer) AND "race condition"' /></label>
              <SearchHelp label="Query syntax">Syntax is checked before replacing the current search. Choose Rules to convert a supported query into editable rules.</SearchHelp>
            </div> : <div className="text-file-input"><SearchInput type="search" value={file.text} onChange={event => file.onText(event.target.value)} placeholder={kind === "file" ? "File path or name…" : "URL or host…"} aria-label="Search library" /></div>}
          </>}
          {filters}
        </div>
        <div id={`${filterID}-dates`}>{textKind ? issueList.filter(i => i.id === "dates").map(i => <p role="alert" className="text-editor-error" key={i.id}>{i.message}</p>) : null}</div>
        {textKind && choicesError && visibleFields.some(f => f.key === "provider" || f.key === "source") ? <p className="text-editor-scope-note">Suggestions could not be loaded. Enter the provider or source name directly.</p> : null}
        <div className="text-query-tools">
          <div className="text-query-actions">
            {visualRules ? <AddClause group={draft.root} onChange={root => patch({ root })} /> : null}
            {addFilter}
          </div>
          {orderControl}
        </div>
      </div>
      {textKind ? <>
        {issueList.filter(issue => issue.id === "text").map(issue => <p className="text-editor-error" key={issue.id}>{issue.message}</p>)}
        {(draft.editor === "visual" ? draft.match !== "rules" : ["literal", "regex"].includes(draft.mode)) && ["all", "tools"].includes(draft.scope.surface) && !draft.scope.conversation_id && !draft.scope.workspace_id ? <p className="text-editor-scope-note">For this search across instructions or tools, add a Conversation ID filter or use workspace properties to select at most 100 workspaces.</p> : null}
        {error ? <div className="text-editor-error text-editor-alert" role="alert"><strong>Search not applied.</strong> {error} {active ? "Your previous results are still shown." : "No search has been applied."}</div> : null}
      </> : null}
      {filterError ? <p role="alert" className="text-editor-error">{filterError}</p> : null}
      <div className="text-editor-footer">
        {textKind ? <div className="text-editor-toggles">
          <MatchToggle label="Match letter case" short="Case" help="Match uppercase and lowercase letters exactly. With this on, Cache does not match cache." checked={draft.caseSensitive} change={caseSensitive => patch({ caseSensitive })} />
          <MatchToggle label="Keep phrase punctuation and spacing" short="Punctuation" help="Keep punctuation and spacing between the words in phrase rules. Exact text and regular expressions already preserve these characters." checked={draft.separators} change={separators => patch({ separators })} disabled={draft.editor === "visual" && draft.match !== "rules"} />
          <MatchToggle label="Include word variations" short="Variations" help="Include related word forms and close spellings. Available for word rules; exact text and regular expressions use their own matching rules." checked={draft.fuzzy} change={fuzzy => patch({ fuzzy })} disabled={draft.editor === "visual" ? draft.match !== "rules" : !["advanced", "terms"].includes(draft.mode)} />
        </div> : <MatchToggle label="Match letter case" short="Case" help="Match uppercase and lowercase letters exactly in file paths or URLs." checked={file.caseSensitive} change={file.onCase} />}
        <div className="text-submit-actions">
          {(textKind ? active : file.active) ? <button type="button" onClick={() => { pending.current?.abort(); if (textKind) onClear(); else file.onClear(); }}>Clear</button> : null}
          <button className="text-search-submit" type="submit" disabled={!!filterError || (textKind ? busy || !compiled.request : !file.text.trim())}><Icon name="search" />{textKind ? busy ? "Checking…" : "Search messages" : "Search"}</button>
        </div>
      </div>
      <div className="text-editor-meta">
        {textKind && draft.editor === "visual" && draft.match === "rules" && compiled.request ? <details className="text-editor-readback"><summary>Read as a sentence</summary><p>Find {selectedScope?.label.toLowerCase() ?? "messages in the chosen scope"} matching {conditionSummary(draft.root)}.</p></details> : null}
        {workspaceScope.editor ? <button type="button" className="text-layout-link" aria-expanded={workspaceOpen} aria-controls={workspaceID} onClick={() => setWorkspaceOpen(!workspaceOpen)}><Icon name="settings" />Result layout</button> : null}
        {(textKind ? dirty : file.dirty) ? <p className="text-draft-status" role="status">Changes haven't been searched yet.</p> : null}
      </div>
    </form>
    <div id={workspaceID} hidden={!workspaceOpen} tabIndex={-1} className="text-workspace-controls" role="region" aria-label="Workspace properties and result layout">
      <div className="text-workspace-editor-heading"><strong>Workspace properties & result layout</strong><button type="button" onClick={() => setWorkspaceOpen(false)}>Done</button></div>
      <p>Workspace filters select the containers to search. Filters and layout changes apply immediately.</p>
      {workspaceScope.editor}
    </div>
  </section></EditorDOMPrefix.Provider>;
}
