import type { CSSProperties } from 'react';

// The handoff icons are 24px, 1.6px stroke, stroke="currentColor". Inlining
// them (rather than <img src>) lets them follow the theme's ink colour.
const files = import.meta.glob('../assets/icons/*.svg', { query: '?raw', import: 'default', eager: true }) as Record<
  string,
  string
>;

const icons: Record<string, string> = {};
for (const [path, svg] of Object.entries(files)) {
  const name = path.split('/').pop()!.replace(/\.svg$/, '');
  icons[name] = svg;
}

export type IconName =
  | 'alert' | 'arrow-left' | 'check' | 'chevron-down' | 'chevron-left' | 'chevron-right' | 'clock'
  | 'download' | 'file-audio' | 'filter' | 'folder' | 'google' | 'link' | 'lock' | 'logout' | 'loop'
  | 'mail' | 'metronome' | 'more' | 'mute' | 'pause' | 'play' | 'plus' | 'refresh' | 'search'
  | 'settings' | 'shield' | 'skip-start' | 'solo' | 'stop' | 'trash' | 'upload' | 'user' | 'users'
  | 'volume' | 'waveform' | 'x' | 'youtube';

export interface IconProps {
  name: IconName;
  size?: number;
  className?: string;
  style?: CSSProperties;
  /** Decorative by default; pass a label to expose it. */
  label?: string;
}

export function Icon({ name, size = 16, className, style, label }: IconProps) {
  const svg = icons[name] ?? '';
  return (
    <span
      className={className ? `rk-icon ${className}` : 'rk-icon'}
      style={{ width: size, height: size, ...style }}
      role={label ? 'img' : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      dangerouslySetInnerHTML={{ __html: svg }}
    />
  );
}

export const ICON_NAMES = Object.keys(icons);
