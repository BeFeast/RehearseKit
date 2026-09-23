import type { Quality } from '../../api/types';

export const QUALITY_HELP: Record<Quality, string> = {
  fast: 'Demucs standard — about 1 minute per song minute.',
  high: 'Demucs HT — cleaner separation, about 3 minutes per song minute.',
  high6: 'Demucs HT 6-source — adds guitar and piano stems, about 4 minutes per song minute.',
};

// Three segments still fit the control while labels stay under ~14 characters (README decision).
export const QUALITY_LABEL: Record<Quality, string> = { fast: 'FAST', high: 'HIGH QUALITY', high6: 'HQ · 6 STEMS' };

export const TRANSCRIBE_HELP = 'Beat grid, tempo map and draft MIDI for drums, bass, guitar and piano in the .dawproject and as .mid files. Needs the 6-stem model.';

export interface QualityPickerProps {
  quality: Quality;
  onQuality: (q: Quality) => void;
  /** Shown only when the account has the "transcribe" feature. */
  transcribeAvailable: boolean;
  transcribe: boolean;
  onTranscribe: (on: boolean) => void;
}

/**
 * Quality segments plus the owner-only Transcribe toggle. Turning
 * Transcribe on pins quality to high6 (the server rejects anything else
 * with transcribe_requires_high6) and disables the other segments.
 */
export function QualityPicker({ quality, onQuality, transcribeAvailable, transcribe, onTranscribe }: QualityPickerProps) {
  const locked = transcribeAvailable && transcribe;
  return (
    <>
      <div className="rk-field">
        <label id="qlabel">Processing quality</label>
        <div className="rk-seg rk-seg--field" role="group" aria-labelledby="qlabel" aria-describedby="qhelp">
          {(['fast', 'high', 'high6'] as Quality[]).map((q) => (
            <button key={q} type="button" aria-pressed={quality === q} disabled={locked && q !== 'high6'} onClick={() => onQuality(q)}>
              {QUALITY_LABEL[q]}
            </button>
          ))}
        </div>
      </div>
      {transcribeAvailable && (
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
                if (e.target.checked) onQuality('high6');
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
export function qualityHelp(quality: Quality, transcribe: boolean): string {
  return transcribe ? TRANSCRIBE_HELP : QUALITY_HELP[quality];
}
