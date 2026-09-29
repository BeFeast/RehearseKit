import type { Quality, QualityInfo } from '../../api/types';

/**
 * What the public stack offers until /config answers (the server decides the
 * real list per account: internal accounts also get Fast, 6-stem Standard
 * and a 6-stem HiFi).
 */
export const DEFAULT_QUALITIES: QualityInfo[] = [
  { id: 'high', label: 'Standard', model: 'SCNet XL', stems: 4 },
  { id: 'hifi', label: 'Plus HiFi', model: 'RoFormer vocals + SCNet XL', stems: 4 },
];

// Short segment labels (the control fits four under ~14 characters each).
export const QUALITY_LABEL: Record<Quality, string> = { fast: 'FAST', high: 'STANDARD', high6: 'STD · 6 STEMS', hifi: 'PLUS HIFI' };

const STEM_LIST: Record<number, string> = { 4: 'vocals, drums, bass and other', 6: 'vocals, drums, bass, guitar, piano and other' };

/** Help line for a preset: its stems and the model behind it. */
export function qualityHelpFor(info: QualityInfo): string {
  const hifi = info.id === 'hifi' ? ' HiFi: vocals are taken first by a dedicated model, then the instrumental is split.' : '';
  return `${info.label}: ${info.stems} stems (${STEM_LIST[info.stems] ?? 'separate stems'}) · ${info.model}.${hifi}`;
}

export const TRANSCRIBE_HELP = 'Beat grid, tempo map and draft MIDI for drums, bass, guitar and piano in the .dawproject and as .mid files. Needs a 6-stem preset.';

export interface QualityPickerProps {
  quality: Quality;
  onQuality: (q: Quality) => void;
  /** The presets this account may use, from /api/v1/config. */
  qualities: QualityInfo[];
  /** Shown only when the account has the "transcribe" feature. */
  transcribeAvailable: boolean;
  transcribe: boolean;
  onTranscribe: (on: boolean) => void;
}

/**
 * Quality segments plus the owner-only Transcribe toggle. Turning
 * Transcribe on pins quality to a 6-stem preset (the server rejects anything
 * else with transcribe_requires_high6) and disables the other segments.
 */
export function QualityPicker({ quality, onQuality, qualities, transcribeAvailable, transcribe, onTranscribe }: QualityPickerProps) {
  const locked = transcribeAvailable && transcribe;
  const six = qualities.filter((q) => q.stems === 6).map((q) => q.id);
  return (
    <>
      <div className="rk-field">
        <label id="qlabel">Processing quality</label>
        <div className="rk-seg rk-seg--field" role="group" aria-labelledby="qlabel" aria-describedby="qhelp">
          {qualities.map((q) => (
            <button key={q.id} type="button" aria-pressed={quality === q.id} disabled={locked && q.stems !== 6} onClick={() => onQuality(q.id)}>
              {QUALITY_LABEL[q.id] ?? q.label.toUpperCase()}
            </button>
          ))}
        </div>
      </div>
      {transcribeAvailable && six.length > 0 && (
        <div className="rk-field">
          <label htmlFor="transcribe">Transcribe</label>
          <label className="rk-check" htmlFor="transcribe">
            <input
              id="transcribe"
              type="checkbox"
              checked={transcribe}
              aria-describedby="thelp"
              data-testid="transcribe-toggle"
              onChange={(e) => {
                onTranscribe(e.target.checked);
                if (e.target.checked && !six.includes(quality)) onQuality(six.includes('high6') ? 'high6' : six[0]);
              }}
            />
            <span>Beat grid + MIDI (.dawproject, .mid)</span>
          </label>
        </div>
      )}
    </>
  );
}

/** Help line under the form: the transcribe blurb takes over when it is on. */
export function qualityHelp(quality: Quality, transcribe: boolean, qualities: QualityInfo[] = DEFAULT_QUALITIES): string {
  if (transcribe) return TRANSCRIBE_HELP;
  const info = qualities.find((q) => q.id === quality);
  return info ? qualityHelpFor(info) : '';
}
