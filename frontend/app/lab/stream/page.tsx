import { Suspense } from 'react';
import { notFound } from 'next/navigation';
import { StreamLab } from './stream-lab';

export const dynamic = 'force-dynamic';

export const metadata = {
  title: 'Streaming playback lab',
  robots: { index: false, follow: false },
};

/**
 * Architecture lab: plays lossless stems by streaming HTTP byte ranges into an
 * AudioWorklet ring buffer. Enabled only with NEXT_PUBLIC_LAB_STREAM=1; never
 * linked from product navigation.
 */
export default function StreamLabPage() {
  if (process.env.NEXT_PUBLIC_LAB_STREAM !== '1') notFound();
  return (
    <Suspense fallback={<div className="p-6 text-sm text-muted-foreground">Loading lab…</div>}>
      <StreamLab />
    </Suspense>
  );
}
