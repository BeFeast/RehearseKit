import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { QualityInfo } from '../../../api/types';
import { DEFAULT_QUALITIES, QualityPicker, qualityHelp, TRANSCRIBE_HELP } from '../QualityPicker';

const INTERNAL: QualityInfo[] = [
  { id: 'fast', label: 'Fast', model: 'Demucs HT', stems: 4 },
  { id: 'high', label: 'Standard', model: 'Demucs HT fine-tuned', stems: 4 },
  { id: 'high6', label: 'Standard + guitar/piano', model: 'Demucs HT 6s', stems: 6 },
  { id: 'hifi', label: 'Plus HiFi', model: 'RoFormer vocals + BS-RoFormer SW', stems: 6 },
];

describe('QualityPicker', () => {
  it('offers the public presets: Standard and Plus HiFi, 4 stems each', () => {
    render(<QualityPicker quality="high" onQuality={vi.fn()} qualities={DEFAULT_QUALITIES} transcribeAvailable={false} transcribe={false} onTranscribe={vi.fn()} />);
    expect(screen.queryByTestId('transcribe-toggle')).not.toBeInTheDocument();
    expect(screen.getAllByRole('button').map((b) => b.textContent)).toEqual(['STANDARD', 'PLUS HIFI']);
    expect(screen.getByRole('button', { name: 'STANDARD' })).toHaveAttribute('aria-pressed', 'true');
    expect(qualityHelp('hifi', false)).toMatch(/^Plus HiFi: 4 stems \(vocals, drums, bass and other\)/);
    expect(qualityHelp('hifi', false)).not.toMatch(/Demucs/);
  });

  it('never shows the transcribe toggle without a 6-stem preset', () => {
    render(<QualityPicker quality="high" onQuality={vi.fn()} qualities={DEFAULT_QUALITIES} transcribeAvailable transcribe={false} onTranscribe={vi.fn()} />);
    expect(screen.queryByTestId('transcribe-toggle')).not.toBeInTheDocument();
  });

  it('turning transcribe on pins quality to a 6-stem preset and disables the others', async () => {
    const user = userEvent.setup();
    const onQuality = vi.fn();
    const onTranscribe = vi.fn();
    const { rerender } = render(<QualityPicker quality="high" onQuality={onQuality} qualities={INTERNAL} transcribeAvailable transcribe={false} onTranscribe={onTranscribe} />);
    await user.click(screen.getByTestId('transcribe-toggle'));
    expect(onTranscribe).toHaveBeenCalledWith(true);
    expect(onQuality).toHaveBeenCalledWith('high6');
    rerender(<QualityPicker quality="high6" onQuality={onQuality} qualities={INTERNAL} transcribeAvailable transcribe onTranscribe={onTranscribe} />);
    expect(screen.getByRole('button', { name: 'FAST' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'STANDARD' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'STD · 6 STEMS' })).toBeEnabled();
    expect(screen.getByRole('button', { name: 'PLUS HIFI' })).toBeEnabled();
    expect(screen.getByRole('button', { name: 'STD · 6 STEMS' })).toHaveAttribute('aria-pressed', 'true');
  });

  it('help line switches to the transcribe blurb', () => {
    expect(qualityHelp('high6', false, INTERNAL)).toMatch(/Demucs HT 6s/);
    expect(qualityHelp('high6', true, INTERNAL)).toBe(TRANSCRIBE_HELP);
  });
});
