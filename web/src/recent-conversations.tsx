import React, { useEffect, useState } from "react";
import { ConversationCard, ResultMessage } from "./conversation-results";
import { PageLink } from "./nested-pages";
import { Icon } from "./icons";

type RecentConversation = {
  id: string; workspace_id: string; title: string; repository?: string;
  provider: string; model?: string; activity_at?: string;
  first_input?: string; last_response?: string; subagent_depth: number;
  labels_ready: boolean; events_ready: boolean;
};

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
        return <ConversationCard key={item.id}
          title={<h3><PageLink className="conversation-result-title" href={href}>{item.title || "Untitled conversation"}<Icon name="arrow-right" /></PageLink></h3>}
          facts={<>
            <div className="conversation-result-meta">{[item.repository, item.provider, item.model].filter(Boolean).join(" · ")}</div>
            <div className="home-recent-facts">
              {item.activity_at && Number.isFinite(Date.parse(item.activity_at)) ? <time dateTime={item.activity_at}>{new Date(item.activity_at).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" })}</time> : null}
              <span>{item.subagent_depth > 0 ? `Sub-agents · Depth ${item.subagent_depth}` : "Main agent only"}</span>
              {item.labels_ready ? <span>Message labels</span> : null}
              {item.events_ready ? <span>Agent events</span> : null}
            </div>
          </>}>
          <div className="conversation-result-preview">
            {item.first_input ? <ResultMessage label="First ask" message={{ role: "user", text: item.first_input }} kind="human" compact /> : null}
            {item.last_response ? <ResultMessage label="Latest response" message={{ role: "assistant", text: item.last_response }} kind="assistant" compact /> : null}
          </div>
        </ConversationCard>;
      })}
    </div>
    {!items.length ? <p className="home-recent-empty" role="status">{loading ? "Loading recent conversations…" : error ? "Recent conversations are unavailable right now." : "Conversations will appear here after your history is indexed."}</p> : null}
    <PageLink className="home-recent-search" href="/library?view=conversation">Search conversations <Icon name="arrow-right" /></PageLink>
  </section>;
}
