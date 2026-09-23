import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QualityPicker, qualityHelp, TRANSCRIBE_HELP } from '../QualityPicker';

describe('QualityPicker', () => {
  it('hides the transcribe toggle without the feature', () => {
    render(<QualityPicker quality="high" onQuality={vi.fn()} transcribeAvailable={false} transcribe={false} onTranscribe={vi.fn()} />);
    expect(screen.queryByTestId('transcribe-toggle')).not.toBeInTheDocument();
    expect(screen.getAllByRole('button')).toHaveLength(3);
    expect(screen.getByRole('button', { name: 'HIGH QUALITY' })).toHaveAttribute('aria-pressed', 'true');
  });

  it('turning transcribe on pins quality to high6 and disables the others', async () => {
    const user = userEvent.setup();
    const onQuality = vi.fn();
    const onTranscribe = vi.fn();
    const { rerender } = render(<QualityPicker quality="high" onQuality={onQuality} transcribeAvailable transcribe={false} onTranscribe={onTranscribe} />);
    await user.click(screen.getByTestId('transcribe-toggle'));
    expect(onTranscribe).toHaveBeenCalledWith(true);
    expect(onQuality).toHaveBeenCalledWith('high6');
    rerender(<QualityPicker quality="high6" onQuality={onQuality} transcribeAvailable transcribe onTranscribe={onTranscribe} />);
    expect(screen.getByRole('button', { name: 'FAST' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'HIGH QUALITY' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'HQ · 6 STEMS' })).toBeEnabled();
    expect(screen.getByRole('button', { name: 'HQ · 6 STEMS' })).toHaveAttribute('aria-pressed', 'true');
  });

  it('help line switches to the transcribe blurb', () => {
    expect(qualityHelp('fast', false)).toMatch(/Demucs standard/);
    expect(qualityHelp('high6', true)).toBe(TRANSCRIBE_HELP);
  });
});
