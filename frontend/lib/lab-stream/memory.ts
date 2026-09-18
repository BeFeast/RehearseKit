import type { WavFormat } from './wav-header';

export interface MemorySample {
  jsHeapUsedMB?: number;
  jsHeapTotalMB?: number;
  jsHeapLimitMB?: number;
  /** performance.measureUserAgentSpecificMemory(): includes ArrayBuffer backing stores. */
  uaMemoryMB?: number;
}

interface LegacyMemory {
  usedJSHeapSize: number;
  totalJSHeapSize: number;
  jsHeapSizeLimit: number;
}

interface PerformanceWithMemory extends Performance {
  memory?: LegacyMemory;
  measureUserAgentSpecificMemory?: () => Promise<{ bytes: number }>;
}

const MB = 1024 * 1024;

export async function readMemory(): Promise<MemorySample> {
  const perf = performance as PerformanceWithMemory;
  const sample: MemorySample = {};
  if (perf.memory) {
    sample.jsHeapUsedMB = perf.memory.usedJSHeapSize / MB;
    sample.jsHeapTotalMB = perf.memory.totalJSHeapSize / MB;
    sample.jsHeapLimitMB = perf.memory.jsHeapSizeLimit / MB;
  }
  if (typeof perf.measureUserAgentSpecificMemory === 'function' && globalThis.crossOriginIsolated) {
    try {
      const r = await perf.measureUserAgentSpecificMemory();
      sample.uaMemoryMB = r.bytes / MB;
    } catch {
      // Not available in this context; keep the legacy numbers.
    }
  }
  return sample;
}

/** Bytes a full decodeAudioData of every stem would occupy (Float32 planar). */
export function baselineBytes(fmts: Pick<WavFormat, 'totalFrames' | 'channels'>[]): number {
  return fmts.reduce((sum, f) => sum + f.totalFrames * f.channels * 4, 0);
}

export function formatMB(bytes: number | undefined, digits = 1): string {
  if (bytes === undefined || !Number.isFinite(bytes)) return 'n/a';
  return `${(bytes / MB).toFixed(digits)} MB`;
}
