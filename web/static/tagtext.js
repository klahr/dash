// Task tags as text: the limits the server keeps (model.CleanTags), adding
// one tag to a list, and the #tags quick add takes out of what is typed.
// Pure, so tagtext_test.mjs can cover it.

export const MAX_TAGS = 20; // model.MaxCardTags
export const MAX_TAG_LEN = 40; // model.MaxCardTag, in bytes as the server counts

const bytes = (s) => new TextEncoder().encode(s).length;
const same = (a, b) => a.toLowerCase() === b.toLowerCase();

// cleanTag is a tag as the server keeps it: trimmed, its spaces squeezed
// into one (commas count as spaces, as they add a tag in the chip input).
export const cleanTag = (s) => String(s ?? "").replace(/,/g, " ").split(/\s+/).filter(Boolean).join(" ");

// addTag adds tag to tags (a tag already there, in any case, is skipped).
// It returns the new list, or an error the user can act on.
export function addTag(tags, tag) {
  const v = cleanTag(tag);
  if (!v || tags.some((x) => same(x, v))) return { tags };
  if (bytes(v) > MAX_TAG_LEN) return { tags, error: `A tag is at most ${MAX_TAG_LEN} characters.` };
  if (tags.length >= MAX_TAGS) return { tags, error: `A task has at most ${MAX_TAGS} tags.` };
  return { tags: [...tags, v] };
}

// A #tag in quick add: # right after a space or the start of a line, then
// letters, digits, - and _, with . / : allowed inside (#scan:deps, #v1.2),
// up to a space or the end, where a trailing . , ; : ! ? ) is left as text.
// It needs a letter, so issue #123 stays as it is; C# and a#b do too, as
// their # doesn't start a word, and so does # on its own (a Markdown heading).
// Code (in `backticks` or a ``` block) is left alone.
const TAG = /^[\p{L}\p{N}_-]+(?:[./:][\p{L}\p{N}_-]+)*/u;
const CODE = /(```[\s\S]*?(?:```|$)|`[^`\n]*`)/;

// quickTags takes the #tags out of text: the text without them and the
// tags, in order, without repeats (at most MAX_TAGS; more stay as text).
export function quickTags(text) {
  const tags = [];
  const parts = String(text ?? "").split(CODE);
  let prev = ""; // the character before the part
  for (let i = 0; i < parts.length; i++) {
    const part = parts[i];
    if (i % 2 === 0 && part) parts[i] = takeTags(part, prev, tags);
    if (part) prev = part.at(-1);
  }
  const rest = parts.join("").split("\n").map((l) => l.replace(/[ \t]+$/, "")).join("\n").replace(/\n{3,}/g, "\n\n").trim();
  return { text: rest, tags };
}

function takeTags(s, before, tags) {
  let out = "";
  let i = 0;
  while (i < s.length) {
    const j = s.indexOf("#", i);
    if (j < 0) break;
    const prev = j > 0 ? s[j - 1] : before;
    const m = (prev === "" || /\s/.test(prev)) && TAG.exec(s.slice(j + 1));
    const tag = m && m[0];
    const end = j + 1 + (tag ? tag.length : 0);
    const ok = tag && /\p{L}/u.test(tag) && (end === s.length || /[\s.,;:!?)]/.test(s[end])) && bytes(tag) <= MAX_TAG_LEN;
    if (!ok) { out += s.slice(i, j + 1); i = j + 1; continue; }
    const known = tags.some((x) => same(x, tag));
    if (!known && tags.length >= MAX_TAGS) { out += s.slice(i, end); i = end; continue; }
    if (!known) tags.push(tag);
    // Take the tag out with the space before it (or, at a line's start, after it).
    let head = s.slice(i, j);
    let next = end;
    if (/[ \t]$/.test(head)) head = head.replace(/[ \t]+$/, "");
    else if (/[ \t]/.test(s[next] || "")) while (/[ \t]/.test(s[next] || "")) next++;
    out += head;
    // Words on both sides keep one space between them.
    if (/\S$/.test(out) && /^[^\s.,;:!?)]/.test(s.slice(next))) out += " ";
    i = next;
  }
  return out + s.slice(i);
}
