import React, { useEffect, useRef, useState } from "react";
import { chartSeriesStyle } from "./chart-colors";
import {
  thinkingBrush,
  thinkingCount,
  thinkingDuration,
  thinkingPercentiles,
  thinkingStyle,
  thinkingWindow,
  type ThinkingResponse,
  type ThinkingState,
  type ThinkingGroup,
  type ThinkingTrace,
  type ThinkingCompaction,
} from "./thinking-model";
const clipLabel = (s: string, width: number) =>
  s.length * 5.8 > width
    ? s.slice(0, Math.max(4, Math.floor(width / 5.8) - 1)) + "…"
    : s;
const fmt = (n: number) =>
  n.toLocaleString(undefined, { maximumFractionDigits: 1 });
const line = (pts: ([number, number] | null)[]) =>
  pts.reduce(
    (s, p, i) =>
      s + (p ? `${!i || !pts[i - 1] ? "M" : "L"}${p[0]},${p[1]} ` : ""),
    "",
  );
const tint = (n: number) =>
  `color-mix(in srgb,var(--sea) ${Math.round(Math.max(0, Math.min(1, n)) * 88)}%,var(--panel))`;
function useWidth() {
  const ref = useRef<HTMLDivElement>(null),
    [width, setWidth] = useState(600);
  useEffect(() => {
    const o = new ResizeObserver(([e]) =>
      setWidth(Math.max(270, e.contentRect.width)),
    );
    if (ref.current) o.observe(ref.current);
    return () => o.disconnect();
  }, []);
  return { ref, width };
}
export function ThinkingFigure({
  title,
  children,
  note,
}: {
  title: string;
  children: (width: number) => React.ReactNode;
  note?: React.ReactNode;
}) {
  const { ref, width } = useWidth(),
    [visible, setVisible] = useState(false);
  useEffect(() => {
    const observer = new IntersectionObserver(
      (entries) => setVisible(entries.some((e) => e.isIntersecting)),
      { rootMargin: "300px" },
    );
    if (ref.current) observer.observe(ref.current);
    return () => observer.disconnect();
  }, []);
  return (
    <figure className="thinking-figure" ref={ref}>
      <figcaption>{title}</figcaption>
      {visible ? (
        children(width)
      ) : (
        <div
          className="thinking-chart-placeholder"
          aria-label="Chart renders when scrolled into view"
        />
      )}
      {note ? <p className="usage-dashboard-note">{note}</p> : null}
    </figure>
  );
}
function Brush({
  width,
  height,
  x = 48,
  y = 20,
  plotWidth,
  plotHeight,
  count,
  vertical = false,
  onBrush,
  children,
}: {
  width: number;
  height: number;
  x?: number;
  y?: number;
  plotWidth: number;
  plotHeight: number;
  count: number;
  vertical?: boolean;
  onBrush?: (a: number, b: number) => void;
  children: React.ReactNode;
}) {
  const [drag, setDrag] = useState<[number, number] | null>(null),
    start = useRef<number | null>(null);
  const coordinate = (e: React.PointerEvent<SVGSVGElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    return Math.max(
      0,
      Math.min(
        vertical ? plotHeight : plotWidth,
        vertical
          ? ((e.clientY - r.top) * height) / r.height - y
          : ((e.clientX - r.left) * width) / r.width - x,
      ),
    );
  };
  return (
    <svg
      className={onBrush ? "thinking-svg thinking-brush" : "thinking-svg"}
      viewBox={`0 0 ${width} ${height}`}
      role="img"
      aria-label={
        onBrush
          ? "Chart. Drag to select dates; use Range for keyboard date selection."
          : "Chart"
      }
      onPointerDown={(e) => {
        if (!onBrush || e.button !== 0) return;
        const p = coordinate(e);
        start.current = p;
        setDrag([p, p]);
        e.currentTarget.setPointerCapture(e.pointerId);
      }}
      onPointerMove={(e) => {
        if (start.current !== null) setDrag([start.current, coordinate(e)]);
      }}
      onPointerCancel={() => {
        start.current = null;
        setDrag(null);
      }}
      onPointerUp={(e) => {
        if (start.current === null) return;
        const a = start.current,
          b = coordinate(e);
        start.current = null;
        setDrag(null);
        if (Math.abs(a - b) > 4)
          onBrush?.(
            (a / (vertical ? plotHeight : plotWidth)) * count,
            (b / (vertical ? plotHeight : plotWidth)) * count,
          );
      }}
    >
      {children}
      {drag ? (
        <rect
          x={vertical ? x : x + Math.min(...drag)}
          y={vertical ? y + Math.min(...drag) : y}
          width={vertical ? plotWidth : Math.abs(drag[1] - drag[0])}
          height={vertical ? Math.abs(drag[1] - drag[0]) : plotHeight}
          fill="var(--sea)"
          opacity=".17"
          pointerEvents="none"
        />
      ) : null}
    </svg>
  );
}
const ticks = (items: string[], max = 6) =>
  items
    .map((s, i) => ({ s, i }))
    .filter(
      (_, i) =>
        i % Math.max(1, Math.ceil(items.length / max)) === 0 ||
        i === items.length - 1,
    );
function Axis({
  width,
  height,
  x = 48,
  y = 20,
  w,
  h,
  yTicks,
  xTicks,
}: {
  width: number;
  height: number;
  x?: number;
  y?: number;
  w: number;
  h: number;
  yTicks: { value: number; label: string }[];
  xTicks: { value: number; label: string }[];
}) {
  return (
    <g className="thinking-axis">
      <path d={`M${x},${y}V${y + h}H${x + w}`} fill="none" />
      {yTicks.map((t, i) => (
        <g key={i}>
          <line x1={x} x2={x + w} y1={t.value} y2={t.value} />
          <text x={x - 7} y={t.value + 3} textAnchor="end">
            {t.label}
          </text>
        </g>
      ))}
      {xTicks.map((t, i) => (
        <text key={i} x={t.value} y={height - 18} textAnchor="middle">
          {t.label}
        </text>
      ))}
    </g>
  );
}
export function ThinkingDistribution({
  data,
  state,
  onChange,
}: {
  data: ThinkingResponse;
  state: ThinkingState;
  onChange: (next: Partial<ThinkingState>, brush?: boolean) => void;
}) {
  const maximum = Math.max(
    1,
    ...data.groups.flatMap((g) =>
      g.counts.map((n) =>
        state.normalization === "share" ? n / Math.max(1, g.stats.n) : n,
      ),
    ),
  );
  const plotGroups = data.groups;
  return (
    <div className="thinking-facets">
      {plotGroups.map((g) => (
        <ThinkingFigure
          key={g.key}
          title={g.label}
          note={`${thinkingCount(g.stats.n)} observations / ${thinkingCount(g.Retained)} retained · ${g.Conversations} conversations${g.stats.n < 30 ? " · sparse sample" : ""}`}
        >
          {(width) => {
            const x = 48,
              y = 20,
              w = width - 68,
              h = 260,
              height = 335,
              style = thinkingStyle(g, state.groupBy);
            const logY = (v: number) =>
              y +
              h -
              ((Math.log10(v) - Math.log10(0.1)) /
                (Math.log10(600) - Math.log10(0.1))) *
                h;
            if (state.view === "time") {
              const periods = data.periods,
                count = periods.length,
                step = w / Math.max(1, count),
                countMax = data.groups.reduce(
                  (m, g) =>
                    Object.values(g.periods).reduce(
                      (n, c) => Math.max(n, ...c.counts),
                      m,
                    ),
                  1,
                );
              return (
                <Brush
                  {...{
                    width,
                    height,
                    x,
                    y,
                    plotWidth: w,
                    plotHeight: h,
                    count,
                  }}
                  onBrush={(a, b) => {
                    const next = thinkingBrush(data, state, a, b);
                    if (next) onChange(next, true);
                  }}
                >
                  <Axis
                    {...{ width, height, x, y, w, h }}
                    yTicks={[0.1, 1, 10, 100, 600].map((v) => ({
                      value: logY(v),
                      label: thinkingDuration(v),
                    }))}
                    xTicks={ticks(periods).map(({ s, i }) => ({
                      value: x + (i + 0.5) * step,
                      label: s.includes("T")
                        ? new Date(s).toLocaleString(undefined, {
                            month: "short",
                            day: "numeric",
                            hour: "numeric",
                          })
                        : s.slice(5),
                    }))}
                  />
                  {periods.flatMap((p, i) => {
                    const c = g.periods[p];
                    if (!c || c.stats.n < state.minimum) return [];
                    return c.counts.map((n, b) => (
                      <rect
                        key={p + "|" + b}
                        x={x + i * step}
                        y={logY(data.edges[b + 1])}
                        width={Math.max(0.5, step - 0.6)}
                        height={Math.max(
                          0.5,
                          logY(data.edges[b]) - logY(data.edges[b + 1]) - 0.4,
                        )}
                        fill={tint(
                          state.normalization === "share"
                            ? n / Math.max(1, c.stats.n)
                            : n / countMax,
                        )}
                      >
                        <title>
                          {g.label} · {p} · {thinkingDuration(data.edges[b])}–
                          {thinkingDuration(data.edges[b + 1])}: {n} /{" "}
                          {c.stats.n} usable gaps · {c.conversations}{" "}
                          conversations
                        </title>
                      </rect>
                    ));
                  })}
                  {thinkingPercentiles
                    .filter((r) => !state.hiddenPercentiles.includes(r.key))
                    .map((r) => (
                      <path
                        key={r.key}
                        d={line(
                          periods.map((p, i) => {
                            const c = g.periods[p],
                              v = c?.stats[r.key];
                            return c &&
                              c.stats.n >= Math.max(state.minimum, r.minimum) &&
                              c.conversations >= 5 &&
                              v !== null
                              ? [x + (i + 0.5) * step, logY(v)]
                              : null;
                          }),
                        )}
                        fill="none"
                        stroke={r.color}
                        strokeWidth="2"
                        strokeDasharray={r.key === "median" ? undefined : "2 5"}
                      />
                    ))}
                </Brush>
              );
            }
            if (state.view === "calendar") {
              const periods = data.calendarPeriods,
                count = periods.length,
                ph = Math.min(560, Math.max(210, count * 6)),
                step = ph / Math.max(1, count),
                ch = ph + 95,
                cells = new Map(
                  g.calendar.map((c) => [c.day + "|" + c.hour, c]),
                ),
                maxRaw = data.groups.reduce(
                  (m, g) =>
                    g.calendar.reduce((n, c) => Math.max(n, c.Raw ?? 0), m),
                  1,
                ),
                cx = 60,
                cw = width - 85;
              return (
                <Brush
                  width={width}
                  height={ch}
                  x={cx}
                  y={y}
                  plotWidth={cw}
                  plotHeight={ph}
                  count={count}
                  vertical
                  onBrush={(a, b) => {
                    const next = thinkingBrush(data, state, a, b, true);
                    if (next) onChange(next, true);
                  }}
                >
                  {periods.flatMap((p, i) =>
                    Array.from({ length: 24 }, (_, hour) => {
                      const c = cells.get(p + "|" + hour),
                        v =
                          state.calendarMetric === "ratio" ? c?.Ratio : c?.Raw;
                      return v == null ? null : (
                        <rect
                          key={p + "|" + hour}
                          x={cx + (hour * cw) / 24}
                          y={y + i * step}
                          width={Math.max(1, cw / 24 - 0.5)}
                          height={Math.max(0.5, step - 0.5)}
                          fill={
                            state.calendarMetric === "ratio"
                              ? `color-mix(in srgb,${v >= 1 ? "var(--warn)" : "var(--sea)"} ${Math.min(90, Math.abs(v - 1) * 160)}%,var(--panel))`
                              : tint(v / maxRaw)
                          }
                        >
                          <title>
                            {p} · {hour}:00 {data.timezone} ·{" "}
                            {state.calendarMetric === "ratio"
                              ? v.toFixed(2) + "×"
                              : thinkingDuration(v)}{" "}
                            ·{" "}
                            {state.calendarMetric === "ratio"
                              ? c?.Adjusted
                              : c?.N}{" "}
                            requests
                          </title>
                        </rect>
                      );
                    }),
                  )}
                  <g className="thinking-axis">
                    {ticks(periods, 10).map(({ s, i }) => (
                      <text
                        key={s}
                        x={cx - 6}
                        y={y + (i + 0.65) * step}
                        textAnchor="end"
                      >
                        {s.length === 7 ? s : s.slice(5)}
                      </text>
                    ))}
                    {[0, 3, 6, 9, 12, 15, 18, 21].map((hour) => (
                      <text
                        key={hour}
                        x={cx + ((hour + 0.5) * cw) / 24}
                        y={y + ph + 20}
                        textAnchor="middle"
                      >
                        {String(hour).padStart(2, "0")}
                      </text>
                    ))}
                    <text x={cx + cw / 2} y={ch - 12} textAnchor="middle">
                      Local hour · {data.timezone}
                    </text>
                  </g>
                </Brush>
              );
            }
            const max = Math.max(
                ...data.groups.flatMap((g) =>
                  g.counts.map((n) =>
                    state.normalization === "share"
                      ? n / Math.max(1, g.stats.n)
                      : n,
                  ),
                ),
                0.001,
              ),
              step = w / g.counts.length;
            return (
              <svg
                viewBox={`0 0 ${width} ${height}`}
                className="thinking-svg"
                role="img"
                aria-label={g.label + " latency histogram"}
              >
                <Axis
                  {...{ width, height, x, y, w, h }}
                  yTicks={[0, 0.5, 1].map((v) => ({
                    value: y + h - v * h,
                    label:
                      state.normalization === "share"
                        ? Math.round(v * max * 100) + "%"
                        : fmt(v * max),
                  }))}
                  xTicks={ticks(
                    data.edges.slice(0, -1).map(thinkingDuration),
                    width < 400 ? 4 : 7,
                  ).map(({ s, i }) => ({
                    value: x + (i + 0.5) * step,
                    label: s,
                  }))}
                />
                {g.counts.map((n, i) => {
                  const v =
                    state.normalization === "share"
                      ? n / Math.max(1, g.stats.n)
                      : n;
                  return (
                    <rect
                      key={i}
                      x={x + i * step + 1}
                      y={y + h - (v / max) * h}
                      width={Math.max(0.5, step - 2)}
                      height={(v / max) * h}
                      fill={style.color}
                    >
                      <title>
                        {thinkingDuration(data.edges[i])}–
                        {thinkingDuration(data.edges[i + 1])}: {n} requests (
                        {((100 * n) / Math.max(1, g.stats.n)).toFixed(1)}%)
                      </title>
                    </rect>
                  );
                })}
              </svg>
            );
          }}
        </ThinkingFigure>
      ))}
    </div>
  );
}
export function ThinkingBoxes({
  data,
  state,
}: {
  data: ThinkingResponse;
  state: ThinkingState;
}) {
  const reasoning = state.view === "reasoning",
    byModel = state.groupBy === "model_effort",
    panels = new Map<string, ThinkingGroup[]>();
  for (const g of data.groups) {
    const name = byModel ? g.model : "All selected groups";
    panels.set(name, [...(panels.get(name) ?? []), g]);
  }
  const max = Math.max(1, ...data.groups.map((g) => g.stats.high ?? 0));
  return (
    <div className="thinking-facets">
      {[...panels].map(([name, groups]) => (
        <ThinkingFigure key={name} title={name}>
          {(width) => {
            const height = Math.max(220, groups.length * 42 + 55),
              x = Math.min(width * 0.46, 170),
              w = width - x - 22,
              y = 20,
              h = height - 55,
              scale = (v: number) =>
                x + (reasoning ? Math.log1p(v) / Math.log1p(max) : v / max) * w,
              supported = (g: ThinkingGroup) =>
                reasoning
                  ? g.Retained >= 30 &&
                    g.RetainedConversations >= 3 &&
                    g.stats.n > 0
                  : g.stats.n >= 30 && g.Conversations >= 3;
            return (
              <svg
                viewBox={`0 0 ${width} ${height}`}
                className="thinking-svg"
                role="img"
                aria-label={
                  reasoning
                    ? "Reported reasoning token distributions"
                    : "Observed wait distributions"
                }
              >
                <Axis
                  {...{ width, height, x, y, w, h }}
                  yTicks={[]}
                  xTicks={[0, 0.5, 1].map((v) => ({
                    value: x + v * w,
                    label: reasoning
                      ? fmt(Math.expm1(v * Math.log1p(max)))
                      : thinkingDuration(v * max),
                  }))}
                />
                {groups.map((g, i) => {
                  const cy = y + ((i + 0.5) * h) / groups.length,
                    st = g.stats,
                    color = thinkingStyle(g, state.groupBy).color;
                  return (
                    <g key={g.key}>
                      <text
                        className="thinking-label"
                        x={x - 9}
                        y={cy + 4}
                        textAnchor="end"
                      >
                        {clipLabel(byModel ? g.effort : g.label, x - 14)}
                        <title>{byModel ? g.effort : g.label}</title>
                      </text>
                      {supported(g) &&
                      st.low !== null &&
                      st.high !== null &&
                      st.q1 !== null &&
                      st.q3 !== null &&
                      st.median !== null ? (
                        <g opacity={st.n < 30 ? 0.4 : 1}>
                          <title>
                            {g.label} · median{" "}
                            {reasoning
                              ? fmt(st.median) + " tokens"
                              : thinkingDuration(st.median)}{" "}
                            · {st.n} observations / {g.Retained} retained
                          </title>
                          <line
                            x1={scale(st.low)}
                            x2={scale(st.high)}
                            y1={cy}
                            y2={cy}
                            stroke={color}
                          />
                          <rect
                            x={scale(st.q1)}
                            y={cy - 9}
                            width={Math.max(1, scale(st.q3) - scale(st.q1))}
                            height="18"
                            fill={color}
                            fillOpacity=".25"
                            stroke={color}
                          />
                          <line
                            x1={scale(st.median)}
                            x2={scale(st.median)}
                            y1={cy - 9}
                            y2={cy + 9}
                            stroke={color}
                          />
                        </g>
                      ) : (
                        <text className="thinking-label" x={x + 4} y={cy + 4}>
                          Below sample threshold
                        </text>
                      )}
                    </g>
                  );
                })}
                <text
                  className="thinking-label"
                  x={x + w / 2}
                  y={height - 2}
                  textAnchor="middle"
                >
                  {reasoning
                    ? "Reported reasoning tokens · log1p scale"
                    : "Observed gap · seconds"}
                </text>
              </svg>
            );
          }}
        </ThinkingFigure>
      ))}
    </div>
  );
}
export function ThinkingHourly({
  data,
  state,
}: {
  data: ThinkingResponse;
  state: ThinkingState;
}) {
  const max = Math.max(
    1.5,
    ...data.groups.flatMap((g) => g.bands.map((b) => b.High ?? 0)),
  );
  return (
    <div className="thinking-facets">
      {data.groups.map((g) => (
        <ThinkingFigure
          key={g.key}
          title={g.label}
          note={
            g.bands.some((b) => b.Ratio !== null)
              ? undefined
              : "Fewer than four qualifying modeled days."
          }
        >
          {(width) => {
            const height = 270,
              x = 44,
              y = 20,
              w = width - 60,
              h = 200,
              ys = (v: number) => y + h - (v / max) * h,
              color = thinkingStyle(g, state.groupBy).color;
            return (
              <svg
                viewBox={`0 0 ${width} ${height}`}
                className="thinking-svg"
                role="img"
                aria-label={g.label + " day-balanced hourly profile"}
              >
                <Axis
                  {...{ width, height, x, y, w, h }}
                  yTicks={[0, 1, max].map((v) => ({
                    value: ys(v),
                    label: v.toFixed(1) + "×",
                  }))}
                  xTicks={g.bands.map((b, i) => ({
                    value: x + ((i + 0.5) * w) / 8,
                    label: String(b.hour).padStart(2, "0"),
                  }))}
                />
                <path
                  d={line(
                    g.bands.map((b, i) =>
                      b.Ratio === null
                        ? null
                        : [x + ((i + 0.5) * w) / 8, ys(b.Ratio)],
                    ),
                  )}
                  fill="none"
                  stroke={color}
                  strokeWidth="2"
                />
                {g.bands.map((b, i) =>
                  b.Ratio === null ? null : (
                    <g key={i}>
                      <title>
                        {thinkingWindow(b.hour)} · {b.Ratio.toFixed(2)}× ·{" "}
                        {b.days} qualifying days · {b.n} modeled gaps
                      </title>
                      <line
                        x1={x + ((i + 0.5) * w) / 8}
                        x2={x + ((i + 0.5) * w) / 8}
                        y1={ys(b.Low!)}
                        y2={ys(b.High!)}
                        stroke={color}
                      />
                      <circle
                        cx={x + ((i + 0.5) * w) / 8}
                        cy={ys(b.Ratio)}
                        r="3"
                        fill={color}
                      />
                    </g>
                  ),
                )}
                <text
                  className="thinking-label"
                  x={x + w / 2}
                  y={height - 1}
                  textAnchor="middle"
                >
                  Three-hour window starting at · {data.timezone}
                </text>
              </svg>
            );
          }}
        </ThinkingFigure>
      ))}
    </div>
  );
}
export function ThinkingCDF({ groups }: { groups: ThinkingGroup[] }) {
  return (
    <ThinkingFigure
      title="How long are the waits?"
      note="Curves require 100 usable gaps from five conversations. Each curve is an individual-request quantile function at 1% steps."
    >
      {(width) => {
        const height = 315,
          x = 45,
          y = 20,
          w = width - 65,
          h = 235,
          xs = (v: number) =>
            x + ((Math.log10(Math.max(0.1, v)) + 1) / Math.log10(6000)) * w;
        return (
          <>
            <svg
              viewBox={`0 0 ${width} ${height}`}
              className="thinking-svg"
              role="img"
              aria-label="Cumulative wait distribution"
            >
              <Axis
                {...{ width, height, x, y, w, h }}
                yTicks={[0, 0.5, 1].map((v) => ({
                  value: y + h - v * h,
                  label: Math.round(v * 100) + "%",
                }))}
                xTicks={[0.1, 1, 10, 100, 600].map((v) => ({
                  value: xs(v),
                  label: thinkingDuration(v),
                }))}
              />
              {groups
                .filter((g) => g.Timed >= 100 && g.Conversations >= 5)
                .map((g) => (
                  <path
                    key={g.key}
                    d={line(
                      (g.cdf ?? []).map(([a, b]) => [xs(a), y + h - b * h]),
                    )}
                    fill="none"
                    stroke={chartSeriesStyle("model_family", g.label).color}
                    strokeWidth="2"
                  >
                    <title>
                      {g.label} · {g.Timed} usable gaps
                    </title>
                  </path>
                ))}
            </svg>
            <div className="thinking-legend">
              {groups
                .filter((g) => g.Timed >= 100 && g.Conversations >= 5)
                .map((g) => (
                  <span
                    key={g.key}
                    style={{
                      color: chartSeriesStyle("model_family", g.label).color,
                    }}
                  >
                    ● {g.label}
                  </span>
                ))}
            </div>
          </>
        );
      }}
    </ThinkingFigure>
  );
}
const median = (values: number[]) => {
  const sorted = [...values].sort((a, b) => a - b);
  return sorted.length
    ? (sorted[Math.floor((sorted.length - 1) / 2)] + sorted[Math.ceil((sorted.length - 1) / 2)]) / 2
    : 0;
};
export function ThinkingCompactions({
  events,
}: {
  events: ThinkingCompaction[];
}) {
  const models = [...new Set(events.map((e) => e.Model))];
  return (
    <div className="thinking-compaction">
      <ThinkingFigure title="Wait after / before">
        {(width) => {
          const height = Math.max(260, models.length * 42 + 65),
            x = Math.min(width * 0.42, 155),
            w = width - x - 20,
            h = height - 65,
            y = 15;
          return (
            <svg
              viewBox={`0 0 ${width} ${height}`}
              className="thinking-svg"
              role="img"
              aria-label="Compaction wait ratios on a fixed linear zero to one axis"
            >
              <Axis
                {...{ width, height, x, y, w, h }}
                yTicks={[]}
                xTicks={[0, 0.25, 0.5, 0.75, 1].map((v) => ({
                  value: x + v * w,
                  label: v + "×",
                }))}
              />
              {models.map((m, i) => {
                const ratios = events
                    .filter((e) => e.Model === m)
                    .map((e) => e.Gap)
                    .sort((a, b) => a - b),
                  q = (p: number) => {
                    const at = p * (ratios.length - 1),
                      lo = Math.floor(at);
                    return (
                      ratios[lo] +
                      (ratios[Math.ceil(at)] - ratios[lo]) * (at - lo)
                    );
                  },
                  cy = y + ((i + 0.5) * h) / models.length,
                  color = chartSeriesStyle("model_family", m).color;
                return (
                  <g key={m}>
                    <text
                      className="thinking-label"
                      x={x - 8}
                      y={cy + 3}
                      textAnchor="end"
                    >
                      {clipLabel(m, x - 14)}
                      <title>{m}</title>
                    </text>
                    <line
                      x1={x + Math.min(1, q(0.1)) * w}
                      x2={x + Math.min(1, q(0.9)) * w}
                      y1={cy}
                      y2={cy}
                      stroke={color}
                    />
                    <rect
                      x={x + Math.min(1, q(0.25)) * w}
                      y={cy - 9}
                      width={Math.max(
                        1,
                        (Math.min(1, q(0.75)) - Math.min(1, q(0.25))) * w,
                      )}
                      height="18"
                      fill={color}
                      fillOpacity=".25"
                      stroke={color}
                    />
                    <line
                      x1={x + Math.min(1, q(0.5)) * w}
                      x2={x + Math.min(1, q(0.5)) * w}
                      y1={cy - 9}
                      y2={cy + 9}
                      stroke={color}
                    />
                    {ratios
                      .filter((v) => v <= 1)
                      .map((v, k) => (
                        <circle
                          key={k}
                          cx={x + v * w}
                          cy={cy + ((k % 5) - 2) * 2}
                          r="2"
                          fill={color}
                          opacity=".5"
                        >
                          <title>
                            {m}: {v.toFixed(2)}× after / before
                          </title>
                        </circle>
                      ))}
                  </g>
                );
              })}
            </svg>
          );
        }}
      </ThinkingFigure>
      <div className="thinking-stat-cards">
        {[
          ["Observed wait", median(events.map((e) => e.Gap))],
          ["Input context", median(events.map((e) => e.Context))],
          ["Output length", median(events.map((e) => e.Output))],
        ].map(([label, value]) => (
          <div key={label}>
            <span>{label}</span>
            <strong>
              {Math.abs(100 * (1 - Number(value))).toFixed(0)}%{" "}
              {Number(value) <= 1 ? "lower" : "higher"}
            </strong>
            <small>median per-event ratio {Number(value).toFixed(2)}×</small>
          </div>
        ))}
        <div>
          <span>Compaction events</span>
          <strong>{events.length}</strong>
          <small>
            {new Set(events.map((e) => e.Conversation)).size} conversations
          </small>
        </div>
      </div>
    </div>
  );
}
export function ThinkingConversation({
  trace,
  scale,
}: {
  trace: ThinkingTrace;
  scale: "linear" | "log";
}) {
  return (
    <ThinkingFigure
      title={trace.Model + " · selected conversation stream"}
      note={`${trace.Points.length} of ${trace.Count} matching requests shown in source order${trace.Count > 1000 ? "; trace is limited to the first 1,000 requests" : ""}. Trailing mean uses up to 11 requests, requires six timed gaps and resets at compaction.`}
    >
      {(width) => {
        const height = 620,
          x = 55,
          y = 20,
          w = width - 75,
          h = 155,
          xs = (i: number) =>
            x + (i / Math.max(1, trace.Points.length - 1)) * w,
          maxContext = Math.max(1, ...trace.Points.map((p) => p.Context)),
          maxOutput = Math.max(1, ...trace.Points.map((p) => p.Output)),
          maxGap = Math.max(
            1,
            ...trace.Points.flatMap((p) => [p.Gap ?? 0, p.Smooth ?? 0]),
          ),
          ys = (v: number, max: number, offset: number, log = false) =>
            offset + h - (log ? Math.log1p(v) / Math.log1p(max) : v / max) * h;
        return (
          <svg
            viewBox={`0 0 ${width} ${height}`}
            className="thinking-svg"
            role="img"
            aria-label="Context, generated output and observed waits across a conversation"
          >
            {[
              ["Input context", maxContext, 20],
              ["Output tokens", maxOutput, 215],
              ["Observed gap (seconds)", maxGap, 410],
            ].map(([title, max, offset]) => (
              <g key={String(title)}>
                <text className="thinking-label" x={x} y={Number(offset) - 5}>
                  {title}
                </text>
                <Axis
                  width={width}
                  height={Number(offset) + h + 50}
                  {...{ x, w, h }}
                  y={Number(offset)}
                  yTicks={[0, Number(max)].map((v) => ({
                    value: ys(v, Number(max), Number(offset)),
                    label: fmt(v),
                  }))}
                  xTicks={[]}
                />
              </g>
            ))}
            <path
              d={line(
                trace.Points.map((p, i) => [
                  xs(i),
                  ys(p.Context, maxContext, 20),
                ]),
              )}
              stroke="var(--sea)"
              fill="none"
              strokeWidth="2"
            />
            <path
              d={line(
                trace.Points.map((p, i) => [
                  xs(i),
                  ys(p.Output, maxOutput, 215),
                ]),
              )}
              stroke="var(--gold)"
              fill="none"
            />
            {trace.Points.map((p, i) => (
              <g key={p.ID}>
                {p.Compaction ? (
                  <line
                    x1={xs(i)}
                    x2={xs(i)}
                    y1="20"
                    y2="565"
                    stroke="var(--gold)"
                    strokeDasharray="2 4"
                  />
                ) : null}
                {p.Gap === null ? null : (
                  <circle
                    cx={xs(i)}
                    cy={ys(p.Gap, maxGap, 410, scale === "log")}
                    r="2"
                    fill="var(--muted)"
                  >
                    <title>
                      Request {p.Rank} · {thinkingDuration(p.Gap)}
                    </title>
                  </circle>
                )}
              </g>
            ))}
            <path
              d={line(
                trace.Points.map((p, i) =>
                  p.Smooth === null
                    ? null
                    : [xs(i), ys(p.Smooth, maxGap, 410, scale === "log")],
                ),
              )}
              stroke="var(--sea)"
              strokeWidth="2"
              fill="none"
            />
            <text
              className="thinking-label"
              x={x + w / 2}
              y="605"
              textAnchor="middle"
            >
              Request order · {scale === "log" ? "log1p" : "linear"} wait scale
              · dotted lines mark compaction
            </text>
          </svg>
        );
      }}
    </ThinkingFigure>
  );
}
export function ThinkingScatter({
  points,
  title,
}: {
  points: [number, number][];
  title: string;
}) {
  return (
    <ThinkingFigure title={title}>
      {(width) => {
        const x = 50,
          y = 20,
          w = width - 70,
          h = 245,
          height = 320,
          max = Math.max(1, ...points.flat()),
          pos = (v: number) => Math.log1p(Math.max(0, v)) / Math.log1p(max);
        return (
          <svg
            viewBox={`0 0 ${width} ${height}`}
            className="thinking-svg"
            role="img"
            aria-label={title + "; log1p axes in seconds"}
          >
            <Axis
              {...{ width, height, x, y, w, h }}
              yTicks={[0, max].map((v) => ({
                value: y + h - pos(v) * h,
                label: thinkingDuration(v),
              }))}
              xTicks={[0, max].map((v) => ({
                value: x + pos(v) * w,
                label: thinkingDuration(v),
              }))}
            />
            <path
              d={`M${x},${y + h}L${x + w},${y}`}
              stroke="var(--muted)"
              strokeDasharray="2 4"
            />
            {points.map(([a, b], i) => (
              <circle
                key={i}
                cx={x + pos(a) * w}
                cy={y + h - pos(b) * h}
                r="2"
                fill="var(--sea)"
                opacity=".3"
              >
                <title>
                  {thinkingDuration(a)} · {thinkingDuration(b)}
                </title>
              </circle>
            ))}
          </svg>
        );
      }}
    </ThinkingFigure>
  );
}
