import { readWindowChoice } from "./day-window";
import { supportsToday } from "./curiosity-data";
import React, { useEffect, useState } from "react";
import type { FieldSchema } from "@pythia-software/query-table-core";
import { cardAnalysisJob, relatedCardDashboardURL, resolveCardAnalysisJob } from "./card-analysis-model";
import { loadCardPreviews, invalidateCardPreviews } from "./card-previews";
import { clearPreviewCache, type PreviewJob, type QueryPreview } from "./query-previews";
import { CardVisualization } from "./query-card";
import { ChartTooltip } from "./chart-tooltip";
import { cardDetailsURL } from "./card-exploration";
import { rollingWindowDays, updateQueryWindowURL } from "./query-links";
import { queryCategories } from "./query-categories";
import { PageLink } from "./nested-pages";
import { usesCardTrend } from "./card-trend";

export function CardAnalysis({ url, schema }: { url: string; schema: FieldSchema }) {
  const [revision, setRevision] = useState(0);
  const [state, setState] = useState<{ job?: PreviewJob; preview?: QueryPreview; error?: string }>({});
  useEffect(() => {
    const refresh = () => setRevision(value => value + 1);
    window.addEventListener("pharos:data-refresh", refresh); window.addEventListener("pharos:usage-refresh", refresh);
    return () => { window.removeEventListener("pharos:data-refresh", refresh); window.removeEventListener("pharos:usage-refresh", refresh); };
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    setState({});
    void (async () => {
      let job: PreviewJob;
      // Resolve a durable token before execution; never substitute a newly
      // instantiated recipe for a missing or unavailable saved population.
      try { job = cardAnalysisJob(url, schema); }
      catch { job = await resolveCardAnalysisJob(url, schema, controller.signal); }
      if (controller.signal.aborted) return;
      setState({ job });
      await loadCardPreviews([job], (_id, preview, error) => {
        if (!controller.signal.aborted) setState({ job, preview, error: error ? String(error) : undefined });
      }, controller.signal);
    })().catch(error => { if (!controller.signal.aborted) setState({ error: String(error) }); });
    return () => controller.abort();
  }, [url, schema, revision]);
  const { job, preview, error } = state, recipe = job?.options?.recipe;
  const trailing = Boolean(job && usesCardTrend(job));
  const params = new URL(url, location.origin).searchParams;
  const details = cardDetailsURL(params);
  const refresh = () => { clearPreviewCache(); invalidateCardPreviews(); setRevision(value => value + 1); };
  return <section className="card-analysis" aria-label="Card analysis" style={{ "--query-tone": queryCategories[recipe?.category ?? "usage"].tone } as React.CSSProperties}>
    <div className="card-analysis-heading"><div><h2>{recipe?.title ?? "Card analysis"}</h2><p>{recipe?.description}</p></div>
      <div className="card-analysis-controls">
        {job && !recipe?.period && !(trailing && recipe?.trend?.lookback) ? <label>{trailing ? "Trailing window" : "Time window"}<select aria-label={trailing ? "Trailing window" : "Time window"} value={job.options?.window?.mode === "today" ? "today" : job.options?.window?.days ?? "all"} onChange={event => {
          window.pharosNavigate?.(updateQueryWindowURL(url, event.target.value === "all" ? null : readWindowChoice(event.target.value), job.query));
        }}>{recipe && supportsToday(recipe) ? <option value="today">Today</option> : null}{rollingWindowDays.map(days => <option key={days} value={days}>{days === 1 ? "Last 24 hours" : `Last ${days} days`}</option>)}<option value="all">All time</option></select></label> : null}
        <button type="button" onClick={refresh}>Refresh</button>
      </div>
    </div>
    {error ? <p className="query-table-error" role="alert">{error}</p> : preview ? preview.state === "ready" ?
      <ChartTooltip><div className="query-cover card-analysis-chart" role="img" aria-label={preview.study?.valueLabel ?? preview.headline ?? recipe?.title ?? "Card chart"}><CardVisualization preview={preview} /></div></ChartTooltip>
      : <p role="status">{preview.emptyLabel ?? "No matching observations"}</p> : <p role="status">Loading analysis…</p>}
    {preview?.refreshError ? <p role="status">Showing saved analysis. Refresh failed: {preview.refreshError}</p> : null}
    {preview?.note ? <p className="usage-dashboard-note">{preview.note}</p> : null}
    <div className="card-analysis-links">{details ? <PageLink href={details}>View supporting query</PageLink> : null}
      <PageLink href={relatedCardDashboardURL(params)}>Related {params.get("analyze_topic") === "parallelism" ? "Parallelism" : "dashboard"} overview</PageLink>
    </div>
    <p className="usage-dashboard-note">The chart uses the card’s calculation, filters and presentation. The supporting query retains its source records and grouped results. The related overview provides broader context.</p>
  </section>;
}
