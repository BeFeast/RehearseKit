import { parseWavHeader, WavHeaderError } from '../wav-header';
import { buildWav, encodeSamples } from '../testing/wav-builder';

const silence = (channels: number, frames: number) =>
  Array.from({ length: channels }, () => new Array<number>(frames).fill(0));

describe('parseWavHeader', () => {
  it('parses 16-bit stereo PCM', () => {
    const pcm = encodeSamples(silence(2, 10), 16, false);
    const { buffer, dataOffset } = buildWav({ channels: 2, sampleRate: 48000, bitsPerSample: 16 }, pcm);
    const fmt = parseWavHeader(buffer);
    expect(fmt).toEqual({
      formatTag: 1,
      channels: 2,
      sampleRate: 48000,
      bitsPerSample: 16,
      blockAlign: 4,
      isFloat: false,
      dataOffset,
      dataBytes: 40,
      totalFrames: 10,
    });
  });

  it('parses 24-bit stereo PCM (product stems)', () => {
    const pcm = encodeSamples(silence(2, 7), 24, false);
    const { buffer } = buildWav({ channels: 2, sampleRate: 48000, bitsPerSample: 24 }, pcm);
    const fmt = parseWavHeader(buffer);
    expect(fmt.bitsPerSample).toBe(24);
    expect(fmt.blockAlign).toBe(6);
    expect(fmt.totalFrames).toBe(7);
    expect(fmt.isFloat).toBe(false);
  });

  it('parses 32-bit float mono via WAVE_FORMAT_EXTENSIBLE', () => {
    const pcm = encodeSamples(silence(1, 5), 32, true);
    const { buffer, dataOffset } = buildWav(
      { channels: 1, sampleRate: 44100, bitsPerSample: 32, float: true, extensible: true },
      pcm,
    );
    const fmt = parseWavHeader(buffer);
    expect(fmt.formatTag).toBe(3);
    expect(fmt.isFloat).toBe(true);
    expect(fmt.channels).toBe(1);
    expect(fmt.dataOffset).toBe(dataOffset);
    expect(fmt.totalFrames).toBe(5);
  });

  it('parses 32-bit integer PCM', () => {
    const pcm = encodeSamples(silence(2, 3), 32, false);
    const { buffer } = buildWav({ channels: 2, sampleRate: 96000, bitsPerSample: 32 }, pcm);
    const fmt = parseWavHeader(buffer);
    expect(fmt.formatTag).toBe(1);
    expect(fmt.isFloat).toBe(false);
    expect(fmt.blockAlign).toBe(8);
  });

  it('skips LIST, PEAK and odd-sized chunks before data', () => {
    const pcm = encodeSamples(silence(2, 4), 16, false);
    const { buffer, dataOffset } = buildWav(
      { channels: 2, sampleRate: 44100, bitsPerSample: 16, extraChunks: [['LIST', 26], ['PEAK', 24], ['junk', 3]] },
      pcm,
    );
    const fmt = parseWavHeader(buffer);
    expect(fmt.dataOffset).toBe(dataOffset);
    expect(fmt.totalFrames).toBe(4);
  });

  it('resolves 0xffffffff data placeholder and clamps to file size', () => {
    const pcm = encodeSamples(silence(2, 6), 16, false);
    const { buffer, dataOffset } = buildWav(
      { channels: 2, sampleRate: 44100, bitsPerSample: 16, dataSizeField: 0xffffffff },
      pcm,
    );
    expect(() => parseWavHeader(buffer)).toThrow(WavHeaderError);
    const fmt = parseWavHeader(buffer, dataOffset + 24);
    expect(fmt.totalFrames).toBe(6);
    const clamped = parseWavHeader(buildWav({ channels: 2, sampleRate: 44100, bitsPerSample: 16 }, pcm).buffer, dataOffset + 9);
    expect(clamped.totalFrames).toBe(2);
    expect(clamped.dataBytes).toBe(8);
  });

  it('parses only the header prefix of a large file', () => {
    const pcm = encodeSamples(silence(2, 1000), 32, true);
    const { buffer, dataOffset } = buildWav({ channels: 2, sampleRate: 44100, bitsPerSample: 32, float: true }, pcm);
    const prefix = buffer.slice(0, 64);
    const fmt = parseWavHeader(prefix, buffer.byteLength);
    expect(fmt.dataOffset).toBe(dataOffset);
    expect(fmt.totalFrames).toBe(1000);
  });

  it('rejects unsupported inputs', () => {
    expect(() => parseWavHeader(new ArrayBuffer(4))).toThrow(WavHeaderError);
    const pcm = encodeSamples(silence(2, 2), 16, false);
    const { buffer } = buildWav({ channels: 2, sampleRate: 44100, bitsPerSample: 16 }, pcm);
    const bad = buffer.slice(0);
    new DataView(bad).setUint16(20, 85, true); // MP3 tag
    expect(() => parseWavHeader(bad)).toThrow(/unsupported format tag/);
    const notRiff = buffer.slice(0);
    new DataView(notRiff).setUint8(0, 0x58);
    expect(() => parseWavHeader(notRiff)).toThrow(/RIFF/);
    expect(() => parseWavHeader(buffer.slice(0, 36))).toThrow(/data chunk/);
    expect(() => parseWavHeader(buffer.slice(0, 30))).toThrow(/fmt chunk truncated/);
  });
});
