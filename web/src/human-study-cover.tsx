import React, { useLayoutEffect, useRef, useState } from "react";
import type { HumanStudyPreview } from "./human-study";
import "./human-study.css";

const percent = (value: number) => value.toLocaleString(undefined, { style: "percent", maximumFractionDigits: 1 });
const number = (value: number) => value.toLocaleString(undefined, { maximumFractionDigits: 1 });
const endpointDate = (time: number) => new Date(time).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });
export function HumanStudyCover({ study }: { study: HumanStudyPreview }) {
  const plot = useRef<HTMLSpanElement>(null), [size, setSize] = useState({ width: 260, height: 120 });
  useLayoutEffect(() => {
    const element = plot.current;
    if (!element) return;
    const measure = (width: number, height: number) => {
      if (width > 0 && height > 0) setSize(previous => previous.width === width && previous.height === height ? previous : { width, height });
    };
    measure(element.clientWidth, element.clientHeight);
    const observer = new ResizeObserver(entries => measure(entries[0].contentRect.width, entries[0].contentRect.height));
    observer.observe(element);
    return () => observer.disconnect();
  }, [study.kind]);
  const points = study.points ?? [], valid = points.filter((n): n is number => n !== null), low = valid.length ? Math.min(...valid) : 0, high = valid.length ? Math.max(...valid) : 0;
  const isPercent = study.format === "percent" || study.kind === "composition", format = isPercent ? percent : study.format === "USD" ? (value: number) => value.toLocaleString(undefined, { style: "currency", currency: "USD", maximumFractionDigits: 2 }) : number;
  const pad = (high - low || Math.max(.1, Math.abs(high) * .1)) * .025, min = Math.max(0, low - pad), max = Math.max(min + .001, Math.min(isPercent ? 1 : Infinity, high + pad));
  const left = 4, right = size.width - 4, top = 5, bottom = size.height - 5;
  const x = (index: number) => left + index / Math.max(1, points.length - 1) * (right - left), y = (value: number) => bottom - (value - min) / (max - min) * (bottom - top);
  const segments: string[] = [];
  points.forEach((value, index) => { if (value !== null) segments.push(`${index === 0 || points[index - 1] === null ? "M" : "L"}${x(index)},${y(value)}`); });
  const history = study.historyDays ?? 30;
  const normDetail = `${history}-day norm\n${study.baseline === undefined ? "No eligible historical windows" : format(study.baseline)}\nMean of the prior ${history} daily endpoints; each uses a trailing ${study.lookbackDays}-day window.`;
  // The dashed reference has a numeric axis tick just like the range limits.
  // Spread nearby labels without changing the values or the reference lines.
  const ticks = (valid.length ? [...new Set([high, low, ...(study.baseline === undefined ? [] : [study.baseline])])].map(value => ({ value, at: y(value), labelAt: y(value) })) : []).sort((a, b) => a.at - b.at);
  for (let index = 1; index < ticks.length; index++) ticks[index].labelAt = Math.max(ticks[index].at, ticks[index - 1].labelAt + 13);
  if (ticks.length && ticks.at(-1)!.labelAt > size.height - 5) {
    ticks.at(-1)!.labelAt = size.height - 5;
    for (let index = ticks.length - 2; index >= 0; index--) ticks[index].labelAt = Math.min(ticks[index].labelAt, ticks[index + 1].labelAt - 13);
  }
  const rowMax = Math.max(1, ...study.rows?.map(row => row.value) ?? []);
  const pointDetail = (value: number | null, index: number) => {
    const time = study.pointTimes?.[index];
    return [
      time === undefined ? `Daily endpoint ${index + 1}` : endpointDate(time),
      value === null ? "No eligible messages in this window" : `${format(value)} ${study.pointValueLabel ?? study.valueLabel}`,
      `Trailing ${study.lookbackDays} days${time === undefined ? "" : ` · ending ${new Date(time).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit", timeZoneName: "short" })}`}`,
    ].join("\n");
  };
  return <span className={`human-study-cover is-${study.kind}`}>
    {study.value !== undefined ? <span className="human-study-headline query-stat-line"><strong>{format(study.value)}</strong><span>{study.valueLabel}</span></span> : <span className="human-study-label">{study.valueLabel}</span>}
    {study.kind === "trend" ? <>
      <span className="human-study-trend">
        <span className="human-study-range" aria-label={valid.length ? `Observed range: ${format(low)} to ${format(high)}${study.baseline === undefined ? "" : `. ${history}-day norm: ${format(study.baseline)}`}` : "No observed values"}>
          <span className="human-study-range-measure" aria-hidden="true">{ticks.length ? ticks.map(tick => format(tick.value)).sort((a, b) => b.length - a.length)[0] : "No values"}</span>
          {!valid.length ? <span className="human-study-tick" style={{ top: "50%" }}>No values</span> : ticks.map(tick => <span key={tick.value} className={`human-study-tick${tick.value === study.baseline ? " is-norm" : ""}`} style={{ top: tick.labelAt }} data-chart-tooltip={tick.value === study.baseline ? normDetail : undefined} title="">{format(tick.value)}</span>)}
        </span>
        <span className="human-study-plot" ref={plot}>
          <svg className="human-study-sparkline" viewBox={`0 0 ${size.width} ${size.height}`} preserveAspectRatio="xMidYMid meet" aria-hidden="true">
            {valid.length ? <g className="human-study-observed-range">
              <rect x={left} y={y(high)} width={right - left} height={Math.max(.5, y(low) - y(high))} fill="currentColor" opacity=".06" />
            </g> : null}
            {study.baseline !== undefined ? <line className="human-study-norm-line" x1={left} x2={right} y1={y(study.baseline)} y2={y(study.baseline)} stroke="currentColor" strokeDasharray="3 4" opacity=".5" /> : null}
            {ticks.filter(tick => Math.abs(tick.labelAt - tick.at) > 1).map(tick => <path key={tick.value} d={`M0,${tick.labelAt} L${left},${tick.at}`} fill="none" stroke="currentColor" strokeWidth=".5" opacity=".35" />)}
            <path d={segments.join(" ")} fill="none" stroke="currentColor" strokeWidth="2" vectorEffect="non-scaling-stroke" />
            {points.at(-1) != null ? <circle className="human-study-endpoint" cx={right} cy={y(points.at(-1)!)} r="3" fill="currentColor" /> : null}
            {points.map((value, index) => <g key={index} className="human-study-point" data-chart-tooltip={pointDetail(value, index)}>
              <g className="human-study-point-guide">
                <line x1={x(index)} x2={x(index)} y1="0" y2={size.height} stroke="currentColor" strokeDasharray="2 3" opacity=".5" vectorEffect="non-scaling-stroke" />
                {value !== null ? <circle cx={x(index)} cy={y(value)} r="3" fill="var(--panel)" stroke="currentColor" strokeWidth="1.5" /> : null}
              </g>
              <rect x={index ? (x(index - 1) + x(index)) / 2 : 0} y="0" width={(index === points.length - 1 ? size.width : (x(index) + x(index + 1)) / 2) - (index ? (x(index - 1) + x(index)) / 2 : 0)} height={size.height} fill="transparent" />
            </g>)}
          </svg>
        </span>
        <span className="human-study-axis human-study-trend-axis"><span>{history} days ago</span><span>{study.trendLabel ?? `Trailing ${study.lookbackDays}d`} · now</span></span>
      </span>
    </> : study.kind === "hours" ? <><span className="human-study-hour-bars">{study.rows?.map((row, index) => <span key={row.label} data-chart-tooltip={`${row.label}–${String((index + 1) % 24).padStart(2, "0")}:00\n${number(row.value)} human-written words\n${study.timeZone ?? "Local time"}`} title=""><i style={{ height: `${row.value / rowMax * 100}%` }} /></span>)}</span><span className="human-study-axis"><span>00:00</span><span>12:00</span><span>23:00</span></span></>
      : <span className="human-study-rows">{study.rows?.map(row => <span className="human-study-row" key={row.label} data-chart-tooltip={`${row.label}\n${row.count === undefined ? "" : `${number(row.count)} ${study.kind === "profile" ? row.count === 1 ? "conversation" : "conversations" : row.count === 1 ? "contribution" : "contributions"} · `}${percent(row.value)}${study.kind === "profile" ? "" : `\n${study.sampleLabel}`}`} title=""><span>{row.label}</span><strong>{percent(row.value)}{row.count !== undefined ? <small> · {row.count}</small> : null}</strong><i><span style={{ width: `${row.value * 100}%` }} /></i></span>)}</span>}
  </span>;
}
