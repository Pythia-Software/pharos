/** Navigation lands at the content landmark without making a page title interactive. */
export function focusPageContent() {
  if (new URLSearchParams(location.search).has("reveal")) return;
  const main = document.querySelector<HTMLElement>("main");
  if (!main) return;
  main.tabIndex = -1;
  main.focus({ preventScroll: true });
}
