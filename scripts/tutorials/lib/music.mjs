// The intro's and the outro's music: short instrumentals from ElevenLabs'
// music API (@elevenlabs/elevenlabs-js), from the prompts in
// brand/brand.json, as long as the part they go under. The music API needs a
// paid plan: on a free one the same prompt goes to the sound effects API
// (up to 30 s). Cached by what went into them, like the narration, so each
// is made once.
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, renameSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

export const MUSIC_MODEL = 'music_v2_5';
// The API makes nothing shorter than this; a shorter part gets the start of it.
const MIN_MS = 3000;

// musicKey is the hash a piece is cached under.
export function musicKey(prompt, seconds) {
  return createHash('sha256').update(JSON.stringify({ v: 1, prompt, ms: musicMs(seconds), model: MUSIC_MODEL })).digest('hex').slice(0, 32);
}

// musicMs is how long a piece is asked for: the part's length, at least the
// API's shortest.
export function musicMs(seconds) {
  return Math.max(MIN_MS, Math.round(seconds * 1000));
}

// musicFor returns the cached piece for prompt and seconds, making it first
// if it isn't cached.
export async function musicFor(prompt, seconds, { cacheDir, log = console.error }) {
  const dir = join(cacheDir, 'music');
  mkdirSync(dir, { recursive: true });
  const file = join(dir, `${musicKey(prompt, seconds)}.mp3`);
  const sfxFile = join(dir, `${musicKey(prompt, seconds)}-sfx.mp3`);
  if (existsSync(file)) return file;
  if (existsSync(sfxFile)) return sfxFile;
  if (!process.env.ELEVENLABS_API_KEY) throw new Error('ELEVENLABS_API_KEY is not set: set it (or put it in .env.local), or set music to false');
  let sdk;
  try {
    sdk = await import('@elevenlabs/elevenlabs-js');
  } catch {
    throw new Error('the ElevenLabs SDK is not installed: run `npm ci --prefix scripts/tutorials`');
  }
  log(`music: ${seconds.toFixed(1)}s of ${JSON.stringify(prompt.slice(0, 60))}`);
  const api = new sdk.ElevenLabsClient({ maxRetries: 2 });
  let stream, out = file;
  try {
    try {
      stream = await api.music.compose({ prompt, musicLengthMs: musicMs(seconds), modelId: MUSIC_MODEL, forceInstrumental: true });
    } catch (err) {
      if (!(err instanceof sdk.ElevenLabsError && err.statusCode === 402)) throw err;
      log('music: the music API needs a paid plan; making it with sound effects');
      out = sfxFile;
      stream = await api.textToSoundEffects.convert({ text: prompt, durationSeconds: Math.min(30, Math.max(0.5, seconds)), promptInfluence: 0.5 });
    }
  } catch (err) {
    if (err instanceof sdk.ElevenLabsError) {
      const detail = typeof err.body === 'string' ? err.body : JSON.stringify(err.body);
      throw new Error(`ElevenLabs answered ${err.statusCode ?? 'with an error'}: ${(detail || err.message).slice(0, 400)}`);
    }
    throw err;
  }
  const chunks = [];
  for await (const c of stream) chunks.push(c);
  writeFileSync(out + '.tmp', Buffer.concat(chunks));
  renameSync(out + '.tmp', out);
  return out;
}
