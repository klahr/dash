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

const num = (n) => n.toLocaleString(undefined).replace(/,/g, " ");

// ---- clock and timers, every second ----

let skew = 0; // server time − local time, so timers agree with the server
let metersAt = 0; // when the usage meters were last drawn

function tick() {
  const now = new Date(Date.now() + skew);
  $("clock").textContent = hhmm(now);
  for (const el of document.querySelectorAll("[data-since]")) {
    el.textContent = elapsed(now - new Date(el.dataset.since));
  }
  if (last && now - metersAt >= 60000) renderHeader(last);
}

// ---- rendering ----

function renderHeader(d) {
  const scope = d.project ? d.project : "all projects";
  $("scope").textContent = (d.org ? d.org + " · " : "") + scope + (d.privacy ? " · public view" : "");

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
  q.className = "queue-state " + cls;
  q.querySelector(".dot").classList.toggle("pulse", !d.queue.paused && busy > 0);
  $("queue-text").textContent = text.length > 70 ? text.slice(0, 69) + "…" : text;

  const box = $("usage");
  box.replaceChildren();
  metersAt = Date.now() + skew;
  const want = [["five_hour", "Claude 5h"], ["seven_day", "Claude 7d"]];
  for (const [name, label] of want) {
    const w = (d.usage.windows || []).find((x) => x.name === name);
    if (!d.usage.known || !w) {
      box.append(h("div", { class: "meter" },
        h("div", { class: "lbl" }, h("span", {}, label), h("b", { class: "num" }, "—")),
        h("div", { class: "bar" }),
        h("div", { class: "lbl", style: "margin-top:4px" }, h("span", {}, "no report yet"), h("span"))));
      continue;
    }
    const pct = Math.round(w.utilization * 100);
    const color = w.utilization >= 1 ? "var(--bad)" : w.utilization >= 0.7 ? "var(--run)" : "var(--ok)";
    const bar = h("div", { class: "bar" }, h("i", { style: `width:${Math.min(100, pct)}%;background:${color}` }));
    box.append(h("div", { class: "meter" },
      h("div", { class: "lbl" }, h("span", {}, label), h("b", { class: "num" }, pct + "%")),
      bar,
      h("div", { class: "lbl", style: "margin-top:4px" }, h("span", {}, resetLabel(w.resets_at)), h("span"))));
  }
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
  $("pipeline").replaceChildren(...stages.map(([k, c, v, sub]) =>
    h("div", { class: "stage", style: `--c:${c}` },
      h("div", { class: "k" }, k),
      h("div", { class: "v num" + (v ? "" : " zero") }, v),
      h("div", { class: "s" }, sub))));
}

// fit hides a list's last children until its panel stops overflowing, and
// says how many it hid.
function fit(list, panel, noun) {
  const kids = [...list.children];
  let hidden = 0;
  while (kids.length - hidden > 1 && panel.scrollHeight > panel.clientHeight + 1) {
    kids[kids.length - 1 - hidden].remove();
    hidden++;
  }
  if (hidden) {
    list.append(h("div", { class: "more" }, "+ " + hidden + " more " + noun));
    // the note itself may push one more out
    if (panel.scrollHeight > panel.clientHeight + 1 && list.children.length > 2) {
      list.children[list.children.length - 2].remove();
      list.lastChild.textContent = "+ " + (hidden + 1) + " more " + noun;
    }
  }
}

function renderRunning(d) {
  $("running-count").textContent = d.running.length;
  const list = $("running");
  if (!d.running.length) {
    list.replaceChildren(h("div", { class: "empty" }, d.queue.paused ? "Nothing running: the queue is paused." : "Nothing running right now."));
    return;
  }
  list.replaceChildren(...d.running.map((r) => {
    const meta = [r.project, d.privacy ? r.runner : "runner " + r.runner, "run " + r.run];
    if (r.cost_usd != null) meta.push(money(r.cost_usd));
    const changed = r.add || r.del;
    return h("div", { class: "card" },
      h("div", {},
        h("div", { class: "t" }, r.title, r.sent_back && h("span", { class: "tag" }, "sent back: " + r.sent_back)),
        h("div", { class: "meta" }, meta.map((m) => h("span", {}, m)))),
      h("div", { class: "right" },
        h("div", { class: "elapsed num", "data-since": r.started_at || "" }, r.started_at ? "" : "—"),
        changed
          ? h("div", { class: "diff num" }, h("span", { class: "add" }, "+" + num(r.add)), " ", h("span", { class: "del" }, "−" + num(r.del)),
            r.files ? " · " + r.files + " file" + (r.files === 1 ? "" : "s") : "")
          : h("div", { class: "diff num", style: "color:var(--faint)" }, "no changes yet")),
      r.activity && h("div", { class: "live" }, r.activity));
  }));
  fit(list, list.parentElement, "running");
}

// What a pull request in Review waits for a person to do (wallNeed.waits).
const PR_WAITS = { review: "Awaiting review", merge: "Waiting to be merged on GitHub", blocked: "Blocked on GitHub" };

function renderNeeds(d, now) {
  const items = d.needs.map((n) => {
    const since = now - new Date(n.since);
    if (n.kind === "review") {
      const why = ["Review", n.project];
      if (n.add || n.del) why.push("+" + num(n.add) + " −" + num(n.del));
      if (n.tests === "passed") why.push("tests ✓");
      if (n.tests === "failed") why.push("tests ✗");
      if (n.pr) why.push("PR #" + n.pr);
      if (PR_WAITS[n.waits]) why.push(PR_WAITS[n.waits]);
      return h("div", { class: "item" + (n.old ? " old" : ""), style: "--c:var(--rev)" }, h("i", { class: "bar2" }),
        h("div", {}, h("div", { class: "t" }, n.title), h("div", { class: "why" }, why.join(" · "))),
        h("div", { class: "age num" }, age(since)));
    }
    const why = ["Failed", n.project];
    if (!d.privacy) why.push(n.reason || "no reason given");
    if (n.resumable) why.push("resumable");
    return h("div", { class: "item" + (n.old ? " old" : ""), style: "--c:var(--bad)" }, h("i", { class: "bar2" }),
      h("div", {}, h("div", { class: "t" }, n.title), h("div", { class: "why" }, why.join(" · "))),
      h("div", { class: "age num" }, age(since)));
  });
  const is = d.issues;
  if (is.open) {
    const sev = ["critical", "high", "medium", "low", "info"].filter((k) => is.severity[k]).map((k) => is.severity[k] + " " + k);
    const why = is.top.length ? is.top.join(" · ") : sev.join(" · ");
    const urgent = (is.severity.critical || 0) + (is.severity.high || 0) > 0;
    items.push(h("div", { class: "item", style: `--c:${urgent ? "var(--bad)" : "var(--queue)"}` }, h("i", { class: "bar2" }),
      h("div", {}, h("div", { class: "t" }, is.open + " open issue" + (is.open === 1 ? "" : "s")), h("div", { class: "why" }, why)),
      h("div", { class: "age num" }, sev[0] || "—")));
  }
  const total = d.stages.review - d.stages.review_github + d.stages.failed + (is.open ? 1 : 0);
  $("needs-count").textContent = total;
  const list = $("needs");
  if (!items.length) {
    list.replaceChildren(h("div", { class: "empty" }, "Nothing waiting for a human. ✓"));
    return;
  }
  list.replaceChildren(...items);
  fit(list, list.parentElement, "waiting");
}

function renderRunners(d, now) {
  $("runners-count").textContent = d.runners.length;
  const box = $("runners");
  if (!d.runners.length) {
    box.replaceChildren(h("div", { class: "empty" }, "No runners registered."));
    return;
  }
  box.replaceChildren(...d.runners.map((r) => {
    const slots = h("div", { class: "slots" });
    const n = Math.min(Math.max(r.slots, r.busy), 12);
    for (let i = 0; i < n; i++) slots.append(h("i", { class: i < r.busy ? "on" : "" }));
    let sub;
    if (!r.online) {
      sub = "offline" + (r.last_seen ? " " + age(now - new Date(r.last_seen)) : "") +
        (r.waiting ? " · " + r.waiting + " task" + (r.waiting === 1 ? "" : "s") + " waiting" : "");
    } else {
      sub = (r.paused ? "paused · " : "") + r.projects + " project" + (r.projects === 1 ? "" : "s") + " · " + r.busy + "/" + r.slots + " busy";
    }
    return h("div", { class: "runner" },
      h("span", { class: "dot", style: `color:${r.online ? (r.paused ? "var(--run)" : "var(--ok)") : "var(--bad)"}` }),
      h("div", {}, h("div", { class: "n" + (r.online ? "" : " off") }, r.name), h("div", { class: "h" + (r.online ? "" : " off") }, sub)),
      slots);
  }));
  fit(box, box.parentElement, "runners");
}

function renderToday(d) {
  const t = d.today;
  const tiles = [["Merged", String(t.merged)]];
  if (t.spend_usd != null) tiles.push(["Spend", money(t.spend_usd)]);
  if (t.first_try != null) tiles.push(["First-try merges", Math.round(t.first_try * 100) + "%"]);
  tiles.push(["Lines landed", "+" + num(d.stages.merged_add)]);
  $("today").replaceChildren(...tiles.slice(0, 4).map(([k, v]) => h("div", {}, k, h("b", { class: "num" }, v))));

  // Merges per hour: from 08:00 (or the first merge, if earlier) to now.
  let from = Math.min(8, t.hour);
  const first = t.per_hour.findIndex((v) => v > 0);
  if (first >= 0 && first < from) from = first;
  const hours = t.per_hour.slice(from, t.hour + 1);
  const max = Math.max(1, ...hours);
  const sp = $("spark");
  sp.replaceChildren();
  const slot = 300 / Math.max(hours.length, 1), w = Math.max(4, slot * 0.72);
  hours.forEach((v, i) => {
    const hgt = v ? Math.max(6, (v / max) * 104) : 3;
    sp.append(svg("rect", { x: (i * slot + (slot - w) / 2).toFixed(1), y: (110 - hgt).toFixed(1), width: w.toFixed(1), height: hgt.toFixed(1), rx: 3,
      style: "fill:var(" + (v ? "--ok" : "--line") + ")", opacity: i === hours.length - 1 ? 1 : 0.55 }));
  });
  $("spark-from").textContent = pad(from) + ":00";
}

let last = null; // the latest data, to lay out again on a resize

function render(d) {
  last = d;
  const now = new Date(d.now);
  renderHeader(d);
  renderPipeline(d, now);
  renderRunning(d);
  renderNeeds(d, now);
  renderRunners(d, now);
  renderToday(d);
  tick();
  fitPage();
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

function schedule(ms) {
  clearTimeout(timer);
  timer = setTimeout(poll, ms);
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
      const d = await res.json();
      skew = new Date(d.now) - Date.now();
      render(d);
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
  document.body.style.height = narrow ? Math.ceil($("tv").offsetHeight * zoom) + "px" : "";
}

let resizing = 0;
addEventListener("resize", () => {
  cancelAnimationFrame(resizing);
  resizing = requestAnimationFrame(() => {
    layout();
    // Lay the lists out again now, not at the next poll: how many cards fit
    // depends on the size.
    if (last) render(last);
  });
});

// A hidden tab's timers are throttled; catch up as soon as it's shown.
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") poll();
});

layout();
tick();
setInterval(tick, 1000);
poll();
// A TV runs for days: reload now and then to pick up a new Dash version.
setTimeout(() => location.reload(), 6 * 3600 * 1000);
