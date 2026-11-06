import React, { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { askWorkPreviewAddress, workPreviewEndpoint, workPreviewView, type WorkPreview, type WorkPreviewAddress } from "./ask-work-model";
import { PreviewBoundary, type AskLinkPreviews } from "./ask-link-previews";

const unavailable = "Work preview unavailable. Open the linked work to explore it.";

export function createAskWorkPreviews(): AskLinkPreviews {
  const openings = new EventTarget();
  return {
    canRender: address => Boolean(askWorkPreviewAddress(address)),
    revalidate: () => openings.dispatchEvent(new Event("open")),
    mount(host, card) {
      const address = askWorkPreviewAddress(card.href);
      if (!address) return () => {};
      const root = createRoot(host);
      root.render(<PreviewBoundary message={unavailable}><AskWorkPreview address={address} title={card.title} openings={openings} /></PreviewBoundary>);
      return () => root.unmount();
    },
  };
}

// Loads when first scrolled into view and again each time Ask reopens; the
// service answers from its cached delegation snapshot.
function AskWorkPreview({ address, title, openings }: { address: WorkPreviewAddress; title: string; openings: EventTarget }) {
  const host = useRef<HTMLElement>(null);
  const [visible, setVisible] = useState(false);
  const [revision, setRevision] = useState(0);
  const [result, setResult] = useState<{ preview?: WorkPreview; error?: string }>({});
  useEffect(() => {
    const observer = new IntersectionObserver(entries => {
      for (const entry of entries) setVisible(entry.isIntersecting);
    });
    if (host.current) observer.observe(host.current);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    const reopen = () => setRevision(value => value + 1);
    openings.addEventListener("open", reopen);
    return () => openings.removeEventListener("open", reopen);
  }, [openings]);
  useEffect(() => {
    if (!visible || document.hidden || !host.current?.getClientRects().length) return;
    const controller = new AbortController();
    fetch(workPreviewEndpoint(address), { signal: controller.signal })
      .then(async response => {
        const body = await response.json();
        if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`);
        setResult({ preview: body });
      })
      .catch(error => { if (!controller.signal.aborted) setResult(previous => previous.preview ? previous : { error: String(error) }); });
    return () => controller.abort();
  }, [address, visible, revision]);
  const { preview, error } = result;
  const view = preview && workPreviewView(preview);
  return <figure ref={host} className="pharos-work-preview" aria-label={`Work preview: ${title}`} aria-busy={!preview && !error}>
    {error ? <p className="pharos-work-preview-status" role="status">{unavailable}</p>
      : !view ? <p className="pharos-work-preview-status" role="status">Loading work summary…</p>
      : [view.where, view.what].map((parts, line) => parts.length ? <p key={line} className="pharos-work-preview-line">{parts.map((part, index) => <span key={index} title={part.title} data-overflow-title={part.title ? undefined : ""}>{part.text}</span>)}</p> : null)}
  </figure>;
}
