import React, { useEffect, useState } from "react";
import { onDatasetRefresh } from "./dataset-refresh";
import { ViewDisclosure } from "./view-disclosure";

type PricingStatus = {
  unpriced_models: Array<{ model: string }>;
  prompt: string;
  error?: string;
};

// Pricing coverage belongs to the whole library, independent of the chart's
// filters, dates and view. Copying the prompt never dismisses an unresolved gap.
export function PricingNotice({ copy }: { copy: (text: string) => Promise<void> }) {
  const [pricing, setPricing] = useState<PricingStatus>();
  const [reload, setReload] = useState(0);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [copying, setCopying] = useState(false);
  useEffect(() => onDatasetRefresh("usage", () => setReload(value => value + 1)), []);
  useEffect(() => {
    const controller = new AbortController();
    setError("");
    setNotice("");
    void fetch("/api/pricing", { signal: controller.signal }).then(async response => {
      if (!response.ok) throw new Error(`Pricing: ${response.status}`);
      const status = await response.json() as PricingStatus;
      if (status.error) throw new Error(status.error);
      if (!controller.signal.aborted) setPricing(status);
    }).catch(() => {
      if (!controller.signal.aborted) setError("Could not check model pricing. Refresh to try again.");
    });
    return () => controller.abort();
  }, [reload]);

  const models = pricing?.unpriced_models ?? [];
  if (!models.length) return error ? <p role="alert" className="query-table-error">{error}</p> : null;
  const copyPrompt = async () => {
    setCopying(true);
    setNotice("");
    try {
      await copy(pricing!.prompt);
      setNotice("Pricing prompt copied. Paste it into your coding agent to research the missing prices.");
    } catch {
      setNotice("Could not copy. Open the pricing prompt below and copy the text manually.");
    } finally {
      setCopying(false);
    }
  };
  return <section className="pricing-notice" aria-label="Missing model prices">
    <div className="pricing-notice-heading">
      <div><h2>Some work has no API price</h2><p>Across this library, {models.length} {models.length === 1 ? "model needs" : "models need"} pricing research: {models.map(model => model.model).join(", ")}. Unpriced usage is missing from API cost totals.</p></div>
      <button type="button" disabled={copying || !pricing?.prompt} onClick={() => void copyPrompt()}>Copy refresh prices prompt</button>
    </div>
    {error ? <p className="query-table-error" role="alert">{error}</p> : null}
    {notice ? <p role="status">{notice}</p> : null}
    <ViewDisclosure viewKey="tokens.pricing-prompt" summary="Pricing prompt">
      <textarea aria-label="Pricing refresh prompt" readOnly value={pricing?.prompt ?? ""} rows={12} />
    </ViewDisclosure>
  </section>;
}
