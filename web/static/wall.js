// The wall view: a read-only TV dashboard. It polls /api/wall every few
// seconds and ticks the clock and the running tasks' timers in between.
// Options come from the page's own URL: ?token= (a wall link, no sign-in),
// ?project= (one project), ?privacy=1 (public mode: only what anyone may
// see; a public wall token always has it), ?nospend=1 (no costs),
// ?hours=8-18 and ?days=1-5 (work hours, for the off-hours summary).
// A module (wall.html) that imports only time.js and walllogic.js (and its
// eta.js), which, like this file, a TV with a wall token and no session may
// load.
import { clockText, resetText } from "./time.js";
import {
  age, isStale, staleText, progress, stuckText, nextCompact, drift, workHours, offHours, quiet as boardQuiet, weekText,
  deltaText, oneDecimal, headline, fullText,
} from "./walllogic.js";

const params = new URLSearchParams(location.search);
const POLL_MS = 5000;
const $ = (id) => document.getElementById(id);

// h builds an element; strings become text nodes (never HTML).
const h = DashPage.el;

function svg(tag, attrs) {
  const el = document.createElementNS("http://www.w3.org/2000/svg", tag);
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  return el;
}

// ---- formatting ----

const pad = (n) => String(n).padStart(2, "0");
const hhmm = (d) => pad(d.getHours()) + ":" + pad(d.getMinutes());
const money = (v) => "$" + v.toFixed(2);

// longAge: "2 h 14 min"
function longAge(ms) {
  const m = Math.max(0, Math.floor(ms / 60000));
  if (m < 60) return m + " min";
  return Math.floor(m / 60) + " h " + (m % 60) + " min";
}

// Counts the API leaves out when 0 (omitempty) arrive as undefined: show them as 0.
const num = (n) => (n || 0).toLocaleString(undefined).replace(/,/g, " ");

// ---- updating in place ----
//
// The wall runs for days on a TV, so it touches the DOM as little as it can:
// a poll whose data hasn't changed draws nothing, a panel is rebuilt only
// when what it shows has changed, and text is written only when it differs.

// setText writes el's text if it differs, and says whether it did.
function setText(el, s) {
  if (el.textContent === s) return false;
  el.textContent = s;
  return true;
}

// drawn holds what each panel last showed (its model as JSON), so render can
// skip the panels that would come out the same.
let drawn = {};

// changed says whether panel's model differs from what it shows now, and
// remembers it if so.
function changed(panel, model) {
  const k = JSON.stringify(model);
  if (drawn[panel] === k) return false;
  drawn[panel] = k;
  return true;
}

// ---- motion ----
//
// Things move only when the data does: a card that arrives slides in, one
// that goes slides out while the rest glide into place, a number that changes
// rolls. Every animation is short and finite, so once it ends the page draws
// nothing until the next change (an endless one would keep Chrome drawing
// frames, see .pulse in wall.html).

const calm = matchMedia("(prefers-reduced-motion: reduce)");
const EASE = "cubic-bezier(.2,.7,.2,1)";
const HOLD = 900; // how long a finished task's card shows how it ended before it goes (wall.html .ghost.held)
let first = true; // the first draw fades the panels in instead
let quiet = false; // drawing again after a resize: nothing moves
const moving = () => !quiet && !calm.matches;

// shown holds what the panels last showed that their animations compare
// with: the stage counts, the today tiles, each runner's busy slots.
const shown = { stages: null, today: {}, busy: {} };

// snapshot records where a list's cards are now, by their data-key.
function snapshot(list) {
  const m = new Map();
  for (const el of list.children) {
    if (el.dataset.key) m.set(el.dataset.key, { el, key: el.dataset.key, r: el.getBoundingClientRect() });
  }
  return m;
}

// appear fades els in one after another (the first draw).
function appear(els, step = 60) {
  if (calm.matches) return;
  els.forEach((el, i) => {
    el.classList.add("appear");
    el.style.setProperty("--d", Math.min(i, 8) * step + "ms");
  });
}

// morph animates list from before (its snapshot) to its new children: new
// cards slide in, cards that stay glide from where they were, and cards that
// went leave as ghosts over the list. outcome(key, ghost) may dress a ghost
// and say to hold it a moment first. It returns how long the list waits
// before its cards move, so a panel can time its own changes after it.
function morph(list, before, { delay = 0, outcome } = {}) {
  const kids = [...list.children];
  if (first) {
    appear(kids);
    return 0;
  }
  if (!moving()) return 0;
  // Reads.
  const box = list.getBoundingClientRect();
  const stay = [], fresh = [];
  for (const el of kids) {
    const old = el.dataset.key && before.get(el.dataset.key);
    if (old) {
      before.delete(el.dataset.key);
      stay.push([el, (old.r.top - el.getBoundingClientRect().top) / zoom]);
    } else if (el.dataset.key || el.classList.contains("empty")) {
      fresh.push(el);
    }
  }
  const gone = [...before.values()].filter((o) => o.r.height);
  // Writes.
  let hold = false;
  for (const { el, key, r } of gone) {
    el.classList.remove("appear", "enter");
    el.classList.add("ghost");
    el.style.left = (r.left - box.left) / zoom + "px";
    el.style.top = (r.top - box.top) / zoom + "px";
    el.style.width = r.width / zoom + "px";
    if (outcome && outcome(key, el)) {
      el.classList.add("held");
      hold = true;
    }
    list.append(el);
  }
  // The others move once the ghosts have mostly faded (wall.html .ghost).
  const wait = delay + (gone.length ? (hold ? HOLD : 0) + 180 : 0);
  for (const { el } of gone) setTimeout(() => el.remove(), wait + 700);
  for (const [el, dy] of stay) {
    if (Math.abs(dy) < 0.5) continue;
    el.animate([{ transform: `translateY(${dy}px)` }, { transform: "none" }],
      { duration: 600, delay: wait, easing: EASE, fill: "backwards" });
  }
  fresh.forEach((el, i) => {
    el.classList.add(el.dataset.key ? "enter" : "appear");
    el.style.setProperty("--d", wait + Math.min(i, 5) * 80 + "ms");
  });
  return hold ? wait : 0;
}

// roll makes el's new number roll in, up if it grew and down if it shrank.
// was and now are numbers, or text holding one number ("$1.20", "−3 400").
function roll(el, was, now) {
  if (was === undefined || was === now || !moving() || first) return false;
  el.classList.add(rollValue(now) < rollValue(was) ? "roll-down" : "roll-up");
  return true;
}

function rollValue(v) {
  if (typeof v === "number") return v;
  return parseFloat(String(v).replace(/−/g, "-").replace(/[^\d.-]/g, ""));
}

// ---- stale data ----
//
// When polling fails (or the last good answer is older than about two
// polls), the board must not look live: it greys out, the running timers
// stop at the time of the last good answer, and a notice over it says how
// old the data is. The next good poll clears it all, drawing only what
// changed.

let lastOk = 0; // when the last good poll answered (local time)
let okNow = 0; // the server's time then
let stale = false;

// dataNow is the time the data is drawn at: the server's now, or, while
// the data is stale, the time it was last good, so nothing counts on.
const dataNow = () => (stale ? okNow : Date.now() + skew);

function checkStale() {
  const now = Date.now();
  const was = stale;
  stale = isStale({ failures, lastOk, now, pollMs: POLL_MS });
  if (stale) setText($("stale"), staleText(lastOk, now));
  if (stale !== was) document.documentElement.classList.toggle("is-stale", stale);
  return stale !== was;
}

// ---- clock and timers, every second ----

let skew = 0; // server time − local time, so timers agree with the server
let timers = []; // the running tasks' timers: {el, since, took, prog, fill, text}, from renderRunning
let minute = ""; // the clock's text, which changes once a minute

function tick() {
  checkStale();
  const now = dataNow();
  if (!stale) for (const t of timers) tickTimer(t, now);
  document.documentElement.classList.toggle("blink", !stale && !document.documentElement.classList.contains("blink")); // the pulsing dots (wall.html)
  const real = Date.now() + skew;
  const clock = hhmm(new Date(real));
  if (clock === minute) return;
  minute = clock;
  $("clock").textContent = clock;
  setText($("off-clock"), clock);
  placeBoard();
  // Ages ("48 min") and the meters' "resets in" are to the minute: draw the
  // panels again, which only rebuilds those whose text moved.
  if (last) renderPanels(last);
}

// tickTimer shows running task t's elapsed time at now, and how it compares
// with the typical time; the bar and its words are written only when they
// change.
function tickTimer(t, now) {
  setText(t.el, clockText(t.since, now));
  if (!t.prog) return;
  const p = progress(now - t.since, t.took);
  if (!p) return;
  const width = (p.fraction * 100).toFixed(0) + "%";
  if (t.fill.style.width !== width) t.fill.style.width = width;
  setText(t.text, p.text);
  // Toggled, not set, so a row's animation classes (motion) stay.
  for (const lvl of ["over", "late"]) {
    if (t.prog.classList.contains(lvl) !== (p.level === lvl)) t.prog.classList.toggle(lvl);
  }
}

// ---- rendering ----

const USAGE = [["five_hour", "Claude 5h", "5h"], ["seven_day", "Claude 7d", "7d"]];
// The header's meters, per Claude account (one set, unnamed, with a single
// account): [[{num, bar, fill, sub}]], built again only when the accounts
// change (metersKey).
let meters = null;
let metersKey = null;

// The overall status's level, as html's class (lvl-ok, lvl-warn, lvl-bad).
let level = "";

function renderHeader(d) {
  const scope = d.project ? d.project : "all projects";
  setText($("scope"), (d.org ? d.org + " · " : "") + scope + (d.privacy ? " · public view" : ""));

  // The overall status: a symbol and words, in its colour.
  const st = headline(d.status);
  setText($("status-sym"), st.symbol);
  setText($("status-text"), st.text);
  setText($("status-why"), st.why);
  const lvl = "lvl-" + (d.status?.level || "ok");
  if (lvl !== level) {
    if (level) document.documentElement.classList.remove(level);
    document.documentElement.classList.add(lvl);
    level = lvl;
  }

  // The queue pill only says what the stage bar doesn't: that the queue is
  // paused, or runs extra slots to catch up.
  const q = $("queue");
  let text = "";
  if (d.queue.paused) text = "Queue paused" + (d.queue.reason ? " · " + d.queue.reason : "");
  else if (d.queue.boost > 0) text = "+" + d.queue.boost + " slot" + (d.queue.boost === 1 ? "" : "s") + " catching up";
  const qcls = "queue-state" + (d.queue.paused ? " paused" : "");
  if (q.className !== qcls) q.className = qcls;
  if (q.hidden !== !text) q.hidden = !text;
  q.querySelector(".dot").classList.toggle("pulse", !d.queue.paused && d.queue.busy > 0);
  setText($("queue-text"), text.length > 60 ? text.slice(0, 59) + "…" : text);

  // One set of meters per Claude account the runners are on, under its
  // name; with one account (or an older hub), one unnamed set.
  const accts = (d.usage.accounts || []).length > 1 ? d.usage.accounts : null;
  const sets = accts
    ? accts.map((a) => ({ label: a.label, known: true, windows: a.windows, paused: a.paused, reason: a.reason }))
    : [{ label: "", known: d.usage.known, windows: d.usage.windows }];
  // The meters are built once per set of accounts and updated in place, so
  // the bar's width transition runs only when its value moves.
  const key = sets.map((s) => s.label).join("\n");
  if (key !== metersKey) {
    metersKey = key;
    const box = $("usage");
    box.replaceChildren();
    box.classList.toggle("multi", !!accts);
    meters = sets.map((s) => {
      const ms = USAGE.map(([, label, short]) => {
        const m = { num: h("b", { class: "num" }), fill: h("i"), sub: h("span") };
        m.bar = h("div", { class: "bar" });
        m.el = h("div", { class: "meter" },
          h("div", { class: "lbl" }, h("span", {}, accts ? short : label), m.num),
          m.bar,
          h("div", { class: "lbl", style: "margin-top:4px" }, m.sub, h("span")));
        return m;
      });
      if (!accts) {
        box.append(...ms.map((m) => m.el));
        return ms;
      }
      ms.name = h("div", { class: "acct-name" }, s.label);
      ms.group = h("div", { class: "acct" }, ms.name, h("div", { class: "acct-meters" }, ...ms.map((m) => m.el)));
      box.append(ms.group);
      return ms;
    });
  }
  sets.forEach((s, j) => {
    const ms = meters[j];
    if (ms.group) {
      ms.group.classList.toggle("paused", !!s.paused);
      const title = s.label + (s.paused && s.reason ? " · " + s.reason : "");
      if (ms.name.title !== title) ms.name.title = title;
    }
    USAGE.forEach(([name], i) => meterTo(ms[i], s, name, !!accts));
  });
}

// meterTo shows window name of usage set s (its known and windows) in m;
// short is for the narrower meters of several accounts.
function meterTo(m, s, name, short) {
  const w = (s.windows || []).find((x) => x.name === name);
  if (!s.known || !w) {
    setText(m.num, "—");
    setText(m.sub, "no report yet");
    if (m.fill.parentNode) m.fill.remove();
    return;
  }
  const pct = Math.round(w.utilization * 100);
  const color = w.utilization >= 1 ? "var(--bad)" : w.utilization >= 0.7 ? "var(--run)" : "var(--ok)";
  setText(m.num, pct + "%");
  // Heading for 100% before it resets (which pauses the queue): say so
  // instead of when it resets.
  const full = fullText(w, dataNow(), short);
  setText(m.sub, full || resetText(w.resets_at, dataNow()));
  const sub = full ? "full" : "";
  if (m.sub.className !== sub) m.sub.className = sub;
  const width = Math.min(100, pct) + "%";
  if (m.fill.style.width !== width) m.fill.style.width = width;
  if (m.fill.style.background !== color) m.fill.style.background = color;
  if (!m.fill.parentNode) m.bar.append(m.fill);
}

function renderPipeline(d, now) {
  const s = d.stages;
  const notes = (...xs) => xs.filter(Boolean).join(" · ");
  const stages = [
    ["Queued", "var(--queue)", s.queued, notes(
      s.blocked && s.blocked + " waiting on dependencies",
      s.waiting_runner && s.waiting_runner + " waiting for a runner",
      s.paused && s.paused + " paused") || (s.queued ? "ready to start" : "nothing waiting")],
    ["Running", "var(--run)", s.running, s.running ? "avg " + longAge(s.running_avg_s * 1000) + " so far" : "idle"],
    ["Review", "var(--rev)", s.review, [
      s.review_oldest ? "oldest " + longAge(now - new Date(s.review_oldest)) : s.review_github ? "" : "all reviewed",
      s.review_github ? s.review_github + " on GitHub" : "",
    ].filter(Boolean).join(" · ")],
    ["Merging", "var(--merge)", s.merging, s.merging_pr ? s.merging_pr + " waiting on PR checks" : s.merging ? "in the merge queue" : ""],
  ];
  if (!changed("pipeline", stages)) return false;
  const was = shown.stages || [];
  shown.stages = stages.map((s) => s[2]);
  const tiles = stages.map(([k, c, v, sub], i) => {
    const n = h("div", { class: "v num" + (v ? "" : " zero") }, v);
    const tile = h("div", { class: "stage", style: `--c:${c}` },
      h("div", { class: "k" }, k), n, h("div", { class: "s" }, sub));
    // A stage whose count moved rolls the number and lights up a moment.
    if (roll(n, was[i], v)) tile.classList.add("bump");
    return tile;
  });
  $("pipeline").replaceChildren(...tiles);
  if (first) appear(tiles, 50);
  return true;
}


// fit hides a list's last children so its panel doesn't overflow, and
// returns how many it hid. It lays the panel out once: it reads where every child
// ends, works out how many fit (with the "+ n more" note, if some don't),
// and only then changes the list.
function fit(list, panel, noun) {
  const kids = [...list.children];
  if (kids.length < 2) return 0;
  const note = h("div", { class: "more" }, "+ " + kids.length + " more " + noun);
  list.append(note);
  // Reads, all from one layout. Rects are scaled by the canvas's zoom.
  const box = panel.getBoundingClientRect();
  const z = box.height / panel.offsetHeight || 1;
  const cs = getComputedStyle(panel);
  // A child fits if it ends above this line: where the panel would start to
  // scroll (scrollHeight > clientHeight + 1) once the padding is added.
  const limit = box.top + (panel.clientTop + panel.clientHeight + 1 - parseFloat(cs.paddingBottom)) * z;
  const ends = kids.map((k) => k.getBoundingClientRect().bottom);
  const nr = note.getBoundingClientRect();
  const noteRoom = nr.bottom - ends[ends.length - 1]; // the gap above the note and the note
  // Writes.
  if (ends[ends.length - 1] <= limit) {
    note.remove();
    return 0;
  }
  let keep = 1; // always show one, as before
  while (keep < kids.length && ends[keep] + noteRoom <= limit) keep++;
  for (let i = keep; i < kids.length; i++) kids[i].remove();
  note.textContent = "+ " + (kids.length - keep) + " more " + noun;
  return kids.length - keep;
}

// withToken is a wall image's URL with the wall's token, which it needs too.
const withToken = (url) => url + (params.get("token") ? "&token=" + encodeURIComponent(params.get("token")) : "");

// projectIcon is a running card's project icon: its image (with the wall's
// token, which the image's URL needs too), else its initial in its color, as
// the app shows it. None in public mode, where it would tell the project.
function projectIcon(r, privacy) {
  if (privacy || !r.project) return null;
  const initial = () => h("span", { class: "picon", style: r.color ? "--pc:" + r.color : "", "aria-hidden": "true" }, [...r.project][0].toUpperCase());
  if (!r.icon) return initial();
  const src = withToken(r.icon);
  const icon = h("span", { class: "picon img", "aria-hidden": "true" }, h("img", { src, alt: "" }));
  icon.firstChild.addEventListener("error", () => icon.replaceWith(initial()), { once: true });
  return icon;
}

// The running list: a card per task while they fit, else compact rows
// (nextCompact). fits is how many cards fitted when last measured.
let compact = false;
let fits = Infinity;

function renderRunning(d) {
  $("running-dot").classList.toggle("pulse", d.running.length > 0);
  const now = dataNow();
  // What a card shows that changes with time: whether its activity is stuck.
  const stuck = d.running.map((r) => stuckText(r.activity, r.activity_at, now));
  if (!changed("running", [d.running, d.privacy, d.queue.paused, stuck, compact])) return false;
  const list = $("running");
  const before = snapshot(list);
  // A task that stopped running shows how it ended for a moment as it goes:
  // in Review, failed, or merged.
  const ended = new Map(d.needs.map((n) => [n.id, n.kind]));
  const outcome = (id, ghost) => {
    const kind = ended.get(id);
    const [cls, text] = kind === "review" ? ["ok", "✓ Ready for review"] : kind === "failed" ? ["bad", "✕ Failed"] : landed ? ["ok", "✓ Done"] : [];
    if (!cls) return false;
    ghost.classList.add(cls);
    ghost.append(h("div", { class: "outcome" }, text));
    return true;
  };
  if (!d.running.length) {
    timers = [];
    list.classList.remove("compact");
    list.replaceChildren(h("div", { class: "empty" }, d.queue.paused ? "Nothing running: the queue is paused." : "Nothing running right now."));
    handoff = morph(list, before, { outcome });
    return true;
  }
  let next = drawRunning(d, list, before, stuck);
  const hidden = fit(list, list.parentElement, "running");
  if (!compact) fits = hidden ? d.running.length - hidden : Math.max(d.running.length, fits === Infinity ? 0 : fits);
  if (nextCompact(compact, d.running.length, fits) !== compact) {
    // Switch, and draw again in the other layout at once.
    compact = !compact;
    drawn.running = JSON.stringify([d.running, d.privacy, d.queue.paused, stuck, compact]);
    next = drawRunning(d, list, before, stuck);
    fit(list, list.parentElement, "running");
  }
  timers = next.filter((t) => t.el.isConnected);
  handoff = morph(list, before, { outcome });
  return true;
}

// drawRunning fills list with d's running tasks, as cards or compact rows,
// and returns their timers.
function drawRunning(d, list, before, stuck) {
  list.classList.toggle("compact", compact);
  const next = [];
  // A timer and its progress bar against the typical time (none without one).
  const timer = (r, base) => {
    const clock = h("div", { class: "elapsed num" }, "—");
    if (!r.started_at) return { clock, bar: null };
    const t = { el: clock, since: new Date(r.started_at).getTime(), took: r.took };
    if (r.took?.length) {
      t.fill = h("i");
      t.text = h("span", { class: "ptext num" });
      t.prog = h("div", { class: base }, h("div", { class: "pbar" }, t.fill), t.text);
    }
    next.push(t);
    return { clock, t };
  };
  list.replaceChildren(...d.running.map((r, i) => {
    if (compact) {
      const { clock, t } = timer(r, "crow");
      const row = t?.prog || h("div", { class: "crow" }, h("div", { class: "pbar", style: "visibility:hidden" }), h("span", { class: "ptext" }));
      row.dataset.key = r.id;
      row.prepend(h("div", { class: "t" }, projectIcon(r, d.privacy), r.title));
      row.append(clock);
      return row;
    }
    const meta = [r.project];
    if (r.run > 1) meta.push("run " + r.run);
    const changes = r.add || r.del;
    const { clock, t } = timer(r, "prog");
    const diff = changes
      ? h("div", { class: "diff num" }, h("span", { class: "add" }, "+" + num(r.add)), " ", h("span", { class: "del" }, "−" + num(r.del)),
        r.files ? " · " + r.files + " file" + (r.files === 1 ? "" : "s") : "")
      : h("div", { class: "diff num", style: "color:var(--faint)" }, "no changes yet");
    // The tool call it is making, small and without a fade; one that stays
    // the same for a while says so instead.
    const live = r.activity && h("div", { class: "live" + (stuck[i] ? " stuck" : "") }, stuck[i] || r.activity);
    // In a card that stays, a changed diff fades in.
    const old = !first && moving() && before.get(r.id)?.el;
    if (old && old.querySelector(".diff")?.textContent !== diff.textContent) diff.classList.add("fresh");
    return h("div", { class: "card", "data-key": r.id },
      h("div", {},
        h("div", { class: "t" }, projectIcon(r, d.privacy), r.title, r.sent_back && h("span", { class: "tag" }, "sent back: " + r.sent_back)),
        h("div", { class: "meta" }, meta.map((m) => h("span", {}, m)))),
      h("div", { class: "right" }, clock, diff),
      t?.prog, live);
  }));
  // Fill the timers in before the panel is laid out, so they're measured
  // with their text.
  const now = dataNow();
  for (const t of next) tickTimer(t, now);
  return next;
}

// What a pull request in Review waits for a person to do (wallNeed.waits).
const PR_WAITS = { review: "Awaiting review", merge: "Waiting to be merged", blocked: "Blocked on GitHub" };

function renderNeeds(d, now) {
  // The model: each row's key, whether it waits too long, color, title, why,
  // age, and who should act (with their avatar), as shown.
  const rows = d.needs.map((n) => {
    const since = now - new Date(n.since);
    const who = [n.person || "", n.avatar || ""];
    if (n.kind === "review") {
      const why = ["Review", n.project];
      if (n.add || n.del) why.push("+" + num(n.add) + " −" + num(n.del));
      if (n.tests === "passed") why.push("tests ✓");
      if (n.tests === "failed") why.push("tests ✗");
      if (n.pr) why.push("PR #" + n.pr);
      if (PR_WAITS[n.waits]) why.push(PR_WAITS[n.waits]);
      return ["review:" + n.id, !!n.old, "var(--rev)", n.title, why.join(" · "), age(since), ...who, ""];
    }
    const why = ["Failed", n.project];
    if (!d.privacy) why.push(n.reason || "no reason given");
    if (n.resumable) why.push("resumable");
    return ["failed:" + n.id, !!n.old, "var(--bad)", n.title, why.join(" · "), age(since), ...who, ""];
  });
  const is = d.issues;
  if (is.open) {
    // The severity goes next to the title: the right-hand column is always an age.
    const sev = ["critical", "high", "medium", "low", "info"].filter((k) => is.severity[k]).map((k) => is.severity[k] + " " + k);
    const why = is.top.length ? is.top.join(" · ") : sev.slice(1).join(" · ") || "reported problems not merged yet";
    const urgent = (is.severity.critical || 0) + (is.severity.high || 0) > 0;
    rows.push(["issues", false, urgent ? "var(--bad)" : "var(--queue)", is.open + " open issue" + (is.open === 1 ? "" : "s"), why, "", "", "", sev[0] || ""]);
  }
  const total = (d.stages.review || 0) - (d.stages.review_github || 0) + (d.stages.failed || 0) + (is.open ? 1 : 0);
  setText($("needs-count"), String(total));
  // A row that waits too long turns the whole panel red, with words.
  $("needs-panel").classList.toggle("alarm", rows.some((r) => r[1]));
  if (!changed("needs", rows)) return false;
  const list = $("needs");
  const before = snapshot(list);
  // A row for a task that just finished running arrives as its card goes.
  if (!rows.length) {
    list.replaceChildren(h("div", { class: "empty" }, "Nothing waiting for a human. ✓"));
  } else {
    list.replaceChildren(...rows.map(([key, old, c, t, why, ago, person, avatar, sev]) => {
      const title = h("div", { class: "t" }, h("span", {}, t), sev && h("span", { class: "sev" }, sev));
      const right = h("div", { class: "age num" });
      if (avatar) {
        const img = h("img", { class: "av", src: withToken(avatar), alt: "" });
        img.addEventListener("error", () => img.remove(), { once: true });
        right.append(img);
      }
      if (person) right.append(h("span", { class: "who" }, person), ago ? " · " : "");
      if (ago) right.append(ago);
      const row = h("div", { class: "item" + (old ? " old" : ""), style: "--c:" + c, "data-key": key }, h("i", { class: "bar2" }),
        h("div", {}, title, h("div", { class: "why" }, why)), right);
      // The issues row stays as their count changes: roll it.
      const was = before.get(key)?.el.querySelector(".t span")?.textContent;
      if (key === "issues") roll(title.firstChild, was, t);
      return row;
    }));
    fit(list, list.parentElement, "waiting");
  }
  morph(list, before, { delay: handoff });
  return true;
}

function renderRunners(d, now) {
  setText($("runners-count"), String(d.runners.length));
  const rows = d.runners.map((r) => {
    let sub;
    if (!r.online) {
      sub = "offline" + (r.last_seen ? " " + age(now - new Date(r.last_seen)) : "") +
        (r.waiting ? " · " + r.waiting + " task" + (r.waiting === 1 ? "" : "s") + " waiting" : "");
    } else {
      sub = (r.paused ? "paused · " : "") + r.projects + " project" + (r.projects === 1 ? "" : "s") + " · " + r.busy + "/" + r.slots + " busy" +
        (r.boost > 0 ? " (+" + r.boost + " catching up)" : "");
    }
    return [r.name, !!r.online, !!r.paused, r.busy, Math.min(Math.max(r.slots, r.busy), 12), sub];
  });
  if (!changed("runners", rows)) return false;
  const box = $("runners");
  const before = snapshot(box);
  const was = shown.busy;
  shown.busy = Object.fromEntries(rows.map((r) => [r[0], r[3]]));
  if (!rows.length) {
    box.replaceChildren(h("div", { class: "empty" }, "No runners registered."));
    morph(box, before);
    return true;
  }
  const motion = !first && moving();
  box.replaceChildren(...rows.map(([name, online, paused, busy, n, sub]) => {
    // A slot that just took a task pops in; one that just freed fades out.
    const prev = motion && name in was ? was[name] : busy;
    const slots = h("div", { class: "slots" });
    for (let i = 0; i < n; i++) {
      slots.append(h("i", { class: (i < busy ? "on" : "") + (i < busy && i >= prev ? " pop" : i >= busy && i < prev ? " drop" : "") }));
    }
    // A shape as well as a colour: ● online, ◐ paused, ✕ offline.
    const [glyph, color, word] = !online ? ["✕", "var(--bad)", "offline"] : paused ? ["◐", "var(--run)", "paused"] : ["●", "var(--ok)", "online"];
    return h("div", { class: "runner", "data-key": name },
      h("span", { class: "rs", style: `color:${color}`, title: word, "aria-label": word }, glyph),
      h("div", {}, h("div", { class: "n" + (online ? "" : " off") }, name), h("div", { class: "h" + (online ? "" : " off") }, sub)),
      slots);
  }));
  fit(box, box.parentElement, "runners");
  morph(box, before);
  return true;
}

function renderToday(d, now) {
  const t = d.today;
  const avg = t.avg || {};
  const add = d.stages.merged_add || 0, del = d.stages.merged_del || 0;
  // Each tile: its label, what it shows, the number roll() compares, and
  // how it compares with the average day at this time.
  const tiles = [["Merged", String(t.merged), t.merged, deltaText(t.merged, avg.merged, oneDecimal)]];
  if (t.spend_usd != null) tiles.push(["Spend", money(t.spend_usd), t.spend_usd, deltaText(t.spend_usd, avg.spend_usd, money)]);
  if (t.first_try != null) {
    const pct = Math.round(t.first_try * 100);
    const pctOf = (v) => Math.round(v * 100) + "%";
    tiles.push(["First-try merges", pct + "%", pct, avg.first_try != null ? deltaText(t.first_try, avg.first_try, pctOf) : ""]);
  }
  // Just landed: the newest merges, with when (today: the time; before: the day too).
  const day = new Date(now).toDateString();
  const lands = (d.landed || []).map((l) => {
    const at = new Date(l.at);
    const when = at.toDateString() === day ? hhmm(at) : at.toLocaleDateString(undefined, { weekday: "short" }) + " " + hhmm(at);
    return [l.id, l.title, d.privacy ? "" : l.project, when];
  });
  if (!changed("today", [tiles.map((x) => x.slice(0, 2).concat(x[3])), add, del, lands])) return false;
  // Lines landed: the net change is the big number, the lines added and
  // removed beside it. Net removed lines (a refactoring) aren't bad news, so
  // they get an accent colour, not --bad.
  // A long net number is set smaller so the split still fits beside it.
  const net = add - del;
  const netText = (net > 0 ? "+" : net < 0 ? "−" : "") + num(Math.abs(net));
  const netClass = ["net", net > 0 ? "add" : net < 0 ? "cut" : "", netText.length > 7 ? "longer" : netText.length > 6 ? "long" : ""];
  tiles.push(["Lines landed", [
    h("span", { class: netClass.filter(Boolean).join(" ") }, netText),
    h("small", { class: "split" }, h("span", {}, "+" + num(add)), h("span", {}, "−" + num(del))),
  ], net, ""]);
  const was = shown.today;
  shown.today = Object.fromEntries(tiles.map(([k, , n]) => [k, n]));
  const cells = tiles.slice(0, 4).map(([k, v, n, delta]) => {
    const b = h("b", { class: Array.isArray(v) ? "num lines" : "num" }, v);
    roll(b, was[k], n);
    return h("div", {}, k, b, delta && h("span", { class: "delta" }, delta));
  });
  $("today").replaceChildren(...cells);
  if (first) appear(cells);

  const list = $("landed");
  const before = snapshot(list);
  if (!lands.length) {
    list.replaceChildren(h("div", { class: "empty" }, "Nothing merged yet."));
  } else {
    list.replaceChildren(...lands.map(([id, title, project, when]) =>
      h("div", { class: "land", "data-key": id }, h("span", { class: "ck", "aria-label": "merged" }, "✓"),
        h("span", { class: "t" }, title, project && h("span", { class: "p" }, " · " + project)), h("span", { class: "at num" }, when))));
    fit(list, list.parentElement, "merged");
  }
  morph(list, before);
  return true;
}

// ---- off hours ----
//
// Outside work hours (workHours: ?hours=, ?days=), with nothing running and
// nothing waiting on a person, the wall dims to a summary of the week; it
// comes back as soon as something runs or needs a human.

const hours = workHours(params);
let off = false;

function renderOff(d, now) {
  const next = offHours(new Date(now), hours) && boardQuiet(d);
  if (next !== off) {
    off = next;
    document.documentElement.classList.toggle("off", off);
  }
  if (!off) return;
  setText($("off-week"), weekText(d.week || { merged: 0 }));
  const st = headline(d.status);
  setText($("off-status"), (st.symbol ? st.symbol + " " : "") + st.text + (st.why ? " · " + st.why : ""));
}

let landed = false; // a task merged since the last draw (renderPanels)
let handoff = 0; // how long the running list holds a finished card this draw, so a new Needs row waits for it

let last = null; // the latest data, to draw again each minute and on a resize

// renderPanels draws d, rebuilding only the panels whose text changed.
function renderPanels(d) {
  last = d;
  const now = new Date(dataNow());
  renderHeader(d);
  // Whether a task merged (or went to merge) since the last draw, for the
  // running cards' "Done".
  const merges = d.stages.merged_today + d.stages.merging;
  landed = shown.merges !== undefined && merges > shown.merges;
  shown.merges = merges;
  handoff = 0;
  // Each runs, so each panel is checked; any rebuilt one may change the
  // stacked page's height.
  const grew = [renderPipeline(d, now), renderRunning(d), renderNeeds(d, now), renderRunners(d, now), renderToday(d, now)];
  renderOff(d, Date.now() + skew);
  if (grew.some(Boolean)) fitPage();
  first = false;
}

function render(d) {
  renderPanels(d);
  tick();
}

// ---- polling ----

function apiURL() {
  const q = new URLSearchParams();
  for (const k of ["token", "project", "privacy", "nospend"]) if (params.get(k)) q.set(k, params.get(k));
  q.set("tz", String(new Date().getTimezoneOffset()));
  return "/api/wall?" + q;
}

let failures = 0;
let timer = 0, inflight = false;
let seen = ""; // the last response, without its "now", to skip drawing the same data again

function schedule(ms) {
  clearTimeout(timer);
  timer = 0;
  // A hidden page doesn't poll; it polls at once when it's shown again.
  if (!document.hidden) timer = setTimeout(poll, ms);
}

async function poll() {
  if (inflight) return;
  inflight = true;
  let next = POLL_MS;
  try {
    const res = await fetch(apiURL(), { credentials: "same-origin", cache: "no-store" });
    if (res.status === 401 || res.status === 403) {
      $("error").textContent = params.get("token") ? "This wall link is invalid or was revoked." : "Sign in to see the wall.";
      $("error").classList.add("on");
      if (!params.get("token")) location.href = "/login.html?next=" + encodeURIComponent(location.pathname + location.search);
      next = 60000;
    } else {
      if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || res.statusText);
      const text = await res.text();
      const d = JSON.parse(text);
      skew = new Date(d.now) - Date.now();
      // Good data: the board is live again (checkStale, in render's tick).
      failures = 0;
      lastOk = Date.now();
      okNow = lastOk + skew;
      checkStale();
      // The server puts "now" first; everything after it is the data.
      const same = text.replace(/^\{"now":"[^"]*",?/, "{");
      if (same !== seen || !last) {
        seen = same;
        render(d);
      } else {
        tick();
      }
      $("error").classList.remove("on");
    }
  } catch (e) {
    failures++;
    tick(); // grey out now, not at the next second
  } finally {
    inflight = false;
  }
  schedule(next);
}

// ---- layout ----
//
// Wide screens get the TV layout: designed at 1920×1080 and scaled so one
// side matches the screen exactly, with the canvas stretched along the other
// side, so 16:10, 4:3 and ultra-wide screens are filled instead of
// letterboxed. Narrow or portrait screens get a stacked, scrolling layout on
// a narrower canvas so the text stays readable.

let zoom = 1, narrow = false;

function layout() {
  const W = innerWidth, H = innerHeight;
  narrow = W < 1100 || W / H < 1.3;
  const phone = narrow && W < 760;
  document.documentElement.classList.toggle("narrow", narrow);
  document.documentElement.classList.toggle("phone", phone);
  const tv = $("tv");
  if (narrow) {
    const base = phone ? 640 : 1000;
    zoom = W / base;
    tv.style.width = base + "px";
    tv.style.height = "";
    tv.style.minHeight = H / zoom + "px";
    tv.style.left = tv.style.top = "0px";
  } else {
    zoom = Math.min(W / 1920, H / 1080);
    tv.style.width = W / zoom + "px";
    tv.style.height = H / zoom + "px";
    tv.style.minHeight = "";
    tv.style.left = tv.style.top = "0px";
  }
  placeBoard();
  fitPage();
}

// placeBoard scales the canvas and, on a TV, moves it by the burn-in drift
// (walllogic.js drift): a few pixels, once every few minutes.
function placeBoard() {
  const { x, y } = narrow ? { x: 0, y: 0 } : drift(Date.now());
  const transform = `translate(${x}px, ${y}px) scale(${zoom})`;
  const tv = $("tv");
  if (tv.style.transform !== transform) tv.style.transform = transform;
}

// fitPage gives the scaled canvas a scroll area in the stacked layout (a
// transform doesn't change the space an element takes up).
function fitPage() {
  const height = narrow ? Math.ceil($("tv").offsetHeight * zoom) + "px" : "";
  if (document.body.style.height !== height) document.body.style.height = height;
}

let resizing = 0;
addEventListener("resize", () => {
  cancelAnimationFrame(resizing);
  resizing = requestAnimationFrame(() => {
    layout();
    // Lay the lists out again now, not at the next poll: how many cards fit
    // depends on the size.
    if (last) {
      drawn = {};
      compact = false; // how many cards fit is measured again
      fits = Infinity;
      quiet = true;
      render(last);
      quiet = false;
    }
  });
});

// The clock and timers tick only while the page is shown.
let ticking = 0;
function startTicking() {
  if (!ticking) ticking = setInterval(tick, 1000);
  tick();
}

// A hidden page neither polls nor ticks; it catches up as soon as it's shown.
document.addEventListener("visibilitychange", () => {
  if (document.hidden) {
    clearInterval(ticking);
    ticking = 0;
    clearTimeout(timer);
    return;
  }
  startTicking();
  poll();
});

layout();
if (document.hidden) tick();
else startTicking();
poll();
// A TV runs for days: reload now and then to pick up a new Dash version.
setTimeout(() => location.reload(), 6 * 3600 * 1000);
