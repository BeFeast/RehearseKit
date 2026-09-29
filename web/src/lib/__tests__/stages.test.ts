import { describe, expect, it } from 'vitest';
import { badgeTone, canCancel, isActive, isTerminal, jobModel, lampStates, modelName, overallProgress, qualityBadge, rowTone, STAGE_COPY, stageNoun, stemModelCaption, statusLabel } from '../stages';

describe('status → stage mapping', () => {
  it('keeps the upstream stage copy (separating no longer names Demucs: #35)', () => {
    expect(STAGE_COPY.separating.message).toBe('Separating stems with AI...');
    expect(STAGE_COPY.separating.detail).toBe('Separating vocals, drums, bass, and other instruments');
    expect(STAGE_COPY.completed.detail).toBe('Stems, DAWproject file and tempo map are ready');
  });

  it('overall progress follows the stage table (converting 14, analyzing 28, separating 76, finalizing 89, packaging 99)', () => {
    expect(overallProgress('pending', 0)).toBe(0);
    expect(overallProgress('converting', 0)).toBe(0);
    expect(overallProgress('converting', 100)).toBe(14);
    expect(overallProgress('analyzing', 0)).toBe(14);
    expect(overallProgress('analyzing', 100)).toBe(28);
    expect(overallProgress('separating', 50)).toBe(52);
    expect(overallProgress('separating', 100)).toBe(76);
    expect(overallProgress('finalizing', 100)).toBe(89);
    expect(overallProgress('packaging', 100)).toBe(99);
    expect(overallProgress('completed', 0)).toBe(100);
    expect(overallProgress('separating', 250)).toBe(76);
  });

  it('lamps: done before, active at, off after the current stage', () => {
    expect(lampStates('separating')).toEqual(['done', 'done', 'active', 'off', 'off', 'off']);
    expect(lampStates('pending')).toEqual(['off', 'off', 'off', 'off', 'off', 'off']);
    expect(lampStates('completed')).toEqual(['done', 'done', 'done', 'done', 'done', 'done']);
    expect(lampStates('packaging')).toEqual(['done', 'done', 'done', 'done', 'active', 'off']);
  });

  it('failed and cancelled freeze at the last live stage', () => {
    expect(lampStates('failed', 'analyzing')).toEqual(['done', 'active', 'off', 'off', 'off', 'off']);
    expect(lampStates('cancelled', null)).toEqual(['done', 'done', 'active', 'off', 'off', 'off']);
    expect(stageNoun('separating')).toBe('stem separation');
    expect(stageNoun('analyzing')).toBe('tempo analysis');
    expect(stageNoun(null)).toBe('stem separation');
  });

  it('classifies statuses', () => {
    expect(isActive('converting')).toBe(true);
    expect(isActive('completed')).toBe(false);
    expect(isTerminal('cancelled')).toBe(true);
    expect(canCancel('separating')).toBe(true);
    expect(canCancel('packaging')).toBe(false);
    expect(canCancel('completed')).toBe(false);
  });

  it('badges and row edges', () => {
    expect(statusLabel('separating')).toBe('SEPARATING');
    expect(badgeTone('separating')).toBe('running');
    expect(badgeTone('completed')).toBe('done');
    expect(badgeTone('failed')).toBe('failed');
    expect(badgeTone('pending')).toBe('outline');
    expect(badgeTone('cancelled')).toBe('outline');
    expect(rowTone('failed')).toBe('failed');
    expect(rowTone('pending')).toBe('running');
    expect(rowTone('completed')).toBe('');
    expect(qualityBadge('high')).toBe('STANDARD');
    expect(qualityBadge('high6')).toBe('STD · 6 STEMS');
    expect(qualityBadge('hifi')).toBe('PLUS HIFI');
  });

  it('names the model behind each stem', () => {
    expect(jobModel({ quality: 'high' })).toBe('htdemucs_ft');
    expect(jobModel({ quality: 'hifi', model: 'kim+scnet_xl_ihf' })).toBe('kim+scnet_xl_ihf');
    expect(stemModelCaption('kim+scnet_xl_ihf', 'vocals')).toBe('ROFORMER');
    expect(stemModelCaption('kim+scnet_xl_ihf', 'drums')).toBe('SCNET XL');
    expect(stemModelCaption('scnet_xl_ihf', 'vocals')).toBe('SCNET XL');
    expect(stemModelCaption('htdemucs_6s', 'guitar')).toBe('DEMUCS 6S');
    expect(modelName('kim+bs_rofo_sw')).toBe('RoFormer vocals + BS-RoFormer SW');
  });
});
