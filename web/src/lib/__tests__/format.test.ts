import { describe, expect, it } from 'vitest';
import {
  barOf,
  formatBarsBeats,
  formatBytes,
  formatDate,
  formatDateTime,
  formatEta,
  formatLoopLabel,
  formatLoopSummary,
  formatRelative,
  formatTimecode,
  hoursUntil,
  initials,
  shortUrl,
  snapToBeat,
} from '../format';

describe('timecode', () => {
  it('formats m:ss and h:mm:ss', () => {
    expect(formatTimecode(0)).toBe('0:00');
    expect(formatTimecode(58.9)).toBe('0:58');
    expect(formatTimecode(214)).toBe('3:34');
    expect(formatTimecode(3725)).toBe('1:02:05');
    expect(formatTimecode(-3)).toBe('0:00');
    expect(formatTimecode(NaN)).toBe('0:00');
  });
});

describe('bars from BPM (4/4)', () => {
  it('matches the design examples (124 BPM: 0:46 → bar 24, 1:24 → bar 44)', () => {
    expect(barOf(46, 124)).toBe(24);
    expect(barOf(84, 124)).toBe(44);
    expect(barOf(0, 124)).toBe(1);
  });
  it('falls back gracefully without a tempo', () => {
    expect(barOf(46, 0)).toBe(1);
    expect(formatBarsBeats(46, 0)).toBe('0:46');
    expect(formatBarsBeats(2.5, 120)).toBe('2.2');
  });
  it('labels the loop in bars or timecode', () => {
    expect(formatLoopLabel(46, 84, 124)).toBe('BARS 24\u201344');
    expect(formatLoopLabel(46, 84, null)).toBe('LOOP 0:46 \u2192 1:24');
    expect(formatLoopSummary(46, 84)).toBe('LOOP 0:46 \u2192 1:24 \u00b7 0:38');
  });
  it('snaps to the beat grid', () => {
    expect(snapToBeat(1.1, 120)).toBeCloseTo(1, 9);
    expect(snapToBeat(1.3, 120)).toBeCloseTo(1.5, 9);
    expect(snapToBeat(1.3, null)).toBe(1.3);
  });
});

describe('bytes and ETA', () => {
  it('formats sizes', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(5 * 1024)).toBe('5 KB');
    expect(formatBytes(612 * 1024 * 1024)).toBe('612 MB');
    expect(formatBytes(1024 * 1024 * 1024)).toBe('1.00 GB');
  });
  it('formats ETA', () => {
    expect(formatEta(40)).toBe('about 40s left');
    expect(formatEta(130)).toBe('about 2 min left');
    expect(formatEta(-1)).toBe('');
  });
});

describe('dates', () => {
  const now = new Date('2026-09-19T12:00:00Z');
  it('relative times', () => {
    expect(formatRelative('2026-09-19T11:48:00Z', now)).toBe('12 min ago');
    expect(formatRelative('2026-09-19T10:00:00Z', now)).toBe('2 hours ago');
    expect(formatRelative('2026-09-18T12:00:00Z', now)).toBe('yesterday');
    expect(formatRelative('2026-09-16T12:00:00Z', now)).toBe('3 days ago');
    expect(formatRelative('2026-03-14T12:00:00Z', now)).toBe('14 Mar 2026');
  });
  it('absolute dates', () => {
    expect(formatDate('2026-03-14T12:00:00Z')).toBe('14 Mar 2026');
    expect(formatDateTime(null)).toBe('\u2014');
    expect(formatDateTime('2026-09-19T09:12:00', now)).toMatch(/^Today at \d\d:\d\d$/);
  });
  it('hours until expiry', () => {
    expect(hoursUntil('2026-09-20T10:00:00Z', now)).toBe(22);
    expect(hoursUntil('2026-09-19T10:00:00Z', now)).toBe(0);
  });
});

describe('strings', () => {
  it('initials', () => {
    expect(initials('Dana Whitfield')).toBe('DW');
    expect(initials('Dana')).toBe('DA');
    expect(initials('', 'priya@example.com')).toBe('PR');
  });
  it('short urls', () => {
    expect(shortUrl('https://www.youtube.com/watch?v=dQw4w9WgXcQ')).toBe('youtube.com/watch?v=dQw4w9WgXcQ');
    expect(shortUrl('not a url')).toBe('not a url');
  });
});
