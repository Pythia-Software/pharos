import { useViewEntry, useViewState } from "./view-state";
import React, { useEffect, useMemo, useRef, useState } from "react";
import { QueryBuilder, QueryFilterRow, type QueryBuilderProps } from "@pythia-software/query-table-ui";
import type { QueryFilterApi } from "@pythia-software/query-table-react";
import { Icon } from "./icons";
import { dataEntities, dataSources, describeDataSource, type DataEntity, type DataSource } from "./data-taxonomy-model";
import { readConcurrencySettings } from "./concurrency-settings";
import "./data-taxonomy.css";

// Any page (including the classic shell) can open the single shared guide.
export function openDataTaxonomy(dataset: DataSource = "library", invoker?: HTMLElement) {
  window.dispatchEvent(new CustomEvent("pharos:open-data-guide", { detail: { dataset, invoker } }));
}

function useDataSource(dataset: DataSource) {
  const [counting, setCounting] = useState(() => readConcurrencySettings().counting);
  useEffect(() => {
    const refresh = () => setCounting(readConcurrencySettings().counting);
    window.addEventListener("pharos:concurrency-settings", refresh);
    window.addEventListener("pharos:preferences-saved", refresh);
    return () => { window.removeEventListener("pharos:concurrency-settings", refresh); window.removeEventListener("pharos:preferences-saved", refresh); };
  }, []);
  return describeDataSource(dataset, counting);
}

function SourceStatement({ dataset }: { dataset: DataSource }) {
  const source = useDataSource(dataset);
  return <div className="qt-qb-row data-source-statement">
    <span className="qt-qb-kw">FROM</span>
    <a className="data-source-link" href={`#data-guide-${source.entity}`} aria-haspopup="dialog" onClick={event => { event.preventDefault(); openDataTaxonomy(dataset, event.currentTarget); }}>{source.label}<Icon name="info" /></a>
  </div>;
}

// Compose source information with WHERE, leaving the builder's native grouping
// clauses and collapsed summary intact. Grouped WHERE edits the source rows.
export function SourceQueryBuilder<Row>({ dataset, whereRow, ...props }: QueryBuilderProps<Row> & { dataset: DataSource }) {
  const [collapsed,setCollapsed]=useViewEntry("query_layout",dataset,Boolean(props.defaultCollapsed));
  const currentApi = useRef(props.api);
  currentApi.current = props.api;
  // A grouped result can replace the source facade. Keep the filter API stable
  // so suggestion effects do not restart on each result.
  const sourceFilters = useMemo(() => new Proxy({} as QueryFilterApi<Row>, {
    get: (_, property) => Reflect.get(currentApi.current.query.grouping ? currentApi.current.grouping!.source : currentApi.current, property),
  }), []);
  return <div className="data-source-query" data-pharos-target={`control:q_${dataset}`}><QueryBuilder {...props} collapsed={props.collapsed??collapsed} onCollapsedChange={props.onCollapsedChange??setCollapsed} whereRow={<>
    <SourceStatement dataset={dataset} />
    {whereRow ?? <QueryFilterRow api={sourceFilters} fields={props.api.query.grouping ? props.api.grouping!.source.schema.fields : props.fields} />}
  </>} /></div>;
}

export function DataTaxonomyModal() {
  const dialog = useRef<HTMLDialogElement>(null);
  const [sourceDataset,setSourceDataset]=useViewState<DataSource>("guide_dataset","library");
  const source=useDataSource(sourceDataset);
  const [entity, setEntity] = useViewState<DataEntity>("guide_entity", "workspaces");
  const [conversation, setConversation] = useViewState("guide_example", "implementation");
  const [shown,setShown] = useViewState("guide_open",false);
  useEffect(()=>{const element=dialog.current!;if(shown&&!element.open)element.showModal();else if(!shown&&element.open)element.close();},[shown]);
  useEffect(() => {
    const element = dialog.current!;
    let invoker: HTMLElement | null = null;
    const show = (event: Event) => {
      const context = (event as CustomEvent<{ dataset?: string; invoker?: HTMLElement }>).detail;
      const requested = context?.dataset;
      const next = requested && Object.hasOwn(dataSources, requested) ? requested as DataSource : "library";
      const openedSource = describeDataSource(next, readConcurrencySettings().counting);
      setSourceDataset(next); setEntity(openedSource.entity); setConversation("implementation");
      setShown(true);
      if (!element.open) { invoker = context?.invoker ?? document.activeElement as HTMLElement; element.showModal(); }
    };
    const close = () => { setShown(false); if (invoker?.isConnected) invoker.focus(); };
    window.addEventListener("pharos:open-data-guide", show);
    element.addEventListener("close", close);
    return () => { window.removeEventListener("pharos:open-data-guide", show); element.removeEventListener("close", close); };
  }, []);
  const selected = dataEntities[entity];
  const mark = (target: DataEntity) => entity === target ? " is-selected" : "";
  const review = conversation === "review";
  return <dialog data-view-field="guide_open" ref={dialog} className="data-guide" aria-labelledby="dataGuideTitle" onClick={event => {
    if (event.target !== event.currentTarget) return;
    const rect = event.currentTarget.getBoundingClientRect();
    if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) event.currentTarget.close();
  }}>
    <header className="data-guide-header">
      <h2 id="dataGuideTitle">Data Guide</h2>
      <nav className="data-guide-layers" aria-label="Data layers">
        {(Object.keys(dataEntities) as DataEntity[]).map((key, index) => <React.Fragment key={key}>
          {index ? <Icon name="chevron-right" /> : null}<button data-view-field="guide_entity" type="button" aria-pressed={entity === key} onClick={() => setEntity(key)}>{dataEntities[key].label}</button>
        </React.Fragment>)}
      </nav>
      <button type="button" className="data-guide-close" aria-label="Close data guide" onClick={() => dialog.current?.close()}><Icon name="close" /></button>
    </header>
    <div className="data-guide-body">
      <div className="data-guide-explainer">
        <div className="data-guide-example">
          <div className={`data-guide-workspace${mark("workspaces")}`}>
            <button data-view-field="guide_entity" type="button" className="data-guide-workspace-title" aria-label="Explain workspaces" onClick={() => setEntity("workspaces")}><span className="data-guide-folder" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M3 7a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v10H3Z" /></svg></span><span><small>WORKSPACE</small><strong>Add search to the app</strong></span></button>
            <div className="data-guide-chat-tabs" role="group" aria-label="Example conversations">
              <button type="button" aria-pressed={!review} onClick={() => { setConversation("implementation"); setEntity("conversations"); }}><span aria-hidden="true">▤</span> Implementation</button>
              <button type="button" aria-pressed={review} onClick={() => { setConversation("review"); setEntity("conversations"); }}><span aria-hidden="true">▤</span> Review</button>
              <span className="data-guide-tab-spacer" aria-hidden="true">···</span>
            </div>
            <div className={`data-guide-transcript${entity === "conversations" || entity === "agents" ? " is-selected" : ""}`}>
              <div className="data-guide-chat-brand" aria-hidden="true"><strong>ChatGPT <Icon name="chevron-down" /></strong><Icon name="more" /></div>
              <button data-view-field="guide_entity" type="button" className={`data-guide-message human${mark("messages")}`} onClick={() => setEntity("messages")}><span className="data-guide-avatar">Y</span><span><small>YOU <span>Human message</span></small><span>{review ? "Check the search change for edge cases." : "Add search to the library."}</span></span></button>
              <button data-view-field="guide_entity" type="button" className={`data-guide-message${mark("messages")}`} onClick={() => setEntity("messages")}><span className="data-guide-avatar agent" aria-hidden="true">✳</span><span><small>ASSISTANT <span>Response</span></small><span>I’ll ask two helpers to check this.</span></span></button>
              <div className={`data-guide-agent-branch${mark("agents")}`} role="group" aria-label="Test helper session">
                <button data-view-field="guide_entity" type="button" className="data-guide-delegation" onClick={() => setEntity("agents")}><span aria-hidden="true">↳</span><span><strong>Test helper</strong><small>Sub-agent session</small></span><Icon name="arrow-right" /></button>
                <button data-view-field="guide_entity" type="button" className={`data-guide-tool${mark("tool_calls")}`} onClick={() => setEntity("tool_calls")}>
                  <span className="data-guide-tool-heading"><span><span aria-hidden="true">⌘</span> exec_command</span><span>Tool call</span></span>
                  <span className="data-guide-tool-line"><small>INPUT</small><code>{review ? "npm test -- search" : "npm test"}</code></span>
                  <span className="data-guide-tool-line"><small>RESULT</small><span><span className="data-guide-success">✓</span> 12 tests passed <span className="data-guide-duration">· 1.4s</span></span></span>
                </button>
              </div>
              <div className={`data-guide-agent-branch${mark("agents")}`} role="group" aria-label="Review helper session">
                <button data-view-field="guide_entity" type="button" className="data-guide-delegation" onClick={() => setEntity("agents")}><span aria-hidden="true">↳</span><span><strong>Review helper</strong><small>Sub-agent session</small></span><Icon name="arrow-right" /></button>
                <button data-view-field="guide_entity" type="button" className={`data-guide-helper-message${mark("messages")}`} onClick={() => setEntity("messages")}>{review ? "No keyboard issues found." : "Empty queries look good."}</button>
              </div>
              <div className="data-guide-chat-composer" aria-hidden="true"><span>Message ChatGPT</span><span>↑</span></div>
            </div>
          </div>
          <span className="data-guide-example-label">EXAMPLE</span>
        </div>
        <div className="data-guide-details">
          <aside id={`data-guide-${entity}`} className="data-guide-inspector" aria-live="polite" aria-atomic="true">
            <h3>{selected.singular}</h3>
            <p>{selected.description}</p>
          </aside>
          {entity === source.entity ? <section className="data-guide-query" aria-label="Dataset row definitions">
            <div className="data-guide-query-heading"><span>THIS QUERY READS</span><strong>{source.label}</strong></div>
            <p className="data-guide-query-grain">{source.grain}.</p>
          </section> : null}
        </div>
      </div>
    </div>
  </dialog>;
}
