import React, { useLayoutEffect, useRef, useState } from "react";
import { NestedPageLayout, useLocationRevision } from "./nested-pages";
import type { NestedPage } from "./nested-pages-model";

// The classic Settings scripts own these panels and their event handlers.
// Read the same route metadata as the shell, then retain the actual DOM nodes
// inside the shared layout: navigating never recreates inputs or React roots.
export function SettingsWorkspace({ content }: { content: HTMLElement }) {
  useLocationRevision();
  const host = useRef<HTMLDivElement>(null);
  const [pages] = useState<NestedPage[]>(() => Array.from(content.querySelectorAll<HTMLElement>("[data-settings-route]"), panel => ({
    id: panel.dataset.settingsRoute!, href: panel.dataset.settingsRoute!,
    label: panel.dataset.pageTitle!, description: panel.dataset.pageDescription,
  })));
  useLayoutEffect(() => {
    const parent = content.parentElement!;
    host.current!.append(content);
    return () => { parent.append(content); };
  }, [content]);
  const current = pages.some(page => page.id === location.pathname) ? location.pathname : "/settings";
  return <NestedPageLayout label="Settings" pages={pages} current={current} headingId="settingsPageHeading">
    <div ref={host} />
  </NestedPageLayout>;
}
