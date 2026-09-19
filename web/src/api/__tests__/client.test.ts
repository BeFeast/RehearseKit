import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, errorMessage, isApiError, lastSeenRequestId, request } from '../client';
import { rememberClaim } from '../../lib/claim-tokens';
import { parseBlock } from '../sse';

// Node 26 ships an experimental global localStorage that shadows jsdom's; use a
// plain in-memory Storage so the claim-token helper behaves as in a browser.
function memoryStorage(): Storage {
  const m = new Map<string, string>();
  return {
    get length() {
      return m.size;
    },
    clear: () => m.clear(),
    getItem: (k) => m.get(k) ?? null,
    key: (i) => Array.from(m.keys())[i] ?? null,
    removeItem: (k) => void m.delete(k),
    setItem: (k, v) => void m.set(k, String(v)),
  };
}

function mockFetch(status: number, body: unknown, headers: Record<string, string> = {}) {
  const text = body === undefined ? null : typeof body === 'string' ? body : JSON.stringify(body);
  const fn = vi.fn(async () => new Response(text, { status, headers: { 'Content-Type': 'application/json', ...headers } }));
  vi.stubGlobal('fetch', fn);
  return fn;
}

describe('api client', () => {
  beforeEach(() => vi.stubGlobal('localStorage', memoryStorage()));
  afterEach(() => vi.unstubAllGlobals());

  it('prefixes /api/v1, sends JSON and the session cookie', async () => {
    const fn = mockFetch(200, { id: 'u1' });
    const r = await request<{ id: string }>('/auth/login', { method: 'POST', body: { email: 'a', password: 'b' } });
    expect(r.id).toBe('u1');
    const [url, init] = fn.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('/api/v1/auth/login');
    expect(init.method).toBe('POST');
    expect(init.credentials).toBe('same-origin');
    expect((init.headers as Headers).get('content-type')).toBe('application/json');
    expect(init.body).toBe(JSON.stringify({ email: 'a', password: 'b' }));
  });

  it('turns the error envelope into ApiError', async () => {
    mockFetch(401, { code: 'invalid_credentials', message: 'email or password is incorrect' }, { 'X-Request-Id': 'req-1' });
    const err = (await request('/auth/login', { method: 'POST', body: {} }).catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(401);
    expect(err.code).toBe('invalid_credentials');
    expect(err.message).toBe('email or password is incorrect');
    expect(err.requestId).toBe('req-1');
    expect(lastSeenRequestId()).toBe('req-1');
    expect(isApiError(err, 'invalid_credentials')).toBe(true);
    expect(errorMessage(err)).toBe('email or password is incorrect');
  });

  it('flags missing routes as unavailable', async () => {
    mockFetch(404, { code: 'not_found', message: 'not found' });
    const err = (await request('/jobs/x/mix').catch((e) => e)) as ApiError;
    expect(err.unavailable).toBe(true);
    mockFetch(501, { code: 'not_implemented', message: 'later' });
    const err2 = (await request('/auth/google', { method: 'POST', body: {} }).catch((e) => e)) as ApiError;
    expect(err2.unavailable).toBe(true);
    expect(new ApiError(404, 'expired', 'gone').unavailable).toBe(false);
  });

  it('returns undefined for 204 and tolerates non-JSON bodies', async () => {
    mockFetch(204, undefined);
    expect(await request('/auth/logout', { method: 'POST' })).toBeUndefined();
    mockFetch(500, 'boom', { 'Content-Type': 'text/plain' });
    const err = (await request('/x').catch((e) => e)) as ApiError;
    expect(err.status).toBe(500);
    expect(err.code).toBe('');
  });

  it('adds X-Claim-Token for anonymous jobs from localStorage', async () => {
    rememberClaim('job-1', 'tok-abc');
    const fn = mockFetch(200, { id: 'job-1' });
    await request('/jobs/job-1', { jobId: 'job-1' });
    const [, init] = fn.mock.calls[0] as unknown as [string, RequestInit];
    expect((init.headers as Headers).get('x-claim-token')).toBe('tok-abc');
    const fn2 = mockFetch(200, { id: 'job-2' });
    await request('/jobs/job-2', { jobId: 'job-2' });
    const [, init2] = fn2.mock.calls[0] as unknown as [string, RequestInit];
    expect((init2.headers as Headers).get('x-claim-token')).toBeNull();
  });

  it('errorMessage falls back', () => {
    expect(errorMessage(new Error(''), 'fallback')).toBe('fallback');
    expect(errorMessage('x', 'fallback')).toBe('fallback');
  });
});

describe('SSE block parser', () => {
  it('parses id/event/data blocks as the Go server writes them', () => {
    const b = parseBlock('id: 12\nevent: status\ndata: {"status":"separating","progress":61,"message":"","at":"2026-09-19T00:00:00Z"}');
    expect(b).toEqual({ id: '12', event: 'status', data: '{"status":"separating","progress":61,"message":"","at":"2026-09-19T00:00:00Z"}' });
  });
  it('ignores comments and joins multi-line data', () => {
    expect(parseBlock(': ping')).toBeNull();
    expect(parseBlock('data: a\ndata: b')).toEqual({ data: 'a\nb' });
    expect(parseBlock('data:no-space\r')).toEqual({ data: 'no-space' });
  });
});
