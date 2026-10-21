import React, { useEffect, useId, useRef, useState } from "react";
import { Icon } from "./icons";
import { localPageNavigation, nestedPageTrail, type NestedPage } from "./nested-pages-model";
import "./nested-pages.css";

// Both classic-script hosts and React pages use the app router. Subscribe to
// its notifications in one place so Back/Forward and in-app links agree.
export function useLocationRevision() {
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const changed = () => setRevision(value => value + 1);
    const events = ["pharos:view", "pharos:route", "pharos:uri-changed", "pharos:library-filter"];
    events.forEach(event => window.addEventListener(event, changed));
    return () => events.forEach(event => window.removeEventListener(event, changed));
  }, []);
  return revision;
}

export function PageLink({ href, children, current, onClick, ...props }: React.ComponentPropsWithoutRef<"a"> & { href: string; current?: boolean }) {
  return <a {...props} href={href} aria-current={current ? "page" : props["aria-current"]} onClick={event => {
    onClick?.(event);
    const target = localPageNavigation(href, location.href, { ...event, target: event.currentTarget.target, download: event.currentTarget.hasAttribute("download") });
    if (!target || !window.pharosNavigate) return;
    event.preventDefault();
    const menu = event.currentTarget.closest(".nested-page-mobile");
    menu?.removeAttribute("open");
    menu?.querySelector<HTMLElement>(":scope > summary")?.focus({ preventScroll: true });
    window.pharosNavigate(target);
  }}>{children}</a>;
}

function NavigationItems({ pages, current }: { pages: readonly NestedPage[]; current: string }) {
  return <>{pages.map(page => page.children ? <details key={page.id} open={nestedPageTrail([page], current).length > 0}>
    <summary className="nested-page-nav-label">{page.label}<Icon name="chevron-down" /></summary>
    <div className="nested-page-nav-children"><NavigationItems pages={page.children} current={current} /></div>
  </details> : <PageLink key={page.id} href={page.href} current={page.id === current}>{page.label}</PageLink>)}</>;
}

/** Shared shell for pages with nested navigation; content keeps its own owner. */
export function NestedPageLayout({ label, pages, current, title, description, actions, headingId, className = "", contentClassName = "", children }: {
  label: string; pages: readonly NestedPage[]; current: string; title?: string; description?: string;
  actions?: React.ReactNode; headingId?: string; className?: string; contentClassName?: string; children: React.ReactNode;
}) {
  const trail = nestedPageTrail(pages, current), page = trail.at(-1);
  const generatedId = useId(), heading = useRef<HTMLHeadingElement>(null), mobile = useRef<HTMLDetailsElement>(null), previous = useRef(current);
  useEffect(() => {
    if (previous.current === current) return;
    previous.current = current;
    if (mobile.current) mobile.current.open = false;
    if (!heading.current?.getClientRects().length) return;
    heading.current.focus({ preventScroll: true });
    document.querySelector("main")?.scrollTo({ top: 0 });
  }, [current]);
  const sidebar = <nav className="nested-page-nav" aria-label={`${label} pages`}><NavigationItems pages={pages} current={current} /></nav>;
  return <div className={`nested-page ${className}`}>
    <div className="nested-page-layout">
      <div className="nested-page-desktop">{sidebar}</div>
      <div className={`nested-page-content ${contentClassName}`}>
        <details ref={mobile} className="nested-page-mobile"><summary>{label} pages<Icon name="chevron-down" /></summary>{sidebar}</details>
        <nav className="nested-page-breadcrumb" aria-label={`${label} breadcrumb`}><ol>
          <li>{label}</li>
          {trail.map((item, index) => <li key={item.id} aria-current={index === trail.length - 1 ? "page" : undefined}>
            {item.href && index < trail.length - 1 ? <PageLink href={item.href}>{item.label}</PageLink> : item.label}
          </li>)}
        </ol></nav>
        <div className="view-heading nested-page-heading"><div>
          <h1 ref={heading} id={headingId ?? generatedId} tabIndex={-1}>{title ?? page?.label}</h1>
          {description ?? page?.description ? <p className="muted">{description ?? page?.description}</p> : null}
        </div>{actions}</div>
        {children}
      </div>
    </div>
  </div>;
}
