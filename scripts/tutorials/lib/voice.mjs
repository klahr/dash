// The narration: one clip per step's "say" line, from ElevenLabs'
// text-to-speech (with timestamps), cached by what went into it. With
// noVoice, silent clips of an estimated length stand in, so the pipeline
// runs without a key or a network.
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { ffmpeg, probeDuration } from './media.mjs';

export const DEFAULT_VOICE = '21m00Tcm4TlvDq8ikWAM'; // "Rachel", one of ElevenLabs' premade voices
export const DEFAULT_MODEL = 'eleven_multilingual_v2';
export const OUTPUT_FORMAT = 'mp3_44100_128';
const API = 'https://api.elevenlabs.io/v1/text-to-speech';

// voiceConfig picks the voice, model and settings: the environment first,
// then the script's header, then the defaults.
export function voiceConfig(script, env = process.env) {
  let settings = script.voice_settings || null;
  if (env.ELEVENLABS_VOICE_SETTINGS) settings = JSON.parse(env.ELEVENLABS_VOICE_SETTINGS);
  return {
    voice: env.ELEVENLABS_VOICE_ID || script.voice || DEFAULT_VOICE,
    model: env.ELEVENLABS_MODEL || script.model || DEFAULT_MODEL,
    settings,
  };
}

// cacheKey is the hash a clip is cached under: everything that changes
// the audio, and nothing else.
export function cacheKey(text, { voice, model, settings }) {
  const canon = (v) => (v && typeof v === 'object' && !Array.isArray(v)
    ? Object.fromEntries(Object.keys(v).sort().map((k) => [k, canon(v[k])])) : v);
  const payload = JSON.stringify({ v: 1, text, voice, model, settings: canon(settings || null), format: OUTPUT_FORMAT });
  return createHash('sha256').update(payload).digest('hex').slice(0, 32);
}

// estimateSeconds guesses how long text takes to say: about 15 characters
// a second (150 words a minute), plus a little for each pause.
export function estimateSeconds(text) {
  if (!text) return 0;
  const pauses = (text.match(/[.,;:!?]/g) || []).length;
  return Math.max(1.2, Math.round((text.length / 15 + pauses * 0.15 + 0.3) * 100) / 100);
}

// estimatedAlignment spreads text's characters evenly over seconds, with
// punctuation counting double, the way ElevenLabs' alignment is shaped.
export function estimatedAlignment(text, seconds) {
  const chars = [...text];
  const weight = (c) => (/[.,;:!?]/.test(c) ? 2 : 1);
  const total = chars.reduce((s, c) => s + weight(c), 0) || 1;
  const per = (seconds - 0.2) / total;
  let t = 0.1;
  const starts = [], ends = [];
  for (const c of chars) {
    starts.push(round(t));
    t += per * weight(c);
    ends.push(round(t));
  }
  return { characters: chars, character_start_times_seconds: starts, character_end_times_seconds: ends };
}
const round = (x) => Math.round(x * 1000) / 1000;

// clipFor returns the step's clip: {file, seconds, alignment}, or null for a
// step with nothing to say. Real clips are cached in cacheDir/voice;
// silent ones are written to workDir.
export async function clipFor(text, cfg, { cacheDir, workDir, noVoice, log = console.error }) {
  if (!text) return null;
  if (noVoice) {
    const seconds = estimateSeconds(text);
    const file = join(workDir, `silent-${cacheKey(text, cfg).slice(0, 12)}.wav`);
    if (!existsSync(file)) await ffmpeg(['-f', 'lavfi', '-i', 'anullsrc=r=44100:cl=mono', '-t', String(seconds), '-c:a', 'pcm_s16le', file]);
    return { file, seconds, alignment: estimatedAlignment(text, seconds), cached: false };
  }
  const dir = join(cacheDir, 'voice');
  mkdirSync(dir, { recursive: true });
  const key = cacheKey(text, cfg);
  const file = join(dir, `${key}.mp3`), meta = join(dir, `${key}.json`);
  if (existsSync(file) && existsSync(meta)) {
    const m = JSON.parse(readFileSync(meta, 'utf8'));
    return { file, seconds: m.seconds, alignment: m.alignment, cached: true };
  }
  const apiKey = process.env.ELEVENLABS_API_KEY;
  if (!apiKey) throw new Error('ELEVENLABS_API_KEY is not set: set it, or use --no-voice for silent clips');
  log(`voice: ${JSON.stringify(text.slice(0, 60))}${text.length > 60 ? '…' : ''}`);
  const body = { text, model_id: cfg.model };
  if (cfg.settings) body.voice_settings = cfg.settings;
  const res = await fetch(`${API}/${encodeURIComponent(cfg.voice)}/with-timestamps?output_format=${OUTPUT_FORMAT}`, {
    method: 'POST',
    headers: { 'xi-api-key': apiKey, 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    // ElevenLabs' error bodies say what went wrong and never echo the key.
    const detail = (await res.text()).slice(0, 400);
    throw new Error(`ElevenLabs answered ${res.status}: ${detail}`);
  }
  const data = await res.json();
  if (!data.audio_base64 || !data.alignment) throw new Error('ElevenLabs answered without audio or alignment');
  writeFileSync(file + '.tmp', Buffer.from(data.audio_base64, 'base64'));
  const seconds = await probeDuration(file + '.tmp');
  writeFileSync(meta, JSON.stringify({ text, voice: cfg.voice, model: cfg.model, settings: cfg.settings, seconds, alignment: data.alignment }));
  renameSync(file + '.tmp', file);
  return { file, seconds, alignment: data.alignment, cached: false };
}

// clipsFor makes (or finds) the clips of every step of script.
export async function clipsFor(script, opts) {
  const cfg = voiceConfig(script);
  const clips = [];
  for (const step of script.steps) clips.push(await clipFor(step.say, cfg, opts));
  return clips;
}
