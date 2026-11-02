import React, { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import type { FieldSchema } from "@pythia-software/query-table-core";
import { askChartPreviewJob, onAskChartRefresh } from "./ask-chart-model";
import { collectDashboardCard, dashboardCardCollected, dashboardCardPresentation, dashboardCardWindow } from "./dashboard-card-model";
import { invalidateCardPreviews, loadCardPreviews } from "./card-previews";
import { QueryCard } from "./query-card";
import { Icon } from "./icons";
import type { QueryDataset } from "./query-links";
import type { PreviewJob, QueryPreview } from "./query-previews";

export type AskChartCard = { href: string; title: string; description: string };
export type AskChartPreviews = {
  canRender: (address: string) => boolean;
  mount: (host: HTMLElement, card: AskChartCard, options?: { eager?: boolean; onOpen?: (url: string) => void }) => () => void;
  revalidate: () => void;
};

export function createAskChartPreviews(schemas: Partial<Record<QueryDataset, FieldSchema>>): AskChartPreviews {
  const openings = new EventTarget();
  return {
    canRender: address => Boolean(askChartPreviewJob(address, schemas)),
    revalidate: () => openings.dispatchEvent(new Event("open")),
    mount(host, card, { eager = false, onOpen = (url: string) => window.pharosNavigate?.(url) } = {}) {
      const job = askChartPreviewJob(card.href, schemas);
      if (!job) return () => {};
      const root = createRoot(host);
      root.render(<PreviewBoundary><AskChartPreview job={job} card={card} openings={openings} eager={eager} onOpen={onOpen} /></PreviewBoundary>);
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

// The chart is a Card Library card for its own dashboard address: the plus
// button collects that address to Home, which renders the same chart.
function AskChartPreview({ job, card, openings, eager, onOpen }: { job: PreviewJob; card: AskChartCard; openings: EventTarget; eager: boolean; onOpen: (url: string) => void }) {
  const host = useRef<HTMLElement>(null), loaded = useRef(false);
  const [visible, setVisible] = useState(false);
  const [revision, setRevision] = useState(0);
  const [result, setResult] = useState<{ preview?: QueryPreview; error?: string }>({});
  const [collected, setCollected] = useState(() => dashboardCardCollected(card.href)), [failure, setFailure] = useState("");
  const { category, loading } = dashboardCardPresentation(job.id);
  const windowLabel = dashboardCardWindow(job.id);
  useEffect(() => {
    const observer = new IntersectionObserver(entries => {
      for (const entry of entries) setVisible(entry.isIntersecting);
    });
    if (host.current) observer.observe(host.current);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    const update = () => setCollected(dashboardCardCollected(card.href));
    window.addEventListener("pharos:bookmarks", update);
    return () => window.removeEventListener("pharos:bookmarks", update);
  }, [card.href]);
  useEffect(() => {
    return onAskChartRefresh(job.dataset, openings, stale => {
      if (stale) invalidateCardPreviews();
      setRevision(value => value + 1);
    });
  }, [job.dataset, openings]);
  useEffect(() => {
    // A fresh answer loads at once, even behind the floating panel, so its
    // chart is ready when the user opens the result. Later loads wait until
    // the chart is visible.
    const hidden = !visible || document.hidden || !host.current?.getClientRects().length;
    if (hidden && (loaded.current || !eager)) return;
    loaded.current = true;
    const controller = new AbortController();
    setResult(previous => ({ preview: previous.preview ? { ...previous.preview, refreshing: true, refreshError: undefined } : undefined }));
    void loadCardPreviews([job], (_id, preview, error) => {
      if (!controller.signal.aborted) setResult({ preview, error: error ? String(error) : undefined });
    }, controller.signal).catch(error => { if (!controller.signal.aborted) setResult({ error: String(error) }); });
    return () => controller.abort();
  }, [job, visible, revision]);
  function keep() {
    try {
      collectDashboardCard(card.href, card.title.slice(0, 200), card.description.slice(0, 500));
      setFailure("");
      // The plus button leaves with collection; keep focus on the card.
      requestAnimationFrame(() => host.current?.querySelector<HTMLElement>(".query-card-open")?.focus({ preventScroll: true }));
    } catch (error) { setFailure(error instanceof Error ? error.message : String(error)); }
  }
  const { preview, error } = result;
  return <figure ref={host} className="pharos-chart-preview" aria-label={`Chart preview: ${card.title}`} aria-busy={!preview && !error || Boolean(preview?.refreshing)}>
    <QueryCard discovery id={`ask:${job.id}`} title={card.title} description={card.description} url={card.href} windowLabel={windowLabel} category={category}
      preview={preview} error={error && unavailable} loadingSpec={loading} onOpen={onOpen} onKeep={collected ? undefined : keep} onReload={() => setRevision(value => value + 1)} />
    {collected ? <figcaption className="card-library-collected" role="status"><Icon name="check" /> In your collection</figcaption> : null}
    {failure ? <p role="alert">{failure}</p> : null}
  </figure>;
}
