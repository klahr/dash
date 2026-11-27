// ffmpeg and ffprobe, for the clips, the recording and the final video.
import { spawn } from 'node:child_process';
import { writeFileSync } from 'node:fs';

export const FPS = 30;
export const WIDTH = 1920, HEIGHT = 1080;
export const XFADE = 0.5; // the crossfade between the intro, the tutorial and the outro

// run runs cmd with args and returns its stdout; on failure the error
// holds the end of its stderr. input, when given, is written to its stdin.
export function run(cmd, args, { input } = {}) {
  return new Promise((resolve, reject) => {
    const p = spawn(cmd, args, { stdio: [input ? 'pipe' : 'ignore', 'pipe', 'pipe'] });
    let out = '', err = '';
    p.stdout.on('data', (d) => { out += d; });
    p.stderr.on('data', (d) => { err = (err + d).slice(-4000); });
    p.on('error', reject);
    p.on('close', (code) => (code === 0 ? resolve(out) : reject(new Error(`${cmd} failed (${code}):\n${err}`))));
    if (input) p.stdin.end(input);
  });
}
export const ffmpeg = (args, opts) => run('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-y', ...args], opts);

export async function probeDuration(file) {
  const out = await run('ffprobe', ['-v', 'error', '-show_entries', 'format=duration', '-of', 'csv=p=0', file]);
  const d = parseFloat(out);
  if (!Number.isFinite(d)) throw new Error(`no duration for ${file}`);
  return d;
}

// The H.264 settings every video part is encoded with, so they join cleanly.
export const H264 = ['-c:v', 'libx264', '-preset', 'medium', '-crf', '18', '-pix_fmt', 'yuv420p', '-r', String(FPS)];

// concatList writes an ffmpeg concat list that shows each frame from its
// timestamp until the next one's, the last until end (seconds, same clock).
// Frames before start are dropped, except the last of them, which is shown
// from start.
export function concatList(frames, start, end) {
  let first = 0;
  while (first + 1 < frames.length && frames[first + 1].t <= start) first++;
  const kept = frames.slice(first).filter((f) => f.t < end);
  const lines = ['ffconcat version 1.0'];
  kept.forEach((f, i) => {
    const from = Math.max(f.t, start);
    const to = i + 1 < kept.length ? kept[i + 1].t : end;
    lines.push(`file '${f.file.replace(/'/g, "'\\''")}'`, `duration ${Math.max(0, to - from).toFixed(6)}`);
  });
  // The concat demuxer ignores the last entry's duration unless it is
  // listed once more.
  if (kept.length) lines.push(`file '${kept[kept.length - 1].file.replace(/'/g, "'\\''")}'`);
  return lines.join('\n') + '\n';
}

// timeMap maps a time in the recording (seconds from its start) to the
// time in the video, where each of spans ({from, to, speed}, recording
// seconds, not overlapping) plays speed times faster.
export function timeMap(spans = []) {
  const fast = spans.filter((s) => s.speed > 1 && s.to > s.from);
  return (t) => {
    let saved = 0;
    for (const s of fast) saved += Math.max(0, Math.min(t, s.to) - s.from) * (1 - 1 / s.speed);
    return t - saved;
  };
}

// retimeFrames moves frames (timestamps on Chrome's clock, from start to
// end) to the video's time, from 0, with spans sped up (see timeMap). It
// returns the frames and the video's length.
export function retimeFrames(frames, start, end, spans = []) {
  const map = timeMap(spans);
  return { frames: frames.map((f) => ({ ...f, t: map(f.t - start) })), length: map(end - start) };
}

// framesToVideo encodes timestamped frames into a constant-rate video of
// end - start seconds, or less where spans (see timeMap) play faster. It
// returns the video's length.
export async function framesToVideo(frames, start, end, listFile, out, spans = []) {
  const r = retimeFrames(frames, start, end, spans);
  writeFileSync(listFile, concatList(r.frames, 0, r.length));
  await ffmpeg(['-f', 'concat', '-safe', '0', '-i', listFile,
    '-vf', `fps=${FPS},scale=${WIDTH}:${HEIGHT}:flags=lanczos,format=yuv420p`, '-t', r.length.toFixed(3), ...H264, '-an', out]);
  return r.length;
}

// mixNarration places each clip at its step's start (seconds into the
// tutorial) on one track of length seconds.
export async function mixNarration(clips, length, out) {
  const placed = clips.filter((c) => c);
  if (!placed.length) {
    await ffmpeg(['-f', 'lavfi', '-i', 'anullsrc=r=48000:cl=stereo', '-t', length.toFixed(3), '-c:a', 'pcm_s16le', out]);
    return;
  }
  const inputs = placed.flatMap((c) => ['-i', c.file]);
  const chains = placed.map((c, i) => {
    const ms = Math.round(c.at * 1000);
    return `[${i}:a]aresample=48000,aformat=channel_layouts=stereo,adelay=${ms}|${ms}[a${i}]`;
  });
  const mix = `${placed.map((_, i) => `[a${i}]`).join('')}amix=inputs=${placed.length}:normalize=0:dropout_transition=0,apad[out]`;
  await ffmpeg([...inputs, '-filter_complex', [...chains, mix].join(';'), '-map', '[out]', '-t', length.toFixed(3), '-c:a', 'pcm_s16le', out]);
}

// joinParts crossfades intro → tutorial → outro and lays the narration
// (fading in as the intro ends) and an optional sting under them, and music
// under the intro and the outro (music: {intro, outro, volume}, files or ''):
// the intro's fades out as the tutorial fades in, the outro's fades in with
// the outro and out at the end.
export async function joinParts({ intro, tutorial, outro, narration, sting, music, out }) {
  const [I, T, O] = await Promise.all([intro, tutorial, outro].map(probeDuration));
  const offset = I - XFADE;
  const total = I + T + O - 2 * XFADE;
  const args = ['-i', intro, '-i', tutorial, '-i', outro, '-i', narration];
  if (sting) args.push('-i', sting);
  const norm = `settb=AVTB,fps=${FPS},format=yuv420p`;
  const f = [
    `[0:v]${norm}[v0]`, `[1:v]${norm}[v1]`, `[2:v]${norm}[v2]`,
    `[v0][v1]xfade=transition=fade:duration=${XFADE}:offset=${offset.toFixed(3)}[v01]`,
    `[v01][v2]xfade=transition=fade:duration=${XFADE}:offset=${(I + T - 2 * XFADE).toFixed(3)}[v]`,
    `[3:a]aresample=48000,aformat=channel_layouts=stereo,afade=t=in:d=${XFADE},adelay=${Math.round(offset * 1000)}:all=1,apad[nar]`,
  ];
  const under = ['[nar]'];
  let next = 4;
  if (sting) {
    f.push(`[${next++}:a]aresample=48000,aformat=channel_layouts=stereo,apad[sting]`);
    under.push('[sting]');
  }
  const vol = music?.volume ?? 0.35;
  if (music?.intro) {
    args.push('-i', music.intro);
    f.push(`[${next++}:a]aresample=48000,aformat=channel_layouts=stereo,atrim=0:${I.toFixed(3)},afade=t=in:d=0.2,` +
      `afade=t=out:st=${Math.max(0, I - XFADE - 0.3).toFixed(3)}:d=${(XFADE + 0.3).toFixed(3)},volume=${vol},apad[mi]`);
    under.push('[mi]');
  }
  if (music?.outro) {
    args.push('-i', music.outro);
    const at = I + T - 2 * XFADE;
    f.push(`[${next++}:a]aresample=48000,aformat=channel_layouts=stereo,atrim=0:${O.toFixed(3)},afade=t=in:d=${XFADE},` +
      `afade=t=out:st=${Math.max(0, O - 1.5).toFixed(3)}:d=1.5,volume=${vol},adelay=${Math.round(at * 1000)}:all=1,apad[mo]`);
    under.push('[mo]');
  }
  f.push(under.length > 1 ? `${under.join('')}amix=inputs=${under.length}:normalize=0:duration=first[a]` : '[nar]anull[a]');
  await ffmpeg([...args, '-filter_complex', f.join(';'), '-map', '[v]', '-map', '[a]', '-t', total.toFixed(3),
    ...H264, '-c:a', 'aac', '-b:a', '192k', '-ar', '48000', '-movflags', '+faststart', out]);
  return { intro: I, tutorial: T, outro: O, total, offset };
}
