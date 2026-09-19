import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Transport } from '../Transport';

const base = {
  playing: false,
  position: 58,
  duration: 214,
  bpm: 124,
  loop: { start: 46, end: 84 },
  loopEnabled: true,
  onTogglePlay: vi.fn(),
  onReturnToStart: vi.fn(),
  onToggleLoop: vi.fn(),
};

describe('Transport', () => {
  it('renders the LCD: position / duration | BPM | bars', () => {
    render(<Transport {...base} />);
    expect(screen.getByTestId('lcd-position')).toHaveTextContent('0:58');
    expect(screen.getByText('/ 3:34')).toBeInTheDocument();
    expect(screen.getByText('124.0')).toBeInTheDocument();
    expect(screen.getByTestId('lcd-loop')).toHaveTextContent('BARS 24–44');
  });

  it('falls back to timecode when there is no BPM', () => {
    render(<Transport {...base} bpm={null} />);
    expect(screen.getByText('BPM —')).toBeInTheDocument();
    expect(screen.getByTestId('lcd-loop')).toHaveTextContent('LOOP 0:46 → 1:24');
  });

  it('play/pause is one aria-pressed button whose label flips', async () => {
    const user = userEvent.setup();
    const onTogglePlay = vi.fn();
    const { rerender } = render(<Transport {...base} onTogglePlay={onTogglePlay} />);
    const play = screen.getByRole('button', { name: 'Play' });
    expect(play).toHaveAttribute('aria-pressed', 'false');
    await user.click(play);
    expect(onTogglePlay).toHaveBeenCalledTimes(1);
    rerender(<Transport {...base} playing onTogglePlay={onTogglePlay} />);
    expect(screen.getByRole('button', { name: 'Pause' })).toHaveAttribute('aria-pressed', 'true');
  });

  it('LOOP reflects the engaged state and return-to-start names loop A', async () => {
    const user = userEvent.setup();
    const onToggleLoop = vi.fn();
    const onReturnToStart = vi.fn();
    render(<Transport {...base} onToggleLoop={onToggleLoop} onReturnToStart={onReturnToStart} />);
    const loop = screen.getByRole('button', { name: 'LOOP' });
    expect(loop).toHaveAttribute('aria-pressed', 'true');
    await user.click(loop);
    expect(onToggleLoop).toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'Return to loop start' }));
    expect(onReturnToStart).toHaveBeenCalled();
  });

  it('compact variant shows only position and duration', () => {
    render(<Transport {...base} compact />);
    expect(screen.getByTestId('lcd-position')).toHaveTextContent('0:58');
    expect(screen.queryByText('124.0')).not.toBeInTheDocument();
  });
});
