import { estimateExplanation } from "./usage-estimate-model";
import React, { useEffect, useState } from "react";
import { FingerprintImage } from "./fingerprint-image";
import { formatConcurrencyDuration } from "./concurrency-data";
import { PageLink } from "./nested-pages";
import { Icon } from "./icons";

type RecentConversation = {
  id: string; workspace_id: string; title: string; repository?: string;
  provider: string; model?: string; effort?: string; activity_at?: string;
  first_input?: string; last_response?: string; subagent_depth: number;
  labels_ready: boolean; duration_ms?: number; tool_calls?: number; tokens?: number; cost_usd?: number; price_status?: string; is_estimate?: boolean;
};

function previewText(text: string, role: "user" | "assistant") {
  return window.pharosMessageSummary?.({ role, text }) ?? text;
}

export function RecentConversations({ active, generation }: { active: boolean; generation: number }) {
  const [items, setItems] = useState<RecentConversation[]>([]);
  const [loading, setLoading] = useState(true), [error, setError] = useState(false);
  useEffect(() => {
    if (!active) return;
    const controller = new AbortController();
    setLoading(true);
    void fetch("/api/library/recent-conversations", { signal: controller.signal }).then(async response => {
      if (!response.ok) throw new Error("Recent conversations unavailable");
      const result = await response.json() as { items: RecentConversation[] };
      if (!controller.signal.aborted) { setItems(result.items); setError(false); }
    }).catch(() => { if (!controller.signal.aborted) setError(true); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [active, generation]);

  return <section className="home-recent" aria-labelledby="recentConversationsTitle">
    <div className="home-recent-heading"><h2 id="recentConversationsTitle">Recent conversations</h2>
      <p className="muted">A few starting points to explore your recent work.</p>
    </div>
    <div className="home-recent-grid" aria-busy={loading}>
      {items.map(item => {
        const href = `/work/${encodeURIComponent(item.workspace_id)}/conversation/${encodeURIComponent(item.id)}`;
        const cost = item.cost_usd == null ? null : item.cost_usd < 0.01 && item.cost_usd > 0 ? "< $0.01" : item.cost_usd.toLocaleString(undefined, { style: "currency", currency: "USD" });
        return <article className="home-recent-card" key={item.id}>
          <div className="home-recent-card-header">
            <div className="home-recent-identity">
              <h3><PageLink className="home-recent-title" href={href}>{item.title || "Untitled conversation"}<Icon name="arrow-right" /></PageLink></h3>
              <div className="home-recent-meta">{[item.repository, item.provider, item.model, item.effort].filter(Boolean).join(" · ")}</div>
            </div>
            {item.labels_ready ? <div className="home-recent-fingerprint"><FingerprintImage id={item.id} /></div> : null}
            <div className="home-recent-facts">
              {item.activity_at && Number.isFinite(Date.parse(item.activity_at)) ? <time dateTime={item.activity_at}>{new Date(item.activity_at).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" })}</time> : null}
              {item.subagent_depth > 0 ? <span>Sub-agents · Depth {item.subagent_depth}</span> : null}
              {item.duration_ms != null ? <span>{formatConcurrencyDuration(item.duration_ms)} elapsed</span> : null}
              {item.tool_calls != null ? <span>{item.tool_calls.toLocaleString()} tool calls</span> : null}
              {item.tokens != null ? <span title={item.is_estimate?estimateExplanation:undefined}>{item.tokens.toLocaleString()} tokens{item.is_estimate?"*":""}</span> : null}
              {cost ? <span title={item.is_estimate ? estimateExplanation : item.price_status && item.price_status !== "complete" ? `Pricing: ${item.price_status}` : "Estimated API list-price equivalent"}>{cost}{item.is_estimate?"*":""} {item.price_status === "unpriced" || item.price_status === "partial" ? "known " : ""}API cost{item.price_status === "assumed" ? " (estimated)" : ""}</span> : null}
            </div>
          </div>
          {(item.first_input || item.last_response) ? <div className="home-recent-exchange">
            {item.first_input ? <div className="home-recent-message human"><p><span>First ask</span>{" "}{previewText(item.first_input, "user")}</p></div> : null}
            {item.last_response ? <div className="home-recent-message assistant"><p><span>Latest response</span>{" "}{previewText(item.last_response, "assistant")}</p></div> : null}
          </div> : null}
        </article>;
      })}
    </div>
    {!items.length ? <p className="home-recent-empty" role="status">{loading ? "Loading recent conversations…" : error ? "Recent conversations are unavailable right now." : "Conversations will appear here after your history is indexed."}</p> : null}
    <PageLink className="home-recent-search" href="/library?view=conversation">Search conversations <Icon name="arrow-right" /></PageLink>
  </section>;
}
