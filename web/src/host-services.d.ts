export type HostErrorCode = 'unsupported' | 'invalid-request' | 'native-failure';
export interface HostError extends Error { code: HostErrorCode }
export interface HostCapability { supported: boolean; available: boolean; reason?: string }
export interface HostUpdateState { version: string; phase: string; progress?: number }
export interface HostDictationHandlers {
  /** A non-final result replaces the previous non-final one; a final one is kept. */
  onText?(text: string, final: boolean): void;
  onState?(state: 'preparing' | 'listening' | 'stopping' | string): void;
  /** Runs exactly once per session, with an error message when it failed. */
  onEnd?(error: string | null): void;
}
export interface HostDictationSession { started: Promise<void>; stop(): Promise<true>; cancel(): Promise<true> }
export interface PharosHost {
  readonly protocolVersion: 1;
  readonly kind: 'legacy-webkit' | 'browser';
  readonly capabilities: Record<'clipboard' | 'folders' | 'updates' | 'menu' | 'eject' | 'dictation', HostCapability>;
  clipboard: { writeText(text: string): Promise<void> };
  folders: { chooseLibraryFolder(purpose?: 'library' | 'source' | 'export'): Promise<string | null> };
  updates: { status(): Promise<HostUpdateState>; install(): Promise<HostUpdateState> };
  /** Dispatch confirms only that the one-way notification was sent. */
  menu: { applyPreferences(settings: Record<string, unknown>): {dispatched: true}; refreshUsage(): {dispatched: true} };
  /** Release acknowledgement is not proof of physical ejection. */
  lifecycle: { requestEject(): Promise<{released: true}> };
  /** On-device transcription in the Mac app; `status` says whether this Mac can offer it. */
  dictation: { status(): Promise<{available: boolean; reason?: string}>; start(handlers: HostDictationHandlers): HostDictationSession };
}
declare global {
  interface Window { pharosHost: PharosHost }
}
