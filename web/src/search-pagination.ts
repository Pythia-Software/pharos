// Persist an ordinal, not a server cursor: cursors bind one execution's view.
// A reopened search replays fresh cursors and stops at the last available page.
export async function loadSearchPage<T extends { next_cursor?: string }>(
  load: (cursor: string) => Promise<T>,
  requestedPage: number,
  cursor = "",
): Promise<{ batch: T; cursor: string; page: number }> {
  if (!Number.isInteger(requestedPage) || requestedPage < 0 || requestedPage > 250) throw new Error("Choose a search page from 0 to 250.");
  let page = cursor ? requestedPage : 0;
  for (;;) {
    const batch = await load(cursor);
    if (page >= requestedPage || !batch.next_cursor) return { batch, cursor, page };
    cursor = batch.next_cursor;
    page++;
  }
}
