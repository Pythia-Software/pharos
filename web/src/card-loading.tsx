import React from "react";
import type { CardLoadingSpec } from "./card-loading-model";

const heights = [40, 75, 55, 85, 30, 65, 50];
const delays = [0, -.7, -.3, -.9, -.5];
export function CardLoading({ spec = { kind: "text", count: 3 } }: { spec?: CardLoadingSpec }) {
  const marks = Array.from({ length: spec.count }, (_, i) => i);
  const timing = (i: number) => ({ animationDelay: `${delays[i % delays.length]}s` });
  return <span className="query-preview-loading" role="status" data-loading-kind={spec.kind} data-loading-count={spec.count}>
    <span className={`query-preview-loading-shape is-${spec.kind}${spec.headline ? " has-headline" : ""}${spec.belowLabels ? " has-below-labels" : ""}`} aria-hidden="true">
      {spec.headline ? <span className="query-loading-headline" /> : null}
      {spec.kind === "value" ? <span className="query-loading-value" />
        : spec.kind === "columns" ? <span className="query-preview-loading-bars">{marks.map(i => <i key={i} style={{ height: `${heights[i % heights.length]}%`, ...timing(i) }} />)}</span>
        : spec.kind === "scatter" ? <span className="query-loading-scatter">{marks.map(i => <i key={i} style={{ left: `${12 + (i * 29) % 76}%`, top: `${12 + (i * 43) % 70}%`, width: `${6 + i % 3 * 2}px`, height: `${6 + i % 3 * 2}px`, "--drift-x": `${i % 2 ? 7 : -5}px`, "--drift-y": `${i % 3 ? -6 : 5}px`, ...timing(i) } as React.CSSProperties} />)}</span>
        : spec.kind === "line" ? <svg className="query-loading-line" viewBox="0 0 100 60" preserveAspectRatio="none"><polyline points={marks.map(i => `${marks.length === 1 ? 50 : i / (marks.length - 1) * 100},${60 - heights[i % heights.length] * .5}`).join(" ")} /></svg>
        : spec.kind === "pie" || spec.kind === "donut" ? <span className="query-loading-circle" style={{ "--loading-segment": `${360 / spec.count}deg` } as React.CSSProperties} />
        : <span className="query-loading-rows">{marks.map(i => <span className="query-loading-row" key={i} style={timing(i)}>
          {spec.kind === "box" ? <><i className="query-loading-row-label" /><span className="query-loading-whisker"><i /></span></>
            : spec.kind === "table" ? <><i /><i /><i /></>
            : spec.kind === "text" ? <i style={{ width: `${heights[i % heights.length]}%` }} />
            : <><i className="query-loading-row-label" /><i className="query-loading-row-bar" style={{ width: `${heights[i % heights.length]}%` }} /></>}
        </span>)}</span>}
    </span>
    <span className="query-preview-spinner" aria-hidden="true" /><span className="query-loading-label">{spec.kind === "text" || spec.kind === "table" ? "Loading preview" : spec.kind === "value" ? "Loading value" : "Loading chart"}</span>
  </span>;
}
