/** A page tree drives navigation, automatic expansion, and breadcrumbs. */
export type NestedPage = {
  id: string;
  label: string;
  description?: string;
} & ({ href: string; children?: never } | { href?: string; children: readonly NestedPage[] });

export function nestedPageTrail(pages: readonly NestedPage[], current: string): NestedPage[] {
  for (const page of pages) {
    if (page.id === current) return [page];
    const children = nestedPageTrail(page.children ?? [], current);
    if (children.length) return [page, ...children];
  }
  return [];
}

/** Preserve normal browser behavior for new tabs, downloads, and external URLs. */
export function localPageNavigation(href: string, base: string, click: {
  button: number; metaKey?: boolean; ctrlKey?: boolean; shiftKey?: boolean; altKey?: boolean;
  defaultPrevented?: boolean; target?: string; download?: boolean;
}): string | null {
  if (click.button || click.metaKey || click.ctrlKey || click.shiftKey || click.altKey || click.defaultPrevented || click.download || (click.target && click.target !== "_self")) return null;
  const url = new URL(href, base);
  return url.origin === new URL(base).origin ? url.pathname + url.search + url.hash : null;
}
