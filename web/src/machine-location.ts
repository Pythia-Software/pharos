
export function pushMachineLocation(url: URL) {
  if (url.href === location.href) return;
  const entry = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`;
  history.pushState({ ...history.state, pharosMachineEntry: entry }, "", url);
  window.dispatchEvent(new Event("pharos:uri-changed"));
}

