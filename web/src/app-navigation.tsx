import { useViewState } from "./view-state";
import React, { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { createRoot } from "react-dom/client";
import { appLocation, appLocationLabel, appSections, isNavigationSectionExpanded, navigationPageURL, toggleNavigationSection, type AppSection, type NavigationExpansion } from "./app-navigation-model";
import { localPageNavigation, nestedPageTrail, type NestedPage } from "./nested-pages-model";
import { PageLink, useLocationRevision } from "./nested-pages";
import { Icon } from "./icons";
import { preferences } from "./preferences";
import { focusPageContent } from "./page-focus";
import { attentionBySource, attentionLabel, withLogSources } from "./logs-model";
import { useLogsSummary } from "./logs-summary";
import "./app-shell.css";

type Layout = "wide" | "compact" | "phone";
const readLayout = () => getComputedStyle(document.documentElement).getPropertyValue("--app-shell-layout").trim() as Layout || "wide";

export function NavigationItems({ pages, current, navigate, expanded, toggle, renderLeaf, depth = 0 }: {
  pages: readonly NestedPage[]; current: string; navigate: () => void; expanded: NavigationExpansion[string]; toggle: (id: string, defaultOpen: boolean) => void;
  depth?: number; renderLeaf?: (page: NestedPage, style: React.CSSProperties) => React.ReactNode;
}) {
  const active = nestedPageTrail(pages, current)[0]?.id;
  // Keep rows full width, with a bounded inset so deeper trees leave room for labels.
  const style = { "--app-navigation-depth": Math.min(depth, 3) } as React.CSSProperties;
  return <>{pages.map(page => page.children ? <details key={page.id} open={isNavigationSectionExpanded(expanded, page.id, depth === 0 || page.id === active)}>
    <summary style={style} data-current-path={page.id === active || undefined} onClick={event => { event.preventDefault(); toggle(page.id, depth === 0 || page.id === active); }}><Icon name="chevron-down" className="app-navigation-chevron" /><span className="app-navigation-label">{page.label}</span><AttentionMark attention={page.attention} /></summary>
    <div className="app-navigation-children"><NavigationItems pages={page.children} current={current} navigate={navigate} expanded={expanded} toggle={toggle} renderLeaf={renderLeaf} depth={depth + 1} /></div>
  </details> : renderLeaf ? <React.Fragment key={page.id}>{renderLeaf(page, style)}</React.Fragment> : <NavigationLink key={page.id} className="app-navigation-page" style={style} href={page.href} current={page.id === current} navigate={navigate}><span className="app-navigation-label">{page.label}</span><AttentionMark attention={page.attention} /></NavigationLink>)}</>;
}

function AttentionMark({ attention }: { attention?: NestedPage["attention"] }) {
  if (!attention) return null;
  return <span className={`app-navigation-attention ${attention}`} title={attentionLabel(attention)}><Icon name={attention === "error" ? "error" : "severity-high"} /><span className="visually-hidden">{attentionLabel(attention)}</span></span>;
}

function NavigationLink({ navigate, ...props }: React.ComponentProps<typeof PageLink> & { navigate: () => void }) {
  return <PageLink {...props} onClick={event => {
    if (localPageNavigation(props.href, location.href, { ...event, target: event.currentTarget.target, download: event.currentTarget.hasAttribute("download") })) navigate();
  }} />;
}

function AppNavigation({ settings, contentNavigation }: { settings: NestedPage[]; contentNavigation: HTMLElement }) {
  const revision = useLocationRevision();
  const [layout, setLayout] = useState(readLayout);
  const [badge, setBadge] = useState("");
  const [collapsed, setCollapsed] = useViewState("nav_collapsed", () => preferences().get("pharos-sidebar-collapsed", false));
  const [open, setOpen] = useState(false), [branch, setBranch] = useState("");
  const dialog = useRef<HTMLDialogElement>(null), trigger = useRef<HTMLButtonElement>(null);
  const navigating = useRef(false), previousURL = useRef(navigationPageURL(location.href));
  const params = new URLSearchParams(location.search), selected = appLocation(location.pathname, params);
  const logs = useLogsSummary(selected.section === "settings");
  const sections = appSections(withLogSources(settings, attentionBySource(logs)), params);
  const { section, page } = appLocationLabel(sections, selected);
  const [expanded, setExpanded] = useViewState<NavigationExpansion>("nav_expanded", {});
  const toggle = (section: string, id: string, defaultOpen: boolean) => setExpanded(state => toggleNavigationSection(state, section, id, defaultOpen));
  const activeBranch = sections.find(item => item.id === branch) ?? section;
  const hasSidebar = layout !== "phone" && section.pages.length > 0;
  const rail = hasSidebar && !collapsed;
  // The page's offsets follow the sidebar through an attribute on <main>, not
  // main:has(...): a :has() on main makes every attribute change in the page,
  // such as opening a transcript turn, re-check or restyle all of it.
  // Sync after every commit, writing only a change.
  useLayoutEffect(() => {
    const main = contentNavigation.parentElement!, value = hasSidebar ? rail ? "open" : "collapsed" : undefined;
    if (main.dataset.appSidebar === value) return;
    if (value) main.dataset.appSidebar = value;
    else delete main.dataset.appSidebar;
  });

  useEffect(() => {
    const header = document.querySelector<HTMLElement>("body > header")!;
    const resize = () => {
      setLayout(readLayout());
      document.documentElement.style.setProperty("--app-header-height", `${header.offsetHeight}px`);
    };
    const observer = new ResizeObserver(resize);
    observer.observe(header);
    window.addEventListener("resize", resize);
    window.addEventListener("pharos:header-layout", resize);
    resize();
    return () => { observer.disconnect(); window.removeEventListener("resize", resize); window.removeEventListener("pharos:header-layout", resize); };
  }, []);
  useEffect(() => { window.dispatchEvent(new Event("resize")); }, [rail]);
  useEffect(() => {
    const sync = () => {
      const count = document.getElementById("findingsBadge");
      setBadge(count && !count.hidden ? count.textContent ?? "" : "");
    };
    const observer = new MutationObserver(sync);
    observer.observe(document.querySelector("body > header .tabs")!, { subtree: true, attributes: true, childList: true, characterData: true });
    sync();
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    const url = navigationPageURL(location.href);
    if (previousURL.current === url && !navigating.current) return;
    previousURL.current = url;
    dialog.current?.close();
    setOpen(false);
    if (navigating.current) {
      navigating.current = false;
      requestAnimationFrame(() => {
        focusPageContent();
        document.querySelector("main")?.scrollTo({ top: 0, left: 0 });
      });
    }
  }, [revision]);
  useEffect(() => {
    if (layout !== "phone" && open) { dialog.current?.close(); setOpen(false); }
    const focused = document.activeElement as HTMLElement | null;
    if (focused && !focused.getClientRects().length && focused.closest("#appNavigationRail, #appLocation, body > header .tabs")) {
      const fallback = trigger.current ?? document.querySelector<HTMLElement>("body > header .tabs button.active");
      fallback?.focus({ preventScroll: true });
    }
  }, [layout, open]);
  const navigate = () => { navigating.current = true; dialog.current?.close(); setOpen(false); };
  const close = () => { dialog.current?.close(); setOpen(false); };
  const toggleSidebar = () => {
    preferences().set("pharos-sidebar-collapsed", !collapsed);
    setCollapsed(!collapsed);
  };
  const show = () => {
    setBranch(section.pages.length ? section.id : "root");
    setOpen(true);
    // Native dialog owns focus, Escape, and inertness behind the switcher.
    dialog.current?.showModal();
  };
  useEffect(() => {
    if (!open || !dialog.current) return;
    const visible = (element: HTMLElement) => element.checkVisibility();
    const current = Array.from(dialog.current.querySelectorAll<HTMLElement>('.app-navigation-pages [aria-current="page"]')).find(visible);
    const ancestor = Array.from(dialog.current.querySelectorAll<HTMLElement>('summary[data-current-path]')).reverse().find(visible);
    const close = Array.from(dialog.current.querySelectorAll<HTMLElement>('.app-navigation-close')).find(visible);
    (current ?? ancestor ?? close)?.focus();
  }, [open, branch]);
  const sectionLabel = (item: AppSection) => <>{item.label}{item.id === "findings" && badge ? <span className="app-navigation-badge">{badge}</span> : null}</>;
  const pickerSections = sections.map(item => item.pages.length
    ? <button type="button" key={item.id} aria-current={item.id === activeBranch.id ? "page" : undefined} onClick={() => setBranch(item.id)}>{sectionLabel(item)}<Icon name="chevron-right" /></button>
    : <NavigationLink key={item.id} href={item.href} current={item.id === section.id} navigate={navigate}>{sectionLabel(item)}</NavigationLink>);
  const path = <span className="app-location-path"><span>{section.label}</span>{page ? <><span className="app-location-separator">/</span><strong>{page.label}</strong></> : selected.section === "library" && location.pathname.startsWith("/work/") ? <><span className="app-location-separator">/</span><strong>{location.pathname.includes("/conversation/") ? "Conversation" : "Workspace"}</strong></> : null}</span>;
  return <>
    <button ref={layout === "phone" ? trigger : undefined} id="appNavigationToggle" className="app-location-switcher" type="button" aria-haspopup="dialog" aria-controls="appNavigationDialog" aria-expanded={open} onClick={show}>{path}<Icon name="chevron-down" /></button>
    {createPortal(<>{hasSidebar ? <button ref={trigger} id="appSidebarToggle" data-view-field="nav_collapsed" className="app-sidebar-toggle" type="button" aria-label={rail ? "Collapse sidebar" : "Expand sidebar"} title={rail ? "Collapse sidebar" : "Expand sidebar"} aria-controls="appNavigationRail" aria-expanded={rail} onClick={toggleSidebar}><Icon name="sidebar" /></button> : null}
      <aside id="appNavigationRail" data-view-field="nav_expanded" className="app-navigation-rail" aria-label={`${section.label} navigation`} hidden={!rail}><div className="app-navigation-rail-head"><h2 className="app-navigation-rail-title">{section.label}</h2></div><nav className="app-navigation-list" aria-label={`${section.label} pages`}>
      <NavigationItems pages={section.pages} current={selected.current} navigate={navigate} expanded={expanded[section.id] ?? {}} toggle={(id, defaultOpen) => toggle(section.id, id, defaultOpen)} />
    </nav></aside></>, contentNavigation)}
    {createPortal(<dialog ref={dialog} id="appNavigationDialog" className={`app-navigation-dialog ${branch === "root" ? "app-navigation-root" : ""}`} aria-label="Navigate Pharos" onClose={() => { setOpen(false); if (!navigating.current) trigger.current?.focus({ preventScroll: true }); }} onClick={event => {
      if (event.target !== dialog.current) return;
      const rect = dialog.current.getBoundingClientRect();
      if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) close();
    }}>
      <div className="app-navigation-sections"><div className="app-navigation-dialog-title">Pharos</div><nav className="app-navigation-list" aria-label="All pages">{pickerSections}</nav></div>
      <div className="app-navigation-pages"><div className="app-navigation-dialog-head"><button className="app-navigation-all" type="button" onClick={() => setBranch("root")}><Icon name="arrow-left" />All pages</button><strong>{activeBranch.label}</strong><button className="app-navigation-close" type="button" aria-label="Close navigation" onClick={close}><Icon name="close" /></button></div><nav className="app-navigation-list" aria-label={`${activeBranch.label} pages`}><NavigationItems pages={activeBranch.pages} current={activeBranch.id === section.id ? selected.current : ""} navigate={navigate} expanded={expanded[activeBranch.id] ?? {}} toggle={(id, defaultOpen) => toggle(activeBranch.id, id, defaultOpen)} /></nav></div>
      {branch === "root" ? <button className="app-navigation-root-close app-navigation-close" type="button" aria-label="Close navigation" onClick={close}><Icon name="close" /></button> : null}
    </dialog>, document.body)}
  </>;
}

export function installAppNavigation() {
  const host = document.getElementById("appLocation");
  if (!host) return;
  const settings = Array.from(document.querySelectorAll<HTMLElement>("[data-settings-route]"), panel => ({ id: panel.dataset.settingsRoute!, href: panel.dataset.settingsRoute!, label: panel.dataset.pageTitle!, description: panel.dataset.pageDescription }));
  const contentNavigation = document.createElement("div");
  contentNavigation.className = "app-content-navigation";
  document.querySelector("main")!.prepend(contentNavigation);
  document.documentElement.classList.add("app-shell");
  createRoot(host).render(<AppNavigation settings={settings} contentNavigation={contentNavigation} />);
  window.dispatchEvent(new Event("resize"));
}
