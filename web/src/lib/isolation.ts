/**
 * Cross-origin isolation is a per-document property: the server sends COOP
 * same-origin + COEP credentialless on /jobs/{id} only (the player needs
 * SharedArrayBuffer there; the same COOP blocks the Google sign-in popup
 * everywhere it is set). A client-side route change keeps the document, so
 * crossing that boundary in either direction has to be a full page load —
 * otherwise a job opened from the list has no SharedArrayBuffer, and a list
 * reached from a job cannot open the Google popup.
 */

const JOB_PAGE = /^\/jobs\/[^/?#]+\/?(?:[?#].*)?$/;

/** True for /jobs/{id} (the only isolated document). */
export function needsIsolation(path: string): boolean {
  return JOB_PAGE.test(path);
}

/** Whether the current document is cross-origin isolated. */
export function isIsolated(): boolean {
  return typeof globalThis.crossOriginIsolated === 'boolean' && globalThis.crossOriginIsolated;
}

/** True when navigating to `path` from this document needs a full load. */
export function crossesIsolation(path: string): boolean {
  return needsIsolation(path) !== isIsolated();
}

/** Build the sign-in URL the job page hands off to (the list opens the dialog). */
export function signInHandoffUrl(next: string): string {
  return `/jobs?signin=1&next=${encodeURIComponent(next)}`;
}

/** Only same-origin absolute paths are followed after sign-in. */
export function safeNext(next: string | undefined | null): string | null {
  if (!next || !next.startsWith('/') || next.startsWith('//') || next.startsWith('/\\')) return null;
  return next;
}
