import { readViewState, writeViewState } from "./view-state";
import { sidebarWidth } from "./workspace-inbox-model";
import { preferences } from "./preferences";
import { focusPageContent } from "./page-focus";
import "./workspace-inbox.css";

type InboxProps = {
  root: HTMLElement;
  workspace: string;
  title: string;
  overview: HTMLElement[];
  reader: HTMLElement;
  onOverview: () => void;
  onCompact: (compact: boolean) => void;
};
export type WorkspaceInbox = {
  workspace: string;
  element: HTMLElement;
  overview: HTMLElement;
  reader: HTMLElement;
  heading: HTMLElement;
  // The overview's content, which leads the reader while merged.
  lead: HTMLElement;
  viewing: boolean;
  // A workspace with one conversation and no delegation hierarchy has nothing
  // to navigate: its overview and conversation read as one page.
  merged: boolean;
  setViewer: (viewing: boolean) => void;
  setMerged: (merged: boolean) => void;
  showSessions: () => void;
  destroy: () => void;
};

function button(text: string, label = text) {
  const element = document.createElement("button");
  element.type = "button";
  element.textContent = text;
  element.setAttribute("aria-label", label);
  return element;
}

function mountInbox(props: InboxProps): WorkspaceInbox {
  const { root, reader } = props;
  const page = root.closest<HTMLElement>(".view")!;
  const element = document.createElement("div");
  element.className = "workspace-inbox";
  const overview = document.createElement("section");
  overview.className = "workspace-overview";
  overview.setAttribute("aria-label", "Workspace overview");
  const overviewContent = document.createElement("div");
  overviewContent.className = "workspace-overview-content";
  const navBar = document.createElement("div");
  navBar.className = "workspace-nav-bar";
  const libraryBack = page.querySelector<HTMLElement>("#back");
  const fullOverview = button("← Workspace overview", "Open workspace overview");
  const close = button("×", "Close workspace sessions");
  fullOverview.onclick = props.onOverview;
  navBar.append(fullOverview, close);
  if (libraryBack) overviewContent.append(libraryBack);
  overviewContent.append(navBar, ...props.overview);
  overview.append(overviewContent);
  const divider = button("", "Resize workspace sessions sidebar");
  divider.className = "workspace-divider";divider.dataset.viewField="sidebar_width";
  divider.setAttribute("role", "separator");
  divider.setAttribute("aria-orientation", "vertical");
  divider.title = "Drag to resize · arrow keys resize · double-click resets";
  const viewer = document.createElement("section");
  viewer.className = "workspace-reader";
  viewer.setAttribute("aria-label", "Conversation viewer");
  const bar = document.createElement("header");
  bar.className = "workspace-reader-bar";
  const breadcrumb = button("←", "Open workspace overview");
  breadcrumb.className = "workspace-reader-workspace";
  breadcrumb.title = `Workspace overview · ${props.title}`;
  breadcrumb.onclick = props.onOverview;
  const heading = document.createElement("div");
  heading.className = "workspace-reader-heading";
  const sessions = button("Workspace Sessions", "Show workspace sessions");
  sessions.className = "workspace-sessions-button";
  sessions.setAttribute("aria-expanded", "false");
  const backdrop = button("", "Close workspace sessions");
  backdrop.className = "workspace-nav-backdrop";
  bar.append(breadcrumb, heading, sessions);
  reader.classList.add("workspace-reader-content");
  viewer.append(bar, reader);
  element.append(overview, divider, viewer, backdrop);
  root.replaceChildren(element);
  page.classList.add("workspace-page");
  const mobile = matchMedia("(max-width: 959px)");
  const prefs = preferences();
  let width = sidebarWidth(Number(readViewState("sidebar_width", String(prefs.get("pharos-workspace-sidebar-width", 400)))));
  let hidden = !readViewState("sidebar_visible", true);
  let sheet = false;
  let lastCompact = false;
  let overviewScroll = 0;
  let navScroll = 0;
  let disposed = false;
  let drag: { pointer: number; x: number; width: number } | null = null;
  function clamped(value: number) {
    return sidebarWidth(value, Math.min(element.clientWidth * .6, element.clientWidth - 520));
  }
  function update() {
    if (disposed) return;
    if (page.classList.contains("active")) {writeViewState("sidebar_visible", !hidden); writeViewState("sidebar_width", String(width));}
    const viewing = api.viewing;
    const split = viewing && !api.merged;
    const visible = split && (mobile.matches ? sheet : !hidden);
    element.classList.toggle("viewing", viewing);
    element.classList.toggle("merged", api.merged);
    element.classList.toggle("navigator-hidden", split && !mobile.matches && hidden);
    element.classList.toggle("sessions-open", split && mobile.matches && sheet);
    element.style.setProperty("--workspace-nav-width", `${clamped(width)}px`);
    const compact = split && (mobile.matches || (!hidden && clamped(width) < 720));
    element.classList.toggle("compact", compact);
    viewer.hidden = !viewing;
    divider.hidden = !split || mobile.matches || hidden;
    overview.inert = api.merged || split && mobile.matches && !sheet || split && !mobile.matches && hidden;
    viewer.inert = split && mobile.matches && sheet;
    if (split && mobile.matches && sheet) {
      overview.setAttribute("role", "dialog");
      overview.setAttribute("aria-modal", "true");
      overview.setAttribute("aria-label", "Workspace sessions");
    } else {
      overview.removeAttribute("role");
      overview.removeAttribute("aria-modal");
      overview.setAttribute("aria-label", "Workspace overview");
    }
    sessions.setAttribute("aria-expanded", String(visible));
    sessions.setAttribute("aria-label", visible ? "Hide workspace sessions" : "Show workspace sessions");
    sessions.textContent = visible ? "Hide sessions" : "Workspace Sessions";
    divider.setAttribute("aria-valuemin", "300");
    divider.setAttribute("aria-valuemax", String(Math.round(clamped(element.clientWidth))));
    divider.setAttribute("aria-valuenow", String(Math.round(clamped(width))));
    document.body.classList.toggle("workspace-inbox-overview", !viewing);
    document.body.classList.toggle("workspace-inbox-viewing", viewing);
    document.body.classList.toggle("workspace-session-sheet-open", split && mobile.matches && sheet);
    const narrow = viewing && viewer.clientWidth < 700;
    element.classList.toggle("reader-narrow", narrow);
    document.body.classList.toggle("workspace-reader-narrow", narrow);
    if (viewing) {
      const top = api.merged ? viewer.getBoundingClientRect().top : bar.getBoundingClientRect().bottom;
      document.documentElement.style.setProperty("--conversation-rail-top", `${Math.ceil(top + 5)}px`);
      document.documentElement.style.setProperty("--workspace-reader-width", `${viewer.clientWidth}px`);
    }
    if (compact !== lastCompact) {
      lastCompact = compact;
      props.onCompact(compact);
    }
  }
  const restoreView = () => {width=sidebarWidth(Number(readViewState("sidebar_width",String(width))));hidden=!readViewState("sidebar_visible",!hidden);update();};
  window.addEventListener("popstate",restoreView);window.addEventListener("pharos:view",restoreView);
  function closeSheet() {
    if (mobile.matches) sheet = false;
    else hidden = true;
    update();
    sessions.focus({ preventScroll: true });
  }
  const api: WorkspaceInbox = {
    workspace: props.workspace,
    element,
    overview,
    reader,
    heading,
    lead: overviewContent,
    viewing: false,
    merged: false,
    setViewer(viewing) {
      if (viewing !== api.viewing) {
        if (viewing) overviewScroll = overview.scrollTop;
        else navScroll = overview.scrollTop;
        api.viewing = viewing;
        sheet = false;
        update();
        requestAnimationFrame(() => {
          if (disposed) return;
          overview.scrollTop = viewing ? navScroll : overviewScroll;
          if (!viewing) focusPageContent();
        });
      } else if (viewing && sheet) {
        sheet = false;
        update();
      }
    },
    setMerged(merged) {
      if (merged === api.merged) return;
      api.merged = merged;
      if (merged) reader.prepend(overviewContent);
      else overview.append(overviewContent);
      update();
    },
    showSessions() {
      if (!api.viewing || api.merged) return;
      hidden = false;
      sheet = mobile.matches;
      update();
      if (mobile.matches) close.focus({ preventScroll: true });
      requestAnimationFrame(() => {
        if (disposed || !api.viewing || (mobile.matches ? !sheet : hidden)) return;
        const selected = overview.querySelector<HTMLElement>(".delegation-inbox-row.current, .delegation-lane-current, .delegation-current");
        selected?.scrollIntoView({ block: "nearest" });
        if (!mobile.matches) (selected?.querySelector<HTMLElement>(".delegation-name") || fullOverview).focus({ preventScroll: true });
      });
    },
    destroy() {
      disposed = true;window.removeEventListener("popstate",restoreView);window.removeEventListener("pharos:view",restoreView);
      observer.disconnect();
      mobile.removeEventListener("change", update);
      window.removeEventListener("resize", update);
      document.removeEventListener("keydown", key);
      page.classList.remove("workspace-page");
      document.body.classList.remove("workspace-inbox-overview", "workspace-inbox-viewing", "workspace-reader-narrow", "workspace-session-sheet-open");
      if (libraryBack) page.insertBefore(libraryBack, root);
    },
  };
  sessions.dataset.viewField="sidebar_visible";
  sessions.onclick = () => {
    if (mobile.matches ? sheet : !hidden) closeSheet();
    else api.showSessions();
  };
  close.onclick = backdrop.onclick = closeSheet;
  divider.onpointerdown = event => {
    if (event.button !== 0) return;
    drag = { pointer: event.pointerId, x: event.clientX, width: clamped(width) };
    element.classList.add("resizing");
    divider.setPointerCapture(event.pointerId);
    event.preventDefault();
  };
  divider.onpointermove = event => {
    if (!drag || drag.pointer !== event.pointerId) return;
    width = clamped(drag.width + event.clientX - drag.x);
    update();
  };
  const finish = () => {
    if (!drag) return;
    drag = null;
    element.classList.remove("resizing");
    prefs.set("pharos-workspace-sidebar-width", width);
  };
  divider.onpointerup = divider.onpointercancel = finish;
  divider.ondblclick = () => { width = 400; prefs.set("pharos-workspace-sidebar-width", width); update(); };
  divider.onkeydown = event => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    width = clamped(event.key === "Home" ? 300 : event.key === "End" ? element.clientWidth : width + (event.key === "ArrowLeft" ? -1 : 1) * (event.shiftKey ? 80 : 20));
    prefs.set("pharos-workspace-sidebar-width", width);
    update();
  };
  function key(event: KeyboardEvent) {
    if (!sheet || !mobile.matches || !api.viewing || !page.classList.contains("active")) return;
    if (event.key === "Escape") {
      if (document.querySelector(".delegation-event-detail")) return;
      event.preventDefault();
      closeSheet();
    } else if (event.key === "Tab") {
      const scope = document.querySelector<HTMLElement>(".delegation-event-detail") || overview;
      const controls = [...scope.querySelectorAll<HTMLElement>('button:not(:disabled), a[href], input, select, summary, [tabindex="0"]')].filter(el => el.getClientRects().length);
      const first = controls[0], last = controls.at(-1);
      if (!scope.contains(document.activeElement)) { event.preventDefault(); (event.shiftKey ? last : first)?.focus(); }
      else if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
    }
  }
  const observer = new ResizeObserver(update);
  observer.observe(element);
  observer.observe(viewer);
  observer.observe(bar);
  mobile.addEventListener("change", update);
  window.addEventListener("resize", update);
  document.addEventListener("keydown", key);
  update();
  return api;
}

declare global {
  interface Window {
    pharosInbox?: { mount: (props: InboxProps) => WorkspaceInbox };
  }
}
export function installWorkspaceInbox() {
  window.pharosInbox = { mount: mountInbox };
}
