import React, { useEffect, useId, useRef, useState } from "react";
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
    window.pharosNavigate(target);
  }}>{children}</a>;
}

/** Page headings and content retain their owners; the app shell owns navigation. */
export function NestedPageLayout({ label, pages, current, title, description, actions, headingId, className = "", contentClassName = "", children }: {
  label: string; pages: readonly NestedPage[]; current: string; title?: string; description?: string;
  actions?: React.ReactNode; headingId?: string; className?: string; contentClassName?: string; children: React.ReactNode;
}) {
  const trail = nestedPageTrail(pages, current), page = trail.at(-1);
  const generatedId = useId(), heading = useRef<HTMLHeadingElement>(null), previous = useRef(current);
  useEffect(() => {
    if (previous.current === current) return;
    previous.current = current;
    if (!heading.current?.getClientRects().length) return;
    heading.current.focus({ preventScroll: true });
    document.querySelector("main")?.scrollTo({ top: 0 });
  }, [current]);
  return <div className={`nested-page ${className}`}>
    <div className="nested-page-layout">
      <div className={`nested-page-content ${contentClassName}`}>
        <div className="view-heading nested-page-heading"><div>
          <h1 ref={heading} id={headingId ?? generatedId} tabIndex={-1}>{title ?? page?.label}</h1>
          {description ?? page?.description ? <p className="muted">{description ?? page?.description}</p> : null}
        </div>{actions}</div>
        {children}
      </div>
    </div>
  </div>;
}
