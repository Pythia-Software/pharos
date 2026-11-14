import type { NestedPage } from "./nested-pages-model";

/** One Settings panel's route metadata, as ui.py declares it. */
export type SettingsPanel = { route: string; title: string; description?: string; group?: string };

/** Panels sharing a group nest under one menu section, placed where its first
 * panel appears (Settings → Colors → Project colors). */
export function settingsPages(panels: readonly SettingsPanel[]): NestedPage[] {
  const pages: NestedPage[] = [], groups = new Map<string, NestedPage[]>();
  for (const panel of panels) {
    const page: NestedPage = { id: panel.route, href: panel.route, label: panel.title, description: panel.description };
    if (!panel.group) { pages.push(page); continue; }
    let children = groups.get(panel.group);
    if (!children) {
      groups.set(panel.group, children = []);
      pages.push({ id: `settings:${panel.group.toLowerCase()}`, label: panel.group, children });
    }
    children.push(page);
  }
  return pages;
}

export function readSettingsPages(root: ParentNode): NestedPage[] {
  return settingsPages(Array.from(root.querySelectorAll<HTMLElement>("[data-settings-route]"), panel => ({
    route: panel.dataset.settingsRoute!, title: panel.dataset.pageTitle!, description: panel.dataset.pageDescription, group: panel.dataset.settingsGroup,
  })));
}
