import React from "react";

// Ask renders a preview above each destination it can describe. Each kind
// claims the addresses it understands; the first kind to claim one renders it.
export type AskLinkPreviews = {
  canRender: (address: string) => boolean;
  mount: (host: HTMLElement, card: { href: string; title: string }) => () => void;
  revalidate: () => void;
};

export function combineAskPreviews(...kinds: AskLinkPreviews[]): AskLinkPreviews {
  return {
    canRender: address => kinds.some(kind => kind.canRender(address)),
    revalidate: () => kinds.forEach(kind => kind.revalidate()),
    mount: (host, card) => kinds.find(kind => kind.canRender(card.href))?.mount(host, card) ?? (() => {}),
  };
}

export class PreviewBoundary extends React.Component<{ message: string; children: React.ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() { return { failed: true }; }
  render() { return this.state.failed ? <p role="status">{this.props.message}</p> : this.props.children; }
}
