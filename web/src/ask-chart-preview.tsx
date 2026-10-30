import React, { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import type { FieldSchema } from "@pythia-software/query-table-core";
import { askChartPreviewJob, onAskChartRefresh } from "./ask-chart-model";
import { dashboardCardPresentation, dashboardCardWindow } from "./dashboard-card-model";
import { invalidateCardPreviews, loadCardPreviews } from "./card-previews";
import { CardVisualization } from "./query-card";
import { CardLoading } from "./card-loading";
import { ChartTooltip } from "./chart-tooltip";
import type { QueryDataset } from "./query-links";
import type { PreviewJob, QueryPreview } from "./query-previews";

export type AskChartPreviews = {
  canRender: (address: string) => boolean;
  mount: (host: HTMLElement, card: { href: string; title: string }) => () => void;
  revalidate: () => void;
};

export function createAskChartPreviews(schemas: Partial<Record<QueryDataset, FieldSchema>>): AskChartPreviews {
  const openings = new EventTarget();
  return {
    canRender: address => Boolean(askChartPreviewJob(address, schemas)),
    revalidate: () => openings.dispatchEvent(new Event("open")),
    mount(host, card) {
      const job = askChartPreviewJob(card.href, schemas);
      if (!job) return () => {};
      const root = createRoot(host);
      root.render(<PreviewBoundary><AskChartPreview job={job} title={card.title} openings={openings} /></PreviewBoundary>);
      return () => root.unmount();
    },
  };
}

const unavailable = "Chart preview unavailable. Open the linked view to explore it.";
class PreviewBoundary extends React.Component<{ children: React.ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() { return { failed: true }; }
  render() { return this.state.failed ? <p role="status">{unavailable}</p> : this.props.children; }
}

function AskChartPreview({ job, title, openings }: { job: PreviewJob; title: string; openings: EventTarget }) {
  const host = useRef<HTMLElement>(null);
  const [visible, setVisible] = useState(false);
  const [revision, setRevision] = useState(0);
  const [result, setResult] = useState<{ preview?: QueryPreview; error?: string }>({});
  const { loading } = dashboardCardPresentation(job.id);
  const windowLabel = dashboardCardWindow(job.id);
  useEffect(() => {
    const observer = new IntersectionObserver(entries => {
      for (const entry of entries) setVisible(entry.isIntersecting);
    });
    if (host.current) observer.observe(host.current);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    return onAskChartRefresh(job.dataset, openings, () => {
      invalidateCardPreviews();
      setRevision(value => value + 1);
    });
  }, [job.dataset, openings]);
  useEffect(() => {
    if (!visible || document.hidden || !host.current?.getClientRects().length) return;
    const controller = new AbortController();
    setResult(previous => ({ preview: previous.preview ? { ...previous.preview, refreshing: true, refreshError: undefined } : undefined }));
    void loadCardPreviews([job], (_id, preview, error) => {
      if (!controller.signal.aborted) setResult({ preview, error: error ? String(error) : undefined });
    }, controller.signal).catch(error => { if (!controller.signal.aborted) setResult({ error: String(error) }); });
    return () => controller.abort();
  }, [job, visible, revision]);
  const { preview, error } = result;
  return <figure ref={host} className="pharos-chart-preview" aria-label={`Chart preview: ${title}`} aria-busy={!preview && !error || Boolean(preview?.refreshing)}>
    <figcaption className="pharos-chart-preview-meta"><span>{windowLabel}</span>{preview?.updatedAt ? <span>{preview.refreshing ? "Refreshing…" : preview.refreshError ? "Saved preview · refresh unavailable" : "Updated"} <time dateTime={new Date(preview.updatedAt).toISOString()} title={new Date(preview.updatedAt).toLocaleString()}>{new Date(preview.updatedAt).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}</time></span> : null}</figcaption>
    <ChartTooltip><span className="query-cover pharos-chart-preview-cover" role={preview?.state === "ready" ? "img" : undefined} aria-label={preview?.state === "ready" ? `${title}. ${preview.summary}${preview.note ? `. ${preview.note}` : ""}` : undefined}>
      {error ? <span className="query-preview-status" role="status">{unavailable}</span>
        : preview?.state === "empty" ? <span className="query-preview-status" role="status">{preview.emptyLabel ?? "No matching data"}</span>
        : preview ? <CardVisualization preview={preview} /> : <CardLoading spec={loading} />}
    </span></ChartTooltip>
  </figure>;
}
