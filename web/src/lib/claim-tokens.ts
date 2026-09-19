/**
 * Anonymous jobs come back from POST /jobs with a one-time claim_token. It is
 * the only proof of authorship, so it lives in localStorage keyed by job id:
 * it is sent as X-Claim-Token on reads and writes, and handed to
 * POST /jobs/{id}/claim once the visitor signs in.
 */

const KEY = 'rk.claims';

type Claims = Record<string, string>;

function read(): Claims {
  try {
    const raw = localStorage.getItem(KEY);
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    return parsed && typeof parsed === 'object' ? (parsed as Claims) : {};
  } catch {
    return {};
  }
}

function write(c: Claims): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(c));
  } catch {
    // storage full or unavailable; the token still lives for this page
  }
}

export function rememberClaim(jobId: string, token: string): void {
  const c = read();
  c[jobId] = token;
  write(c);
}

export function claimFor(jobId: string): string | null {
  return read()[jobId] ?? null;
}

export function forgetClaim(jobId: string): void {
  const c = read();
  if (jobId in c) {
    delete c[jobId];
    write(c);
  }
}

export function allClaims(): Claims {
  return read();
}
