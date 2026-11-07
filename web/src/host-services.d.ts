export type HostErrorCode = 'unsupported' | 'invalid-request' | 'native-failure';
export interface HostError extends Error { code: HostErrorCode }
export interface HostCapability { supported: boolean; available: boolean; reason?: string }
export interface HostUpdateState { version: string; phase: string; progress?: number }
export interface PharosHost {
  readonly protocolVersion: 1;
  readonly kind: 'legacy-webkit' | 'browser';
  readonly capabilities: Record<'clipboard' | 'folders' | 'updates' | 'menu' | 'eject', HostCapability>;
  clipboard: { writeText(text: string): Promise<void> };
  folders: { chooseLibraryFolder(purpose?: 'library' | 'source' | 'export'): Promise<string | null> };
  updates: { status(): Promise<HostUpdateState>; install(): Promise<HostUpdateState> };
  /** Dispatch confirms only that the one-way notification was sent. */
  menu: { applyPreferences(settings: Record<string, unknown>): {dispatched: true}; refreshUsage(): {dispatched: true} };
  /** Release acknowledgement is not proof of physical ejection. */
  lifecycle: { requestEject(): Promise<{released: true}> };
}
declare global {
  interface Window { pharosHost: PharosHost }
}
