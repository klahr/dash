#!/usr/bin/env node
// The tutorial pipeline's steps; make.sh runs them in order.
//
//   node tutorial.mjs scene <script>             the seed scene the script needs
//   node tutorial.mjs voice <script> [--no-voice] make (or find) the narration clips
//   node tutorial.mjs make <script> [--no-voice]  record the demo Dash and assemble the video
//   node tutorial.mjs brand "<title>" [--next "<title>"]  render the intro and outro to out/
//
// The demo Dash must be running for make (see make.sh).
import { existsSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { loadScript } from './lib/script.mjs';
import { clipsFor } from './lib/voice.mjs';
import { brandClips } from './lib/brand.mjs';
import { record } from './lib/record.mjs';
import { framesToVideo, joinParts, mixNarration, probeDuration } from './lib/media.mjs';
import { cues, toSRT } from './lib/srt.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const cacheDir = process.env.TUTORIAL_CACHE || join(here, 'cache');
const outDir = process.env.TUTORIAL_OUT || join(here, 'out');
const log = (...a) => console.error(...a);

// Chrome: $CHROME, or the first one found.
if (!process.env.CHROME) {
  for (const c of ['/usr/bin/google-chrome-stable', '/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser',
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome']) {
    if (existsSync(c)) { process.env.CHROME = c; break; }
  }
}

const [cmd, arg, ...rest] = process.argv.slice(2);
const flag = (f) => rest.includes(f);
const option = (f) => (rest.includes(f) ? rest[rest.indexOf(f) + 1] : undefined);
const noVoice = flag('--no-voice');

async function make(file) {
  const script = loadScript(file);
  const work = join(outDir, `${script.name}.work`);
  rmSync(work, { recursive: true, force: true });
  mkdirSync(work, { recursive: true });

  const clips = await clipsFor(script, { cacheDir, workDir: work, noVoice, log });
  const brand = await brandClips(script, { cacheDir, log });

  log('recording');
  const rec = await record(script, clips, {
    base: process.env.DASH_URL || 'http://127.0.0.1:7791',
    user: process.env.DASH_USERNAME || 'demo',
    pass: process.env.DASH_PASSWORD || 'demo-password-for-shots',
    workDir: work, log,
  });
  const length = rec.end - rec.start;
  const gaps = rec.frames.slice(1).map((f, i) => f.t - rec.frames[i].t);
  log(`recorded ${rec.frames.length} frames over ${length.toFixed(1)}s (${(rec.frames.length / length).toFixed(1)} fps on average, longest gap ${(Math.max(0, ...gaps) * 1000).toFixed(0)} ms)`);

  log('encoding the recording');
  const tutorial = join(work, 'tutorial.mp4');
  await framesToVideo(rec.frames, rec.start, rec.end, join(work, 'frames.txt'), tutorial);
  if (!process.env.TUTORIAL_KEEP_FRAMES) rmSync(join(work, 'frames'), { recursive: true, force: true });
  const placed = clips.map((c, i) => (c ? { ...c, at: rec.steps[i].at, text: script.steps[i].say } : null));
  const narration = join(work, 'narration.wav');
  await mixNarration(placed, length, narration);

  log('joining the intro, the tutorial and the outro');
  const sting = process.env.TUTORIAL_STING || (script.sting ? resolve(dirname(file), script.sting) : '');
  if (sting && !existsSync(sting)) throw new Error(`no sting file ${sting}`);
  mkdirSync(outDir, { recursive: true });
  const mp4 = join(outDir, `${script.name}.mp4`);
  const parts = await joinParts({ intro: brand.intro, tutorial, outro: brand.outro, narration, sting, out: mp4 });
  const srt = join(outDir, `${script.name}.srt`);
  writeFileSync(srt, toSRT(cues(placed, parts.offset)));

  // Where each line ends up, for checking the sync by eye.
  const timing = script.steps.map((s, i) => ({
    step: i + 1, at: +(parts.offset + rec.steps[i].at).toFixed(2), took: +(rec.steps[i].end - rec.steps[i].at).toFixed(2),
    line: clips[i] ? +clips[i].seconds.toFixed(2) : 0, say: s.say,
  }));
  writeFileSync(join(work, 'timing.json'), JSON.stringify({ parts, steps: timing }, null, 2));
  const total = await probeDuration(mp4);
  log(`${mp4}: ${total.toFixed(1)}s (intro ${parts.intro.toFixed(1)}s, tutorial ${parts.tutorial.toFixed(1)}s, outro ${parts.outro.toFixed(1)}s)`);
  log(srt);
}

try {
  switch (cmd) {
    case 'scene':
      console.log(loadScript(arg).seed);
      break;
    case 'voice': {
      const script = loadScript(arg);
      const work = join(outDir, `${script.name}.work`);
      mkdirSync(work, { recursive: true });
      const clips = await clipsFor(script, { cacheDir, workDir: work, noVoice, log });
      const live = clips.filter(Boolean);
      log(`${live.length} lines, ${live.reduce((s, c) => s + c.seconds, 0).toFixed(1)}s of narration${noVoice ? ' (silent, estimated)' : `, ${live.filter((c) => c.cached).length} from the cache`}`);
      break;
    }
    case 'make':
      await make(arg);
      break;
    case 'brand': {
      if (!arg) throw new Error('usage: node tutorial.mjs brand "<title>" [--next "<title>"]');
      const out = await brandClips({ title: arg, next: option('--next') || '' }, { cacheDir, previewDir: outDir, log });
      for (const f of Object.values(out)) log(`${f}: ${(await probeDuration(f)).toFixed(2)}s`);
      log(`stills in ${outDir}`);
      break;
    }
    default:
      log('usage: node tutorial.mjs scene|voice|make <script> [--no-voice]\n       node tutorial.mjs brand "<title>" [--next "<title>"]');
      process.exit(2);
  }
} catch (e) {
  log(`error: ${e.message}`);
  process.exit(1);
}
