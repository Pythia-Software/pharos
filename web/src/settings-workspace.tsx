import React, { useLayoutEffect, useRef, useState } from "react";
import { NestedPageLayout, useLocationRevision } from "./nested-pages";
import { nestedPageTrail, type NestedPage } from "./nested-pages-model";
import { readSettingsPages } from "./settings-pages-model";
import { logSourceFromParams, logSourceNavigationID, logsRoute, withLogSources } from "./logs-model";

// The classic Settings scripts own these panels and their event handlers.
// Read the same route metadata as the shell, then retain the actual DOM nodes
// inside the shared layout: navigating never recreates inputs or React roots.
export function SettingsWorkspace({ content }: { content: HTMLElement }) {
  useLocationRevision();
  const host = useRef<HTMLDivElement>(null);
  const [pages] = useState<NestedPage[]>(() => readSettingsPages(content));
  useLayoutEffect(() => {
    const parent = content.parentElement!;
    host.current!.append(content);
    return () => { parent.append(content); };
  }, [content]);
  // Logs heads each of its sources with the source's own title.
  const current = location.pathname === logsRoute ? logSourceNavigationID(logSourceFromParams(new URLSearchParams(location.search)))
    : nestedPageTrail(pages, location.pathname).length ? location.pathname : "/settings";
  return <NestedPageLayout label="Settings" pages={withLogSources(pages)} current={current} headingId="settingsPageHeading">
    <div ref={host} />
  </NestedPageLayout>;
}
