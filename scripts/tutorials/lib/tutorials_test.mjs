// Tests of the parts of the tutorial pipeline that need no network, Chrome
// or demo Dash:
//
//   node --test scripts/tutorials/lib/tutorials_test.mjs   (go test ./scripts/tutorials runs it too)
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { loadScript, parseYAML, validate } from './script.mjs';
import { cacheKey, clipFor, estimateSeconds, estimatedAlignment, voiceConfig, DEFAULT_MODEL } from './voice.mjs';
import { chunks, cues, stamp, toSRT, wrap } from './srt.mjs';
import { concatList, probeDuration } from './media.mjs';
import { rootVars } from './brand.mjs';

const here = dirname(fileURLToPath(import.meta.url));

test('parseYAML reads maps, lists, scalars and block text', () => {
  const v = parseYAML(`# a comment
title: Your first task   # trailing comment
next: "Goals: plan the work"
gap: 0.5
on: true
none: null
quote: 'it''s'
settings:
  stability: 0.4
  speed: 1
steps:
  - say: >
      Two lines
      folded into one.
    do:
      - click: "#new-todo"
      - type: Hello, world
        into: .quick-row
      - pause: 1.5
  - say: |
      kept
      as is
    do: []
  -
    say: plain
`);
  assert.deepEqual(v, {
    title: 'Your first task', next: 'Goals: plan the work', gap: 0.5, on: true, none: null, quote: "it's",
    settings: { stability: 0.4, speed: 1 },
    steps: [
      { say: 'Two lines folded into one.\n', do: [{ click: '#new-todo' }, { type: 'Hello, world', into: '.quick-row' }, { pause: 1.5 }] },
      { say: 'kept\nas is\n', do: [] },
      { say: 'plain' },
    ],
  });
});

test('parseYAML takes a list at its key\'s indentation, and URLs in values', () => {
  assert.deepEqual(parseYAML('steps:\n- say: a\n- say: b\nlink: https://dash.example.com\n'),
    { steps: [{ say: 'a' }, { say: 'b' }], link: 'https://dash.example.com' });
});

test('parseYAML reports where it fails', () => {
  assert.throws(() => parseYAML('a: 1\n   b: 2\n'), /line 2/);
  assert.throws(() => parseYAML('a: 1\na: 2\n'), /line 2: duplicate key a/);
  assert.throws(() => parseYAML('a: "open\n'), /line 1: unterminated/);
});

test('first-task.yaml is valid', () => {
  const s = loadScript(join(here, '..', 'first-task.yaml'));
  assert.equal(s.name, 'first-task');
  assert.equal(s.title, 'Your first task on the board');
  assert.equal(s.seed, 'first-task');
  assert.ok(s.steps.length >= 5);
  for (const st of s.steps) assert.ok(st.say && !st.say.includes('\n'), 'say lines are one line of text');
  assert.ok(s.steps.flatMap((st) => st.do).some((a) => a.kind === 'click'));
});

test('validate fills in defaults and rejects mistakes', () => {
  const s = validate({ title: ' T ', steps: [{ say: 'Hi  there\n', do: [{ pause: 1 }] }] });
  assert.equal(s.title, 'T');
  assert.equal(s.steps[0].say, 'Hi there');
  assert.equal(s.seed, 'landing');
  assert.equal(s.start, '/');
  assert.throws(() => validate({ steps: [{ say: 'x' }] }), /title is required/);
  assert.throws(() => validate({ title: 'T', steps: [] }), /steps/);
  assert.throws(() => validate({ title: 'T', steps: [{ say: 'x', do: [{ tap: '#a' }] }] }), /needs exactly one of/);
  assert.throws(() => validate({ title: 'T', steps: [{ say: 'x', do: [{ click: '#a', into: 'b' }] }] }), /click takes no into/);
  assert.throws(() => validate({ title: 'T', colour: 'red', steps: [{ say: 'x' }] }), /unknown header field colour/);
});

test('cacheKey changes with the text, voice, model and settings only', () => {
  const cfg = { voice: 'v1', model: 'm1', settings: { stability: 0.5, speed: 1 } };
  const k = cacheKey('Hello.', cfg);
  assert.match(k, /^[0-9a-f]{32}$/);
  assert.equal(cacheKey('Hello.', { ...cfg, settings: { speed: 1, stability: 0.5 } }), k, 'key order does not matter');
  assert.notEqual(cacheKey('Hello!', cfg), k);
  assert.notEqual(cacheKey('Hello.', { ...cfg, voice: 'v2' }), k);
  assert.notEqual(cacheKey('Hello.', { ...cfg, model: 'm2' }), k);
  assert.notEqual(cacheKey('Hello.', { ...cfg, settings: { stability: 0.6, speed: 1 } }), k);
  assert.notEqual(cacheKey('Hello.', { ...cfg, settings: null }), k);
});

test('voiceConfig: the environment, then the script, then the defaults', () => {
  const script = { voice: 'from-script', model: '', voice_settings: { speed: 1.1 } };
  assert.deepEqual(voiceConfig(script, {}), { voice: 'from-script', model: DEFAULT_MODEL, settings: { speed: 1.1 } });
  assert.deepEqual(voiceConfig(script, { ELEVENLABS_VOICE_ID: 'env', ELEVENLABS_MODEL: 'em', ELEVENLABS_VOICE_SETTINGS: '{"stability":0.3}' }),
    { voice: 'env', model: 'em', settings: { stability: 0.3 } });
});

test('estimated lengths and alignments for --no-voice', () => {
  assert.equal(estimateSeconds(''), 0);
  assert.equal(estimateSeconds('Hi.'), 1.2);
  const text = 'Press New task, and describe the change you want in plain words.';
  const sec = estimateSeconds(text);
  assert.ok(sec > 4 && sec < 6, `${sec}`);
  const al = estimatedAlignment(text, sec);
  assert.equal(al.characters.join(''), text);
  assert.ok(al.character_start_times_seconds.every((t, i, a) => i === 0 || t >= a[i - 1]));
  assert.ok(al.character_end_times_seconds.at(-1) <= sec);
});

const ffmpegOK = spawnSync('ffmpeg', ['-version']).status === 0;
test('--no-voice makes a silent clip of the estimated length', { skip: !ffmpegOK && 'no ffmpeg' }, async () => {
  const dir = mkdtempSync(join(tmpdir(), 'tut-test-'));
  try {
    const text = 'Save it as a draft. Claude reads it and gives the task a short name.';
    const clip = await clipFor(text, { voice: 'v', model: 'm', settings: null }, { cacheDir: dir, workDir: dir, noVoice: true });
    assert.equal(clip.seconds, estimateSeconds(text));
    assert.ok(Math.abs((await probeDuration(clip.file)) - clip.seconds) < 0.05);
    assert.equal(await clipFor('', {}, { cacheDir: dir, workDir: dir, noVoice: true }), null);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('chunks split at sentences, then clauses, and keep prices whole', () => {
  const text = 'This is Dash. Every task in a project lives on this board, and moves from left to right: drafts, the queue, running, review, and done. It costs €9.99.';
  const parts = chunks(text).map(([a, b]) => text.slice(a, b));
  assert.equal(parts[0], 'This is Dash.');
  assert.ok(parts.every((p) => p.length <= 80), parts.join(' | '));
  assert.equal(parts.at(-1), 'It costs €9.99.');
  assert.equal(parts.join(' '), text);
});

test('wrap balances two lines', () => {
  assert.equal(wrap('Short line.'), 'Short line.');
  const w = wrap('Every task in a project lives on this board, and moves from left');
  assert.equal(w.split('\n').length, 2);
  assert.ok(w.split('\n').every((l) => l.length <= 42));
});

test('cues are shifted by the step and the intro, and SRT is well formed', () => {
  const text = 'Hello there. General Kenobi.';
  const al = estimatedAlignment(text, 3);
  const list = cues([{ at: 10, text, alignment: al }, null, { at: 20, text: '', alignment: null }], 4.2);
  assert.equal(list.length, 2);
  assert.ok(Math.abs(list[0].start - (4.2 + 10 + 0.1)) < 1e-9, `${list[0].start}`);
  assert.ok(list[0].end < list[1].start, 'no overlap');
  assert.ok(list[1].end <= 4.2 + 10 + 3);
  assert.equal(stamp(3725.5), '01:02:05,500');
  const srt = toSRT(list);
  assert.match(srt, /^1\n00:00:14,300 --> 00:00:\d\d,\d{3}\nHello there\.\n\n2\n/);
});

test('cues map proportionally when the alignment has other characters', () => {
  const al = estimatedAlignment('Hello world, again', 2); // one character fewer than the text
  const [c] = cues([{ at: 0, text: 'Hello world, again!', alignment: al }]);
  assert.ok(c.start >= 0 && c.end <= 2);
});

test('concatList holds each frame until the next, from the start to the end', () => {
  const frames = [{ t: 9, file: 'a.jpg' }, { t: 10.5, file: 'b.jpg' }, { t: 11, file: "c'.jpg" }, { t: 30, file: 'late.jpg' }];
  const list = concatList(frames, 10, 12);
  assert.equal(list, [
    'ffconcat version 1.0',
    "file 'a.jpg'", 'duration 0.500000',
    "file 'b.jpg'", 'duration 0.500000',
    "file 'c'\\''.jpg'", 'duration 1.000000',
    "file 'c'\\''.jpg'",
  ].join('\n') + '\n');
});

test('rootVars takes the colours from the app', () => {
  const v = rootVars();
  assert.match(v, /^:root\s*\{/);
  assert.match(v, /--bg:\s*#/);
  assert.match(v, /--accent:/);
  assert.match(v, /--ui:/);
});
