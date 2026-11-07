import { useViewEntry, useViewState } from "./view-state";
import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { QueryBuilder, QueryFilterRow, type QueryBuilderProps } from "@pythia-software/query-table-ui";
import type { QueryFilterApi } from "@pythia-software/query-table-react";
import { Icon } from "./icons";
import { dataEntities, dataEntityGroups, dataGuideSelectionKeys, dataSources, describeDataSource, resolveGuideEntity, type DataEntity, type DataGuideSelection, type DataSource } from "./data-taxonomy-model";
import { DataGuideExample, guideSceneForEntity } from "./data-guide-examples";
import { readConcurrencySettings } from "./concurrency-settings";
import { NavigationItems } from "./app-navigation";
import { toggleNavigationSection, type NavigationExpansion } from "./app-navigation-model";
import { DashboardSelect } from "./dashboard-select";
import "./data-taxonomy.css";

// Any page (including the classic shell) can open the single shared guide.
export function openDataTaxonomy(dataset?: DataSource, invoker?: HTMLElement, entity?: DataGuideSelection) {
  window.dispatchEvent(new CustomEvent("pharos:open-data-guide", { detail: { dataset, invoker, entity } }));
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
    <a className="data-source-link" href={`#data-guide-${source.entity}`} title={source.grain} aria-description={source.grain} aria-haspopup="dialog" onClick={event => { event.preventDefault(); openDataTaxonomy(dataset, event.currentTarget); }}>{source.label}<Icon name="info" /></a>
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
    {dataset !== "findings" && dataset !== "safety" && dataset !== "fixes" ? <SourceStatement dataset={dataset} /> : null}
    {whereRow ?? <QueryFilterRow api={sourceFilters} fields={props.api.query.grouping ? props.api.grouping!.source.schema.fields : props.fields} />}
  </>} /></div>;
}

export function DataTaxonomyModal() {
  const dialog = useRef<HTMLDialogElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const termNavigation = useRef<HTMLElement>(null);
  const termPicker = useRef<HTMLLabelElement>(null);
  const previousFocus = useRef<HTMLElement | null>(null);
  const [,setSourceDataset]=useViewState<DataSource>("guide_dataset","library");
  const [selection, setEntity] = useViewState<DataGuideSelection>("guide_entity", "conversations");
  const entity = resolveGuideEntity(selection);
  const scene = guideSceneForEntity(entity);
  const [conversation, setConversation] = useViewState("guide_example", "implementation");
  const [expanded, setExpanded] = useViewState<NavigationExpansion>("nav_expanded", {});
  const pages = dataEntityGroups.map(group => ({ id: group.id, label: group.label, children: group.entities.map(key => ({ id: key, label: dataEntities[key].label, href: `#data-guide-${key}` })) }));
  const [shown,setShown] = useViewState("guide_open",false);
  const rememberFocus = () => {
    const active = document.activeElement;
    previousFocus.current = active instanceof HTMLElement && dialog.current?.contains(active) ? active : null;
  };
  const selectEntity = (next: DataEntity) => { if (next === entity) return; rememberFocus(); setEntity(next); };
  const selectConversation = (next: "implementation" | "review") => { if (next === conversation && entity === "conversations") return; rememberFocus(); setConversation(next); setEntity("conversations"); };
  useLayoutEffect(() => {
    const previous = previousFocus.current;
    previousFocus.current = null;
    const element = dialog.current;
    // An illustration can remove its own button when it changes scenes. Keep
    // a deliberate selection inside the dialog, but leave surviving focus and
    // URL restoration alone.
    if (!previous || previous.isConnected || !element?.open || element.contains(document.activeElement)) return;
    const selectedTerm = termNavigation.current?.querySelector<HTMLButtonElement>('button[aria-current="page"]');
    // A collapsed menu branch hides the selected term. Keep that choice of
    // expansion and return to its visible summary; phones use the term picker.
    const groupSummary = selectedTerm?.closest("details")?.querySelector<HTMLElement>(":scope > summary");
    for (const target of [selectedTerm, groupSummary, termPicker.current?.querySelector("select"), element.querySelector<HTMLElement>(".data-guide-close")]) {
      if (!target?.getClientRects().length) continue;
      target.focus();
      // Closed details can retain layout rectangles even though their children
      // refuse focus. Keep looking until a reachable control accepts it.
      if (document.activeElement === target) break;
    }
  }, [entity, conversation]);
  useEffect(()=>{const element=dialog.current!;if(shown&&!element.open)element.showModal();else if(!shown&&element.open)element.close();},[shown]);
  useEffect(() => {
    const element = dialog.current!;
    let invoker: HTMLElement | null = null;
    const show = (event: Event) => {
      const context = (event as CustomEvent<{ dataset?: string; entity?: DataGuideSelection; invoker?: HTMLElement }>).detail;
      const requested = context?.dataset;
      const next = requested && Object.hasOwn(dataSources, requested) ? requested as DataSource : undefined;
      setSourceDataset(next ?? "library");
      setEntity(context?.entity && dataGuideSelectionKeys.includes(context.entity) ? context.entity : next ? describeDataSource(next, readConcurrencySettings().counting).entity : "conversations");
      setConversation("implementation");
      setShown(true);
      if (!element.open) { invoker = context?.invoker ?? document.activeElement as HTMLElement; element.showModal(); }
    };
    const close = () => { previousFocus.current = null; setShown(false); if (invoker?.isConnected) invoker.focus(); };
    window.addEventListener("pharos:open-data-guide", show);
    element.addEventListener("close", close);
    return () => { window.removeEventListener("pharos:open-data-guide", show); element.removeEventListener("close", close); };
  }, []);
  useEffect(() => { dialog.current!.dataset.ready = "true"; window.dispatchEvent(new Event("pharos:data-guide-ready")); }, []);
  useEffect(() => { content.current?.scrollTo({ top: 0 }); }, [scene]);
  useEffect(() => {
    if (!shown) return;
    const menuItem = termNavigation.current?.querySelector<HTMLElement>('[aria-current="page"]');
    if (menuItem?.getClientRects().length) menuItem.scrollIntoView({ block: "nearest", inline: "nearest" });
    const active = document.activeElement;
    const invoked = active instanceof HTMLElement && content.current?.contains(active) ? active.closest<HTMLElement>(".pharos-guide-highlight") : null;
    // Repeated messages and usage receipts can occur far apart. A link in the
    // story keeps attention there; selecting a guide term starts at its first example.
    if (entity === "agents" && !invoked) {
      const history = content.current?.querySelector<HTMLElement>(".guide-chat-history");
      const helper = history?.querySelector<HTMLElement>(".guide-helper-description");
      if (history && helper) history.scrollTo({ top: history.scrollTop + helper.getBoundingClientRect().top - history.getBoundingClientRect().top - 16 });
      content.current?.querySelector(".guide-main-thread")?.scrollIntoView({ block: "start", inline: "nearest" });
      return;
    }
    const target = invoked ?? content.current?.querySelector<HTMLElement>(".pharos-guide-highlight");
    target?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [entity, shown]);
  const selected = dataEntities[entity];
  return <dialog data-view-field="guide_open" ref={dialog} className="data-guide" aria-labelledby="dataGuideTitle" onClick={event => {
    if (event.target !== event.currentTarget) return;
    const rect = event.currentTarget.getBoundingClientRect();
    if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) event.currentTarget.close();
  }}>
    <header className="data-guide-header">
      <h2 id="dataGuideTitle">Data Guide</h2>
      <button type="button" className="data-guide-close" aria-label="Close data guide" onClick={() => dialog.current?.close()}><Icon name="close" /></button>
    </header>
    <div className="data-guide-body">
      <nav ref={termNavigation} className="data-guide-terms app-navigation-list" aria-label="Data Guide terms">
        <NavigationItems pages={pages} current={entity} navigate={() => {}} expanded={expanded["data-guide"] ?? {}} toggle={(id, fallback) => setExpanded(state => toggleNavigationSection(state, "data-guide", id, fallback))} renderLeaf={(page, style) => <button data-view-field="guide_entity" type="button" className="app-navigation-page" style={style} aria-current={entity === page.id ? "page" : undefined} onClick={() => selectEntity(page.id as DataEntity)}><span className="app-navigation-label">{page.label}</span></button>} />
      </nav>
      <div ref={content} className="data-guide-content">
        <label ref={termPicker} className="data-guide-picker">Guide term<DashboardSelect data-view-field="guide_entity" value={entity} onChange={event => selectEntity(event.target.value as DataEntity)}>{dataEntityGroups.map(group => <optgroup key={group.label} label={group.label}>{group.entities.map(key => <option key={key} value={key}>{dataEntities[key].label}</option>)}</optgroup>)}</DashboardSelect></label>
        <aside id={`data-guide-${entity}`} className="data-guide-inspector" aria-live="polite" aria-atomic="true">
          <h3>{selected.singular}</h3>
          <p>{selected.description}</p>
        </aside>
        {scene === "machines" ? <p className="muted data-guide-detail">{selected.detail}</p> : null}
        <DataGuideExample entity={entity} onSelect={selectEntity} conversation={conversation} onConversationChange={selectConversation} />
        {scene !== "machines" ? <p className="muted data-guide-detail">{selected.detail}</p> : null}
      </div>
    </div>
  </dialog>;
}
