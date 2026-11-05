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
import {nativeTimelineLabels,type NativeTimelineMessage} from "./label-timeline-overlay";
import type {Taxonomy} from "./conversation-labels";
import {splitTimelineCallers} from "./timeline-caller-model";
import {useTimelineColumns} from "./timeline-columns";
import {timelineSessionBranches} from "./timeline-label-model";
import {TimelineLegend,timelineLegendWindow} from "./timeline-legend";
import {ribbonSourceURL,type RibbonObservation} from "./label-session-ribbon-model";

// reader-model.js loads before the bundle and also supplies turn ages.
declare function relativeAge(value: number | null | undefined): string;

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
  headingHost?: HTMLElement | null;
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
  "calls",
  "errors",
  "files",
  "clock",
  "toolTime",
];
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
export function timelineTickLabels(range: Range, count = 6) {
  const width=range[1]-range[0];
  const time=new Intl.DateTimeFormat(undefined,{
    hour:"numeric",minute:"2-digit",
    ...(width<=60000?{second:"2-digit" as const}:{}),
    ...(new Date(range[0]).getTimezoneOffset()!==new Date(range[1]).getTimezoneOffset()?{timeZoneName:"short" as const}:{}),
  });
  let previous="";
  return Array.from({length:count},(_,i)=>{
    const at=range[0]+width*i/Math.max(1,count-1),value=new Date(at),day=`${value.getFullYear()}-${value.getMonth()}-${value.getDate()}`;
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
export function cellTitle(metric: Metric, spec: Spec) {
  const exact =
    metric.value == null
      ? ""
      : spec.unit === "date"
        ? `${localTime(metric.value)}\n`
        : spec.unit === "usd"
          ? `${formatUSD(metric.value)}\n`
          : `${metric.value.toLocaleString(undefined, { maximumFractionDigits: 6 })} ${spec.unit}\n`;
  return `${exact}${spec.definition}\n${metric.quality} · ${metric.known}/${metric.total} retained coverage. ${metric.value === null ? "No sufficient retained evidence." : "Source coverage may be incomplete."}`;
}

// Frame each parent's visible subtree across the entire table. At page edges,
// leave a continuing branch open rather than implying that the subtree ended.
export function hierarchyFrames(rows: Session[], continues: boolean) {
  const frames = new Map<string, HierarchyFrame>();
  const open: Session[] = [];
  rows.forEach((n, i) => {
    const frame: HierarchyFrame = { active: [], ends: [] };
    if (n.child_count) {
      frame.start = n.depth;
      open.push(n);
    }
    frame.active = [...open];
    const next = rows[i + 1];
    while (open.length && (next || !continues)) {
      const parent = open[open.length - 1];
      if (next?.outline.startsWith(`${parent.outline}.`)) break;
      frame.ends.push(parent.depth);
      open.pop();
    }
    frames.set(n.id, frame);
  });
  return frames;
}
type HierarchyFrame = { start?: number; active: Session[]; ends: number[] };
const hierarchyColor = (depth: number) =>
  ["var(--sea)", "var(--delegation-tint)", "var(--muted)"][depth % 3];

function FrameEdges({
  frame,
  edge,
}: {
  frame?: HierarchyFrame;
  edge?: "left" | "right";
}) {
  if (!frame) return null;
  const line = (depth: number, kind: string, bottom?: number) => (
    <i
      key={`${kind}:${depth}`}
      className={`delegation-frame-line ${kind}`}
      data-level={depth + 1}
      style={{
        background: hierarchyColor(depth),
        left: edge === "left" ? depth * 8 : 0,
        right: edge === "right" ? depth * 8 : 0,
        bottom,
      }}
    />
  );
  return (
    <span className="delegation-frame-edges" aria-hidden="true">
      {edge &&
        frame.active.map((parent) => {
          const end = frame.ends.indexOf(parent.depth);
          return (
            <i
              key={parent.id}
              className={`delegation-frame-side ${edge}`}
              data-level={parent.depth + 1}
              style={{
                background: hierarchyColor(parent.depth),
                [edge]: parent.depth * 8,
                bottom: end < 0 ? 0 : (frame.ends.length - end - 1) * 6,
              }}
            />
          );
        })}
      {frame.start != null && line(frame.start, "start")}
      {frame.start != null &&
        line(frame.start, "parent-divider", frame.ends.length * 6)}
      {frame.ends.map((depth, i) =>
        line(depth, "end", (frame.ends.length - i - 1) * 6),
      )}
    </span>
  );
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
          Choose columns. Drag handles to change their order in both families.
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
  headingHost,
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
    [scope, setScope] = useState(initial.scope),
    [direction, setDirection] = useState(initial.direction);
  const [tint, setTint] = useViewState<Tint>("delegation_tint",
    preferences().get("pharos-delegation-tint", "all"),active,
  );
  const [columns, setColumns] = useViewState<string[]>("delegation_columns", () => {
    const prefs = preferences();
    const saved = prefs.get("pharos-delegation-columns", defaultColumns);
    if (prefs.get("pharos-delegation-files-column-added", false)) return saved;
    const next = [...new Set([...saved, "files"])];
    prefs.set("pharos-delegation-files-column-added", true);
    prefs.set("pharos-delegation-columns", next);
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
      setScope(route.scope);
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
      mode: tab === "table" ? mode : "hierarchy",
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
    return mode === "hierarchy" || tab !== "table"
      ? [...data.rows, ...data.ancestors].sort((a, b) => a.order - b.order)
      : data.rows;
  }, [data, mode, tab]);
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
  const frames = useMemo(
    () =>
      mode === "hierarchy"
        ? hierarchyFrames(rows, !!data?.next_cursor)
        : new Map(),
    [rows, data?.next_cursor, mode],
  );
  const current =
    data?.focus.filter((n) => n.conversation_id === conversation) || [];
  const shownSession = current.find((n) => n.id === selected) || current[0];
  const childSessions =
    data?.focus.filter((n) => current.some((c) => c.id === n.parent_id)) || [];
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
    for (const n of childSessions) {
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
  const heading = shownSession && (
    <div
      className="delegation-session-heading"
      data-session={shownSession.id}
    >
      <span className="delegation-session-number">
        Session {shownSession.outline}
      </span>
      <div>
        <strong>{shownSession.label}</strong>
        <small>
          {shownSession.provider} ·{" "}
          {shownSession.model || "model unknown"} ·{" "}
          {shownSession.effort || "unknown"}
        </small>
      </div>
    </div>
  );
  const context =
    contextHost &&
    createPortal(
      <>
        {!headingHost && heading}
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
          {current.length > 0 && (
            <span title="Reconciled usage of this conversation's sessions">
              {formatted(
                current.some((n) => n.self.tokens.value !== null)
                  ? current.reduce(
                      (sum, n) => sum + (n.self.tokens.value || 0),
                      0,
                    )
                  : null,
              )}{" "}
              self tokens
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
                      {n.provider} · {formatted(n.self.tokens.value)} tokens ·{" "}
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
    </div>
  );
  return (
    <>
      {context}
      {headingHost && createPortal(heading, headingHost)}
      {statsHost && createPortal(totals, statsHost)}
      <section className={`delegation-panel${single ? " delegation-single" : ""}`} aria-label={single ? "Conversation views" : "Workspace sessions"}>
        <div className="delegation-controls delegation-toolbar">
          {!single && <span className="delegation-heading">Workspace Sessions</span>}
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
                  metrics={orderedMetrics}
                  shown={shownSession?.id || ""}
                  mode={mode}
                  metric={sort}
                  scope={scope}
                  direction={direction}
                  tint={tint}
                  collapsed={collapsed}
                  toggle={toggle}
                  openNode={openNode}
                  hierarchy={showHierarchy}
                  sortColumn={sortColumn}
                  choose={(metric, scope) => {
                    setSort(metric);
                    setScope(scope);
                    setMode("rank");
                    setDirection("desc");
                    resetPage();
                  }}
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
                            <th rowSpan={2} className="delegation-label-cell">
                              <button
                                onClick={showHierarchy}
                                title="Show the session hierarchy"
                              >
                                Session
                              </button>
                            </th>
                            {specs.length > 0 && (
                              <>
                                <th
                                  colSpan={specs.length}
                                  className="delegation-family"
                                >
                                  Self
                                </th>
                                <th
                                  colSpan={specs.length}
                                  className="delegation-family family-all"
                                >
                                  Self + children
                                </th>
                              </>
                            )}
                            {mode === "hierarchy" && (
                              <th
                                rowSpan={2}
                                className="delegation-border-cell"
                                aria-hidden="true"
                              />
                            )}
                          </tr>
                          <tr>
                            {["self", "all"].flatMap((family) =>
                              specs.map((m, i) => (
                                <th
                                  key={`${family}:${m.id}`}
                                  className={
                                    family === "all" && i === 0
                                      ? "family-all"
                                      : ""
                                  }
                                  title={m.definition}
                                  aria-sort={
                                    mode === "rank" &&
                                    sort === m.id &&
                                    scope === family
                                      ? direction === "desc"
                                        ? "descending"
                                        : "ascending"
                                      : "none"
                                  }
                                >
                                  <button
                                    onClick={() => sortColumn(m.id, family)}
                                    title="Click to sort descending, then ascending, then return to hierarchy"
                                  >
                                    {m.label}
                                    {mode === "rank" &&
                                    sort === m.id &&
                                    scope === family
                                      ? direction === "desc"
                                        ? " ↓"
                                        : " ↑"
                                      : ""}
                                  </button>
                                </th>
                              )),
                            )}
                          </tr>
                        </thead>
                        <tbody>
                          {rows.map((n) => {
                            const ancestor =
                              n.context_only ||
                              !data.rows.some((row) => row.id === n.id) ||
                              !data.matched[n.id];
                            const parent = byID.get(n.parent_id || "");
                            const frame = frames.get(n.id);
                            return (
                              <tr
                                key={n.id} data-session-id={n.id}
                                data-session={n.id}
                                data-depth={n.depth}
                                className={`${ancestor ? "delegation-ancestor" : ""} ${n.id === shownSession?.id ? "delegation-current" : ""} ${frame?.start != null ? "delegation-group-start" : ""} ${frame?.ends.length ? "delegation-group-end" : ""}`}
                                onClick={(event) => {
                                  if (
                                    !(event.target as HTMLElement).closest(
                                      "button, a",
                                    )
                                  )
                                    openNode(n);
                                }}
                                style={
                                  {
                                    "--delegation-level-color": hierarchyColor(
                                      n.depth,
                                    ),
                                    "--delegation-row-top":
                                      frame?.start != null ? "10px" : "6px",
                                    "--delegation-row-bottom": frame?.ends
                                      .length
                                      ? `${6 + frame.ends.length * 6}px`
                                      : "6px",
                                  } as React.CSSProperties
                                }
                              >
                                <td className="delegation-label-cell">
                                  <FrameEdges frame={frame} edge="left" />
                                  <div
                                    className="delegation-node"
                                    style={{
                                      paddingLeft:
                                        mode === "hierarchy"
                                          ? n.depth * 8 + 4
                                          : 0,
                                    }}
                                  >
                                    <button
                                      className="delegation-chevron"
                                      aria-label={`${collapsed.has(n.id) ? "Expand" : "Collapse"} ${n.label}`}
                                      aria-expanded={!collapsed.has(n.id)}
                                      disabled={!n.child_count}
                                      onClick={() => toggle(n)}
                                    >
                                      {n.child_count ? (
                                        <Icon
                                          name={
                                            collapsed.has(n.id)
                                              ? "chevron-right"
                                              : "chevron-down"
                                          }
                                        />
                                      ) : (
                                        "·"
                                      )}
                                    </button>
                                    <span className="delegation-outline">
                                      {n.outline}
                                    </span>
                                    {mode === "hierarchy" && (
                                      <span
                                        className="delegation-level"
                                        title={`Hierarchy level ${n.depth + 1}`}
                                      >
                                        L{n.depth + 1}
                                      </span>
                                    )}
                                    <div>
                                      <button
                                        className="delegation-name"
                                        title={`${n.label}\n${n.provider} · ${n.model} · Effort: ${n.effort || "unknown"}\n${n.id} · ${n.coverage} coverage · ${n.parent_evidence || "root"}${n.external_child ? " · child transcript in another workspace" : ""}`}
                                        aria-current={
                                          n.id === shownSession?.id
                                            ? "true"
                                            : undefined
                                        }
                                        onClick={() => openNode(n)}
                                      >
                                        {n.label}{n.external_child || n.navigation ? " ↗" : ""}
                                      </button>
                                      <small
                                        className="delegation-session-meta"
                                        title={`${n.provider} · ${n.model || "model unknown"}${ancestor ? " · ancestor context" : ""}${n.parent_id && mode !== "hierarchy" ? ` · Parent: ${parent?.label || n.outline.split(".").slice(0, -1).join(".")}` : ""}`}
                                      >
                                        {n.provider} ·{" "}
                                        {n.model || "model unknown"} ·{" "}
                                        {n.effort || "unknown"}
                                        {n.context_only ? " · Parent context" : ""}
                                        <SessionAge start={n.start} prefix=" · " />
                                      </small>
                                    </div>
                                  </div>
                                </td>
                                {["self", "all"].flatMap((family) =>
                                  specs.map((m, i) => {
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
                                        className={
                                          family === "all" && i === 0
                                            ? "family-all"
                                            : ""
                                        }
                                        style={{
                                          background: strength
                                            ? `color-mix(in srgb, var(--delegation-tint) ${strength * 34}%, var(--panel))`
                                            : undefined,
                                        }}
                                        title={cellTitle(metric, m)}
                                      >
                                        <FrameEdges frame={frame} />
                                        {formatted(metric.value, m.unit)}
                                        {metric.quality === "partial" &&
                                          metric.value != null && (
                                            <span className="delegation-partial">
                                              +
                                            </span>
                                          )}
                                      </td>
                                    );
                                  }),
                                )}
                                {mode === "hierarchy" && (
                                  <td
                                    className="delegation-border-cell"
                                    aria-hidden="true"
                                    style={{
                                      minWidth: Math.max(
                                        12,
                                        ...rows.map((row) => row.depth * 8 + 8),
                                      ),
                                    }}
                                  >
                                    <FrameEdges frame={frame} edge="right" />
                                  </td>
                                )}
                              </tr>
                            );
                          })}
                        </tbody>
                      </table>
                      {!rows.length && <p>No sessions match these filters.</p>}
                    </div>
                  </div>
                </>
              ) : range && data.start != null && data.end != null ? (
                <ParallelActivity
                  timeline
                  single={single}
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
                  selected={shownSession?.id || ""}
                  openNode={(node, message, sourceConversation) => { if (single) setReaderView("conversation"); openNode(node, message, sourceConversation); }}
                />
              ) : (
                <p>No retained timestamps are available for a timeline.</p>
              )}
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
              {!single && <ViewDisclosure viewKey="delegation:definitions" className="delegation-definitions" summary="Metric definitions & evidence">
                <p>
                  Self is one execution. Self + children includes distinct
                  ownership descendants. Workspace totals count each canonical
                  execution once. Unknown values are —; + denotes partial
                  coverage. Last evidence is not proof of completion. Source
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
          {!data && !error && <p>Loading session analysis…</p>}
        </div>
      </section>
    </>
  );
}

function InboxSessions({ rows, data, metrics, shown, mode, metric, scope, direction, tint, collapsed, toggle, openNode, hierarchy, sortColumn, choose }: {
  rows: Session[];
  data: Analysis;
  metrics: Spec[];
  shown: string;
  mode: string;
  metric: string;
  scope: string;
  direction: string;
  tint: Tint;
  collapsed: Set<string>;
  toggle: (node: Session) => void;
  openNode: (node: Session) => void;
  hierarchy: () => void;
  sortColumn: (metric: string, scope: string) => void;
  choose: (metric: string, scope: string) => void;
}) {
  const spec = metrics.find(m => m.id === metric) || metrics[0];
  const family = scope === "self" ? "self" : "all";
  const ranked = mode === "rank";
  const frames = ranked ? new Map<string, HierarchyFrame>() : hierarchyFrames(rows, !!data.next_cursor);
  const metricInset = ranked ? 8 : Math.max(8, ...rows.map(row => row.depth * 8 + 8));
  return <div className="delegation-inbox-list">
    <div className="delegation-inbox-controls">
      <button onClick={hierarchy} aria-pressed={!ranked}>Hierarchy</button>
      <select aria-label="Session comparison metric" value={`${family}:${spec.id}`} onChange={event => {
        const [scope, id] = event.target.value.split(":");
        choose(id, scope);
      }}>
        {["self", "all"].map(family => <optgroup key={family} label={family === "self" ? "Self" : "Self + children"}>
          {metrics.map(spec => <option key={spec.id} value={`${family}:${spec.id}`}>{family === "self" ? "Self" : "Self + children"} · {spec.label}</option>)}
        </optgroup>)}
      </select>
      <button onClick={() => sortColumn(spec.id, family)} aria-label={`Sort ${spec.label}${ranked ? `, currently ${direction === "desc" ? "descending" : "ascending"}` : ""}`} title="Descending → ascending → hierarchy">{ranked ? direction === "desc" ? "↓" : "↑" : "↕"}</button>
    </div>
    <div className="delegation-inbox-heading"><span>{ranked ? "Metric ranking" : "Session hierarchy"}</span><span>{spec.label}</span></div>
    {rows.map(n => {
      const value = (family === "self" ? n.self : n.all)[spec.id];
      const maximum = data.maxima[family]?.[spec.id] || 0;
      const strength = tint !== "off" && (tint !== "sort" || ranked) && value?.value != null ? tintStrength(value.value, maximum) : 0;
      const frame = frames.get(n.id);
      return <div key={n.id} data-session-id={n.id} data-session={n.id} data-depth={n.depth} className={`delegation-inbox-row ${n.context_only ? "delegation-ancestor" : ""} ${n.id === shown ? "current" : ""}`} onClick={event => { if (!(event.target as HTMLElement).closest("button")) openNode(n); }} style={{ paddingLeft: (ranked ? 0 : Math.min(n.depth, 8)) * 10 + 10, paddingTop: frame?.start != null ? 5 : undefined, paddingBottom: frame?.ends.length ? frame.ends.length * 6 + 3 : undefined }}>
        <FrameEdges frame={frame} edge="left" />
        <FrameEdges frame={frame} edge="right" />
        <button className="delegation-chevron" aria-label={`${collapsed.has(n.id) ? "Expand" : "Collapse"} ${n.label}`} aria-expanded={!collapsed.has(n.id)} disabled={!n.child_count || ranked} onClick={() => toggle(n)}>{n.child_count && !ranked ? <Icon name={collapsed.has(n.id) ? "chevron-right" : "chevron-down"} /> : "·"}</button>
        <button className="delegation-name" onClick={() => openNode(n)} title={`${n.outline} · ${n.label}\n${n.provider} · ${n.model} · ${n.effort || "unknown"}`} aria-current={n.id === shown ? "true" : undefined}>
          <span><b>{n.outline}</b> {n.label}{n.external_child || n.navigation ? " ↗" : ""}</span>
          <small>{n.provider} · {n.model || "unknown model"} · {n.effort || "unknown"}{n.context_only ? " · Parent context" : ""}</small>
        </button>
        <div className="delegation-inbox-values" style={{ marginRight: metricInset }}>
          <span className="delegation-inbox-metric" title={value ? cellTitle(value, spec) : "No retained evidence"} style={strength ? { background: `color-mix(in srgb, var(--delegation-tint) ${strength * 28}%, transparent)` } : undefined}>{formatted(value?.value, spec.unit)}{value?.quality === "partial" ? "+" : ""}</span>
          <SessionAge start={n.start} />
        </div>
      </div>;
    })}
    {!rows.length && <p>No retained sessions.</p>}
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
}: {
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
  const percentage = (time: number) => ((time - full[0]) / width) * 100;
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
      at: timelinePointerTime(event.clientX,rect,full),
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
    d.draft = timelineDragRange(d.mode,d.initial,full,d.at,timelinePointerTime(event.clientX,d.rect,full));
    setDraft(d.draft);
  }
  function up(event: React.PointerEvent) {
    const d=drag.current;
    if (!d || d.pointer !== event.pointerId) return;
    if(d.draft||Math.abs(event.clientX-d.x)>=4)change(timelineDragRange(d.mode,d.initial,full,d.at,timelinePointerTime(event.clientX,d.rect,full)));
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
      change(timelineDragRange(handle,range,full,anchor,anchor+delta));
    }
  }
  return (
    <div className="delegation-overview-wrap" ref={element=>{if(element)element.inert=readOnly;}}>
      <div className="delegation-overview-label">
        <strong>{whole?label:`Showing ${duration(view[1]-view[0])} of ${duration(width)}`}</strong>
        <small>
          {pending?"Loading this window…":"drag to zoom and pan"}
        </small>
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
        <div className="delegation-histogram">
          {density.map((count, i) => (
            <i key={i} style={{ height: `${(count / maxDensity) * 100}%` }} />
          ))}
        </div>
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
  timeline=false,single=false,previewOnly=false,openOverview,
}: {
  previewOnly?:boolean;openOverview?:()=>void;
  timeline?:boolean;single?:boolean;
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
  const actors=useMemo(()=>timeline?splitTimelineCallers(nativeLabels.messages,rows):{caller:[],messages:labelData?.messages??new Map()},[timeline,nativeLabels,rows,labelData]);
  const allMessages=useMemo(()=>[...nativeLabels.messages.values()].flat(),[nativeLabels]);
  const [timelineDisclosures]=useViewState<Record<string,boolean>>("disclosures",{},timeline);
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
    const at =timelinePointerTime(event.clientX,rect,range);
    gesture.current = {
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
    const now =timelinePointerTime(event.clientX,g.rect,g.initial);
    g.draft = timelineDragRange('select',g.initial,g.initial,g.at,now);
    setBrush(g.draft);
  }
  function finishBrush(event?: React.PointerEvent<HTMLDivElement>) {
    const g = gesture.current;
    if (!g || (event && g.pointer !== event.pointerId)) return;
    if(event?.type==='pointerup'&&(g.draft||Math.abs(event.clientX-g.x)>=4))g.draft=timelineDragRange('select',g.initial,g.initial,g.at,timelinePointerTime(event.clientX,g.rect,g.initial));
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
  const ticks = timelineTickLabels(range,Math.max(2,Math.min(6,Math.floor((clockWidth||350)/100))));
  const lanes = new Map(visibleActivity?.lanes.map((l) => [l.session, l]));
  const sourceHref=(node:Session,item:RibbonObservation)=>ribbonSourceURL(item,{workspace:node.workspace_id,conversation:node.conversation_id,session:node.id},location.href);
  return (
    <div className={`delegation-parallel ${previewOnly?"is-preview":""} ${recursive?"is-recursive":""} ${timeline?"is-timeline":""}`} ref={track} style={timeline?columns.style:undefined}>
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
      />
      {timeline?<TimelineLegend messages={legend.messages} origins={legend.origins} taxonomy={nativeLabels.taxonomy} tools={Object.fromEntries(Object.entries(categories).filter(([id])=>legend.tools.includes(id)))} error={legend.error}/>:<div className="delegation-legend">
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
          <span className="lt-column-heading">Events{columns.handle('events')}</span>
          <div ref={clock} className="lt-clock-heading" {...brushHandlers}>{ticks.map((tick,i)=><small key={i} style={{left:`${i/(ticks.length-1)*100}%`}}><span className="delegation-tick-date">{tick.date}</span><time dateTime={new Date(tick.at).toISOString()} title={localTime(tick.at)}>{tick.time}</time></small>)}</div>
        </div>:<div className="delegation-axis">
          <span className={recursive?"lt-axis-names":undefined}>{recursive?<><span>Session</span><span>Events</span></>:"Session"}</span>
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
          <RecursiveSessionRibbon collapsed={previewOnly} caller sessionID={`caller:${data.rows[0]?.workspace_id??endpoint}`} depth={0} name="Caller" metadata="Human & supplied input" messages={actors.caller} boundaryMessages={allMessages} boundaryMarks={visibleActivity?.lanes.flatMap(lane=>lane.marks)??[]} marks={[]} inferred={[]} taxonomy={nativeLabels.taxonomy} range={range} detail brushHandlers={brushHandlers} preview={brush}
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
            <RecursiveSessionRibbon collapsed={previewOnly} sessionID={n.id} depth={n.depth} outline={n.outline} branch={sessionBranches.get(n.id)} name={n.label} metadata={`${n.provider} · ${n.model||"model not recorded"}`} messages={actors.messages.get(n.id)??[]} boundaryMessages={timeline?labelData.messages.get(n.id):undefined} marks={lane?.marks??[]} inferred={timeline?[]:lane?.model_inferred??[]} taxonomy={labelData.taxonomy} range={range} detail brushHandlers={brushHandlers} preview={brush}
              openSession={timeline?()=>openNode(n):undefined}
              sourceHref={timeline?item=>sourceHref(n,item):undefined}
              foldControl={!n.child_count?null:previewOnly?<span className="lt-preview-fold"><Icon name="tree"/></span>:<button type="button" className="delegation-chevron" aria-label={`${collapsed.has(n.id)?"Expand":"Collapse"} child sessions of ${n.label}`} onClick={()=>toggle(n)}><Icon name={collapsed.has(n.id)?"chevron-right":"chevron-down"}/></button>}
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
                  aria-label={`${collapsed.has(n.id) ? "Expand" : "Collapse"} ${n.label}`}
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
                  title={`${n.outline} · ${n.label}\n${n.provider} · ${n.model} · Effort: ${n.effort || "unknown"}`}
                  className="delegation-name"
                  onClick={() => openNode(n)}
                >
                  <strong>
                    {n.outline} · {n.label}
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
        <ViewDisclosure viewKey="delegation:definitions" className="delegation-definitions" summary="Selected session · inferred evidence episodes">
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
      <small>Session {node.outline} · {node.label}</small>
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
