// Tutorial scripts: one YAML (or JSON) file per video, a header and steps.
// See ../README.md for the format. parseYAML reads the small part of YAML
// the scripts use, so the pipeline needs no npm packages: maps, lists,
// plain and quoted scalars, numbers, booleans, null, comments and block
// scalars (| and >).
import { readFileSync } from 'node:fs';
import { basename, extname } from 'node:path';

// parseYAML parses text and returns its value.
export function parseYAML(text) {
  const lines = text.replace(/\r\n?/g, '\n').split('\n').map((raw, i) => ({ raw, n: i + 1 }));
  let pos = 0;
  const fail = (line, msg) => { throw new Error(`line ${line.n}: ${msg}`); };
  const indentOf = (s) => s.match(/^ */)[0].length;
  const blank = (s) => /^\s*(#.*)?$/.test(s);
  const skip = () => { while (pos < lines.length && blank(lines[pos].raw)) pos++; };

  // block parses the lines indented exactly ind as a map or a list.
  function block(ind) {
    skip();
    if (pos >= lines.length) return null;
    const line = lines[pos];
    if (/\t/.test(line.raw.match(/^\s*/)[0])) fail(line, 'tabs are not allowed for indentation');
    const body = line.raw.slice(ind);
    return body.startsWith('- ') || body === '-' ? list(ind) : map(ind);
  }

  function list(ind) {
    const out = [];
    for (skip(); pos < lines.length; skip()) {
      const line = lines[pos];
      const i = indentOf(line.raw);
      if (i < ind) break;
      if (i > ind) fail(line, 'unexpected indentation');
      const body = line.raw.slice(ind);
      if (!(body.startsWith('- ') || body === '-')) break;
      const rest = body.slice(1).replace(/^ +/, '');
      const restInd = ind + body.length - body.slice(1).replace(/^ +/, '').length;
      if (rest === '' || rest.startsWith('#')) {
        pos++;
        skip();
        out.push(pos < lines.length && indentOf(lines[pos].raw) > ind ? block(indentOf(lines[pos].raw)) : null);
      } else if (keyMatch(rest)) {
        // "- key: value" starts a map whose keys line up with "key".
        lines[pos] = { raw: ' '.repeat(restInd) + rest, n: line.n };
        out.push(map(restInd));
      } else {
        pos++;
        out.push(scalar(rest, line, ind));
      }
    }
    return out;
  }

  function map(ind) {
    const out = {};
    for (skip(); pos < lines.length; skip()) {
      const line = lines[pos];
      const i = indentOf(line.raw);
      if (i < ind) break;
      if (i > ind) fail(line, 'unexpected indentation');
      const body = line.raw.slice(ind);
      if (body.startsWith('- ')) break;
      const m = keyMatch(body);
      if (!m) fail(line, `expected "key: value", got ${JSON.stringify(body.trim())}`);
      const key = unquote(m[1], line);
      if (Object.hasOwn(out, key)) fail(line, `duplicate key ${key}`);
      const rest = m[2].trim();
      pos++;
      if (rest === '' || rest.startsWith('#')) {
        skip();
        const next = pos < lines.length ? lines[pos] : null;
        const ni = next ? indentOf(next.raw) : -1;
        // A list may sit at the key's own indentation.
        if (next && (ni > ind || (ni === ind && next.raw.slice(ind).startsWith('- ')))) out[key] = block(ni);
        else out[key] = null;
      } else {
        out[key] = scalar(rest, line, ind);
      }
    }
    return out;
  }

  // scalar reads a value that starts on line (after "key:" or "- ").
  function scalar(s, line, ind) {
    if (s === '|' || s === '>' || /^[|>][-+]?$/.test(s)) return blockScalar(s, ind);
    if (s.startsWith('"') || s.startsWith("'")) {
      const end = closingQuote(s);
      if (end < 0) fail(line, 'unterminated quoted string');
      const after = s.slice(end + 1).trim();
      if (after && !after.startsWith('#')) fail(line, `unexpected text after the string: ${after}`);
      return unquote(s.slice(0, end + 1), line);
    }
    if (s.startsWith('[') || s.startsWith('{')) {
      try { return JSON.parse(s); } catch { fail(line, 'flow collections must be valid JSON'); }
    }
    const v = s.replace(/\s+#.*$/, '').trim();
    if (v === 'true') return true;
    if (v === 'false') return false;
    if (v === 'null' || v === '~') return null;
    if (/^-?\d+(\.\d+)?$/.test(v)) return Number(v);
    return v;
  }

  function blockScalar(head, ind) {
    const folded = head[0] === '>';
    const chomp = head[1];
    const raw = [];
    let bodyInd = -1;
    while (pos < lines.length) {
      const l = lines[pos].raw;
      if (l.trim() === '') { raw.push(''); pos++; continue; }
      const i = indentOf(l);
      if (i <= ind) break;
      if (bodyInd < 0) bodyInd = i;
      if (i < bodyInd) break;
      raw.push(l.slice(bodyInd));
      pos++;
    }
    while (raw.length && raw[raw.length - 1] === '') raw.pop();
    let text;
    if (folded) {
      text = '';
      for (let k = 0; k < raw.length; k++) {
        if (raw[k] === '') text += '\n';
        else text += (k > 0 && raw[k - 1] !== '' ? ' ' : '') + raw[k];
      }
    } else text = raw.join('\n');
    return chomp === '-' ? text : text + '\n';
  }

  function unquote(s, line) {
    if (s.startsWith('"')) {
      try { return JSON.parse(s); } catch { fail(line, `bad double-quoted string ${s}`); }
    }
    if (s.startsWith("'")) return s.slice(1, -1).replace(/''/g, "'");
    return s;
  }

  const v = block(0);
  skip();
  if (pos < lines.length) fail(lines[pos], 'unexpected indentation');
  return v;
}

// keyMatch matches "key: rest" or "key:" with a plain or quoted key.
function keyMatch(s) {
  return s.match(/^("(?:[^"\\]|\\.)*"|'(?:[^']|'')*'|[^\s#'"\-][^:#]*?|-[^\s:#][^:#]*?)\s*:(?:\s+(.*)|$)/)?.slice(0, 3).map((x) => x ?? '');
}

function closingQuote(s) {
  const q = s[0];
  for (let i = 1; i < s.length; i++) {
    if (q === '"' && s[i] === '\\') { i++; continue; }
    if (s[i] === q) {
      if (q === "'" && s[i + 1] === "'") { i++; continue; }
      return i;
    }
  }
  return -1;
}

// The actions a step's "do" list can hold, and the options each one takes.
export const ACTIONS = {
  click: ['text', 'in', 'timeout'],
  hover: ['text', 'in', 'timeout'],
  type: ['into', 'text', 'in', 'cps'],
  key: [],
  wait: ['text', 'in', 'timeout', 'gone'],
  scroll: ['text', 'in', 'by', 'timeout'],
  drag: ['from', 'to', 'circle', 'seconds', 'text', 'in', 'timeout'],
  pause: [],
  open: [],
  terminal: ['output', 'close', 'run', 'from'],
  caption: [],
};
const HEADER = ['title', 'next', 'voice', 'model', 'voice_settings', 'seed', 'start', 'sign_in', 'brand', 'sting', 'music', 'gap', 'lead', 'tail', 'steps'];
export const BRANDS = ['full', 'short'];

// validate checks a parsed script and fills in defaults. name is used in
// error messages and as the output's name.
export function validate(s, name = 'script') {
  const err = (msg) => { throw new Error(`${name}: ${msg}`); };
  if (!s || typeof s !== 'object' || Array.isArray(s)) err('expected a map with a title and steps');
  for (const k of Object.keys(s)) if (!HEADER.includes(k)) err(`unknown header field ${k} (known: ${HEADER.join(', ')})`);
  if (typeof s.title !== 'string' || !s.title.trim()) err('title is required');
  if (!Array.isArray(s.steps) || !s.steps.length) err('steps must be a non-empty list');
  for (const k of ['gap', 'lead', 'tail']) if (s[k] != null && !(typeof s[k] === 'number' && s[k] >= 0)) err(`${k} must be a number of seconds`);
  if (s.voice_settings != null && (typeof s.voice_settings !== 'object' || Array.isArray(s.voice_settings))) err('voice_settings must be a map');
  if (s.sign_in != null && typeof s.sign_in !== 'boolean') err('sign_in must be true or false');
  if (s.brand != null && !BRANDS.includes(s.brand)) err(`brand must be ${BRANDS.join(' or ')}`);
  const steps = s.steps.map((st, i) => {
    const where = `step ${i + 1}`;
    if (!st || typeof st !== 'object' || Array.isArray(st)) err(`${where}: expected a map with say and do`);
    for (const k of Object.keys(st)) if (!['say', 'do', 'hold', 'speed'].includes(k)) err(`${where}: unknown field ${k}`);
    if (st.say != null && typeof st.say !== 'string') err(`${where}: say must be text`);
    if (st.hold != null && !(typeof st.hold === 'number' && st.hold >= 0)) err(`${where}: hold must be a number of seconds`);
    if (st.speed != null && !(typeof st.speed === 'number' && st.speed > 1)) err(`${where}: speed must be a number above 1 (how many times faster the step plays)`);
    const actions = (st.do ?? []).map((a, j) => action(a, `${where}, action ${j + 1}`, err));
    if (!Array.isArray(st.do ?? [])) err(`${where}: do must be a list`);
    const say = (st.say ?? '').replace(/\s+/g, ' ').trim();
    return { say, do: actions, hold: st.hold ?? 0, speed: st.speed ?? 1 };
  });
  return {
    title: s.title.trim(), next: s.next ? String(s.next).trim() : '',
    voice: s.voice || '', model: s.model || '', voice_settings: s.voice_settings || null,
    seed: s.seed || 'landing', start: s.start || '/', sign_in: s.sign_in ?? true, brand: s.brand || 'full', sting: s.sting || '', music: s.music ?? true,
    gap: s.gap ?? 0.4, lead: s.lead ?? 0.8, tail: s.tail ?? 1.2,
    steps,
  };
}

function action(a, where, err) {
  if (typeof a === 'string') a = { [a]: true };
  if (!a || typeof a !== 'object' || Array.isArray(a)) err(`${where}: expected a map like {click: "#new-todo"}`);
  const kinds = Object.keys(a).filter((k) => Object.hasOwn(ACTIONS, k));
  if (kinds.length !== 1) err(`${where}: needs exactly one of ${Object.keys(ACTIONS).join(', ')}`);
  const kind = kinds[0];
  for (const k of Object.keys(a)) if (k !== kind && !ACTIONS[kind].includes(k)) err(`${where}: ${kind} takes no ${k}`);
  const v = a[kind];
  if (kind === 'pause' && !(typeof v === 'number' && v >= 0)) err(`${where}: pause takes seconds`);
  if (kind === 'caption' && typeof v !== 'string') err(`${where}: caption takes text ("" clears it)`);
  if (kind === 'terminal') {
    if (typeof v !== 'string' || !v) err(`${where}: terminal takes the command to type`);
    if (a.output != null && !(Array.isArray(a.output) && a.output.every((l) => typeof l === 'string'))) err(`${where}: terminal's output must be a list of lines`);
    if (a.close != null && !(typeof a.close === 'number' && a.close >= 0)) err(`${where}: terminal's close is the seconds to keep it before it closes`);
    if (a.run != null && (typeof a.run !== 'string' || !a.run.trim())) err(`${where}: terminal's run takes a shell command`);
    if (a.from != null && (typeof a.from !== 'string' || !a.from)) err(`${where}: terminal's from takes a selector`);
    if (v.includes('{from}') && a.from == null) err(`${where}: terminal's command has {from} but no from: selector`);
  }
  if (['click', 'hover', 'wait', 'scroll', 'drag', 'type', 'key', 'open'].includes(kind) && (typeof v !== 'string' || !v)) err(`${where}: ${kind} takes text`);
  if (kind === 'drag') {
    // Points are [x, y] in the element's box: 0 is its left or top edge, 1 its right or bottom.
    const point = (p, name) => {
      if (!Array.isArray(p) || p.length !== 2 || !p.every((n) => typeof n === 'number' && n >= 0 && n <= 1)) err(`${where}: drag's ${name} must be [x, y], each from 0 to 1`);
    };
    if (a.from != null) point(a.from, 'from');
    if ((a.to == null) === (a.circle == null)) err(`${where}: drag needs to: [x, y] (a line) or circle: <radius> (a circle around from)`);
    if (a.to != null) point(a.to, 'to');
    if (a.circle != null && !(typeof a.circle === 'number' && a.circle > 0 && a.circle <= 0.5)) err(`${where}: drag's circle is a radius, from 0 to 0.5 of the element's smaller side`);
    if (a.seconds != null && !(typeof a.seconds === 'number' && a.seconds > 0)) err(`${where}: drag's seconds must be a number above 0`);
  }
  return { ...a, kind };
}

// dragPath is the points a drag moves through, as [x, y] fractions of the
// element's box: n steps along a line from "from" to "to", or once around a
// circle of radius "circle" (a share of the box's smaller side) centred on
// "from", starting at the top and a little past it, the way a hand closes a
// circle. aspect is the box's width over its height, so the circle stays round.
export function dragPath({ from = [0.5, 0.5], to, circle }, aspect = 1, n = 40) {
  if (to) return Array.from({ length: n + 1 }, (_, i) => [from[0] + (to[0] - from[0]) * i / n, from[1] + (to[1] - from[1]) * i / n]);
  const rx = circle * Math.min(1, 1 / aspect), ry = circle * Math.min(1, aspect);
  return Array.from({ length: n + 1 }, (_, i) => {
    const t = -Math.PI / 2 + (2 * Math.PI * 1.06) * i / n;
    return [from[0] + rx * Math.cos(t), from[1] + ry * Math.sin(t)];
  });
}

// loadScript reads and validates a script file (.yaml, .yml or .json).
export function loadScript(file) {
  const text = readFileSync(file, 'utf8');
  const parsed = extname(file) === '.json' ? JSON.parse(text) : parseYAML(text);
  const s = validate(parsed, basename(file));
  s.name = basename(file, extname(file));
  return s;
}
