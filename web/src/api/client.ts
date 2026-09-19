import { claimFor } from '../lib/claim-tokens';

export const API_BASE = '/api/v1';

/** The JSON error envelope every rk endpoint uses: {"code","message"}. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string | null;
  /** The decoded error envelope; some errors carry more than code/message (403 pending_approval has "user"). */
  readonly body: unknown;

  constructor(status: number, code: string, message: string, requestId: string | null = null, body: unknown = null) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.requestId = requestId;
    this.body = body;
  }

  /** Route missing in this build (404) or declared but unimplemented (501). */
  get unavailable(): boolean {
    return this.status === 404 && (this.code === 'not_found' || this.code === '') ? true : this.status === 501;
  }
}

export interface RequestOptions extends Omit<RequestInit, 'body'> {
  body?: unknown;
  /** Job id whose claim token (if any) should ride along as X-Claim-Token. */
  jobId?: string;
  /** Raw body (FormData, Blob…) passed through untouched. */
  raw?: BodyInit;
}

let lastRequestId: string | null = null;

/** The X-Request-Id of the most recent response, for the error boundary. */
export function lastSeenRequestId(): string | null {
  return lastRequestId;
}

/**
 * JSON fetch with the cookie session, the claim-token header for anonymous
 * jobs and the error envelope turned into ApiError. Returns undefined for
 * 204s.
 */
export async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { body, jobId, raw, headers, ...init } = opts;
  const h = new Headers(headers);
  if (body !== undefined) h.set('Content-Type', 'application/json');
  if (jobId) {
    const tok = claimFor(jobId);
    if (tok) h.set('X-Claim-Token', tok);
  }
  const res = await fetch(`${API_BASE}${path}`, {
    credentials: 'same-origin',
    ...init,
    headers: h,
    body: raw ?? (body !== undefined ? JSON.stringify(body) : undefined),
  });
  const rid = res.headers.get('x-request-id');
  if (rid) lastRequestId = rid;
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = null;
    }
  }
  if (!res.ok) {
    const env = (data ?? {}) as { code?: string; message?: string };
    throw new ApiError(res.status, env.code ?? '', env.message ?? res.statusText ?? 'request failed', rid, data);
  }
  return data as T;
}

export function isApiError(err: unknown, code?: string): err is ApiError {
  return err instanceof ApiError && (code === undefined || err.code === code);
}

/** Human message for any thrown value. */
export function errorMessage(err: unknown, fallback = 'Something went wrong'): string {
  if (err instanceof ApiError) return err.message || fallback;
  if (err instanceof Error) return err.message || fallback;
  return fallback;
}
