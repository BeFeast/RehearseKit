import { useEffect } from 'react';
import { Panel } from '../components/Panel';

interface Credit {
  role: string;
  model: string;
  licence: string;
  by: string;
}

/** Mirrors the public-stack table of docs/CREDITS.md. */
const PUBLIC_MODELS: Credit[] = [
  {
    role: 'Stem separation (Standard; drums, bass and other in Plus HiFi)',
    model: 'SCNet XL IHF, Music-Source-Separation-Training release v1.0.15',
    licence: 'MIT',
    by: 'Roman Solovyev (ZFTurbo); SCNet by Tong et al.',
  },
  {
    role: 'Vocals in Plus HiFi',
    model: 'MelBand RoFormer, Kimberley Jensen edition',
    licence: 'MIT',
    by: 'Kimberley Jensen; MelBand RoFormer by Wang et al.; the UVR community',
  },
  { role: 'Tempo and key', model: 'librosa', licence: 'ISC', by: 'the librosa development team' },
];

/** Models, licences and attributions behind the public service. */
export function CreditsRoute() {
  useEffect(() => {
    document.title = 'Credits — RehearseKit';
  }, []);
  return (
    <main className="rk-shell" style={{ paddingTop: 'var(--rk-space-11)' }}>
      <Panel>
        <h2>Credits</h2>
        <p>Stems are separated by open models whose authors publish the weights under permissive licences. Thank you to them.</p>
        <ul data-testid="credits-models">
          {PUBLIC_MODELS.map((c) => (
            <li key={c.model}>
              <strong>{c.role}</strong>: {c.model} — {c.licence}. By {c.by}.
            </li>
          ))}
        </ul>
        <p>Model code: Music-Source-Separation-Training (MIT). Drum kit samples in the editor: virtuosity_drums, CC0 1.0.</p>
      </Panel>
    </main>
  );
}
