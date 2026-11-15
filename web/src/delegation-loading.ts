// Keep the API's conflict code: its human-readable message is not a retry policy.
export class DelegationRequestError extends Error {
  constructor(message: string, readonly status: number, readonly code?: string) {
    super(message);
  }
}

export const snapshotChanged = (error: unknown) => error instanceof DelegationRequestError
  && error.status === 409 && error.code === 'snapshot_changed';

// Visible lanes and the whole-window idle calculation can fail together.
// Coalesce their refreshes and bound consecutive failures, including when a
// reload returns the same token but lane evidence changes immediately after it.
export class DelegationSnapshotRecovery {
  private snapshot = '';
  private pending = false;
  private attempts = 0;

  loaded(snapshot: string) { this.snapshot = snapshot; this.pending = false; }
  reset() { this.pending = false; this.attempts = 0; }
  succeeded(snapshot: string) {
    if (!this.pending && snapshot === this.snapshot) this.attempts = 0;
  }
  changed(snapshot: string): 'refresh' | 'ignore' | 'exhausted' {
    if (this.pending || snapshot !== this.snapshot) return 'ignore';
    if (this.attempts >= 2) return 'exhausted';
    this.attempts++;
    this.pending = true;
    return 'refresh';
  }
}
