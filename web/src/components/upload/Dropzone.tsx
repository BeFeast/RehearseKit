import { useEffect, useRef, useState, type DragEvent } from 'react';
import { Icon } from '../Icon';
import { formatBytes } from '../../lib/format';

export interface DropzoneProps {
  onFile(file: File): void;
  error: string | null;
  onClearError(): void;
  maxBytes: number;
}

export const ACCEPT = '.wav,.mp3,.flac,.m4a,.aac,.ogg,.opus,.aiff,.aif,audio/*';

/**
 * components/file-dropzone: idle / drag over / rejected. A drag anywhere over
 * the page highlights the zone; a real <input type=file> sits behind it for
 * keyboard and assistive tech.
 */
export function Dropzone({ onFile, error, onClearError, maxBytes }: DropzoneProps) {
  const input = useRef<HTMLInputElement>(null);
  const [over, setOver] = useState(false);
  const [overName, setOverName] = useState<string | null>(null);
  const depth = useRef(0);

  useEffect(() => {
    // Highlight when a file is dragged anywhere over the page.
    const enter = (e: globalThis.DragEvent) => {
      if (!e.dataTransfer?.types.includes('Files')) return;
      depth.current += 1;
      setOver(true);
    };
    const leave = () => {
      depth.current = Math.max(0, depth.current - 1);
      if (depth.current === 0) {
        setOver(false);
        setOverName(null);
      }
    };
    const drop = () => {
      depth.current = 0;
      setOver(false);
      setOverName(null);
    };
    const prevent = (e: globalThis.DragEvent) => e.preventDefault();
    window.addEventListener('dragenter', enter);
    window.addEventListener('dragleave', leave);
    window.addEventListener('dragover', prevent);
    window.addEventListener('drop', drop);
    return () => {
      window.removeEventListener('dragenter', enter);
      window.removeEventListener('dragleave', leave);
      window.removeEventListener('dragover', prevent);
      window.removeEventListener('drop', drop);
    };
  }, []);

  const onDrop = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    depth.current = 0;
    setOver(false);
    setOverName(null);
    const f = e.dataTransfer.files?.[0];
    if (f) onFile(f);
  };

  const open = () => input.current?.click();
  const state = over ? 'over' : error ? 'error' : 'idle';

  return (
    <div
      className="rk-drop"
      data-state={state}
      data-testid="dropzone"
      tabIndex={0}
      role="button"
      aria-label={over ? 'Release to add the file' : 'Choose an audio file or drop one here'}
      aria-describedby={error ? 'droperr' : undefined}
      onClick={open}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          open();
        }
      }}
      onDragOver={(e) => {
        e.preventDefault();
        const item = e.dataTransfer.items?.[0];
        if (item && item.kind === 'file') setOverName((n) => n);
      }}
      onDrop={onDrop}
    >
      <input
        ref={input}
        type="file"
        accept={ACCEPT}
        onChange={(e) => {
          const f = e.target.files?.[0];
          if (f) onFile(f);
          e.target.value = '';
        }}
        tabIndex={-1}
        aria-hidden="true"
      />
      {over ? (
        <>
          <Icon name="file-audio" size={28} />
          <h3>Release to add</h3>
          <p>{overName ?? 'Drop the audio file to choose it'}</p>
        </>
      ) : error ? (
        <>
          <Icon name="alert" size={28} />
          <h3>That file will not work</h3>
          <p id="droperr" role="alert">
            {error}
          </p>
          <button
            className="rk-btn"
            type="button"
            onClick={(e) => {
              e.stopPropagation();
              onClearError();
              open();
            }}
          >
            Choose another file
          </button>
        </>
      ) : (
        <>
          <Icon name="upload" size={28} />
          <h3>Drop an audio file here</h3>
          <p>
            or <strong>browse</strong> — WAV, MP3, FLAC, AIFF. Up to {formatBytes(maxBytes)}.
          </p>
        </>
      )}
    </div>
  );
}
