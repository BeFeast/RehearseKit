import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { FADER, UNITY_POSITION } from '../../../lib/decibel';
import { Strip, tickBottom } from '../Strip';

const base = {
  stem: 'vocals' as const,
  caption: 'DEMUCS HT',
  position: UNITY_POSITION,
  muted: false,
  silenced: false,
  soloed: false,
  selected: false,
  level: 0.5,
  hold: 0.6,
  index: 0,
  onPosition: vi.fn(),
  onToggleMute: vi.fn(),
  onToggleSolo: vi.fn(),
  onSelect: vi.fn(),
};

describe('Strip', () => {
  it('shows the dB readout and S/M off by default', () => {
    render(<Strip {...base} />);
    expect(screen.getByTestId('db-vocals')).toHaveTextContent('0.0');
    expect(screen.getByRole('button', { name: 'Solo VOCALS' })).toHaveAttribute('aria-pressed', 'false');
    expect(screen.getByRole('button', { name: 'Mute VOCALS' })).toHaveAttribute('aria-pressed', 'false');
    const fader = screen.getByRole('slider', { name: 'VOCALS level' });
    expect(fader).toHaveAttribute('aria-valuetext', '0.0 dB');
  });

  it('muted: M on, readout −∞, meter dark, fader keeps its position', () => {
    render(<Strip {...base} muted position={FADER.x(-6)} />);
    expect(screen.getByRole('button', { name: 'Mute VOCALS' })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByTestId('db-vocals')).toHaveTextContent('-∞');
    expect(screen.getByRole('slider', { name: 'VOCALS level' })).toHaveAttribute('aria-valuetext', 'muted');
    expect(screen.getByRole('slider', { name: 'VOCALS level' })).toHaveAttribute('aria-valuenow', String(Math.round(FADER.x(-6) * 100)));
    expect(screen.getByLabelText('VOCALS channel strip')).toHaveAttribute('data-dead', 'true');
  });

  it('silenced by solo is distinct from muted: −∞ but S and M both off', () => {
    render(<Strip {...base} silenced />);
    expect(screen.getByTestId('db-vocals')).toHaveTextContent('-∞');
    expect(screen.getByRole('button', { name: 'Solo VOCALS' })).toHaveAttribute('aria-pressed', 'false');
    expect(screen.getByRole('button', { name: 'Mute VOCALS' })).toHaveAttribute('aria-pressed', 'false');
    const strip = screen.getByLabelText('VOCALS channel strip');
    expect(strip).toHaveAttribute('data-silenced', 'true');
    expect(strip).not.toHaveAttribute('data-muted');
    expect(screen.getByRole('slider', { name: 'VOCALS level' })).toHaveAttribute('aria-valuetext', 'silenced by solo');
    expect(screen.getByText('VOCALS silenced by solo')).toBeInTheDocument();
  });

  it('soloed strip shows S on and is not silenced', () => {
    render(<Strip {...base} soloed selected />);
    expect(screen.getByRole('button', { name: 'Solo VOCALS' })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByTestId('db-vocals')).toHaveTextContent('0.0');
    expect(screen.getByLabelText('VOCALS channel strip')).toHaveAttribute('data-selected', 'true');
  });

  it('S/M toggle without selecting the strip; clicking the body selects', async () => {
    const user = userEvent.setup();
    const onToggleSolo = vi.fn();
    const onToggleMute = vi.fn();
    const onSelect = vi.fn();
    render(<Strip {...base} onToggleSolo={onToggleSolo} onToggleMute={onToggleMute} onSelect={onSelect} />);
    await user.click(screen.getByRole('button', { name: 'Solo VOCALS' }));
    await user.click(screen.getByRole('button', { name: 'Mute VOCALS' }));
    expect(onToggleSolo).toHaveBeenCalledTimes(1);
    expect(onToggleMute).toHaveBeenCalledTimes(1);
    expect(onSelect).not.toHaveBeenCalled();
    await user.click(screen.getByText('DEMUCS HT'));
    expect(onSelect).toHaveBeenCalledTimes(1);
  });

  it('fader keyboard: ±1 dB, Shift ±5, Home/End extremes, Backspace resets to unity', () => {
    const onPosition = vi.fn();
    render(<Strip {...base} position={FADER.x(-6)} onPosition={onPosition} />);
    const fader = screen.getByRole('slider', { name: 'VOCALS level' });
    fireEvent.keyDown(fader, { key: 'ArrowUp' });
    expect(FADER.y(onPosition.mock.calls[0][0])).toBeCloseTo(-5, 6);
    fireEvent.keyDown(fader, { key: 'ArrowDown', shiftKey: true });
    expect(FADER.y(onPosition.mock.calls[1][0])).toBeCloseTo(-11, 6);
    fireEvent.keyDown(fader, { key: 'Home' });
    expect(onPosition.mock.calls[2][0]).toBe(1);
    fireEvent.keyDown(fader, { key: 'End' });
    expect(onPosition.mock.calls[3][0]).toBe(0);
    fireEvent.keyDown(fader, { key: 'Backspace' });
    expect(onPosition.mock.calls[4][0]).toBe(UNITY_POSITION);
  });

  it('scale ticks follow the fader law (0 dB above −6 above −24)', () => {
    expect(tickBottom(0)).toBeGreaterThan(tickBottom(-6));
    expect(tickBottom(-6)).toBeGreaterThan(tickBottom(-24));
    expect(tickBottom(-24)).toBeGreaterThan(7);
  });
});
