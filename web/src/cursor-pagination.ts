// Cursor tokens belong to one live query execution. The URL carries only an
// ordinal; a new execution replays cursors, while ordinary paging reuses them.
export class CursorPagination {
  private scope = "";
  private cursors = [""];

  reset() {
    this.scope = "";
    this.cursors = [""];
  }

  async load<T extends { next_cursor?: string }>(scope: string, wanted: number, request: (cursor: string) => Promise<T>, signal: AbortSignal) {
    if (!Number.isInteger(wanted) || wanted < 0 || wanted > 250) throw new Error("Choose a session page from 0 to 250.");
    if (this.scope !== scope) {
      this.scope = scope;
      this.cursors = [""];
    }
    // Capture the execution's array: an aborted old request cannot populate a
    // replacement execution's cache after reset or a query change.
    const cursors = this.cursors;
    let page = Math.min(wanted, cursors.length - 1);
    for (;;) {
      const cursor = cursors[page];
      const body = await request(cursor);
      signal.throwIfAborted();
      if (body.next_cursor) {
        if (cursors[page + 1] !== body.next_cursor) cursors.length = page + 1;
        cursors[page + 1] = body.next_cursor;
      } else cursors.length = page + 1;
      if (page >= wanted || !body.next_cursor) return { body, cursor, previous: cursors.slice(0, page), page };
      page++;
    }
  }
}
