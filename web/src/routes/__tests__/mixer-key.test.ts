import { describe, expect, it } from 'vitest';
import type { Job } from '../../api/types';
import { mixerKey } from '../JobDetail';

const job = (names: string[]) => ({ id: 'j1', stems: names.map((name) => ({ name })) }) as unknown as Pick<Job, 'id' | 'stems'>;

describe('mixerKey', () => {
  it('changes when the stem set arrives, so the mixer is rebuilt with the stems', () => {
    // SSE flips the job to completed before the refetch brings the stems:
    // the key must differ between the empty and the populated set.
    expect(mixerKey(job([]))).not.toBe(mixerKey(job(['vocals', 'drums', 'bass', 'other', 'guitar', 'piano'])));
    expect(mixerKey(job(['vocals', 'drums']))).toBe(mixerKey(job(['vocals', 'drums'])));
  });
});
