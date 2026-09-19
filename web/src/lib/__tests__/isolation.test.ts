import { afterEach, describe, expect, it, vi } from 'vitest';
import { crossesIsolation, isIsolated, needsIsolation, safeNext, signInHandoffUrl } from '../isolation';

describe('cross-origin isolation boundary', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('only the job page needs the isolation headers', () => {
    expect(needsIsolation('/jobs/abc')).toBe(true);
    expect(needsIsolation('/jobs/aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee')).toBe(true);
    expect(needsIsolation('/jobs/abc/')).toBe(true);
    expect(needsIsolation('/jobs/abc?x=1#y')).toBe(true);
    expect(needsIsolation('/jobs')).toBe(false);
    expect(needsIsolation('/jobs?signin=1&next=%2Fjobs%2Fabc')).toBe(false);
    expect(needsIsolation('/jobs/abc/stems')).toBe(false);
    expect(needsIsolation('/')).toBe(false);
    expect(needsIsolation('/profile')).toBe(false);
  });

  it('a navigation crosses the boundary when the target and the document disagree', () => {
    vi.stubGlobal('crossOriginIsolated', false);
    expect(isIsolated()).toBe(false);
    expect(crossesIsolation('/jobs/abc')).toBe(true);
    expect(crossesIsolation('/jobs')).toBe(false);
    vi.stubGlobal('crossOriginIsolated', true);
    expect(isIsolated()).toBe(true);
    expect(crossesIsolation('/jobs/abc')).toBe(false);
    expect(crossesIsolation('/jobs')).toBe(true);
    expect(crossesIsolation('/')).toBe(true);
  });

  it('hands off to the list with the return path encoded', () => {
    expect(signInHandoffUrl('/jobs/abc')).toBe('/jobs?signin=1&next=%2Fjobs%2Fabc');
  });

  it('follows only same-origin absolute paths after sign-in', () => {
    expect(safeNext('/jobs/abc')).toBe('/jobs/abc');
    expect(safeNext('//evil.example/x')).toBeNull();
    expect(safeNext('/\\evil.example')).toBeNull();
    expect(safeNext('https://evil.example')).toBeNull();
    expect(safeNext('')).toBeNull();
    expect(safeNext(undefined)).toBeNull();
  });
});
