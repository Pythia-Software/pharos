// The visual editor produces the engine's typed tree. Text entered into a rule
// never becomes Boolean syntax, even when it contains AND, OR, or parentheses.
export type TextQuery = { op: "term" | "phrase" | "prefix" | "and" | "or" | "not" | "near" | "pre" | "literal" | "regex"; text?: string; distance?: number; children?: TextQuery[] };
export type RuleKind = "words" | "anyWords" | "phrase" | "prefix" | "near" | "pre";
export type TextRule = { id: string; kind: RuleKind; text: string; other: string; distance: string; exclude: boolean };
export type TextGroup = { id: string; kind: "group"; join: "all" | "any"; children: TextCondition[]; exclude: boolean };
export type TextCondition = TextRule | TextGroup;
export type TextScope = { surface: string; roles: string[]; kinds: string[]; repository: string; provider: string; source: string; from: string; to: string; conversation_id: string; workspace_id: string };
export type TextSearchDraft = { editor: "visual" | "syntax"; match: "rules" | "literal" | "regex"; root: TextGroup; text: string; mode: string; scope: TextScope; fuzzy: boolean; caseSensitive: boolean; separators: boolean };
export type SearchIssue = { id: string; message: string };
let nextID = 0;
export const newRule = (text = ""): TextRule => ({ id: `rule-${++nextID}`, kind: "words", text, other: "", distance: "8", exclude: false });
export const newGroup = (): TextGroup => ({ id: `group-${++nextID}`, kind: "group", join: "all", children: [newRule()], exclude: false });
export const emptyScope = (): TextScope => ({ surface: "prose", roles: [], kinds: [], repository: "", provider: "", source: "", from: "", to: "", conversation_id: "", workspace_id: "" });
export const emptyTextSearch = (): TextSearchDraft => ({ editor: "visual", match: "rules", root: newGroup(), text: "", mode: "advanced", scope: emptyScope(), fuzzy: false, caseSensitive: false, separators: false });
export const ruleLabels: Record<RuleKind, string> = { words: "All of these words", anyWords: "Any of these words", phrase: "This phrase", prefix: "A word beginning with", near: "Two phrases near each other", pre: "Two phrases in this order" };
export const scopes = [
  { id: "prose", label: "Conversation messages", description: "User messages, agent replies, and any saved agent reasoning.", surface: "prose" },
  { id: "user", label: "User messages", description: "What users sent to agents, including pasted or attached text retained in those messages.", surface: "user" },
  { id: "assistant", label: "Agent replies", description: "The responses agents sent back to users.", surface: "assistant" },
  { id: "reasoning", label: "Saved agent reasoning", description: "Reasoning explicitly saved by the source. Some sources do not retain it.", surface: "reasoning" },
  { id: "system", label: "System instructions", description: "Setup and rules supplied in system messages, when retained by the source.", surface: "all", role: "system" },
  { id: "developer", label: "Developer instructions", description: "Instructions supplied by the application in developer messages, when retained.", surface: "all", role: "developer" },
  { id: "tools", label: "Tool calls and output", description: "Commands, tool requests, and the output returned to an agent.", surface: "tools" },
  { id: "all", label: "All stored text", description: "Every stored message type, including instructions and tool activity.", surface: "all" },
  { id: "typed", label: "Words typed by users", description: "Only spans classified as typed. Unclassified or stale spans may be missing.", surface: "typed" },
  { id: "pasted", label: "Pasted text", description: "Only spans classified as pasted into a user's message.", surface: "pasted" },
  { id: "quoted", label: "Quoted text", description: "Only spans classified as quotations.", surface: "quoted" },
  { id: "resent", label: "Resent text", description: "Only spans classified as repeated from an earlier message.", surface: "resent" },
  { id: "template", label: "Template text", description: "Only spans classified as template content.", surface: "template" },
  { id: "harness", label: "Agent setup text", description: "Only spans classified as content inserted by the agent harness.", surface: "harness" },
  { id: "automated", label: "Automatically inserted text", description: "Only spans classified as automated content.", surface: "automated" },
  { id: "attachment", label: "Attachment text", description: "Only spans classified as attachment content inside retained messages.", surface: "attachment" },
] as const;
export function scopeChoice(scope: TextScope) { return scopes.find(s => s.surface === scope.surface && scope.kinds.length === 0 && scope.roles.join(",") === ("role" in s ? s.role : "")); }
export function chooseScope(scope: TextScope, id: string): TextScope { const choice = scopes.find(s => s.id === id); return choice ? { ...scope, surface: choice.surface, roles: "role" in choice ? [choice.role] : [], kinds: [] } : scope; }
export const sourceLabel = (value: string) => ({ claude: "Claude", codex: "Codex", chatgpt: "ChatGPT", conductor: "Conductor", antigravity: "Antigravity", canonical: "Imported archive", tl1: "TL1", "tl1-export": "TL1 export" }[value] ?? value);
const hasWord = (text: string) => /[\p{L}\p{N}]/u.test(text);
const combine = (op: "and" | "or", nodes: TextQuery[]): TextQuery => nodes.length === 1 ? nodes[0] : { op, children: nodes };
export function compileRules(root: TextGroup): { where?: TextQuery; issues: SearchIssue[] } {
  const issues: SearchIssue[] = [];
  let count = 0;
  const compile = (condition: TextCondition, depth: number): TextQuery | undefined => {
    if (++count > 32 || depth > 4) { issues.push({ id: condition.id, message: "This search is too complex. Use at most 32 rules and groups, nested up to four levels." }); return; }
    if (condition.kind === "group") {
      const included: TextQuery[] = [], excluded: TextQuery[] = [];
      for (const child of condition.children) { const node = compile(child, depth + 1); if (node) (child.exclude ? excluded : included).push(node); }
      if (!condition.children.some(c => !c.exclude)) issues.push({ id: condition.id, message: "Include at least one rule in this group before excluding matches." });
      if (!included.length) return;
      const positive = combine(condition.join === "all" ? "and" : "or", included);
      return excluded.length ? { op: "not", children: [positive, combine("or", excluded)] } : positive;
    }
    const issue = (message: string) => { issues.push({ id: condition.id, message }); return undefined; };
    const text = condition.text.trim();
    if (!hasWord(text)) return issue("Add a word or phrase to this rule.");
    if (new TextEncoder().encode(text).length > 2000) return issue("Shorten this rule to 2,000 bytes or fewer.");
    if (condition.kind === "near" || condition.kind === "pre") {
      if (!hasWord(condition.other)) return issue("Add the second phrase.");
      const distance = Number(condition.distance);
      if (!condition.distance.trim() || !Number.isInteger(distance) || distance < 0 || distance > 100) return issue("Choose between 0 and 100 words between the phrases.");
      return { op: condition.kind, distance, children: [{ op: "phrase", text }, { op: "phrase", text: condition.other.trim() }] };
    }
    if (condition.kind === "prefix") {
      if (/\s|[*?]/u.test(text)) return issue("Enter one word beginning, such as migrat. No wildcard symbol is needed.");
      return { op: "prefix", text };
    }
    if (condition.kind === "phrase") return { op: "phrase", text };
    const words = text.split(/\s+/u).filter(hasWord);
    return combine(condition.kind === "anyWords" ? "or" : "and", words.map(text => ({ op: "term", text })));
  };
  const where = compile(root, 0);
  if (where && JSON.stringify(where).length > 16000) issues.push({ id: root.id, message: "Shorten the search or use fewer rules." });
  return { where: issues.length ? undefined : where, issues };
}
export function searchRequest(draft: TextSearchDraft): { request?: Record<string, unknown>; issues: SearchIssue[] } {
  let where: TextQuery | undefined;
  let issues: SearchIssue[] = [];
  if (draft.editor === "visual" && draft.match === "rules") { ({ where, issues } = compileRules(draft.root)); }
  else if (!draft.text) issues.push({ id: "text", message: "Enter the text to find." });
  else if (draft.editor === "visual") where = { op: draft.match === "literal" ? "literal" : "regex", text: draft.text };
  if (/^\d{4}-\d{2}-\d{2}$/.test(draft.scope.from) && /^\d{4}-\d{2}-\d{2}$/.test(draft.scope.to) && draft.scope.from > draft.scope.to) issues.push({ id: "dates", message: "The end date must be on or after the start date." });
  if (issues.length) return { issues };
  const { roles, kinds, ...filters } = draft.scope;
  return { request: { ...(where ? { where } : { query: draft.text, mode: draft.mode }), ...filters, ...(roles.length ? { roles } : {}), ...(kinds.length ? { kinds } : {}), fuzzy: draft.fuzzy, case_sensitive: draft.caseSensitive, separators: draft.separators }, issues };
}
export function textFindParameters(draft: TextSearchDraft, cursor = ""): string {
  const { request } = searchRequest(draft);
  if (!request) return "";
  const params = new URLSearchParams({ find_fuzzy: request.fuzzy ? "1" : "0" });
  if (request.where) params.set("find_where", JSON.stringify(request.where));
  else { params.set("find", String(request.query)); params.set("find_mode", String(request.mode)); }
  if (draft.caseSensitive) params.set("find_case", "1");
  if (draft.separators) params.set("find_separators", "1");
  for (const [key, value] of Object.entries(draft.scope)) { const encoded = Array.isArray(value) ? value.join(",") : value; if (encoded) params.set(`find_${key}`, encoded); }
  if (cursor) params.set("find_cursor", cursor);
  return params.toString();
}
export function conditionSummary(c: TextCondition): string {
  if (c.kind === "group") {
    const positive = c.children.filter(c => !c.exclude).map(conditionSummary).join(c.join === "all" ? "; and " : "; or ");
    const negative = c.children.filter(c => c.exclude).map(conditionSummary).join("; or ");
    return `${c.children.length > 1 ? "(" : ""}${positive}${negative ? `; excluding ${negative}` : ""}${c.children.length > 1 ? ")" : ""}`;
  }
  const quote = (value: string) => `“${value}”`;
  if (c.kind === "near" || c.kind === "pre") return `${quote(c.text)} ${c.kind === "pre" ? "followed by" : "near"} ${quote(c.other)}, up to ${c.distance} words apart`;
  return `${ruleLabels[c.kind].toLowerCase()} ${quote(c.text)}`;
}
export const textSearchSummary = (d: TextSearchDraft) => `${scopeChoice(d.scope)?.label ?? "Custom message scope"} · ${d.editor === "syntax" ? d.text : d.match === "rules" ? conditionSummary(d.root) : `${d.match === "literal" ? "exact text" : "pattern"} “${d.text}”`}`;
const quoted = (text: string) => `"${text.replaceAll("\\", "\\\\").replaceAll('"', '\\"')}"`;
export function querySyntax(q: TextQuery, separators = false): string {
  if (q.op === "literal" || q.op === "regex") return q.text ?? "";
  if (q.op === "term") {
    const text = q.text ?? "";
    if (!/[\s"()*]/u.test(text) && !/^(AND|OR|NOT|NEAR\/\d+|PRE\/\d+)$/u.test(text)) return text;
    if (separators && /[^\p{L}\p{N}\p{M}]/u.test(text)) throw new Error("This word rule contains punctuation that query syntax would interpret differently. Keep it in the visual editor to preserve its meaning.");
  }
  if (q.op === "term" || q.op === "phrase") return quoted(q.text ?? "");
  if (q.op === "prefix") return `${quoted(q.text ?? "")}*`;
  if (q.op === "near" || q.op === "pre") return `${querySyntax(q.children![0], separators)} ${q.op.toUpperCase()}/${q.distance} ${querySyntax(q.children![1], separators)}`;
  return `(${q.children!.map(child => querySyntax(child, separators)).join(` ${q.op.toUpperCase()} `)})`;
}
export function visualFromQuery(q: TextQuery, separators = false): Pick<TextSearchDraft, "root" | "match" | "text" | "editor"> {
  if (q.op === "literal" || q.op === "regex") return { editor: "visual", root: newGroup(), match: q.op, text: q.text ?? "" };
  const convert = (q: TextQuery): TextCondition => {
    if (q.op === "and" || q.op === "or") return { ...newGroup(), join: q.op === "and" ? "all" : "any", children: q.children!.map(convert) };
    if (q.op === "not") return { ...newGroup(), children: [convert(q.children![0]), { ...convert(q.children![1]), exclude: true }] };
    if (q.op === "near" || q.op === "pre") {
      if (q.children!.some(c => c.op === "prefix")) throw new Error("This query combines proximity with word beginnings. Keep it in query mode to preserve its meaning.");
      if (separators && q.children!.some(c => c.op === "term" && /[^\p{L}\p{N}\p{M}]/u.test(c.text ?? ""))) throw new Error("This proximity query uses word punctuation that visual phrase rules would interpret differently. Keep it in query mode to preserve its meaning.");
      return { ...newRule(q.children![0].text), kind: q.op, other: q.children![1].text ?? "", distance: String(q.distance ?? 0) };
    }
    return { ...newRule(q.text), kind: q.op === "prefix" ? "prefix" : q.op === "term" && !/\s/u.test(q.text ?? "") ? "words" : "phrase" };
  };
  const node = convert(q), root = node.kind === "group" ? node : { ...newGroup(), children: [node] };
  const result = compileRules(root);
  if (result.issues.length) throw new Error("This query is too complex for the visual editor. You can continue editing its syntax.");
  return { editor: "visual", root, match: "rules", text: "" };
}
export function readTextSearch(params: URLSearchParams): { draft: TextSearchDraft; active: boolean; error: string } {
  const draft = emptyTextSearch();
  const stored = params.get("text_builder");
  if (stored !== null) {
    try {
      if (stored.length > 24000) throw new Error();
      const value = JSON.parse(stored) as TextSearchDraft;
      if (!value || !["visual", "syntax"].includes(value.editor) || !["rules", "literal", "regex"].includes(value.match) || !["advanced", "terms", "phrase", "literal", "regex"].includes(value.mode) || typeof value.text !== "string" || !value.scope) throw new Error();
      const scope = emptyScope();
      for (const [key, field] of Object.entries(scope)) {
        const next = value.scope[key as keyof TextScope];
        if (Array.isArray(field) ? !Array.isArray(next) || next.some(v => typeof v !== "string") : typeof next !== "string") throw new Error();
        Object.assign(scope, { [key]: next });
      }
      let count = 0;
      const restore = (c: TextCondition, depth = 0): TextCondition => {
        if (!c || ++count > 32 || depth > 4 || typeof c.exclude !== "boolean") throw new Error();
        if (c.kind === "group") {
          if (!["all", "any"].includes(c.join) || !Array.isArray(c.children)) throw new Error();
          return { ...newGroup(), join: c.join, exclude: c.exclude, children: c.children.map(v => restore(v, depth + 1)) };
        }
        if (!Object.hasOwn(ruleLabels, c.kind) || typeof c.text !== "string" || typeof c.other !== "string" || typeof c.distance !== "string") throw new Error();
        return { ...newRule(), kind: c.kind, text: c.text, other: c.other, distance: c.distance, exclude: c.exclude };
      };
      const root = restore(value.root);
      if (root.kind !== "group" || root.exclude) throw new Error();
      const restored: TextSearchDraft = { editor: value.editor, match: value.match, text: value.text, mode: value.mode, scope, root, fuzzy: value.fuzzy === true, caseSensitive: value.caseSensitive === true, separators: value.separators === true };
      if (!searchRequest(restored).request) throw new Error();
      return { draft: restored, active: true, error: "" };
    } catch { return { draft, active: false, error: "This saved search could not be opened. Edit the rules below and search again. The table is currently unfiltered by text." }; }
  }
  const query = params.get("search") ?? "";
  // Legacy links omitted fuzzy when it was on; only fuzzy=0 opted out.
  // Empty/new drafts still start with variations off, as do new saved rules.
  draft.fuzzy = params.has("fuzzy") ? params.get("fuzzy") !== "0" : !!query;
  draft.caseSensitive = params.get("case") === "1";
  draft.separators = params.get("separators") === "1";
  for (const key of Object.keys(draft.scope) as (keyof TextScope)[]) {
    const value = params.get(`text_${key}`);
    if (value === null) continue;
    if (key === "roles" || key === "kinds") draft.scope[key] = value.split(",").filter(Boolean);
    else draft.scope[key] = value;
  }
  draft.mode = params.get("text_mode") ?? "advanced";
  if (["1", "phrase"].includes(params.get("text_plain") ?? "") || (!params.has("text_mode") && !/["()*]|\b(?:AND|OR|NOT|NEAR\/|PRE\/)/u.test(query))) draft.root.children = [{ ...newRule(query), kind: params.get("text_plain") === "phrase" ? "phrase" : "words" }];
  else { draft.editor = "syntax"; draft.text = query; }
  return { draft, active: !!query, error: "" };
}
