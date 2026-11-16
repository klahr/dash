// Subtitles: SRT cues from the narration's character timings.

const MAX_LINE = 42; // characters a subtitle line holds
const MAX_CUE = 80; // two lines, with room to balance them

// chunks splits text into cue-sized pieces, at sentence ends first, then
// at commas and similar, then between words. It returns [start, end)
// character ranges into text.
export function chunks(text) {
  const out = [];
  const piece = (from, to) => {
    while (from < to && /\s/.test(text[from])) from++;
    while (to > from && /\s/.test(text[to - 1])) to--;
    if (to <= from) return;
    if (to - from <= MAX_CUE) { out.push([from, to]); return; }
    // Too long: split at the last break that keeps the first part short
    // enough, preferring sentence ends, then clause marks, then spaces.
    for (const re of [/[.!?]\s/g, /[,;:–—]\s/g, /\s/g]) {
      let best = -1;
      re.lastIndex = from;
      for (let m; (m = re.exec(text)) && m.index < to;) {
        const cut = m.index + m[0].length - 1;
        if (cut - from <= MAX_CUE && cut - from >= 12) best = cut;
      }
      if (best > 0) { piece(from, best); piece(best, to); return; }
    }
    out.push([from, to]); // one very long word
  };
  // Every sentence is a cue of its own when it fits.
  // A sentence ends at . ! or ? before a space, so "€9.99" stays whole.
  const re = /\S.*?(?:[.!?]+(?=\s|$)|$)/gs;
  for (let m; (m = re.exec(text));) piece(m.index, m.index + m[0].length);
  return out;
}

// wrap breaks a cue's text into at most two balanced lines.
export function wrap(s) {
  if (s.length <= MAX_LINE) return s;
  let best = -1;
  for (let i = 0; i < s.length; i++) {
    if (s[i] !== ' ') continue;
    if (best < 0 || Math.abs(i - s.length / 2) < Math.abs(best - s.length / 2)) best = i;
  }
  return best < 0 ? s : s.slice(0, best) + '\n' + s.slice(best + 1);
}

// timeAt returns the alignment's [start, end] seconds for text's characters
// from..to-1. When the alignment's characters don't line up with text, it
// maps positions proportionally.
function timeAt(text, al, from, to) {
  const n = al.characters.length;
  const map = (i) => (n === text.length ? i : Math.min(n - 1, Math.round((i * (n - 1)) / Math.max(1, text.length - 1))));
  return [al.character_start_times_seconds[map(from)], al.character_end_times_seconds[map(to - 1)]];
}

// cues makes the subtitle cues of the narration. lines are {at, text,
// alignment} with at in seconds into the tutorial; offset is where the
// tutorial starts in the final video (the intro's length less the
// crossfade).
export function cues(lines, offset = 0) {
  const out = [];
  for (const l of lines) {
    if (!l || !l.text) continue;
    for (const [from, to] of chunks(l.text)) {
      const [s, e] = timeAt(l.text, l.alignment, from, to);
      out.push({ start: offset + l.at + s, end: offset + l.at + e, text: wrap(l.text.slice(from, to)) });
    }
  }
  // Keep each cue up long enough to read, without running into the next.
  for (let i = 0; i < out.length; i++) {
    const next = out[i + 1];
    out[i].end = Math.max(out[i].end, out[i].start + 1);
    if (next) out[i].end = Math.min(out[i].end, next.start - 0.05);
    out[i].end = Math.max(out[i].end, out[i].start + 0.2);
  }
  return out;
}

export function stamp(sec) {
  const ms = Math.max(0, Math.round(sec * 1000));
  const p = (n, w = 2) => String(n).padStart(w, '0');
  return `${p(Math.floor(ms / 3600000))}:${p(Math.floor(ms / 60000) % 60)}:${p(Math.floor(ms / 1000) % 60)},${p(ms % 1000, 3)}`;
}

// toSRT formats cues as an SRT file.
export function toSRT(list) {
  return list.map((c, i) => `${i + 1}\n${stamp(c.start)} --> ${stamp(c.end)}\n${c.text}\n`).join('\n');
}
