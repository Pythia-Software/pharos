import { formatUSD } from "./currency";
import { CursorPagination } from "./cursor-pagination";
import { ViewDisclosure } from "./view-disclosure";
import { useViewState } from "./view-state";
import React, { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { createPortal } from "react-dom";
import { preferences } from "./preferences";
import { Icon } from "./icons";
import { delegationRouteValues, delegationTabs, readDelegationRoute, type DelegationRoute } from "./delegation-route";
import "./delegation.css";
import {RecursiveSessionRibbon} from "./label-session-ribbon";
import {SessionIdentity,sessionBranchX} from "./session-identity";
import {eventKindCounts} from "./event-merge";
import {nativeTimelineLabels,type NativeTimelineMessage} from "./label-timeline-overlay";
import {withEventLink} from "./event-link-url";
import type {AgentEventLink} from "./event-link-layout";
import type {LabelMessage} from "./conversation-labels";
import type {Taxonomy} from "./conversation-labels";
import {splitTimelineCallers} from "./timeline-caller-model";
import {cutAxisLabels,fromVirtual,idleGaps,laneBusy,timeScale,toVirtual,type IdleGap,type TimeScale} from "./timeline-idle-model";
import {useTimelineColumns} from "./timeline-columns";
import {timelineSessionBranches} from "./timeline-label-model";
import {TimelineLegend,timelineLegendWindow} from "./timeline-legend";
import {updateTimelineVisibility,type TimelineVisibility} from "./timeline-visibility";
import {ribbonSourceURL,type RibbonObservation} from "./label-session-ribbon-model";

// reader-model.js loads before the bundle and also supplies turn ages.
declare function relativeAge(value: number | null | undefined): string;
// reader-model.js names the role of related work (peer_reader.go).
declare function peerRoleLabel(role: string): string;

type Metric = {
  value: number | null;
  quality: string;
  known: number;
  total: number;
};
type Spec = { id: string; label: string; unit: string; definition: string };
type Reference = {
  conversation_id: string;
  workspace_id: string;
  label: string;
  provider: string;
  message_id?: string;
};
export type Session = {
  id: string;
  conversation_id: string;
  workspace_id: string;
  parent_id?: string;
  parent_evidence?: string;
  message_id?: string;
  external_parent?: Reference;
  external_child?: Reference;
  navigation?: Reference;
  context_only?: boolean;
  // Certain related work shown beside the workspace's own sessions.
  peer?: { workspace_id: string; title: string; role: string };
  label: string;
  provider: string;
  model: string;
  effort?: string;
  coverage: string;
  usage_status: string;
  depth: number;
  outline: string;
  order: number;
  child_count: number;
  start: number | null;
  end: number | null;
  subtree_start: number | null;
  subtree_end: number | null;
  token_attribution?: {
    accounted_tokens: number;
    direct_tokens: number;
    unallocated_tokens: number;
    observed_minimum_tokens: number;
    evidence?: {scope: string; total_tokens: number; message_id: string; conversation_id: string; workspace_id: string}[];
  };
  self: Record<string, Metric>;
  all: Record<string, Metric>;
};
export type Analysis = {
  snapshot: string;
  rows: Session[];
  ancestors: Session[];
  ancestors_truncated: boolean;
  focus: Session[];
  focus_truncated: boolean;
  matched: Record<string, boolean>;
  total: number;
  session_count: number;
  offset: number;
  next_cursor: string;
  metrics: Spec[];
  maxima: Record<string, Record<string, number>>;
  totals: Record<string, Metric>;
  // With related work's sessions; absent when there is none.
  group_totals?: Record<string, Metric> | null;
  start: number | null;
  end: number | null;
  density: number[];
  warnings: string[];
};
type Span = { start: number; end: number };
type Mark = Span & {
  end_known: boolean;
  category: string;
  count: number;
  error: boolean;
  tool_id?: string;
  message_id?: string;
  preview?: string;
  preview_truncated?: boolean;
  status?: string;
  name: string;
};
type Lane = {
  session: string;
  marks: Mark[];
  model_inferred: Span[];
  episodes: Span[];
  truncated: boolean;
  messages?: NativeTimelineMessage[];
  messages_truncated?: boolean;
  labels_pending?: boolean;
};
export type Activity = Span & { snapshot: string; lanes: Lane[]; inference: string; taxonomy?: Taxonomy };
type Props = {
  workspace: string;
  conversation: string;
  contextHost: HTMLElement | null;
  statsHost?: HTMLElement | null;
  readerHost?: HTMLElement | null;
  compact?: boolean;
  splitReader?: boolean;
  openOverview?: () => void;
  openConversation: (id: string, message?: string, session?: string) => Promise<void>;
  // Reports whether the workspace's sessions form a hierarchy: more than one
  // session, or one linked to a parent or child elsewhere.
  onHierarchy?: (hierarchical: boolean) => void;
};
type Tint = "all" | "off" | "sort";
type Range = [number, number];
const categories: Record<string, string> = {
  read: "Read / search",
  write: "Edit / write",
  run: "Run / build / test",
  git: "Git",
  network: "Network",
  delegate: "Delegate / message",
  wait: "Wait call",
  poll: "Poll / delay",
  other: "Other tool",
};
const defaultColumns = [
  "tokens",
  "cost",
  "messages",
  "calls",
  "events",
  "errors",
  "files",
  "filesRead",
  "clock",
  "toolTime",
];
/** Upgrade saved column choices once, keeping custom order and hidden metrics. */
export function addSessionColumns(saved: string[]) {
  const next = [...saved];
  for (const [id, anchor] of [["messages", "calls"], ["events", "calls"], ["filesRead", "files"]]) {
    if (next.includes(id)) continue;
    const index = next.indexOf(anchor);
    next.splice(index < 0 ? next.length : index + (id === "messages" ? 0 : 1), 0, id);
  }
  return next;
}
/** Open the session's immediate event groups; deeper groups start closed. */
export function sessionTimelineState(node: Session, disclosures: Record<string, boolean>) {
  if (node.start == null || node.end == null || !Number.isFinite(node.start) || !Number.isFinite(node.end)) return null;
  const prefix = `delegation-labels:${node.id}:`;
  const next = Object.fromEntries(Object.entries(disclosures).filter(([key]) => !key.startsWith(prefix)));
  next[`${prefix}session`] = true;
  return { range: [node.start, Math.max(node.start + 1, node.end)] as Range, disclosures: next };
}
export const tintStrength = (value: number, maximum: number) =>
  maximum > 0 && value > 0
    ? Math.pow(
        Math.log1p(99 * Math.min(1, value / maximum)) / Math.log(100),
        1.45,
      )
    : 0;
export function duration(ms: number) {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return minutes < 60
    ? `${minutes}m ${seconds % 60}s`
    : `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}
export function formatted(value: number | null | undefined, unit = "count") {
  if (value == null) return "—";
  if (unit === "ms") return duration(value);
  if (unit === "date")
    return localMetricDateTime.format(new Date(value));
  if (unit === "usd")
    return formatUSD(value);
  if (Math.abs(value) >= 1000)
    return value.toLocaleString("en-US", {
      notation: "compact",
      maximumSignificantDigits: 3,
    });
  return value.toLocaleString(undefined, { maximumFractionDigits: 0 });
}
const localMetricDateTime = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "numeric",
  minute: "2-digit",
  timeZoneName: "short",
});
const localDateTime = new Intl.DateTimeFormat(undefined, {
  year: "numeric",
  month: "short",
  day: "numeric",
  hour: "numeric",
  minute: "2-digit",
  second: "2-digit",
  timeZoneName: "short",
});
const localDate = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
});
const resumeTime = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" });
/** Idle length beside a cut: whole minutes, or hours and minutes. */
const idleDuration = (ms: number) => { const minutes = Math.round(ms / 60000); return minutes < 60 ? `${minutes}m` : `${Math.floor(minutes / 60)}h${minutes % 60 ? ` ${minutes % 60}m` : ""}`; };
const localCutoffTime = new Intl.DateTimeFormat(undefined, {
  hour: "numeric",
  minute: "2-digit",
  second: "2-digit",
  timeZoneName: "short",
});
export function localTime(value: number) {
  return localDateTime.format(new Date(value));
}
/** Date headings mark local-day boundaries, independently of UTC or DST. */
export function timelineTickLabels(range: Range, count = 6, clock?: (fraction: number) => number) {
  const width=range[1]-range[0];
  const time=new Intl.DateTimeFormat(undefined,{
    hour:"numeric",minute:"2-digit",
    ...(width<=60000?{second:"2-digit" as const}:{}),
    ...(new Date(range[0]).getTimezoneOffset()!==new Date(range[1]).getTimezoneOffset()?{timeZoneName:"short" as const}:{}),
  });
  let previous="";
  return Array.from({length:count},(_,i)=>{
    const fraction=i/Math.max(1,count-1),at=clock?clock(fraction):range[0]+width*fraction,value=new Date(at),day=`${value.getFullYear()}-${value.getMonth()}-${value.getDate()}`;
    const date=day!==previous?localDate.format(value):null;previous=day;
    return {at,date,time:time.format(value)};
  });
}
function SessionAge({ start, prefix = "" }: { start: number | null; prefix?: string }) {
  const label = relativeAge(start);
  if (!label || start == null) return null;
  return <>{prefix}<time className="delegation-session-age" dateTime={new Date(start).toISOString()} data-relative-time={start} title={`First evidence: ${localTime(start)}`}>{label}</time></>;
}
export function clampRange(range: Range, full: Range): Range {
  const width = Math.max(1, Math.min(full[1] - full[0], range[1] - range[0]));
  const start = Math.max(full[0], Math.min(full[1] - width, range[0]));
  return [start, start + width];
}
export function timelinePointerTime(clientX:number,rect:{left:number;width:number},full:Range){
  return full[0]+(clientX-rect.left)/Math.max(1,rect.width)*(full[1]-full[0]);
}
/** The time under the pointer on a (possibly compressed) clock, clamped to its range. */
export function scaledPointerTime(clientX:number,rect:{left:number;width:number},clock:TimeScale){
  return clock.time(Math.max(0,Math.min(1,(clientX-rect.left)/Math.max(1,rect.width))));
}
/** Panning preserves duration; selection and resize clamp endpoints independently. */
export function timelineDragRange(mode:string,initial:Range,full:Range,anchor:number,current:number):Range{
  const delta=current-anchor,limit=(at:number)=>Math.max(full[0],Math.min(full[1],at));
  if(mode==='pan')return clampRange([initial[0]+delta,initial[1]+delta],full);
  if(mode==='start')return [Math.min(initial[1]-1,limit(current<=full[0]?full[0]:initial[0]+delta)),initial[1]];
  if(mode==='end')return [initial[0],Math.max(initial[0]+1,limit(current>=full[1]?full[1]:initial[1]+delta))];
  const start=Math.min(limit(anchor),limit(current)),end=Math.max(limit(anchor),limit(current));
  return [Math.min(start,full[1]-1),Math.min(full[1],Math.max(start+1,end))];
}
async function readJSON<T>(url: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(url, { signal });
  const body = await response.json();
  if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`);
  return body;
}
export function metricText(metric: Metric | undefined, unit = "tokens") {
  const value = formatted(metric?.value, unit);
  if (metric?.value != null && metric.quality === "reported-minimum") {
    // Do not round a lower bound upward (116,837 is not at least 117,000).
    const scale = metric.value >= 1e9 ? 1e9 : metric.value >= 1e6 ? 1e6 : metric.value >= 1e3 ? 1e3 : 1;
    const minimum = Math.floor(metric.value / scale * 10) / 10;
    return `≥${minimum.toLocaleString("en-US", {maximumFractionDigits: 1})}${scale === 1e9 ? "B" : scale === 1e6 ? "M" : scale === 1e3 ? "K" : ""}`;
  }
  return metric?.value != null && metric.quality === "partial" ? `${value}+` : value;
}
export function tableMetricText(metric: Metric | undefined, unit = "tokens") {
  return metricText(metric, unit) + (metric?.value != null && metric.quality === "reported-minimum" ? "*" : "");
}
export function tokenAttributionText(session: Session) {
  const a = session.token_attribution;
  if (!a) return "";
  const parts: string[] = [];
  if (a.observed_minimum_tokens > a.direct_tokens) parts.push(`${metricText(session.self.tokens)} tokens · Reported minimum; may cover only the last request. Full session usage unknown. Input/cache/output breakdown not recorded for this observation; API cost unavailable.`);
  if (a.unallocated_tokens > 0) parts.push(`${formatted(a.unallocated_tokens)} tokens unallocated within this branch. Included in Self + children; child observations may overlap this amount. API cost for this remainder is included in the branch total.`);
  return parts.join(" ");
}
export function cellTitle(metric: Metric, spec: Spec) {
  const raw = metric.value?.toLocaleString(undefined, { maximumFractionDigits: 6 });
  const shown = formatted(metric.value, spec.unit);
  const exact =
    metric.value == null || spec.unit === "usd" || raw === shown || `${raw}${spec.unit}` === shown.replace(/\s/g, "")
      ? ""
      : spec.unit === "date"
        ? `${localTime(metric.value)}\n`
        : `${raw} ${spec.unit}\n`;
  return `${exact}${spec.definition}${metric.quality === "reported-minimum" ? "\n* Reported minimum; may cover only the last request. Full session usage and token breakdown were not recorded." : ""}\n${metric.quality} · ${metric.known}/${metric.total} retained coverage. ${metric.value === null ? "No sufficient retained evidence." : "Source coverage may be incomplete."}`;
}

function ColumnPicker({
  metrics,
  columns,
  order,
  onColumns,
  onOrder,
}: {
  metrics: Spec[];
  columns: string[];
  order: string[];
  onColumns: (columns: string[]) => void;
  onOrder: (order: string[]) => void;
}) {
  const dragged = useRef<string | null>(null);
  const [dragging, setDragging] = useState("");
  const [target, setTarget] = useState<{ id: string; after: boolean } | null>(
    null,
  );
  const [announcement, setAnnouncement] = useState("");
  function commit(id: string, next: string[]) {
    onOrder(next);
    setAnnouncement(
      `${metrics.find((m) => m.id === id)?.label} moved to position ${next.indexOf(id) + 1} of ${next.length}.`,
    );
  }
  function finish() {
    dragged.current = null;
    setDragging("");
    setTarget(null);
  }
  function moveKey(event: React.KeyboardEvent, id: string) {
    const index = order.indexOf(id);
    const to =
      event.key === "ArrowUp"
        ? index - 1
        : event.key === "ArrowDown"
          ? index + 1
          : event.key === "Home"
            ? 0
            : event.key === "End"
              ? order.length - 1
              : null;
    if (to === null) return;
    event.preventDefault();
    if (to < 0 || to >= order.length || to === index) return;
    const next = order.filter((key) => key !== id);
    next.splice(to, 0, id);
    commit(id, next);
  }
  return (
    <ViewDisclosure viewKey="delegation:columns" className="delegation-columns" summary={<>Columns · {columns.length}</>} summaryTarget="control:delegation_columns">
      <div>
        <p>
          Choose columns. Drag handles to change their order.
        </p>
        <div
          className="delegation-column-list"
          role="list"
          data-view-field="delegation_column_order" aria-label="Column order"
        >
          {metrics.map((m) => (
            <div
              key={m.id}
              role="listitem"
              data-column={m.id}
              className={`delegation-column-option ${dragging === m.id ? "dragging" : ""} ${target?.id === m.id ? (target.after ? "drop-after" : "drop-before") : ""}`}
              onDragOver={(event) => {
                if (!dragged.current || dragged.current === m.id) return;
                event.preventDefault();
                event.dataTransfer.dropEffect = "move";
                const rect = event.currentTarget.getBoundingClientRect();
                setTarget({
                  id: m.id,
                  after: event.clientY > rect.top + rect.height / 2,
                });
              }}
              onDrop={(event) => {
                event.preventDefault();
                const id = dragged.current;
                if (id && id !== m.id) {
                  const rect = event.currentTarget.getBoundingClientRect();
                  const after = event.clientY > rect.top + rect.height / 2;
                  const next = order.filter((key) => key !== id);
                  next.splice(next.indexOf(m.id) + (after ? 1 : 0), 0, id);
                  commit(id, next);
                }
                finish();
              }}
            >
              <button
                type="button"
                className="delegation-column-grip"
                draggable
                aria-label={`Move ${m.label}`}
                title="Drag to reorder; arrow keys move up or down. Home/End move to the first/last position."
                onKeyDown={(event) => moveKey(event, m.id)}
                onDragStart={(event) => {
                  dragged.current = m.id;
                  setDragging(m.id);
                  event.dataTransfer.effectAllowed = "move";
                  event.dataTransfer.setData("text/plain", m.id);
                  event.dataTransfer.setDragImage(
                    event.currentTarget.parentElement!,
                    12,
                    12,
                  );
                }}
                onDragEnd={finish}
              >
                <Icon name="grip" />
              </button>
              <label title={m.definition}>
                <input
                  type="checkbox"
                  checked={columns.includes(m.id)}
                  onChange={(event) =>
                    onColumns(
                      event.target.checked
                        ? [...columns, m.id]
                        : columns.filter((id) => id !== m.id),
                    )
                  }
                />
                {m.label}
              </label>
            </div>
          ))}
        </div>
        <span className="delegation-column-announcement" role="status">
          {announcement}
        </span>
      </div>
    </ViewDisclosure>
  );
}

export function DelegationExplorer({
  workspace,
  conversation,
  contextHost,
  statsHost,
  readerHost,
  compact = false,
  splitReader = false,
  openOverview,
  openConversation,
  onHierarchy,
}: Props) {
  const onWorkPage=()=>location.pathname===`/work/${encodeURIComponent(workspace)}`||location.pathname.startsWith(`/work/${encodeURIComponent(workspace)}/`);
  const active=onWorkPage();
  const endpoint = `/api/work/${encodeURIComponent(workspace)}`;
  const initial = readDelegationRoute(new URLSearchParams(location.search));
  const [tab, setTab] = useState<string>(initial.tab);
  const [readerView, setReaderView] = useViewState("reader_view", "conversation", active);
  const [mode, setMode] = useState<DelegationRoute["mode"]>(initial.mode);
  const [sort, setSort] = useState(initial.sort),
    [direction, setDirection] = useState(initial.direction);
  const [scope, setScope] = useViewState("delegation_scope", "all", active);
  const [, setDisclosures] = useViewState<Record<string, boolean>>("disclosures", {}, active);
  const [tint, setTint] = useViewState<Tint>("delegation_tint",
    preferences().get("pharos-delegation-tint", "all"),active,
  );
  const [columns, setColumns] = useViewState<string[]>("delegation_columns", () => {
    const prefs = preferences();
    const saved = prefs.get("pharos-delegation-columns", defaultColumns);
    if (prefs.get("pharos-delegation-session-columns-added", false)) return saved;
    const next = addSessionColumns(prefs.get("pharos-delegation-files-column-added", false) ? saved : [...new Set([...saved, "files"])]);
    prefs.set("pharos-delegation-files-column-added", true);
    prefs.set("pharos-delegation-session-columns-added", true);
    prefs.set("pharos-delegation-columns", next);
    prefs.set("pharos-delegation-column-order", addSessionColumns(prefs.get("pharos-delegation-column-order", next)));
    return next;
  },active);
  const [columnOrder, setColumnOrder] = useViewState<string[]>("delegation_column_order",
    preferences().get(
      "pharos-delegation-column-order",
      preferences().get("pharos-delegation-columns", defaultColumns),
    ),active,
  );
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set(initial.collapsed));
  const [pageIndex, setPageIndex] = useViewState("delegation_page", "0",active);
  const livePages = useRef(new CursorPagination());
  const [cursor, setCursor] = useState(""),
    [pages, setPages] = useState<string[]>([]);
  const [data, setData] = useState<Analysis | null>(null),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true);
  const [revision, setRevision] = useState(0),
    [selected, setSelected] = useState(initial.selected);
  const [range, setRange] = useState<Range | null>(null);
  const single = !!data && data.session_count === 1 && !data.rows.some(n => n.context_only || n.parent_id || n.external_parent || n.external_child);
  const timeline = single ? readerView === "timeline" : tab === "timeline";
  useEffect(() => {
    readerHost?.classList.toggle("conversation-timeline-active", single && timeline);
    return () => readerHost?.classList.remove("conversation-timeline-active");
  }, [readerHost, single, timeline]);
  useEffect(() => {
    const restore = () => {
      const url = new URL(location.href);
      const route = readDelegationRoute(url.searchParams);
      if (onWorkPage() && url.searchParams.get("explorer") === "parallel") {
        url.searchParams.set("explorer", route.tab);
        history.replaceState(history.state, "", url);
        window.dispatchEvent(new Event("pharos:uri-changed"));
      }
      setSelected(route.selected);
      setTab(route.tab);
      setMode(route.mode);
      setSort(route.sort);
      setDirection(route.direction);
      setCollapsed(new Set(route.collapsed));
      livePages.current.reset();
      setRange(route.range);
    };
    restore();
    window.addEventListener("pharos:workspace-history", restore);
    return () => window.removeEventListener("pharos:workspace-history", restore);
  }, []);
  const collapsedKey = [...collapsed].sort().join(",");
  useEffect(() => {
    if(!onWorkPage())return;
    const controller = new AbortController();
    setLoading(true);
    setError("");
    const params = new URLSearchParams({
      mode: tab === "table" && !compact ? mode : "hierarchy",
      sort,
      scope,
      direction,
      conversation,
      collapsed: collapsedKey,
      cursor,
      limit: "40",
    });
    const loadPage = async () => {
      params.set("cursor", "");
      const execution = JSON.stringify([endpoint, params.toString(), revision]);
      const wanted = Math.max(0, Math.min(250, Number(pageIndex) || 0));
      const result = await livePages.current.load(execution, wanted, cursor => {
        params.set("cursor", cursor);
        return readJSON<Analysis>(`${endpoint}/delegation?${params}`, controller.signal);
      }, controller.signal);
      setPages(result.previous); setCursor(result.cursor);
      return result.body;
    };
    loadPage()
      .then(
        (body) => {
          setData(body);
          setRange((old) =>
            old && data?.snapshot === body.snapshot
              ? old
              : body.start != null && body.end != null
                ? (() => {
                    const full: Range = [
                      body.start!,
                      Math.max(body.start! + 1, body.end!),
                    ];
                    return !data && initial.range && initial.range.every(Number.isFinite)
                      ? clampRange(initial.range, full)
                      : full;
                  })()
                : null,
          );
        },
        (e) => {
          if (e.name !== "AbortError") setError(e.message);
        },
      )
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [
    active,
    compact,
    endpoint,
    conversation,
    tab,
    mode,
    sort,
    scope,
    direction,
    collapsedKey,
    pageIndex,
    revision,
  ]);
  useEffect(() => {
    const refresh = () => {
      livePages.current.reset();
      setRevision((v) => v + 1);
      setCursor("");
      setPages([]);
    };
    window.addEventListener("pharos:data-refresh", refresh);
    return () => window.removeEventListener("pharos:data-refresh", refresh);
  }, []);
  useEffect(() => {
    if (!data||!onWorkPage()) return;
    const url = new URL(location.href);
    const values = delegationRouteValues({ mode, sort, scope, direction, collapsed: [...collapsed], selected, range });
    for (const [key, value] of Object.entries(values)) {
      if (value) url.searchParams.set(key, value);
      else url.searchParams.delete(key);
    }
    if (url.href !== location.href) {
      history.replaceState(null, "", url);
      window.dispatchEvent(new Event("pharos:uri-changed"));
    }
  }, [data, mode, sort, scope, direction, collapsed, selected, range]);
  useEffect(() => {
    if (data && onHierarchy)
      onHierarchy(data.session_count > 1 || data.rows.some((n) => n.context_only || n.parent_id || n.external_parent || n.external_child));
  }, [data]);
  function resetPage() {
    livePages.current.reset();
    setCursor("");
    setPages([]);setPageIndex("0");
  }
  function showHierarchy() {
    setMode("hierarchy");
    setDirection("desc");
    resetPage();
  }
  function sortColumn(key: string, family: string) {
    const active = mode === "rank" && sort === key && scope === family;
    if (active && direction === "asc") {
      showHierarchy();
      return;
    }
    setMode("rank");
    setSort(key);
    setScope(family);
    setDirection(active ? "asc" : "desc");
    resetPage();
  }
  function display(next: string) {
    setTab(next);
    resetPage();
    const url = new URL(location.href);
    url.searchParams.set("explorer", next);
    url.searchParams.delete("sessions");
    history.replaceState(null, "", url);
    window.dispatchEvent(new Event("pharos:uri-changed"));
  }
  function openTimeline(n: Session) {
    const state = sessionTimelineState(n, {});
    if (!state) return;
    setDisclosures(previous => sessionTimelineState(n, previous)!.disclosures);
    setRange(state.range);
    setSelected(n.id);
    // Keep the session on its hierarchy page, including when opened from rank.
    setPageIndex(String(Math.floor(n.order / 40)));
    setCollapsed(new Set());
    setTab("timeline");
    const url = new URL(location.href);
    url.searchParams.set("explorer", "timeline");
    url.searchParams.set("delegation_session", n.id);
    url.searchParams.set("delegation_start", String(state.range[0]));
    url.searchParams.set("delegation_end", String(state.range[1]));
    url.searchParams.delete("delegation_collapsed");
    history.replaceState(history.state, "", url);
    window.dispatchEvent(new Event("pharos:uri-changed"));
    if (splitReader) openOverview?.();
  }
  function toggle(n: Session) {
    resetPage();
    setCollapsed((old) => {
      const next = new Set(old);
      next.has(n.id) ? next.delete(n.id) : next.add(n.id);
      return next;
    });
  }
  const rows = useMemo(() => {
    if (!data) return [];
    return compact || mode === "hierarchy" || tab !== "table"
      ? [...data.rows, ...data.ancestors].sort((a, b) => a.order - b.order)
      : data.rows;
  }, [data, mode, tab, compact]);
  const byID = useMemo(
    () => new Map([...rows, ...(data?.focus || [])].map((n) => [n.id, n])),
    [rows, data],
  );
  const orderedMetrics = useMemo(() => {
    const byKey = new Map(data?.metrics.map((m) => [m.id, m]));
    return [
      ...new Set([...columnOrder, ...(data?.metrics.map((m) => m.id) || [])]),
    ]
      .map((id) => byKey.get(id))
      .filter((m): m is Spec => !!m);
  }, [data, columnOrder]);
  const specs = orderedMetrics.filter((m) => columns.includes(m.id));
  const reportedMinimumShown = rows.some(n => (compact || scope === "self" ? n.self : n.all).tokens?.quality === "reported-minimum");
  const sessionBranches = useMemo(() => timelineSessionBranches(rows), [rows]);
  const current =
    data?.focus.filter((n) => n.conversation_id === conversation) || [];
  const shownSession = current.find((n) => n.id === selected) || current[0];
  const currentSessionID = splitReader || single ? shownSession?.id : undefined;
  const conversationChildren =
    data?.focus.filter((n) => current.some((c) => c.id === n.parent_id)) || [];
  const childSessions = conversationChildren.filter(n => n.parent_id === shownSession?.id);
  function externalURL(ref: Reference, message?: string) {
    const params = new URLSearchParams({ conversation: ref.conversation_id });
    if (message) params.set("message", message);
    return `/work/${encodeURIComponent(ref.workspace_id)}?${params}`;
  }
  function openExternal(ref: Reference) {
    const target = externalURL(ref, ref.message_id);
    if (window.pharosNavigate) window.pharosNavigate(target);
    else location.assign(target);
  }
  const openNode = (n: Session, message?: string, sourceConversation?: string) => {
    if (n.navigation) {
      openExternal(n.navigation);
      return;
    }
    if (n.external_child && !message) {
      openExternal(n.external_child);
      return;
    }
    if (!message && n.id === currentSessionID) return;
    setSelected(n.id);
    void openConversation(
      sourceConversation || n.conversation_id,
      message ||
        (n.parent_id &&
        byID.get(n.parent_id)?.conversation_id === n.conversation_id
          ? n.message_id
          : undefined),
      n.id,
    );
  };
  // Delegation launch entries acquire a link when the reader materializes a turn.
  useEffect(() => {
    if (!contextHost || !data) return;
    const panel = contextHost.closest(".conversation-panel");
    if (!panel) return;
    const added: HTMLElement[] = [];
    const pending = new Map<string, Session[]>();
    for (const n of conversationChildren) {
      const hasTranscript = n.external_child || n.conversation_id !== conversation ||
        n.self.messages?.value || n.self.requests?.value || n.self.calls?.value;
      if (!n.message_id || !hasTranscript) continue;
      const sessions = pending.get(n.message_id) || [];
      sessions.push(n);
      pending.set(n.message_id, sessions);
    }
    function decorate(event?: Event) {
      if (!pending.size) return;
      const roots = (event as CustomEvent<{ roots?: Element[] }> | undefined)?.detail?.roots || [panel!];
      for (const root of roots) {
        const targets = [
          ...(root.matches("[data-message-id]") ? [root as HTMLElement] : []),
          ...root.querySelectorAll<HTMLElement>("[data-message-id]"),
        ];
        for (const target of targets) {
          const id = target.dataset.messageId!;
          const sessions = pending.get(id);
          if (!sessions) continue;
          const summary = target.querySelector(".subagent-summary-stats, .action-summary");
          if (!summary) continue;
          for (const n of sessions) {
            if (summary.querySelector(`[data-delegation-session="${CSS.escape(n.id)}"]`)) continue;
            const link = document.createElement("button");
            link.type = "button";
            link.className = "delegation-transcript-link";
            link.dataset.delegationSession = n.id;
            link.textContent = n.external_child
              ? `${n.external_child.provider} · Open child ↗`
              : `${n.provider} · ${formatted(n.self.clock?.value, "ms")} · Open child`;
            link.onclick = (event) => {
              event.preventDefault();
              event.stopPropagation();
              openNode(n);
            };
            summary.append(link);
            added.push(link);
          }
          pending.delete(id);
        }
      }
    }
    decorate();
    panel.addEventListener("transcript-materialized", decorate);
    return () => {
      panel.removeEventListener("transcript-materialized", decorate);
      added.forEach((el) => el.remove());
    };
  }, [contextHost, data, conversation]);
  const headingHost = contextHost?.closest(".conversation-head")?.querySelector(".conversation-title-line");
  const heading = shownSession && (
    <div
      className="delegation-session-heading"
      data-session={shownSession.id}
    >
      <h2>{shownSession.label}</h2>
      <small>
        {shownSession.provider} ·{" "}
        {shownSession.model || "model unknown"} ·{" "}
        {shownSession.effort || "unknown"}
      </small>
    </div>
  );
  const context =
    contextHost &&
    createPortal(
      <>
        <div className="delegation-context">
          {current
            .filter((n) => n.external_parent)
            .map((n) => (
              <button
                key={n.id} data-session-id={n.id}
                onClick={() => openExternal(n.external_parent!)}
                title="Parent conversation in another workspace"
              >
                ← Parent · {n.external_parent!.provider} ·{" "}
                {n.external_parent!.label}
              </button>
            ))}
          {shownSession && (
            <span title="Usage attributed to the selected agent session. Aggregate branch remainders are shown separately.">
              {metricText(shownSession.self.tokens)} tokens · This agent session
            </span>
          )}
          {childSessions.length > 0 && (
            <ViewDisclosure viewKey="delegation:scope" summary={<>
                {childSessions.length}
                {data?.focus_truncated ? "+" : ""} child sessions
              </>}>
              <div className="delegation-child-menu">
                {childSessions.map((n) => (
                  <button key={n.id} data-session-id={n.id} onClick={() => openNode(n)}>
                    <strong>{n.label}</strong>
                    <small>
                      {n.external_child ? "Another workspace · " : ""}
                      {n.provider} · {metricText(n.self.tokens)} tokens ·{" "}
                      {formatted(n.self.clock.value, "ms")} evidence span
                    </small>
                  </button>
                ))}
                {data?.focus_truncated && (
                  <small>
                    More session links are available in the workspace hierarchy.
                  </small>
                )}
              </div>
            </ViewDisclosure>
          )}
        </div>
        {shownSession && tokenAttributionText(shownSession) && <p className="delegation-token-evidence">
          {tokenAttributionText(shownSession)}{" "}
          {shownSession.token_attribution?.evidence?.slice(0, 1).map(e => <a key={e.message_id} href={externalURL({workspace_id: e.workspace_id, conversation_id: e.conversation_id, label: "Token evidence", provider: shownSession.provider}, e.message_id)}>View token evidence</a>)}
        </p>}
      </>,
      contextHost,
    );
  const totals = data && (
    <div className="delegation-totals">
      {["tokens", "cost", "calls", "clock", "total"].map((key) => {
        const spec = data.metrics.find((m) => m.id === key)!;
        return (
          <span key={key} title={cellTitle(data.totals[key], spec)}>
            <small>{spec.label}</small>
            <strong>
              {formatted(data.totals[key].value, spec.unit)}
              {data.totals[key].quality === "partial" ? "+" : ""}
            </strong>
          </span>
        );
      })}
      {data.group_totals && (() => {
        const spec = data.metrics.find((m) => m.id === "cost")!;
        const group = data.group_totals.cost;
        return group && <span className="delegation-group-total" title={`This workspace and the related work shown among its agent sessions.\n${cellTitle(group, spec)}`}>
          <small>With related work</small>
          <strong>{formatted(group.value, spec.unit)}{group.quality === "partial" ? "+" : ""}</strong>
        </span>;
      })()}
    </div>
  );
  return (
    <>
      {context}
      {headingHost && createPortal(heading, headingHost)}
      {statsHost && createPortal(totals, statsHost)}
      <section className={`delegation-panel${single ? " delegation-single" : ""}`} aria-label={single ? "Conversation views" : "Agent sessions"}>
        <div className="delegation-controls delegation-toolbar">
          {!single && <span className="delegation-heading">Agent sessions</span>}
          <div
            className="delegation-tabs"
            role="tablist"
            aria-label={single ? "Conversation view" : "Delegation view"}
            style={{"--tab-count":2,"--tab-index":single ? (timeline ? 1 : 0) : delegationTabs.indexOf(tab as DelegationRoute["tab"])} as React.CSSProperties}
            data-view={single ? readerView : tab}
          >
            {(single ? ["conversation", "timeline"] : delegationTabs).map((t, index, tabs) => (
              <button
                type="button"
                role="tab"
                aria-selected={(single ? readerView : tab) === t}
                tabIndex={(single ? readerView : tab) === t ? 0 : -1}
                key={t}
                onClick={() => single ? setReaderView(t) : display(t)}
                onKeyDown={(event) => {
                  if (
                    !["ArrowLeft", "ArrowRight", "Home", "End"].includes(
                      event.key,
                    )
                  )
                    return;
                  event.preventDefault();
                  const nextIndex = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : (index + (event.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length;
                  if (single) setReaderView(tabs[nextIndex]); else display(tabs[nextIndex]);
                  event.currentTarget.parentElement!.querySelectorAll<HTMLButtonElement>("button")[nextIndex].focus();
                }}
              >
                {t === "table" ? "Hierarchy & usage" : t === "conversation" ? "Conversation" : "Timeline"}
              </button>
            ))}
          </div>
          {!single && data && tab === "table" && !compact && (
            <>
              <span className="delegation-controls-spacer" />
              <select data-view-field="delegation_scope" aria-label="Session metric scope" value={scope} onChange={event => {
                setScope(event.target.value);
                resetPage();
              }}>
                <option value="self">Self</option>
                <option value="all">Self + children</option>
              </select>
              <select
                data-view-field="delegation_tint" aria-label="Cell tint"
                value={tint}
                onChange={(e) => {
                  const value = e.target.value as Tint;
                  setTint(value);
                  preferences().set("pharos-delegation-tint", value);
                }}
              >
                <option value="all">Tint · All columns</option>
                <option value="off">Tint · Off</option>
                <option value="sort">Tint · Sort column only</option>
              </select>
              <ColumnPicker
                metrics={orderedMetrics}
                columns={columns}
                order={orderedMetrics.map((m) => m.id)}
                onColumns={(next) => {
                  setColumns(next);
                  preferences().set("pharos-delegation-columns", next);
                }}
                onOrder={(next) => {
                  setColumnOrder(next);
                  preferences().set("pharos-delegation-column-order", next);
                }}
              />
            </>
          )}
        </div>
        <div className="delegation-body" hidden={single && !timeline}>
          {error && (
            <div className="delegation-error" role="alert">
              {error}{" "}
              <button
                onClick={() => {
                  resetPage();
                  setRevision((v) => v + 1);
                }}
              >
                Refresh analysis
              </button>
            </div>
          )}
          {data && (!single || timeline) && (
            <>
              {!single && tab === "table" && compact ? (
                <InboxSessions
                  rows={rows}
                  data={data}
                  shown={currentSessionID || ""}
                  collapsed={collapsed}
                  toggle={toggle}
                  openNode={openNode}
                />
              ) : !single && tab === "table" ? (
                <>
                  <div className="delegation-table-size">
                    <div
                      className="delegation-table-scroll"
                      aria-busy={loading}
                    >
                      <table className="delegation-table">
                        <thead>
                          <tr>
                            <th className="delegation-label-cell">
                              <button onClick={showHierarchy} title="Show the agent session hierarchy">Session</button>
                            </th>
                            {specs.map(m => (
                              <th key={m.id} title={m.definition} aria-sort={mode === "rank" && sort === m.id ? direction === "desc" ? "descending" : "ascending" : "none"}>
                                <button onClick={() => sortColumn(m.id, scope)} title="Click to sort descending, then ascending, then return to hierarchy">
                                  {m.label}{mode === "rank" && sort === m.id ? direction === "desc" ? " ↓" : " ↑" : ""}
                                </button>
                              </th>
                            ))}
                            {!splitReader && <th>Timeline</th>}
                          </tr>
                        </thead>
                        <tbody>
                          {rows.map((n) => {
                            const ancestor =
                              n.context_only ||
                              !data.rows.some((row) => row.id === n.id) ||
                              !data.matched[n.id];
                            return (
                              <tr
                                key={n.id} data-session-id={n.id}
                                data-session={n.id}
                                data-depth={n.depth}
                                className={`${ancestor ? "delegation-ancestor" : ""} ${n.id === currentSessionID ? "delegation-current" : ""}`}
                                onClick={(event) => {
                                  if (
                                    !(event.target as HTMLElement).closest(
                                      "button, a",
                                    )
                                  )
                                    openNode(n);
                                }}
                              >
                                <td className="delegation-label-cell">
                                  <div className="delegation-node">
                                    <SessionIdentity
                                      name={`${n.label}${n.external_child || n.navigation ? " ↗" : ""}`}
                                      metadata={<>{peerPrefix(n)}{n.provider} · {n.model || "model unknown"} · {n.effort || "unknown"}{n.context_only ? " · Parent context" : ""}<SessionAge start={n.start} prefix=" · " /></>}
                                      title={`${n.id} · ${n.coverage} coverage · ${n.parent_evidence || "root"}`}
                                      depth={mode === "hierarchy" ? n.depth : 0}
                                      branch={mode === "hierarchy" ? sessionBranches.get(n.id) : undefined}
                                      folded={mode === "hierarchy" && !!n.child_count}
                                      current={n.id === currentSessionID}
                                      openSession={() => openNode(n)}
                                    />
                                    {mode === "hierarchy" && !!n.child_count && <button
                                      className="delegation-chevron session-fold"
                                      style={{left: sessionBranchX(n.depth) - 10}}
                                      aria-label={`${collapsed.has(n.id) ? "Expand" : "Collapse"} ${n.label}`} title={collapsed.has(n.id) ? "Expand sessions" : "Collapse sessions"}
                                      aria-expanded={!collapsed.has(n.id)}
                                      onClick={() => toggle(n)}
                                    ><Icon name={collapsed.has(n.id) ? "chevron-right" : "chevron-down"} /></button>}
                                  </div>
                                </td>
                                {specs.map((m) => {
                                    const family = scope;
                                    const metric = (
                                      family === "self" ? n.self : n.all
                                    )[m.id];
                                    const on =
                                      tint === "all" ||
                                      (tint === "sort" &&
                                        mode === "rank" &&
                                        sort === m.id &&
                                        scope === family);
                                    const strength = on
                                      ? tintStrength(
                                          metric.value || 0,
                                          data.maxima[family][m.id],
                                        )
                                      : 0;
                                    return (
                                      <td
                                        key={`${family}:${m.id}`}
                                        style={{
                                          background: strength
                                            ? `color-mix(in srgb, var(--delegation-tint) ${strength * 34}%, var(--panel))`
                                            : undefined,
                                        }}
                                        title={`${cellTitle(metric, m)}${family === "self" && tokenAttributionText(n) ? "\n" + tokenAttributionText(n) : ""}`}
                                      >
                                        {tableMetricText(metric, m.unit)}
                                      </td>
                                    );
                                  })}
                                {!splitReader && <td>
                                  <button type="button" className="delegation-timeline-button" aria-label={`View timeline for ${n.label}`} disabled={!sessionTimelineState(n, {})} onClick={() => openTimeline(n)} title="Show this session’s time span with its event rows expanded">View</button>
                                </td>}
                              </tr>
                            );
                          })}
                        </tbody>
                      </table>
                      {!rows.length && <p>No agent sessions match these filters.</p>}
                    </div>
                  </div>
                </>
              ) : range && data.start != null && data.end != null ? (
                <ParallelActivity
                  timeline
                  single={single}
                  compact={compact}
                  previewOnly={splitReader}
                  openOverview={openOverview}
                  endpoint={endpoint}
                  data={data}
                  rows={rows}
                  range={range}
                  setRange={setRange}
                  full={[data.start, Math.max(data.start + 1, data.end)]}
                  collapsed={collapsed}
                  toggle={toggle}
                  selected={currentSessionID || ""}
                  openNode={(node, message, sourceConversation) => { if (single) setReaderView("conversation"); openNode(node, message, sourceConversation); }}
                />
              ) : (
                <p>No retained timestamps are available for a timeline.</p>
              )}
              {!single && tab === "table" && reportedMinimumShown && (compact || specs.some(m => m.id === "tokens")) && <p className="delegation-token-evidence">
                * Reported token minimum; may cover only the last request. Full session usage is unknown.
              </p>}
              {(data.offset > 0 || !!data.next_cursor) && (
                <div className="delegation-pagination">
                  <span>
                    {loading
                      ? "Loading…"
                      : `${data.offset + (data.rows.length ? 1 : 0)}–${data.offset + data.rows.length} of ${data.total} sessions`}
                    {data.ancestors.length
                      ? ` · ${data.ancestors.length} ancestors retained as context`
                      : ""}
                    {data.ancestors_truncated
                      ? " · additional ancestors omitted"
                      : ""}
                  </span>
                  <button data-view-field="delegation_page"
                    disabled={!pages.length || loading}
                    onClick={() => {
                      const previous = [...pages];
                      setCursor(previous.pop() || "");
                      setPages(previous);setPageIndex(String(previous.length));
                    }}
                  >
                    Previous
                  </button>
                  <button data-view-field="delegation_page"
                    disabled={!data.next_cursor || loading}
                    onClick={() => {
                      setPages([...pages, cursor]);
                      setCursor(data.next_cursor);setPageIndex(String(pages.length+1));
                    }}
                  >
                    Next
                  </button>
                </div>
              )}
              {!single && !compact && !splitReader && <ViewDisclosure viewKey="delegation:definitions" className="delegation-definitions" summary="Metric definitions & evidence">
                <p>
                  Self counts one agent session. Self + children includes its
                  delegated sub-agents. Workspace totals count each recorded
                  execution once. Unknown values are —; + denotes partial
                  coverage; * marks a reported token minimum that may cover only the last request. Last evidence is not proof of completion. Source
                  coverage and lifecycle boundaries may be incomplete.
                </p>
                {data.warnings.map((warning, i) => (
                  <p key={i}>{warning}</p>
                ))}
                <dl>
                  {data.metrics.map((m) => (
                    <React.Fragment key={m.id}>
                      <dt>{m.label}</dt>
                      <dd>{m.definition}</dd>
                    </React.Fragment>
                  ))}
                </dl>
              </ViewDisclosure>}
            </>
          )}
          {!data && !error && <p>Loading agent session analysis…</p>}
        </div>
      </section>
    </>
  );
}

// Related work's sessions name their role first.
const peerPrefix = (n: Session) => n.peer ? `${peerRoleLabel(n.peer.role)} · ` : "";

function InboxSessions({ rows, data, shown, collapsed, toggle, openNode }: {
  rows: Session[];
  data: Analysis;
  shown: string;
  collapsed: Set<string>;
  toggle: (node: Session) => void;
  openNode: (node: Session) => void;
}) {
  const branches = timelineSessionBranches(rows);
  // Self usage keeps each session comparable; parallel children do not inflate its run time.
  const metrics = ["cost", "clock"].map(id => data.metrics.find(spec => spec.id === id)).filter((spec): spec is Spec => !!spec);
  return <div className="delegation-inbox-list" role="table" aria-label="Agent session hierarchy and usage">
    <div className="delegation-inbox-heading" role="row"><span role="columnheader">Agent session</span>{metrics.map(spec => <span key={spec.id} role="columnheader" title={`Self · ${spec.definition}`}>{spec.id === "clock" ? "Run time" : spec.label}</span>)}</div>
    {rows.map(n => <div key={n.id} role="row" data-session-id={n.id} data-session={n.id} data-depth={n.depth} className={`delegation-inbox-row ${n.context_only ? "delegation-ancestor" : ""} ${n.id === shown ? "current" : ""}`} onClick={event => { if (!(event.target as HTMLElement).closest("button")) openNode(n); }}>
      <div role="cell" className="delegation-inbox-session">
        <SessionIdentity name={`${n.label}${n.external_child || n.navigation ? " ↗" : ""}`} metadata={<>{peerPrefix(n)}{n.provider} · {n.model || "unknown model"} · {n.effort || "unknown"}{n.context_only ? " · Parent context" : ""}{n.self.tokens?.quality === "reported-minimum" ? ` · ${tableMetricText(n.self.tokens)} tokens · Breakdown not recorded` : n.token_attribution?.unallocated_tokens ? ` · ${formatted(n.token_attribution.unallocated_tokens)} tokens unallocated in branch` : ""}</>} depth={n.depth} branch={branches.get(n.id)} folded={!!n.child_count} current={n.id === shown} openSession={() => openNode(n)} />
        {!!n.child_count && <button className="delegation-chevron session-fold" style={{left: sessionBranchX(n.depth) - 10}} aria-label={`${collapsed.has(n.id) ? "Expand" : "Collapse"} ${n.label}`} title={collapsed.has(n.id) ? "Expand sessions" : "Collapse sessions"} aria-expanded={!collapsed.has(n.id)} onClick={() => toggle(n)}><Icon name={collapsed.has(n.id) ? "chevron-right" : "chevron-down"} /></button>}
      </div>
      {metrics.map(spec => {
        const value = n.self[spec.id];
        return <span key={spec.id} role="cell" className="delegation-inbox-metric" title={`${value ? cellTitle(value, spec) : "No retained evidence"}${tokenAttributionText(n) ? "\n" + tokenAttributionText(n) : ""}`}>{metricText(value, spec.unit)}</span>;
      })}
    </div>)}
    {!rows.length && <p>No saved agent sessions.</p>}
  </div>;
}

function TimelineOverview({
  readOnly = false,
  full,
  range,
  density,
  preview,
  change,
  pending = false,
  label = "Whole workspace",
  scale = timeScale(full),
  idle,
}: {
  scale?: TimeScale;
  idle?: { skip: boolean; set: (skip: boolean) => void; gaps: number };
  full: Range;
  range: Range;
  density: number[];
  preview: Range | null;
  change: (range: Range) => void;
  pending?: boolean;
  label?: string;
  readOnly?: boolean;
}) {
  const host = useRef<HTMLDivElement>(null);
  const cutoffs = useRef<HTMLOutputElement>(null);
  const [sizes, setSizes] = useState({host:0,cutoffs:0});
  useLayoutEffect(() => {
    const measure = () => {
      const next={host:host.current?.clientWidth??0,cutoffs:cutoffs.current?.offsetWidth??0};
      setSizes(previous=>previous.host===next.host&&previous.cutoffs===next.cutoffs?previous:next);
    };
    measure();const observer=new ResizeObserver(measure);
    if(host.current)observer.observe(host.current);
    if(cutoffs.current)observer.observe(cutoffs.current);
    return()=>observer.disconnect();
  },[]);
  const [draft, setDraft] = useState<Range | null>(null);
  const drag = useRef<{
    mode: string;
    at: number;
    initial: Range;
    pointer: number;
    draft: Range | null;
    rect: DOMRect;
    x: number;
  } | null>(null);
  const view = draft || preview || range,
    width = full[1] - full[0];
  // The overview draws on the same clock as the lanes: skipped gaps are slivers,
  // and pan/resize move by what it shows (virtual time), not by real time.
  const percentage = (time: number) => scale.fraction(time) * 100;
  const virtualFull: Range = [0, scale.span];
  const pointer = (clientX: number, rect: { left: number; width: number }) => toVirtual(scale, scale.time(Math.max(0, Math.min(1, (clientX - rect.left) / Math.max(1, rect.width)))));
  const drawn = (mode: string, initial: Range, anchor: number, current: number): Range => {
    const next = timelineDragRange(mode, [toVirtual(scale, initial[0]), toVirtual(scale, initial[1])], virtualFull, anchor, current);
    return [fromVirtual(scale, next[0]), fromVirtual(scale, next[1])];
  };
  const whole = view[0]===full[0]&&view[1]===full[1];
  const cutoffLeft = Math.max(8,Math.min(sizes.host-sizes.cutoffs-8,percentage((view[0]+view[1])/2)/100*sizes.host-sizes.cutoffs/2));
  const differentDays = new Date(view[0]).toDateString()!==new Date(view[1]).toDateString();
  const cutoff = (time:number) => `${differentDays?localDate.format(new Date(time))+' · ':''}${localCutoffTime.format(new Date(time))}`;
  const maxDensity = Math.max(1, ...density);
  function down(event: React.PointerEvent) {
    if (event.button !== 0) return;
    if (drag.current) return;
    const mode =
      (event.target as HTMLElement).closest<HTMLElement>("[data-handle]")
        ?.dataset.handle || "select";
    const rect=host.current!.getBoundingClientRect();
    drag.current = {
      mode,
      at: pointer(event.clientX,rect),
      initial: range,
      pointer: event.pointerId,
      draft: null,
      rect,
      x:event.clientX,
    };
    host.current!.setPointerCapture(event.pointerId);
  }
  function move(event: React.PointerEvent) {
    const d = drag.current;
    if (!d || d.pointer !== event.pointerId || !d.draft&&Math.abs(event.clientX-d.x)<4) return;
    event.preventDefault();
    d.draft = drawn(d.mode,d.initial,d.at,pointer(event.clientX,d.rect));
    setDraft(d.draft);
  }
  function up(event: React.PointerEvent) {
    const d=drag.current;
    if (!d || d.pointer !== event.pointerId) return;
    if(d.draft||Math.abs(event.clientX-d.x)>=4)change(drawn(d.mode,d.initial,d.at,pointer(event.clientX,d.rect)));
    setDraft(null);
    drag.current = null;
    if (host.current?.hasPointerCapture(event.pointerId))
      host.current.releasePointerCapture(event.pointerId);
  }
  function cancelDrag(){
    const d=drag.current;drag.current=null;setDraft(null);
    if(d&&host.current?.hasPointerCapture(d.pointer))host.current.releasePointerCapture(d.pointer);
  }
  useEffect(()=>{
    const cancel=(event:KeyboardEvent)=>{if(event.key==='Escape'&&drag.current){event.preventDefault();cancelDrag();}};
    window.addEventListener('keydown',cancel);return()=>window.removeEventListener('keydown',cancel);
  },[]);
  function key(event: React.KeyboardEvent, handle: string) {
    if(event.key==='Escape'){if(drag.current){event.preventDefault();cancelDrag();}return;}
    const step = (range[1] - range[0]) / (event.shiftKey ? 2 : 10);
    if (event.key === "Home" || event.key === "End") {
      event.preventDefault();
      change(
        handle === "start"
          ? [event.key === "Home" ? full[0] : range[1] - 1, range[1]]
          : handle === "end"
            ? [range[0], event.key === "End" ? full[1] : range[0] + 1]
            : event.key === "Home"
              ? clampRange([full[0], full[0] + range[1] - range[0]], full)
              : clampRange([full[1] - range[1] + range[0], full[1]], full),
      );
    } else if (["ArrowLeft", "ArrowRight"].includes(event.key)) {
      event.preventDefault();
      const delta = event.key === "ArrowLeft" ? -step : step;
      const anchor=handle==='end'?range[1]:range[0];
      change(drawn(handle,range,toVirtual(scale,anchor),toVirtual(scale,anchor+delta)));
    }
  }
  return (
    <div className="delegation-overview-wrap" ref={element=>{if(element)element.inert=readOnly;}}>
      <div className="delegation-overview-label">
        <strong>{whole?label:`Showing ${duration(view[1]-view[0])} of ${duration(width)}`}</strong>
        <small>
          {pending?"Loading this window…":"drag to zoom and pan"}
        </small>
        {idle?<label className="delegation-idle-toggle" title="Remove stretches of 15 minutes or more with nothing running in any session that end with the caller speaking. Each is marked with the time the session resumed.">
          <input type="checkbox" data-view-field="timeline_idle" checked={idle.skip} onChange={event=>idle.set(event.currentTarget.checked)}/>
          Skip idle gaps{idle.skip&&idle.gaps?<small> · {idle.gaps}</small>:null}
        </label>:null}
        <button type="button" className="delegation-overview-reset" onClick={()=>{cancelDrag();change(full);}}>Reset</button>
      </div>
      <div
        className="delegation-overview"
        ref={host}
        data-dragging={draft ? drag.current?.mode : undefined}
        onPointerDown={down}
        onPointerMove={move}
        onPointerUp={up}
        onPointerCancel={cancelDrag}
        onLostPointerCapture={cancelDrag}
        onKeyDown={event=>{if(event.key==='Escape'&&drag.current){event.preventDefault();cancelDrag();}}}
        onDoubleClick={() => change(full)}
      >
        <div className={`delegation-histogram${scale.compressed?" is-compressed":""}`}>
          {density.map((count, i) => {
            const from = full[0] + (width * i) / density.length, to = full[0] + (width * (i + 1)) / density.length;
            return <i key={i} style={{ height: `${(count / maxDensity) * 100}%`, ...(scale.compressed ? { left: `${percentage(from)}%`, width: `${Math.max(0, percentage(to) - percentage(from))}%` } : {}) }} />;
          })}
        </div>
        {scale.cuts.map(cut => <i key={cut.end} className="delegation-idle-cut" style={{ left: `${cut.fraction * 100}%` }} title={`Idle ${duration(cut.end - cut.start)}, skipped; resumed ${localTime(cut.end)}`} />)}
        <div
          className="delegation-dim"
          style={{ left: 0, width: `${percentage(view[0])}%` }}
        />
        <div
          className="delegation-dim"
          style={{ left: `${percentage(view[1])}%`, right: 0 }}
        />
        <div
          className="delegation-viewport"
          style={{
            left: `${percentage(view[0])}%`,
            width: `${Math.max(0.15, percentage(view[1]) - percentage(view[0]))}%`,
          }}
          data-handle="pan"
          role="button"
          tabIndex={0}
          aria-label="Pan selected timeline window"
          onKeyDown={(e) => key(e, "pan")}
        >
          <span
            data-handle="start"
            role="slider"
            tabIndex={0}
            aria-label="Timeline window start"
            aria-valuemin={full[0]}
            aria-valuemax={view[1]}
            aria-valuenow={view[0]}
            aria-valuetext={localTime(view[0])}
            onKeyDown={(e) => {
              e.stopPropagation();
              key(e, "start");
            }}
          />
          <span
            data-handle="end"
            role="slider"
            tabIndex={0}
            aria-label="Timeline window end"
            aria-valuemin={view[0]}
            aria-valuemax={full[1]}
            aria-valuenow={view[1]}
            aria-valuetext={localTime(view[1])}
            onKeyDown={(e) => {
              e.stopPropagation();
              key(e, "end");
            }}
          />
        </div>
        <output ref={cutoffs} className="delegation-overview-cutoffs" aria-label="Selected timeline window" aria-live="off" hidden={whole} style={{left:cutoffLeft}}>
          <time dateTime={new Date(view[0]).toISOString()} title={localTime(view[0])}>{cutoff(view[0])}</time>
          <span aria-hidden="true">→</span>
          <time dateTime={new Date(view[1]).toISOString()} title={localTime(view[1])}>{cutoff(view[1])}</time>
        </output>
      </div>
      <div className="delegation-overview-bounds">
        <span>{localTime(full[0])}</span>
        <span>{localTime(full[1])}</span>
      </div>
    </div>
  );
}

export function ParallelActivity({
  endpoint,
  data,
  rows,
  range,
  setRange,
  full,
  collapsed,
  toggle,
  selected,
  openNode,
  timeline=false,single=false,compact=false,previewOnly=false,openOverview,
}: {
  previewOnly?:boolean;openOverview?:()=>void;
  timeline?:boolean;single?:boolean;compact?:boolean;
  endpoint: string;
  data: Analysis;
  rows: Session[];
  range: Range;
  setRange: (range: Range) => void;
  full: Range;
  collapsed: Set<string>;
  toggle: (node: Session) => void;
  selected: string;
  openNode: (node: Session, message?: string, sourceConversation?: string) => void;
}) {
  const [activity, setActivity] = useState<Activity | null>(null),
    [error, setError] = useState("");
  const nativeLabels=useMemo(()=>nativeTimelineLabels(activity?.snapshot===data.snapshot&&activity.start===range[0]&&activity.end===range[1]?activity:null),[activity,data.snapshot,range[0],range[1]]);
  const labelData=nativeLabels;
  // Events per kind across every session (whole conversations, not the zoom window): a merged mark shows its rarest kind (event-merge.ts).
  const eventCounts=useMemo(()=>eventKindCounts([...nativeLabels.events.values()].flat()),[nativeLabels]);
  const actors=useMemo(()=>timeline?splitTimelineCallers(nativeLabels.messages,rows):{caller:[],messages:labelData?.messages??new Map()},[timeline,nativeLabels,rows,labelData]);
  const allMessages=useMemo(()=>[...nativeLabels.messages.values()].flat(),[nativeLabels]);
  const [timelineDisclosures]=useViewState<Record<string,boolean>>("disclosures",{},timeline);
  const [timelineVisibility,setTimelineVisibility]=useViewState<TimelineVisibility>("timeline_visibility",{},timeline);
  const recursive=timeline;
  const [brush, setBrush] = useState<Range | null>(null);
  const [workExpanded,setWorkExpanded]=useViewState<string[]>("delegation_expanded",[],location.pathname.startsWith("/work/"));
  const expandedIDs=workExpanded,setExpandedIDs=setWorkExpanded;
  const expanded = useMemo(() => new Set(expandedIDs), [expandedIDs]);
  const setExpanded = (change: (value: Set<string>) => Set<string>) => setExpandedIDs(previous => [...change(new Set(previous))].sort());
  const gesture = useRef<{
    pointer: number;
    capture: HTMLElement;
    rect: DOMRect;
    initial: Range;
    x: number;
    at: number;
    draft: Range | null;
    clock: TimeScale;
  } | null>(null);
  const suppressClick = useRef(false);
  const [mark, setMark] = useState<{
    node: Session;
    mark: Mark;
    anchor: HTMLButtonElement;
  } | null>(null);
  const [workEvent,setWorkEvent]=useViewState("delegation_event","",location.pathname.startsWith("/work/"));
  const eventID=workEvent,setEventID=setWorkEvent;
  const closeMark = useCallback(() => {setMark(null);setEventID("");}, [setEventID]);
  const [pending, setPending] = useState(true);
  // Idle gaps are found over the whole workspace, so the overview and every
  // zoomed window share one clock; a zoomed window fetches the whole range once.
  const [skipIdle, setSkipIdle] = useViewState<boolean>("timeline_idle", true, timeline && location.pathname.startsWith("/work/"));
  const [wholeActivity, setWholeActivity] = useState<Activity | null>(null);
  const track = useRef<HTMLDivElement>(null);
  const clock = useRef<HTMLDivElement>(null);
  const [trackWidth, setTrackWidth] = useState(900);
  const [clockWidth, setClockWidth] = useState(0);
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout>;
    const observer = new ResizeObserver(() => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        setTrackWidth(track.current?.clientWidth || 900);
        setClockWidth(clock.current?.clientWidth || 0);
      }, 150);
    });
    if (track.current) observer.observe(track.current);
    if (clock.current) observer.observe(clock.current);
    return () => { clearTimeout(timer); observer.disconnect(); };
  }, [timeline,recursive]);
  // A page may carry extra ancestor context; lane requests remain bounded.
  const laneRows = useMemo(() => {
    const included = new Set(data.rows.map((n) => n.id));
    for (const n of rows) {
      if (included.size >= 100) break;
      included.add(n.id);
    }
    return rows.filter((n) => included.has(n.id));
  }, [rows, data.rows]);
  const ids = laneRows.map((n) => n.id).join(",");
  useEffect(() => {
    setMark(null);
    setBrush(null);
  }, [range]);
  useEffect(() => {
    const controller = new AbortController();
    setPending(true);
    setError("");
    const params = new URLSearchParams({
      snapshot: data.snapshot,
      start: String(range[0]),
      end: String(range[1]),
      sessions: ids,
      width: String(trackWidth),
      ...(timeline ? {labels:"1"} : {}),
    });
    readJSON<Activity>(`${endpoint}/parallel?${params}`, controller.signal)
      .then(setActivity, (e) => {
        if (e.name !== "AbortError") setError(e.message);
      })
      .finally(() => {
        if (!controller.signal.aborted) setPending(false);
      });
    return () => controller.abort();
  }, [endpoint, ids, data.snapshot, range, trackWidth,timeline]);
  const visibleActivity =
    activity?.start === range[0] && activity?.end === range[1]
      ? activity
      : null;
  const wholeWindow = range[0] === full[0] && range[1] === full[1];
  useEffect(() => {
    if (!timeline || !skipIdle || wholeWindow) return;
    if (wholeActivity?.snapshot === data.snapshot && wholeActivity.start === full[0] && wholeActivity.end === full[1] && (wholeActivity as Activity & { sessions?: string }).sessions === ids) return;
    const controller = new AbortController();
    const params = new URLSearchParams({ snapshot: data.snapshot, start: String(full[0]), end: String(full[1]), sessions: ids, width: "1000", labels: "1" });
    readJSON<Activity>(`${endpoint}/parallel?${params}`, controller.signal).then(value => setWholeActivity({ ...value, sessions: ids } as Activity), () => {});
    return () => controller.abort();
  }, [timeline, skipIdle, wholeWindow, endpoint, ids, data.snapshot, full[0], full[1]]);
  const idleSource = wholeWindow ? visibleActivity : wholeActivity?.snapshot === data.snapshot && wholeActivity.start === full[0] && wholeActivity.end === full[1] ? wholeActivity : null;
  const gaps = useMemo<IdleGap[]>(() => {
    if (!timeline || !idleSource) return [];
    const byID = new Map(rows.map(row => [row.id, row]));
    const busy = idleSource.lanes.flatMap(lane => laneBusy(lane, byID.get(lane.session)));
    const callers = splitTimelineCallers(nativeTimelineLabels(idleSource).messages, rows).caller.flatMap(message => { const at = message.at ? Date.parse(message.at) : NaN; return Number.isFinite(at) ? [at] : []; });
    return idleGaps({ busy, callers });
  }, [timeline, idleSource, rows]);
  const shownGaps = skipIdle ? gaps : [];
  const fullScale = useMemo(() => timeScale(full, shownGaps), [full[0], full[1], shownGaps]);
  const windowScale = useMemo(() => timeScale(range, shownGaps), [range[0], range[1], shownGaps]);
  const shownLaneRows=useMemo(()=>{
    const active=rows.filter(node=>node.start!=null&&node.end!=null&&node.start<=range[1]&&node.end>=range[0]);
    return laneRows.filter(node=>active.some(other=>other.id===node.id||other.outline.startsWith(`${node.outline}.`)));
  },[rows,laneRows,range[0],range[1]]);
  const sessionBranches=useMemo(()=>timelineSessionBranches(shownLaneRows),[shownLaneRows]);
  const legend=useMemo(()=>timelineLegendWindow([
    {id:`caller:${data.rows[0]?.workspace_id??endpoint}`,caller:true,messages:actors.caller,marks:visibleActivity?.lanes.flatMap(lane=>lane.marks)??[],neighbors:allMessages},
    ...shownLaneRows.map(node=>({id:node.id,messages:actors.messages.get(node.id)??[],marks:visibleActivity?.lanes.find(lane=>lane.session===node.id)?.marks??[],neighbors:nativeLabels.messages.get(node.id)})),
  ],range),[actors,visibleActivity,shownLaneRows,nativeLabels,allMessages,range[0],range[1]]);
  const eventsExpanded=!previewOnly&&legend.populated.some(id=>timelineDisclosures[`delegation-labels:${id}:session`]);
  const hideEvents=compact || previewOnly;
  const columns=useTimelineColumns(timeline,{eventsCollapsed:timeline&&!eventsExpanded});
  const width = range[1] - range[0];
  function change(next: Range) {
    setRange(clampRange(next, full));
  }
  function brushDown(event: React.PointerEvent<HTMLDivElement>) {
    if (event.button !== 0 || gesture.current) return;
    suppressClick.current = false;
    const rect = event.currentTarget.getBoundingClientRect();
    const capture = event.target as HTMLElement;
    const at =scaledPointerTime(event.clientX,rect,windowScale);
    gesture.current = {
      clock: windowScale,
      pointer: event.pointerId,
      capture,
      rect,
      initial: range,
      x: event.clientX,
      at,
      draft: null,
    };
    // Capture the original target so an ordinary tool-event click still works.
    capture.setPointerCapture(event.pointerId);
  }
  function brushMove(event: React.PointerEvent<HTMLDivElement>) {
    const g = gesture.current;
    if (
      !g ||
      g.pointer !== event.pointerId ||
      (!g.draft && Math.abs(event.clientX - g.x) < 4)
    )
      return;
    event.preventDefault();
    const now =scaledPointerTime(event.clientX,g.rect,g.clock);
    g.draft = timelineDragRange('select',g.initial,g.initial,g.at,now);
    setBrush(g.draft);
  }
  function finishBrush(event?: React.PointerEvent<HTMLDivElement>) {
    const g = gesture.current;
    if (!g || (event && g.pointer !== event.pointerId)) return;
    if(event?.type==='pointerup'&&(g.draft||Math.abs(event.clientX-g.x)>=4))g.draft=timelineDragRange('select',g.initial,g.initial,g.at,scaledPointerTime(event.clientX,g.rect,g.clock));
    gesture.current = null;
    if (g.capture.hasPointerCapture(g.pointer))
      g.capture.releasePointerCapture(g.pointer);
    if (g.draft) {
      suppressClick.current = true;
      if (event?.type === "pointerup") change(g.draft);
      setTimeout(() => {
        suppressClick.current = false;
      }, 0);
    }
    setBrush(null);
  }
  useEffect(() => {
    const cancel = (event: KeyboardEvent) => {
      if (event.key === "Escape" && gesture.current) {
        event.preventDefault();
        finishBrush();
      }
    };
    window.addEventListener("keydown", cancel);
    return () => window.removeEventListener("keydown", cancel);
  }, []);
  const brushHandlers = {
    onPointerDown: brushDown,
    onPointerMove: brushMove,
    onPointerUp: finishBrush,
    onPointerCancel: finishBrush,
    onLostPointerCapture:finishBrush,
  };
  function style(span: Span): React.CSSProperties {
    const left = Math.max(range[0], span.start),
      right = Math.min(range[1], Math.max(span.start, span.end));
    return {
      left: `${((left - range[0]) / width) * 100}%`,
      width: `${(Math.max(0, right - left) / width) * 100}%`,
    };
  }
  const ticks = timelineTickLabels(range,Math.max(2,Math.min(6,Math.floor((clockWidth||350)/100))),windowScale.compressed?windowScale.time:undefined);
  const cutLabels = cutAxisLabels(ticks.map((_,i)=>i/Math.max(1,ticks.length-1)),windowScale.cuts,clockWidth||350);
  const lanes = new Map(visibleActivity?.lanes.map((l) => [l.session, l]));
  const sourceHref=(node:Session,item:RibbonObservation)=>ribbonSourceURL(item,{workspace:node.workspace_id,conversation:node.conversation_id,session:node.id},location.href);
  const eventHref=(node:Session,e:AgentEventLink)=>withEventLink(ribbonSourceURL({id:e.id,start:0,end:0,title:"",color:"",thinking:false,message:{id:e.message_id} as LabelMessage},{workspace:node.workspace_id,conversation:node.conversation_id,session:node.id},location.href),e);
  return (
    <div className={`delegation-parallel ${previewOnly?"is-preview":""} ${recursive?"is-recursive":""} ${timeline?"is-timeline":""} ${hideEvents?"without-events":""}`} ref={track} style={timeline?columns.style:undefined}>
      {previewOnly?<button type="button" className="lt-preview-activate" aria-label="Open full-width timeline" onPointerDown={event=>{if(event.button===0)event.currentTarget.setPointerCapture(event.pointerId);}} onClick={openOverview}><span>Open timeline to explore <Icon name="arrow-right"/></span></button>:null}
      <div>
      <TimelineOverview
        readOnly={previewOnly}
        label={single ? "Whole conversation" : "Whole workspace"}
        full={full}
        range={range}
        density={data.density}
        preview={brush}
        change={change}
        pending={pending}
        scale={fullScale}
        idle={timeline&&!previewOnly?{skip:skipIdle,set:setSkipIdle,gaps:gaps.length}:undefined}
      />
      {timeline?<TimelineLegend data-view-field="timeline_visibility" visibility={timelineVisibility} onSelect={(section,values,action)=>setTimelineVisibility(previous=>updateTimelineVisibility(previous,section,values,action))} messages={legend.messages} origins={legend.origins} taxonomy={nativeLabels.taxonomy} tools={Object.fromEntries(Object.entries(categories).filter(([id])=>legend.tools.includes(id)))} error={legend.error} events={[...nativeLabels.events.values()].flat()}/>:<div className="delegation-legend">
        {!timeline?<span>
          <i className="model" />
          Model / orchestration · inferred
        </span>:null}
        {Object.entries(categories).map(([key, text]) => (
          <span key={key}>
            <i className={key} />
            {text}
          </span>
        ))}
        <span>
          <i className="error" />
          Error
        </span>
      </div>}
      {!timeline?<p className="delegation-evidence-note">
        Dark violet is inferred activity between nearby observations. Tools
        punctuate this background; blank time is unclassified. Width is
        call-to-result elapsed time, not CPU time or process lifetime. Wait
        calls and polling are distinct; blocking time needs explicit boundaries.
      </p>:null}
      {error && (
        <p className="delegation-error" role="alert">
          {error}
        </p>
      )}
      {laneRows.length < rows.length && (
        <p className="delegation-evidence-note">
          This window includes the current page and up to 100 lanes. Additional
          ancestor context remains available in the hierarchy table.
        </p>
      )}
      <div
        className="delegation-lane-scroll"
        aria-busy={pending}
        data-selecting={brush ? "true" : undefined}
        onClickCapture={(event) => {
          if (suppressClick.current) {
            event.preventDefault();
            event.stopPropagation();
          }
        }}
      >
        {timeline?<div className="delegation-axis lt-resizable-axis">
          <span className="lt-column-heading">Session{columns.handle('execution')}</span>
          {!hideEvents && <span className="lt-column-heading">Events{columns.handle('events')}</span>}
          <div ref={clock} className="lt-clock-heading" {...brushHandlers}>{ticks.map((tick,i)=>cutLabels.hiddenTicks.has(i)?null:<small key={i} style={{left:`${i/(ticks.length-1)*100}%`}}><span className="delegation-tick-date">{tick.date}</span><time dateTime={new Date(tick.at).toISOString()} title={localTime(tick.at)}>{tick.time}</time></small>)}{windowScale.cuts.map((cut,i)=><span key={cut.end} className="lt-idle-cut" style={{left:`${cut.fraction*100}%`}} title={`Nothing ran for ${duration(cut.end-cut.start)}; resumed ${localTime(cut.end)}`}>{cutLabels.labelled.has(i)?<time dateTime={new Date(cut.end).toISOString()}><b>{resumeTime.format(new Date(cut.end))}</b>+{idleDuration(cut.end-cut.start)}</time>:null}</span>)}</div>
        </div>:<div className="delegation-axis">
          <span className={recursive?"lt-axis-names":undefined}>{recursive?<><span>Agent session</span><span>Events</span></>:"Agent session"}</span>
          <div ref={clock} {...brushHandlers}>
            {ticks.map((tick, i) => (
              <small key={i} style={{ left: `${i/(ticks.length-1)*100}%` }}>
                <span className="delegation-tick-date">{tick.date}</span>
                <time dateTime={new Date(tick.at).toISOString()} title={localTime(tick.at)}>{tick.time}</time>
              </small>
            ))}
            {brush && (
              <div
                className="delegation-brush"
                style={style({ start: brush[0], end: brush[1] })}
              />
            )}
          </div>
        </div>}
        {timeline?<div className="delegation-lane lt-recursive-lane lt-caller-row" data-session-id="caller">
          <RecursiveSessionRibbon visibility={timelineVisibility} collapsed={previewOnly} hideEvents={hideEvents} caller sessionID={`caller:${data.rows[0]?.workspace_id??endpoint}`} depth={0} name="Caller" metadata="Human & supplied input" messages={actors.caller} boundaryMessages={allMessages} boundaryMarks={visibleActivity?.lanes.flatMap(lane=>lane.marks)??[]} marks={[]} inferred={[]} taxonomy={nativeLabels.taxonomy} range={range} scale={windowScale} detail brushHandlers={brushHandlers} preview={brush}
            sourceHref={item=>{const owner=rows.find(row=>row.id===item.message?.session_id);return owner?sourceHref(owner,item):null;}}
            foldControl={<Icon name="human"/>} restore={()=>{}}
            select={item=>{if(!item.message)return;const owner=rows.find(row=>row.id===item.message!.session_id);if(owner){setEventID('label:'+item.message.id);openNode(owner,item.message.id,item.message.conversation_id);}}}/>
        </div>:null}
        {shownLaneRows.map((n) => {
          const lane = lanes.get(n.id);
          const detailed = expanded.has(n.id);
          const visible =
            n.start != null &&
            n.end != null &&
            n.start <= range[1] &&
            n.end >= range[0];
          if(recursive&&labelData)return <div key={n.id} data-session-id={n.id} data-session={n.id} className={`delegation-lane lt-recursive-lane ${selected===n.id?"delegation-lane-current":""}`}>
            <RecursiveSessionRibbon visibility={timelineVisibility} collapsed={previewOnly} hideEvents={hideEvents} current={selected===n.id} sessionID={n.id} depth={n.depth} branch={sessionBranches.get(n.id)} name={n.label} metadata={`${peerPrefix(n)}${n.provider} · ${n.model||"model not recorded"}`} messages={actors.messages.get(n.id)??[]} boundaryMessages={timeline?labelData.messages.get(n.id):undefined} marks={lane?.marks??[]} inferred={timeline?[]:lane?.model_inferred??[]} taxonomy={labelData.taxonomy} range={range} scale={windowScale} detail brushHandlers={brushHandlers} preview={brush}
              openSession={timeline?()=>openNode(n):undefined}
              sourceHref={timeline?item=>sourceHref(n,item):undefined}
              events={timeline?nativeLabels.events.get(n.id):undefined} eventCounts={eventCounts} eventHref={timeline?e=>eventHref(n,e):undefined}
              foldControl={!n.child_count?null:previewOnly?<span className="lt-preview-fold"><Icon name="tree"/></span>:<button type="button" className="delegation-chevron" aria-label={`${collapsed.has(n.id)?"Expand":"Collapse"} child sessions of ${n.label}`} title={collapsed.has(n.id) ? "Expand sessions" : "Collapse sessions"} onClick={()=>toggle(n)}><Icon name={collapsed.has(n.id)?"chevron-right":"chevron-down"}/></button>}
              restore={(item,anchor)=>{if(item.mark&&!mark&&eventID===JSON.stringify([n.id,item.mark.message_id||item.mark.tool_id||item.mark.name,item.mark.start,item.mark.end,item.mark.category])){anchor.scrollIntoView({block:"nearest",inline:"nearest"});setMark({node:n,mark:item.mark,anchor});}}}
              select={(item,anchor)=>{if(item.message){setEventID("label:"+item.message.id);openNode(n,item.message.id,item.message.conversation_id);}else if(item.mark){setEventID(JSON.stringify([n.id,item.mark.message_id||item.mark.tool_id||item.mark.name,item.mark.start,item.mark.end,item.mark.category]));setMark({node:n,mark:item.mark,anchor});}}}/>
            {lane?.truncated?<small className="delegation-truncation">Showing first 2,400 marks; zoom in for remaining events.</small>:null}
            {lane?.messages_truncated?<small className="delegation-truncation">Message display is limited to 2,400 observations per session. Zoom in to see more.</small>:null}
          </div>;
          return (
            <div
              key={n.id} data-session-id={n.id}
              data-session={n.id}
              className={`delegation-lane ${detailed ? "detailed" : ""} ${selected === n.id ? "delegation-lane-current" : ""} ${!visible ? "delegation-ancestor" : ""}`}
              onClick={(event) => {
                if (!(event.target as HTMLElement).closest("button, a"))
                  openNode(n);
              }}
            >
              <div
                className="delegation-lane-label"
                style={{ paddingLeft: Math.min(n.depth, 8) * 12 + 6 }}
              >
                <button
                  className="delegation-chevron"
                  aria-label={`${collapsed.has(n.id) ? "Expand" : "Collapse"} ${n.label}`} title={collapsed.has(n.id) ? "Expand sessions" : "Collapse sessions"}
                  disabled={!n.child_count}
                  onClick={() => toggle(n)}
                >
                  {n.child_count ? (
                    <Icon
                      name={
                        collapsed.has(n.id) ? "chevron-right" : "chevron-down"
                      }
                    />
                  ) : (
                    "·"
                  )}
                </button>
                <button
                  className="delegation-name" data-overflow-title=""
                  onClick={() => openNode(n)}
                >
                  <strong>
                    {n.label}
                  </strong>
                  <small>
                    {n.provider} · {n.model || "unknown model"}
                    {` · ${n.effort || "unknown"}`}
                  </small>
                </button>
                <button
                  className="delegation-activity-toggle"
                  title={detailed ? "Collapse activity lanes" : "Expand activity lanes"}
                  aria-label={`${detailed ? "Collapse" : "Expand"} activity for ${n.label}`}
                  aria-expanded={detailed}
                  onClick={() => {
                    closeMark();
                    setExpanded((old) => {
                      const next = new Set(old);
                      next.has(n.id) ? next.delete(n.id) : next.add(n.id);
                      return next;
                    });
                  }}
                >
                  Activity <Icon name={detailed ? "chevron-down" : "chevron-right"} />
                </button>
              </div>
              <div className="delegation-lane-track" {...brushHandlers}>
                {ticks.map((_, i) => (
                  <i
                    key={i}
                    className="delegation-gridline"
                    style={{ left: `${i * 20}%` }}
                  />
                ))}
                {lane?.model_inferred.map((span, i) => (
                  <div
                    key={i}
                    className="delegation-model-bg"
                    style={style(span)}
                    title="Inferred model / orchestration; not measured processing time"
                  />
                ))}
                {detailed &&
                  Object.keys(categories).map((category, i) => (
                    <small
                      key={category}
                      className="delegation-category-label"
                      style={{ top: i * 34 + 3 }}
                    >
                      {categories[category]}
                    </small>
                  ))}
                {lane?.marks.map((m, i) => (
                  <button data-view-field="delegation_event"
                    key={i}
                    className={`delegation-mark ${m.category} ${m.error ? "failed" : ""}`}
                    style={{
                      ...style(m),
                      top: detailed
                        ? Object.keys(categories).indexOf(m.category) * 34 + 20
                        : m.error
                          ? 25
                          : 9,
                    }}
                    title={`${m.name}${m.count > 1 ? ` · ${m.count} grouped calls` : ""} · ${m.error ? "reported error · " : ""}${m.end_known ? duration(m.end - m.start) : "end unavailable"}\n${localTime(m.start)}${m.end_known ? ` → ${localTime(m.end)}` : ""}`}
                    aria-label={`${m.name}, ${m.count} call${m.count === 1 ? "" : "s"}${m.error ? ", error" : ""}`}
                    aria-haspopup="dialog"
                    data-event-id={JSON.stringify([n.id,m.message_id||m.tool_id||m.name,m.start,m.end,m.category])}
                    ref={anchor=>{if(anchor && !mark && eventID===anchor.dataset.eventId) setMark({node:n,mark:m,anchor});}}
                    aria-expanded={mark?.mark === m && mark?.node.id === n.id}
                    onClick={(event) => {
                      setEventID(event.currentTarget.dataset.eventId!);
                      setMark({ node: n, mark: m, anchor: event.currentTarget });
                    }}
                  >
                    {m.count > 1 ? m.count : ""}
                  </button>
                ))}
                {brush && (
                  <div
                    className="delegation-brush"
                    style={style({ start: brush[0], end: brush[1] })}
                  />
                )}
              </div>
              {lane?.truncated && (
                <small className="delegation-truncation">
                  Showing first 2,400 marks; zoom in for remaining events.
                </small>
              )}
            </div>
          );
        })}
      </div>
      {!previewOnly && mark && (
        <ActivityPopover
          key={`${mark.node.id}:${mark.mark.start}:${mark.mark.name}`}
          {...mark}
          close={closeMark}
          jump={() => {
            closeMark();
            openNode(mark.node, mark.mark.message_id);
          }}
        />
      )}
      {selected && !previewOnly && (
        <ViewDisclosure viewKey="delegation:definitions" className="delegation-definitions" summary="Selected agent session · estimated activity periods">
          <p>
            Clusters use tool/request timestamps and the first and last session
            message timestamps. A gap over 30 minutes starts a new cluster;
            intermediate message activity may be missing from this sample. These
            are not asserted run boundaries. Usage stays at session level.
          </p>
          {lanes.get(selected)?.episodes.map((episode, i) => (
            <button
              key={i}
              onClick={() =>
                change([
                  episode.start,
                  Math.max(episode.start + 1000, episode.end),
                ])
              }
            >
              Cluster {i + 1} · {localTime(episode.start)} ·{" "}
              {duration(episode.end - episode.start)}
            </button>
          ))}
        </ViewDisclosure>
      )}
      </div>
    </div>
  );
}

function ActivityPopover({
  node,
  mark,
  anchor,
  close,
  jump,
}: {
  node: Session;
  mark: Mark;
  anchor: HTMLButtonElement;
  close: () => void;
  jump: () => void;
}) {
  const panel = useRef<HTMLDivElement>(null);
  const action = useRef<HTMLButtonElement>(null);
  const [position, setPosition] = useState({ left: 0, top: 0 });
  const reposition = useCallback(() => {
    const bounds = anchor.getBoundingClientRect();
    if (!anchor.isConnected || bounds.bottom <= 0 || bounds.top >= innerHeight) {
      close();
      return;
    }
    const size = panel.current!.getBoundingClientRect();
    const below = bounds.bottom + 8;
    setPosition({
      left: Math.max(12, Math.min(bounds.left, innerWidth - size.width - 12)),
      top: Math.max(12, Math.min(
        below + size.height <= innerHeight - 12 ? below : bounds.top - size.height - 8,
        innerHeight - size.height - 12,
      )),
    });
  }, [anchor, close]);
  useLayoutEffect(() => {
    reposition();
    action.current?.focus({ preventScroll: true });
  }, [reposition]);
  useEffect(() => {
    const outside = (event: PointerEvent) => {
      if (!panel.current?.contains(event.target as Node) &&
          !anchor.contains(event.target as Node)) close();
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        close();
        anchor.focus({ preventScroll: true });
      }
    };
    const moved = (event: Event) => {
      if (!panel.current?.contains(event.target as Node)) reposition();
    };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("keydown", escape);
    window.addEventListener("scroll", moved, true);
    window.addEventListener("resize", reposition);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("keydown", escape);
      window.removeEventListener("scroll", moved, true);
      window.removeEventListener("resize", reposition);
    };
  }, [anchor, close, reposition]);
  return createPortal(
    <div
      ref={panel}
      className="delegation-event-detail"
      role="dialog"
      aria-label={`Activity: ${mark.name}`}
      style={position}
    >
      <div className="delegation-event-heading">
        <strong>{mark.name}</strong>
        <button
          onClick={() => {
            close();
            anchor.focus({ preventScroll: true });
          }}
          aria-label="Close activity details"
        >
          <Icon name="close" />
        </button>
      </div>
      <small>Session {node.label}</small>
      <button ref={action} className="primary delegation-event-jump" onClick={jump}>
        {mark.count > 1 ? "Jump to first tool call" : "Jump to tool call"} ↗
      </button>
      <span>
        {categories[mark.category]} · {mark.count} retained call{mark.count === 1 ? "" : "s"} ·{" "}
        {mark.end_known
          ? `${duration(mark.end - mark.start)} elapsed`
          : "elapsed time unavailable"}
        {mark.error ? " · Reported error" : ""}
      </span>
      <small>
        {localTime(mark.start)}
        {mark.end_known ? ` → ${localTime(mark.end)}` : " · observed start"}
      </small>
      {mark.count > 1 && (
        <small>
          Short calls are grouped at this zoom. Zoom in to separate them;
          this opens the first retained call.
        </small>
      )}
    </div>,
    document.body,
  );
}

declare global {
  interface Window {
    pharosDelegation?: {
      mount: (
        element: HTMLElement,
        props: Props,
      ) => {
        conversation: (id: string, host: HTMLElement | null) => void;
        layout: (compact: boolean, splitReader: boolean) => void;
        destroy: () => void;
      };
    };
  }
}
export function installDelegationExplorer() {
  let root: Root | null = null;
  window.pharosDelegation = {
    mount(element, props) {
      root?.unmount();
      root = createRoot(element);
      const owned = root;
      let current = props;
      owned.render(<DelegationExplorer {...current} />);
      return {
        conversation(id, host) {
          current = { ...current, conversation: id, contextHost: host };
          owned.render(<DelegationExplorer {...current} />);
        },
        layout(compact, splitReader) {
          current = { ...current, compact, splitReader };
          owned.render(<DelegationExplorer {...current} />);
        },
        destroy() {
          owned.unmount();
          if (root === owned) root = null;
        },
      };
    },
  };
  window.dispatchEvent(new Event("pharos:delegation-ready"));
}
