// The wall view: a read-only TV dashboard. It polls /api/wall every few
// seconds and ticks the clock and the running tasks' timers in between.
// Options come from the page's own URL: ?token= (a wall link, no sign-in),
// ?project= (one project), ?privacy=1 (public mode: only what anyone may
// see; a public wall token always has it).
"use strict";

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

// age: "2 h 14", "48 min", "now"
function age(ms) {
  const m = Math.max(0, Math.floor(ms / 60000));
  if (m < 1) return "now";
  if (m < 60) return m + " min";
  const hrs = Math.floor(m / 60);
  if (hrs < 48) return hrs + " h " + pad(m % 60);
  return Math.floor(hrs / 24) + " d";
}

// longAge: "2 h 14 min"
function longAge(ms) {
  const m = Math.max(0, Math.floor(ms / 60000));
  if (m < 60) return m + " min";
  return Math.floor(m / 60) + " h " + (m % 60) + " min";
}

// elapsed: "21:00", "1:02:03"
function elapsed(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  const hrs = Math.floor(s / 3600);
  const mm = pad(Math.floor((s % 3600) / 60)), ss = pad(s % 60);
  return hrs ? hrs + ":" + mm + ":" + ss : mm + ":" + ss;
}

function resetLabel(iso, now = new Date(Date.now() + skew)) {
  const d = new Date(iso);
  if (isNaN(d) || d.getFullYear() < 2000) return "";
  const mins = Math.floor((d - now) / 60000);
  if (mins < 1) return "resets now";
  const days = Math.floor(mins / 1440), hrs = Math.floor((mins % 1440) / 60), m = mins % 60;
  if (days) return "resets in " + days + "d" + (hrs ? " " + hrs + "h" : "");
  if (hrs) return "resets in " + hrs + "h" + (m ? " " + m + "m" : "");
  return "resets in " + m + "m";
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
// with: the stage counts, the today tiles, the hours' bars, each runner's
// busy slots.
const shown = { stages: null, today: {}, hours: {}, busy: {} };

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
function roll(el, was, now) {
  if (was === undefined || was === now || !moving() || first) return false;
  el.classList.add(parseFloat(String(now).replace(/[^\d.-]/g, "")) < parseFloat(String(was).replace(/[^\d.-]/g, "")) ? "roll-down" : "roll-up");
  return true;
}

// ---- clock and timers, every second ----

let skew = 0; // server time − local time, so timers agree with the server
let timers = []; // the running tasks' timers: {el, since}, from renderRunning
let minute = ""; // the clock's text, which changes once a minute

function tick() {
  const now = Date.now() + skew;
  for (const t of timers) setText(t.el, elapsed(now - t.since));
  document.documentElement.classList.toggle("blink"); // the pulsing dots (wall.html)
  const clock = hhmm(new Date(now));
  if (clock === minute) return;
  minute = clock;
  $("clock").textContent = clock;
  // Ages ("48 min") and the meters' "resets in" are to the minute: draw the
  // panels again, which only rebuilds those whose text moved.
  if (last) renderPanels(last);
}

// ---- rendering ----

const USAGE = [["five_hour", "Claude 5h"], ["seven_day", "Claude 7d"]];
let meters = null; // the header's meters, built once: [{num, bar, fill, sub}]

function renderHeader(d) {
  const scope = d.project ? d.project : "all projects";
  setText($("scope"), (d.org ? d.org + " · " : "") + scope + (d.privacy ? " · public view" : ""));

  const q = $("queue");
  const busy = d.queue.busy, slots = d.queue.slots;
  let text, cls;
  if (d.queue.paused) {
    cls = "paused";
    text = "Queue paused" + (d.queue.reason ? " · " + d.queue.reason : "");
  } else {
    cls = busy > 0 ? "" : "idle";
    text = (busy > 0 ? "Queue running" : "Queue idle") + " · " + busy + " of " + slots + " slot" + (slots === 1 ? "" : "s") + " busy";
  }
  const qcls = "queue-state " + cls;
  if (q.className !== qcls) q.className = qcls;
  q.querySelector(".dot").classList.toggle("pulse", !d.queue.paused && busy > 0);
  setText($("queue-text"), text.length > 70 ? text.slice(0, 69) + "…" : text);

  // The meters are built once and updated in place, so the bar's width
  // transition runs only when its value moves.
  if (!meters) {
    meters = USAGE.map(([, label]) => {
      const m = { num: h("b", { class: "num" }), fill: h("i"), sub: h("span") };
      m.bar = h("div", { class: "bar" });
      $("usage").append(h("div", { class: "meter" },
        h("div", { class: "lbl" }, h("span", {}, label), m.num),
        m.bar,
        h("div", { class: "lbl", style: "margin-top:4px" }, m.sub, h("span"))));
      return m;
    });
  }
  USAGE.forEach(([name], i) => {
    const m = meters[i];
    const w = (d.usage.windows || []).find((x) => x.name === name);
    if (!d.usage.known || !w) {
      setText(m.num, "—");
      setText(m.sub, "no report yet");
      if (m.fill.parentNode) m.fill.remove();
      return;
    }
    const pct = Math.round(w.utilization * 100);
    const color = w.utilization >= 1 ? "var(--bad)" : w.utilization >= 0.7 ? "var(--run)" : "var(--ok)";
    setText(m.num, pct + "%");
    setText(m.sub, resetLabel(w.resets_at));
    const width = Math.min(100, pct) + "%";
    if (m.fill.style.width !== width) m.fill.style.width = width;
    if (m.fill.style.background !== color) m.fill.style.background = color;
    if (!m.fill.parentNode) m.bar.append(m.fill);
  });
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
    ["Merging", "var(--merge)", s.merging, s.merging_pr ? s.merging_pr + " waiting on PR checks" : s.merging ? "in the merge queue" : "—"],
    ["Merged today", "var(--ok)", s.merged_today, s.merged_today ? "+" + num(s.merged_add) + " / −" + num(s.merged_del) + " lines" : "none yet"],
    ["Failed", "var(--bad)", s.failed, s.failed ? (s.resumable ? s.resumable + " resumable" : "need a look") : "none"],
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


// fit hides a list's last children so its panel doesn't overflow, and says
// how many it hid. It lays the panel out once: it reads where every child
// ends, works out how many fit (with the "+ n more" note, if some don't),
// and only then changes the list.
function fit(list, panel, noun) {
  const kids = [...list.children];
  if (kids.length < 2) return;
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
    return;
  }
  let keep = 1; // always show one, as before
  while (keep < kids.length && ends[keep] + noteRoom <= limit) keep++;
  for (let i = keep; i < kids.length; i++) kids[i].remove();
  note.textContent = "+ " + (kids.length - keep) + " more " + noun;
}

function renderRunning(d) {
  setText($("running-count"), String(d.running.length));
  $("running-dot").classList.toggle("pulse", d.running.length > 0);
  if (!changed("running", [d.running, d.privacy, d.queue.paused])) return false;
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
    list.replaceChildren(h("div", { class: "empty" }, d.queue.paused ? "Nothing running: the queue is paused." : "Nothing running right now."));
    handoff = morph(list, before, { outcome });
    return true;
  }
  const next = [];
  list.replaceChildren(...d.running.map((r) => {
    const meta = [r.project, d.privacy ? r.runner : "runner " + r.runner, "run " + r.run];
    if (r.cost_usd != null) meta.push(money(r.cost_usd));
    const changes = r.add || r.del;
    const clock = h("div", { class: "elapsed num" }, "—");
    if (r.started_at) {
      clock.textContent = "";
      next.push({ el: clock, since: new Date(r.started_at).getTime() });
    }
    const diff = changes
      ? h("div", { class: "diff num" }, h("span", { class: "add" }, "+" + num(r.add)), " ", h("span", { class: "del" }, "−" + num(r.del)),
        r.files ? " · " + r.files + " file" + (r.files === 1 ? "" : "s") : "")
      : h("div", { class: "diff num", style: "color:var(--faint)" }, "no changes yet");
    const live = r.activity && h("div", { class: "live" }, r.activity);
    // In a card that stays, what changed fades in: the latest activity, the diff.
    const old = !first && moving() && before.get(r.id)?.el;
    if (old) {
      if (live && old.querySelector(".live")?.textContent !== r.activity) live.classList.add("fresh");
      if (old.querySelector(".diff")?.textContent !== diff.textContent) diff.classList.add("fresh");
    }
    return h("div", { class: "card", "data-key": r.id },
      h("div", {},
        h("div", { class: "t" }, r.title, r.sent_back && h("span", { class: "tag" }, "sent back: " + r.sent_back)),
        h("div", { class: "meta" }, meta.map((m) => h("span", {}, m)))),
      h("div", { class: "right" }, clock, diff),
      live);
  }));
  // Fill the timers in before the panel is laid out, so they're measured
  // with their text.
  const now = Date.now() + skew;
  for (const t of next) t.el.textContent = elapsed(now - t.since);
  fit(list, list.parentElement, "running");
  timers = next.filter((t) => t.el.isConnected);
  handoff = morph(list, before, { outcome });
  return true;
}

// What a pull request in Review waits for a person to do (wallNeed.waits).
const PR_WAITS = { review: "Awaiting review", merge: "Waiting to be merged on GitHub", blocked: "Blocked on GitHub" };

function renderNeeds(d, now) {
  // The model: each row's key, color, title, why and age, as shown.
  const rows = d.needs.map((n) => {
    const since = now - new Date(n.since);
    if (n.kind === "review") {
      const why = ["Review", n.project];
      if (n.add || n.del) why.push("+" + num(n.add) + " −" + num(n.del));
      if (n.tests === "passed") why.push("tests ✓");
      if (n.tests === "failed") why.push("tests ✗");
      if (n.pr) why.push("PR #" + n.pr);
      if (PR_WAITS[n.waits]) why.push(PR_WAITS[n.waits]);
      return ["review:" + n.id, !!n.old, "var(--rev)", n.title, why.join(" · "), age(since)];
    }
    const why = ["Failed", n.project];
    if (!d.privacy) why.push(n.reason || "no reason given");
    if (n.resumable) why.push("resumable");
    return ["failed:" + n.id, !!n.old, "var(--bad)", n.title, why.join(" · "), age(since)];
  });
  const is = d.issues;
  if (is.open) {
    const sev = ["critical", "high", "medium", "low", "info"].filter((k) => is.severity[k]).map((k) => is.severity[k] + " " + k);
    const why = is.top.length ? is.top.join(" · ") : sev.join(" · ");
    const urgent = (is.severity.critical || 0) + (is.severity.high || 0) > 0;
    rows.push(["issues", false, urgent ? "var(--bad)" : "var(--queue)", is.open + " open issue" + (is.open === 1 ? "" : "s"), why, sev[0] || "—"]);
  }
  const total = d.stages.review - d.stages.review_github + d.stages.failed + (is.open ? 1 : 0);
  setText($("needs-count"), String(total));
  if (!changed("needs", rows)) return false;
  const list = $("needs");
  const before = snapshot(list);
  // A row for a task that just finished running arrives as its card goes.
  if (!rows.length) {
    list.replaceChildren(h("div", { class: "empty" }, "Nothing waiting for a human. ✓"));
  } else {
    list.replaceChildren(...rows.map(([key, old, c, t, why, ago]) => {
      const row = h("div", { class: "item" + (old ? " old" : ""), style: "--c:" + c, "data-key": key }, h("i", { class: "bar2" }),
        h("div", {}, h("div", { class: "t" }, t), h("div", { class: "why" }, why)),
        h("div", { class: "age num" }, ago));
      // The issues row stays as their count changes: roll it.
      const was = before.get(key)?.el.querySelector(".t")?.textContent;
      if (key === "issues") roll(row.querySelector(".t"), was, t);
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
      sub = (r.paused ? "paused · " : "") + r.projects + " project" + (r.projects === 1 ? "" : "s") + " · " + r.busy + "/" + r.slots + " busy";
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
    return h("div", { class: "runner", "data-key": name },
      h("span", { class: "dot", style: `color:${online ? (paused ? "var(--run)" : "var(--ok)") : "var(--bad)"}` }),
      h("div", {}, h("div", { class: "n" + (online ? "" : " off") }, name), h("div", { class: "h" + (online ? "" : " off") }, sub)),
      slots);
  }));
  fit(box, box.parentElement, "runners");
  morph(box, before);
  return true;
}

function renderToday(d) {
  const t = d.today;
  if (!changed("today", [t, d.stages.merged_add])) return false;
  const tiles = [["Merged", String(t.merged)]];
  if (t.spend_usd != null) tiles.push(["Spend", money(t.spend_usd)]);
  if (t.first_try != null) tiles.push(["First-try merges", Math.round(t.first_try * 100) + "%"]);
  tiles.push(["Lines landed", "+" + num(d.stages.merged_add)]);
  const was = shown.today;
  shown.today = Object.fromEntries(tiles);
  const cells = tiles.slice(0, 4).map(([k, v]) => {
    const b = h("b", { class: "num" }, v);
    roll(b, was[k], v);
    return h("div", {}, k, b);
  });
  $("today").replaceChildren(...cells);
  if (first) appear(cells);

  // Merges per hour: from 08:00 (or the first merge, if earlier) to now.
  let from = Math.min(8, t.hour);
  const early = t.per_hour.findIndex((v) => v > 0);
  if (early >= 0 && early < from) from = early;
  const hours = t.per_hour.slice(from, t.hour + 1);
  const max = Math.max(1, ...hours);
  const slot = 300 / Math.max(hours.length, 1), w = Math.max(4, slot * 0.72);
  // A bar grows from its old height (or from nothing, on the first draw).
  const grew = [];
  const heights = {};
  $("spark").replaceChildren(...hours.map((v, i) => {
    const hgt = v ? Math.max(6, (v / max) * 104) : 3;
    const bar = svg("rect", { x: (i * slot + (slot - w) / 2).toFixed(1), y: (110 - hgt).toFixed(1), width: w.toFixed(1), height: hgt.toFixed(1), rx: 3,
      style: "fill:var(" + (v ? "--ok" : "--line") + ")", opacity: i === hours.length - 1 ? 1 : 0.55 });
    const hour = from + i;
    heights[hour] = hgt;
    const old = first ? 0 : shown.hours[hour];
    if (old !== undefined && Math.abs(old - hgt) > 0.5) grew.push([bar, old / hgt, first ? i * 30 : 0]);
    return bar;
  }));
  shown.hours = heights;
  if ((first && !calm.matches) || moving()) {
    for (const [bar, s, delay] of grew) {
      bar.animate([{ transform: `scaleY(${s})` }, { transform: "none" }], { duration: 700, delay, easing: EASE, fill: "backwards" });
    }
  }
  setText($("spark-from"), pad(from) + ":00");
  return true;
}

let landed = false; // a task merged since the last draw (renderPanels)
let handoff = 0; // how long the running list holds a finished card this draw, so a new Needs row waits for it

let last = null; // the latest data, to draw again each minute and on a resize

// renderPanels draws d, rebuilding only the panels whose text changed.
function renderPanels(d) {
  last = d;
  const now = new Date(Date.now() + skew);
  renderHeader(d);
  // Whether a task merged (or went to merge) since the last draw, for the
  // running cards' "Done".
  const merges = d.stages.merged_today + d.stages.merging;
  landed = shown.merges !== undefined && merges > shown.merges;
  shown.merges = merges;
  handoff = 0;
  // Each runs, so each panel is checked; any rebuilt one may change the
  // stacked page's height.
  const grew = [renderPipeline(d, now), renderRunning(d), renderNeeds(d, now), renderRunners(d, now), renderToday(d)];
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
  for (const k of ["token", "project", "privacy"]) if (params.get(k)) q.set(k, params.get(k));
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
      // The server puts "now" first; everything after it is the data.
      const same = text.replace(/^\{"now":"[^"]*",?/, "{");
      if (same !== seen || !last) {
        seen = same;
        render(d);
      } else {
        tick();
      }
      failures = 0;
      $("error").classList.remove("on");
      $("stale").classList.remove("on");
    }
  } catch (e) {
    failures++;
    if (failures >= 2) $("stale").classList.add("on");
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
  tv.style.transform = `scale(${zoom})`;
  fitPage();
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
