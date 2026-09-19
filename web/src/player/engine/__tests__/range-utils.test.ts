import { describe, expect, it } from 'vitest';
import { assertPartial, parseContentRange, rangeHeader, RangeUnsupportedError } from '../range-utils';

describe('range-utils', () => {
  it('formats Range headers', () => {
    expect(rangeHeader(0, 65535)).toBe('bytes=0-65535');
    expect(() => rangeHeader(5, 4)).toThrow();
    expect(() => rangeHeader(-1, 4)).toThrow();
  });

  it('parses Content-Range', () => {
    expect(parseContentRange('bytes 0-65535/222085336')).toEqual({ start: 0, end: 65535, total: 222085336 });
    expect(() => parseContentRange(null)).toThrow(RangeUnsupportedError);
    expect(() => parseContentRange('bytes 0-10/*')).toThrow(RangeUnsupportedError);
    expect(() => parseContentRange('items 0-10/20')).toThrow(RangeUnsupportedError);
    expect(() => parseContentRange('bytes 10-5/20')).toThrow(RangeUnsupportedError);
    expect(() => parseContentRange('bytes 0-20/20')).toThrow(RangeUnsupportedError);
  });

  it('validates partial responses', () => {
    expect(assertPartial(206, 'bytes 100-199/1000', 100, 199).total).toBe(1000);
    // Tail shorter than asked is fine at end of file.
    expect(assertPartial(206, 'bytes 900-999/1000', 900, 1500).end).toBe(999);
    expect(() => assertPartial(200, null, 0, 10)).toThrow(/ignored Range/);
    expect(() => assertPartial(404, null, 0, 10)).toThrow(/unexpected status/);
    expect(() => assertPartial(206, 'bytes 0-199/1000', 100, 199)).toThrow(/start mismatch/);
    expect(() => assertPartial(206, 'bytes 100-150/1000', 100, 199)).toThrow(/end mismatch/);
  });
});
