/** Navigation lands at the content landmark without making a page title interactive. */
export function focusPageContent() {
  if (new URLSearchParams(location.search).has("reveal")) return;
  const main = document.querySelector<HTMLElement>("main");
  if (!main) return;
  // Focusable only until focus leaves it. A press on content that cannot take
  // focus (every button, in WebKit) focuses its nearest focusable ancestor, so
  // a lasting tabindex would pull focus out of popovers and close them.
  main.tabIndex = -1;
  main.addEventListener("blur", () => main.removeAttribute("tabindex"), { once: true });
  main.focus({ preventScroll: true });
}
