export class RangeUnsupportedError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'RangeUnsupportedError';
  }
}

/** Build a `Range` header value for an inclusive byte range. */
export function rangeHeader(start: number, end: number): string {
  if (!Number.isInteger(start) || !Number.isInteger(end) || start < 0 || end < start) {
    throw new Error(`invalid byte range ${start}-${end}`);
  }
  return `bytes=${start}-${end}`;
}

export interface ContentRange {
  start: number;
  end: number;
  total: number;
}

const CONTENT_RANGE = /^bytes\s+(\d+)-(\d+)\/(\d+|\*)$/i;

export function parseContentRange(header: string | null | undefined): ContentRange {
  if (!header) throw new RangeUnsupportedError('missing Content-Range header');
  const m = CONTENT_RANGE.exec(header.trim());
  if (!m) throw new RangeUnsupportedError(`unparseable Content-Range: ${header}`);
  const start = Number(m[1]);
  const end = Number(m[2]);
  if (m[3] === '*') throw new RangeUnsupportedError('Content-Range without total length');
  const total = Number(m[3]);
  if (end < start || end >= total) throw new RangeUnsupportedError(`inconsistent Content-Range: ${header}`);
  return { start, end, total };
}

/**
 * Validate a range response: status must be 206 and Content-Range must cover
 * exactly the requested range (a shorter tail is allowed only at end of file).
 */
export function assertPartial(
  status: number,
  contentRange: string | null | undefined,
  start: number,
  end: number,
): ContentRange {
  if (status === 200) {
    throw new RangeUnsupportedError('server ignored Range and returned 200 (full body)');
  }
  if (status !== 206) throw new RangeUnsupportedError(`unexpected status ${status} for range request`);
  const cr = parseContentRange(contentRange);
  if (cr.start !== start) throw new RangeUnsupportedError(`range start mismatch: asked ${start}, got ${cr.start}`);
  const expectedEnd = Math.min(end, cr.total - 1);
  if (cr.end !== expectedEnd) throw new RangeUnsupportedError(`range end mismatch: asked ${end}, got ${cr.end}`);
  return cr;
}
