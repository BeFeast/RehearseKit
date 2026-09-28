import { describe, expect, it, vi } from 'vitest';
import { ApiError } from '../client';
import { checkDrumExport } from '../index';
import { EditConflict } from '../../lib/drums/types';

const ID = '0f5b8c2e-1d3a-4b7c-9e0f-1a2b3c4d5e6f';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

describe('checkDrumExport', () => {
  it('POSTs the revision and stops at the headers of a 200 (the body is never read)', async () => {
    let signal: AbortSignal | undefined;
    let sent: RequestInit | undefined;
    const body = new ReadableStream({ start() {} }); // never ends: reading it would hang
    const f = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
      sent = init;
      signal = init?.signal ?? undefined;
      return new Response(body, { status: 200, headers: { 'Content-Type': 'application/zip' } });
    });
    await checkDrumExport(ID, 3, f as unknown as typeof fetch);
    expect(f).toHaveBeenCalledWith(`/api/v1/jobs/${ID}/drums/export`, expect.objectContaining({ method: 'POST' }));
    expect(JSON.parse(String(sent?.body))).toEqual({ edit_rev: 3 });
    expect(signal?.aborted).toBe(true);
  });

  it('turns 409 edit_conflict into EditConflict with the server revision', async () => {
    const f = vi.fn(async () => jsonResponse(409, { code: 'edit_conflict', message: 'stale', edit_rev: 7 }));
    const err = await checkDrumExport(ID, 3, f as unknown as typeof fetch).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(EditConflict);
    expect((err as EditConflict).editRev).toBe(7);
  });

  it('throws other errors as ApiError with the server message', async () => {
    const f = vi.fn(async () => jsonResponse(404, { code: 'stem_not_found', message: 'the drum stem is missing' }));
    const err = await checkDrumExport(ID, 3, f as unknown as typeof fetch).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(404);
    expect((err as ApiError).message).toBe('the drum stem is missing');
  });
});
