import React from "react";

// Ask renders a preview above each destination it can describe. Each kind
// claims the addresses it understands; the first kind to claim one renders it.
// A kind that replaces the link renders its own way to open the destination.
// An eager preview loads before it is visible, as for an answer that just
// arrived; onOpen opens the destination the way Ask's link would.
export type AskLinkCard = { href: string; title: string; description: string };
export type AskLinkPreviewOptions = { eager?: boolean; onOpen?: (url: string) => void };
export type AskLinkPreviews = {
  canRender: (address: string) => boolean;
  replacesLink?: (address: string) => boolean;
  mount: (host: HTMLElement, card: AskLinkCard, options?: AskLinkPreviewOptions) => () => void;
  revalidate: () => void;
};

export function combineAskPreviews(...kinds: AskLinkPreviews[]): Required<AskLinkPreviews> {
  const kindFor = (address: string) => kinds.find(kind => kind.canRender(address));
  return {
    canRender: address => Boolean(kindFor(address)),
    replacesLink: address => kindFor(address)?.replacesLink?.(address) ?? false,
    revalidate: () => kinds.forEach(kind => kind.revalidate()),
    mount: (host, card, options) => kindFor(card.href)?.mount(host, card, options) ?? (() => {}),
  };
}

export class PreviewBoundary extends React.Component<{ message: string; children: React.ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() { return { failed: true }; }
  render() { return this.state.failed ? <p role="status">{this.props.message}</p> : this.props.children; }
}
