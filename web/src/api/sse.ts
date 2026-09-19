import { API_BASE } from './client';
import { claimFor } from '../lib/claim-tokens';
import type { JobEvent } from './types';

export interface EventSubscription {
  close(): void;
}

/**
 * Subscribe to GET /jobs/{id}/events (Server-Sent Events) with fetch, so the
 * claim-token header can ride along for anonymous jobs. Reconnects with
 * Last-Event-ID after a dropped connection; stops after a terminal status
 * (the server closes the stream then).
 */
export function subscribeJobEvents(
  jobId: string,
  onEvent: (e: JobEvent, id: string) => void,
  onClose?: (reason: 'ended' | 'error' | 'closed') => void,
): EventSubscription {
  let closed = false;
  let lastId = '';
  let attempt = 0;
  let controller: AbortController | null = null;
  let retryTimer: ReturnType<typeof setTimeout> | null = null;

  const connect = async () => {
    if (closed) return;
    controller = new AbortController();
    const headers = new Headers({ Accept: 'text/event-stream' });
    if (lastId) headers.set('Last-Event-ID', lastId);
    const tok = claimFor(jobId);
    if (tok) headers.set('X-Claim-Token', tok);
    let sawTerminal = false;
    try {
      const res = await fetch(`${API_BASE}/jobs/${jobId}/events`, {
        headers,
        credentials: 'same-origin',
        signal: controller.signal,
        cache: 'no-store',
      });
      if (!res.ok || !res.body) {
        if (res.status === 401 || res.status === 403 || res.status === 404 || res.status === 410) {
          closed = true;
          onClose?.('error');
          return;
        }
        throw new Error(`events: ${res.status}`);
      }
      attempt = 0;
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buf = '';
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        let idx: number;
        while ((idx = buf.indexOf('\n\n')) >= 0) {
          const block = buf.slice(0, idx);
          buf = buf.slice(idx + 2);
          const parsed = parseBlock(block);
          if (!parsed) continue;
          if (parsed.id) lastId = parsed.id;
          if (parsed.event === 'status' && parsed.data) {
            try {
              const ev = JSON.parse(parsed.data) as JobEvent;
              onEvent(ev, lastId);
              if (ev.status === 'completed' || ev.status === 'failed' || ev.status === 'cancelled') sawTerminal = true;
            } catch {
              // malformed line; ignore
            }
          }
        }
      }
    } catch (err) {
      if (closed || (err instanceof DOMException && err.name === 'AbortError')) return;
    }
    if (closed) return;
    if (sawTerminal) {
      closed = true;
      onClose?.('ended');
      return;
    }
    // Dropped (or the server closed after replay without a terminal event) — reconnect.
    const delay = Math.min(15000, 500 * Math.pow(2, attempt++));
    retryTimer = setTimeout(() => void connect(), delay);
  };

  void connect();

  return {
    close() {
      if (closed) return;
      closed = true;
      if (retryTimer) clearTimeout(retryTimer);
      controller?.abort();
      onClose?.('closed');
    },
  };
}

interface Block {
  id?: string;
  event?: string;
  data?: string;
}

/** Parse one SSE block ("id: 1\nevent: status\ndata: {...}"). */
export function parseBlock(block: string): Block | null {
  const out: Block = {};
  const dataLines: string[] = [];
  for (const rawLine of block.split('\n')) {
    const line = rawLine.replace(/\r$/, '');
    if (!line || line.startsWith(':')) continue;
    const colon = line.indexOf(':');
    const field = colon < 0 ? line : line.slice(0, colon);
    let value = colon < 0 ? '' : line.slice(colon + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'id') out.id = value;
    else if (field === 'event') out.event = value;
    else if (field === 'data') dataLines.push(value);
  }
  if (dataLines.length) out.data = dataLines.join('\n');
  if (!out.id && !out.event && !out.data) return null;
  return out;
}
