import { $, $$, h, ib, ico, plural, setLabel, dateTimeText } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, busy, fail, railOf, toast } from "./dialogs.js";
import { renderMarkdown } from "../editor.js";
import { SHEET, planSource, startAdd } from "./projects.js";

// ------------------------------------------------------------------ plan a new project
// The Add project sheet's first section: the user describes an idea and
// Claude interviews them about it, a few questions at a time, each with a
// suggested answer, writing the project's brief as it goes (internal/api/
// plans.go keeps the plan, so a draft survives a reload). Five steps:
// the idea, the interview (the brief beside it on a wide screen), the
// brief to read and edit, the repository (one of the sheet's own sources,
// picked with "Use for the plan"), and Launch, which adds the project as
// the sheet does (startAdd) and goes on to commit the brief and start a goal.

const STEPS = [
  { key: "idea", title: "Idea" },
  { key: "interview", title: "Interview" },
  { key: "brief", title: "Brief" },
  { key: "repo", title: "Repository" },
  { key: "launch", title: "Launch" },
];
const stepIndex = (key) => STEPS.findIndex((s) => s.key === key);

// pl is the planner's state: the drafts, the open plan (as the server has
// it), the step shown, the answers being given to the latest round, and
// what launching needs.
const pl = {
  drafts: null,
  cur: null,
  step: "idea",
  answers: new Map(), // question id → { text, default, skipped }
  message: "",
  thinking: false,
  error: "",
  retry: null, // the interview request to send again after an error
  repo: null, // projects.js planRepo(), once a source is picked
  launch: null, // GET /api/plans/{id}/launch
  briefView: "edit",
  saving: null, // the brief's pending save
  visible: false,
};

const plansAPI = (id = "", rest = "") => `/api/plans${id ? `/${encodeURIComponent(id)}` : ""}${rest}`;
const call = (method, path, body) => api(method, path, body, ""); // plans are the hub's
const lastRound = () => pl.cur?.rounds?.at(-1) || null;
const waiting = () => { const r = lastRound(); return r && !r.answered ? r : null; };

export const planner = {
  // open reports whether a plan is open: the sources then pick its repository.
  open: () => !!pl.cur,
  title: () => pl.cur?.name || pl.cur?.idea?.slice(0, 60) || "new plan",
  // shown is called when the sheet's section changes: on (the planner's) or off.
  shown(on) {
    pl.visible = on;
    if (on && !pl.cur && !pl.drafts) loadDrafts().catch(fail);
    render();
  },
  focus,
  // backTo shows the planner at step.
  backTo(step) {
    railOf(SHEET).show("plan");
    go(step);
  },
  // useRepo takes the sheet's source for the plan (projects.js planRepo()).
  useRepo(repo) {
    if (!repo.ready) {
      if (repo.why) toast(repo.why, true);
      return;
    }
    pl.repo = repo;
    planner.backTo("launch");
  },
  // closed: the sheet closed. An edit to the brief not saved yet is saved.
  closed() { flushBrief().catch(fail); reset(); },
  launched() { pl.drafts = null; reset(); },
};

function reset() {
  clearTimeout(pl.saving?.timer);
  Object.assign(pl, { cur: null, step: "idea", message: "", thinking: false, error: "", retry: null, repo: null, launch: null, saving: null });
  pl.answers.clear();
  render();
}

// ---- drafts

async function loadDrafts() {
  pl.drafts = await call("GET", plansAPI());
  render();
}

function renderHome() {
  const list = $("#plan-drafts");
  const drafts = pl.drafts || [];
  $("#plan-drafts-head").hidden = !drafts.length;
  list.replaceChildren(...drafts.map((p) => h("li", { class: "plan-draft" },
    h("button", { type: "button", class: "plan-draft-open", onclick: attempt(() => openPlan(p.id)) },
      h("span", { class: "plan-draft-title" }, p.name || p.idea || "Untitled plan"),
      h("span", { class: "plan-draft-meta" }, `${STEPS[Math.max(0, stepIndex(p.step))].title} · ${plural(p.rounds?.length || 0, "round")} · ${dateTimeText(p.updated_at)}`)),
    ib({ type: "button", class: "icon-btn", "aria-label": `Delete ${p.name || "plan"}`, title: "Delete", onclick: attempt(() => deleteDraft(p)) }, "trash"))));
}

async function deleteDraft(p) {
  if (!await ask({ title: `Delete ${p.name || "this plan"}?`, body: "Its interview and brief go with it.", ok: "Delete", danger: true })) return;
  await call("DELETE", plansAPI(p.id));
  pl.drafts = (pl.drafts || []).filter((x) => x.id !== p.id);
  render();
  $("#plan-new").focus();
}

async function openPlan(id) {
  const p = await call("GET", plansAPI(id));
  reset();
  pl.cur = p;
  // The repository isn't kept with the plan: picked again after a reload.
  pl.step = ["repo", "launch"].includes(p.step) ? "repo" : p.step || "idea";
  if (pl.step !== "idea" && !p.rounds.length && !p.brief) pl.step = "idea";
  render();
  focus();
}

$("#plan-new").addEventListener("click", () => {
  reset();
  pl.cur = { id: "", name: "", idea: "", rounds: [], topics: [], brief: "", open: [], verdict: {}, step: "idea" };
  render();
  focus();
});
$("#plan-all").addEventListener("click", attempt(async () => {
  await flushBrief();
  reset();
  await loadDrafts();
  $("#plan-new").focus();
}));

// ---- moving between steps

// reachable reports whether step i can be shown: each needs what the one
// before it makes.
function reachable(i) {
  const p = pl.cur;
  if (!p) return false;
  return [true, !!p.id && !!p.idea, !!p.brief, !!p.brief, !!p.brief && !!pl.repo][i];
}

async function go(step) {
  const i = stepIndex(step);
  if (i < 0 || !reachable(i)) return;
  if (pl.step === "brief") await flushBrief();
  pl.step = step;
  if (pl.cur?.id && pl.cur.step !== step) {
    pl.cur.step = step;
    call("PATCH", plansAPI(pl.cur.id), { step }).catch(() => {}); // only where to open it next time
  }
  render();
  focus();
  if (step === "launch") loadLaunch().catch(fail);
}

// focus puts the focus where the step starts.
function focus() {
  if (!pl.visible) return;
  if (!pl.cur) return $("#plan-new").focus();
  const pane = $(`.plan-pane[data-step="${pl.step}"]`);
  const el = {
    idea: () => $("#plan-idea"),
    interview: () => $(".plan-q-card .plan-chip", pane) || $("#plan-say") || $(".plan-error .btn", pane),
    brief: () => pl.briefView === "edit" ? $("#plan-brief") : $('[data-brief="preview"]'),
    repo: () => $(".plan-source", pane),
    launch: () => $("#plan-next"),
  }[pl.step]?.();
  (el || $(".plan-stepper [aria-current=step]"))?.focus({ preventScroll: pl.step === "interview" });
}

function render() {
  const home = !pl.cur;
  $(`#${SHEET}`).classList.toggle("np-planning", !home);
  $("#plan-home").hidden = !home;
  $("#plan-work").hidden = home;
  if (home) renderHome();
  else {
    renderStepper();
    for (const pane of $$(".plan-pane")) pane.hidden = pane.dataset.step !== pl.step;
    ({ idea: renderIdea, interview: renderInterview, brief: renderBrief, repo: renderRepo, launch: renderLaunch })[pl.step]?.();
  }
  renderFooter();
}

function renderStepper() {
  const cur = stepIndex(pl.step);
  $("#plan-stepper").replaceChildren(...STEPS.map((s, i) => {
    const done = i < cur && reachable(i), here = i === cur;
    return h("li", { class: `plan-st${here ? " is-current" : done ? " is-done" : " is-later"}` },
      h("button", {
        type: "button", "aria-current": here ? "step" : null, disabled: !here && !reachable(i),
        "aria-label": `${s.title}${done ? ", done" : ""}`, onclick: attempt(() => go(s.key)),
      }, h("span", { class: "plan-st-mark", "aria-hidden": "true" }, done ? ico("check") : String(i + 1)), h("span", { class: "plan-st-title" }, s.title)));
  }));
  $("#plan-stepper-compact").textContent = `Step ${cur + 1} of ${STEPS.length} · ${STEPS[cur].title}`;
}

// The footer: Back and the step's Next in place of Add project.
function renderFooter() {
  const back = $("#plan-back"), next = $("#plan-next"), on = pl.visible && !!pl.cur;
  back.hidden = !on || pl.step === "idea";
  next.hidden = !on;
  // Add project's own button is for the sources (startAdd hides it while adding).
  $("#new-project-go").hidden = pl.visible;
  if (!on) return;
  const p = pl.cur;
  const [icon, label, ok] = {
    idea: ["spark", p.rounds.length ? "Continue the interview" : "Start the interview", !pl.thinking],
    interview: ["right", "Review the brief", !!p.brief && !pl.thinking],
    brief: ["right", "Choose the repository", !!p.brief],
    repo: ["right", "Continue", !!pl.repo],
    launch: ["rocket", "Launch", !!pl.launch && !!pl.repo],
  }[pl.step];
  setLabel(next, icon, label);
  next.disabled = !ok;
}

$("#plan-back").addEventListener("click", attempt(() => go(STEPS[Math.max(0, stepIndex(pl.step) - 1)].key)));
$("#plan-next").addEventListener("click", attempt(async (e) => {
  const btn = e.currentTarget;
  if (pl.step === "idea") return busy(btn, startInterview);
  if (pl.step === "launch") return busy(btn, launch);
  await go(STEPS[stepIndex(pl.step) + 1].key);
}));

// ---- 1. the idea

let whereText = null;
function renderIdea() {
  const f = { idea: $("#plan-idea"), name: $("#plan-name") };
  if (document.activeElement !== f.idea) f.idea.value = pl.cur.idea;
  if (document.activeElement !== f.name) f.name.value = pl.cur.name;
  if (whereText === null) {
    whereText = "";
    // Like the other Claude calls without a project: the first of the user's runners online.
    api("GET", "/api/claude", null, "").then((c) => {
      const r = (c.runners || []).find((x) => x.online);
      whereText = c.local ? "" : r ? `Claude interviews you on your runner ${r.name}.` : "None of your runners is online: start `dash runner` on your computer to plan with Claude.";
      $("#plan-where").textContent = whereText;
      $("#plan-where").classList.toggle("warn", !r && !c.local);
    }).catch(() => { whereText = null; });
  }
  $("#plan-where").textContent = whereText || "";
}

// saveIdea saves the idea and name: the plan is made with the first save.
async function saveIdea() {
  const idea = $("#plan-idea").value.trim(), name = $("#plan-name").value.trim();
  if (!idea) {
    $("#plan-idea").focus();
    throw new Error("Describe the idea first: a sentence or two is enough.");
  }
  if (!pl.cur.id) pl.cur = await call("POST", plansAPI(), { idea, name, step: "interview" });
  else if (idea !== pl.cur.idea || name !== pl.cur.name) pl.cur = { ...await call("PATCH", plansAPI(pl.cur.id), { idea, name }) };
}

async function startInterview() {
  await saveIdea();
  pl.drafts = null; // listed again next time
  await go("interview");
  if (!pl.cur.rounds.length) await interview({});
}

for (const el of ["#plan-idea", "#plan-name"]) {
  $(el).addEventListener("keydown", (e) => {
    if (e.key !== "Enter" || e.shiftKey || e.isComposing) return;
    e.preventDefault();
    if (!$("#plan-next").disabled) $("#plan-next").click();
  });
}

// ---- 2. the interview

// interview sends body (the answers) and shows Claude's next round.
async function interview(body) {
  const id = pl.cur.id;
  Object.assign(pl, { thinking: true, error: "", retry: body });
  render();
  scrollTalk();
  try {
    const p = await call("POST", plansAPI(id, "/interview"), body);
    if (pl.cur?.id !== id) return;
    pl.cur = p;
    pl.answers.clear();
    pl.message = "";
  } catch (e) {
    if (pl.cur?.id !== id) return;
    pl.error = e.message || String(e);
  } finally {
    if (pl.cur?.id === id) {
      pl.thinking = false;
      render();
      scrollTalk();
      if (pl.step === "interview") focus();
    }
  }
}

// answersBody is what the latest round's answers send: every question,
// one not answered as skipped.
function answersBody() {
  const r = waiting();
  return {
    answers: (r?.questions || []).map((q) => {
      const a = pl.answers.get(q.id);
      return a && !a.skipped && a.text.trim() ? { question: q.id, text: a.text.trim(), default: !!a.default } : { question: q.id, text: "" };
    }),
    message: pl.message.trim(),
  };
}

function send() {
  if (pl.thinking) return;
  const body = answersBody();
  if (!waiting()?.questions?.length && !body.message) {
    $("#plan-say")?.focus();
    return;
  }
  interview(body);
}

const topicTitle = (id) => (pl.cur.topics || []).find((t) => t.id === id)?.title || "";
const TOPIC_MARK = { done: ["check", "done"], skipped: ["minimize", "skipped"], open: ["circle", "open"] };

function renderInterview() {
  const r = waiting(), asking = new Set((r?.questions || []).map((q) => q.topic));
  $("#plan-topics").replaceChildren(...(pl.cur.topics || []).map((t) => {
    const [icon, word] = TOPIC_MARK[t.status] || TOPIC_MARK.open;
    return h("li", { class: `plan-topic is-${t.status}${asking.has(t.id) ? " is-asking" : ""}`, title: `${t.title}: ${word}` },
      ico(icon), h("span", {}, t.title), h("span", { class: "sr-only" }, `, ${word}${asking.has(t.id) ? ", asked now" : ""}`));
  }));
  renderTalk();
  renderLive();
}

function renderTalk() {
  const talk = $("#plan-talk"), rounds = pl.cur.rounds || [];
  const out = [];
  for (const r of rounds) {
    if (r === waiting()) break;
    out.push(h("div", { class: "msg assistant" },
      r.note ? h("div", { class: "reply", innerHTML: renderMarkdown(r.note) }) : null,
      r.questions?.length ? h("ul", { class: "plan-qa" }, ...r.questions.map((q) => {
        const a = (r.answers || []).find((x) => x.question === q.id);
        return h("li", {}, h("span", { class: "plan-qa-q" }, q.text),
          h("span", { class: `plan-qa-a${!a || a.skipped ? " is-skipped" : ""}` }, !a || a.skipped ? "Skipped" : a.text),
          a?.default ? h("span", { class: "plan-qa-tag" }, "suggested") : null);
      })) : null));
    if (r.message) out.push(h("div", { class: "msg user" }, r.message));
  }
  const r = waiting();
  if (r) out.push(roundForm(r));
  if (pl.thinking) out.push(h("div", { class: "msg assistant pending", role: "status" }, h("div", { class: "reply" }), h("span", { class: "sr-only" }, "Claude is thinking")));
  if (pl.error) {
    out.push(h("div", { class: "plan-error", role: "alert" }, ico("alert"), h("span", {}, pl.error),
      ib({ type: "button", class: "btn small primary", onclick: () => interview(pl.retry || {}) }, "reload", "Try again")));
  } else if (!rounds.length && !pl.thinking) {
    out.push(h("div", { class: "plan-start" }, ib({ type: "button", class: "btn primary", onclick: () => interview({}) }, "spark", "Start the interview")));
  }
  talk.replaceChildren(...out);
}

// roundForm is the latest round: Claude's note, its questions with their
// suggestions and a field for an answer of one's own, and anything else to say.
function roundForm(r) {
  const off = pl.thinking;
  r.questions ||= [];
  const cards = r.questions.map((q) => {
    const a = pl.answers.get(q.id);
    const opts = [...new Set([q.default, ...(q.options || [])].filter(Boolean))];
    const own = h("input", {
      class: "plan-own", placeholder: "Or your own answer", "aria-label": `Your own answer: ${q.text}`, disabled: off, maxlength: 2000,
      value: a && !a.skipped && !opts.includes(a.text) ? a.text : "",
      oninput: (e) => { const v = e.target.value; if (v.trim()) pl.answers.set(q.id, { text: v }); else pl.answers.delete(q.id); syncCard(card, q); },
      onkeydown: (e) => { if (e.key === "Enter" && !e.isComposing) { e.preventDefault(); send(); } },
    });
    const card = h("fieldset", { class: `plan-q-card${q.missing ? " is-missing" : ""}`, "data-q": q.id, disabled: off },
      h("legend", { class: "plan-q-text" },
        q.topic ? h("span", { class: "plan-q-topic" }, topicTitle(q.topic)) : null,
        q.missing ? h("span", { class: "plan-q-missing" }, ico("alert"), "Not mentioned yet") : null,
        h("span", {}, q.text)),
      h("div", { class: "plan-chips" }, ...opts.map((o) => h("button", {
        type: "button", class: "plan-chip", "data-text": o, "aria-pressed": "false",
        title: o === q.default ? "Claude's suggestion" : null,
        onclick: () => { pl.answers.set(q.id, { text: o, default: o === q.default }); own.value = ""; syncCard(card, q); },
      }, o === q.default ? ico("spark") : null, h("span", {}, o), o === q.default ? h("span", { class: "sr-only" }, " (suggested)") : null)),
      h("button", { type: "button", class: "plan-chip plan-skip", "data-skip": "1", "aria-pressed": "false", onclick: () => { pl.answers.set(q.id, { text: "", skipped: true }); own.value = ""; syncCard(card, q); } }, "Decide later")),
      own);
    syncCard(card, q);
    return card;
  });
  const say = h("textarea", {
    id: "plan-say", class: "plan-say", rows: 2, maxlength: 2000, disabled: off,
    placeholder: r.questions.length ? "Anything else? (optional)" : "Add or change something",
    "aria-label": "Anything else to tell Claude", value: pl.message,
    oninput: (e) => { pl.message = e.target.value; },
    onkeydown: (e) => { if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); send(); } },
  });
  return h("div", { class: "plan-round" },
    h("div", { class: "msg assistant" }, r.note ? h("div", { class: "reply", innerHTML: renderMarkdown(r.note) }) : null,
      !r.questions.length ? h("p", { class: "hint" }, "No more questions: review the brief, or tell Claude what to change.") : null),
    r.questions.length > 1 ? h("button", { type: "button", class: "link plan-all-defaults", disabled: off, onclick: useDefaults }, ico("spark"), "Use all suggestions") : null,
    ...cards,
    h("div", { class: "plan-compose" }, say,
      ib({ type: "button", class: "btn primary", disabled: off, onclick: send }, "send", r.questions.length ? "Send answers" : "Send")),
    r.questions.length ? h("p", { class: "hint plan-compose-hint" }, "Enter sends. Questions you don't answer are left for later.") : null);
}

// syncCard marks a card's chosen answer.
function syncCard(card, q) {
  const a = pl.answers.get(q.id);
  for (const c of $$(".plan-chip", card)) {
    const on = !!a && (c.dataset.skip ? !!a.skipped : !a.skipped && c.dataset.text === a.text);
    c.setAttribute("aria-pressed", String(on));
  }
  card.classList.toggle("is-answered", !!a);
}

function useDefaults() {
  for (const q of waiting()?.questions || []) if (q.default && !pl.answers.has(q.id)) pl.answers.set(q.id, { text: q.default, default: true });
  renderTalk();
  $("#plan-say")?.focus();
}

function scrollTalk() {
  const main = $("#new-project-main");
  if (!pl.visible || pl.step !== "interview") return;
  const target = $(".plan-round", $("#plan-talk")) || $("#plan-talk").lastElementChild;
  if (target) main.scrollTop = Math.max(0, target.offsetTop - main.offsetTop - 12);
}

// The brief beside the questions, as it fills in; phones switch with tabs.
function renderLive() {
  briefMD($("#plan-live"), pl.cur.brief, "Claude writes the brief here as you answer.");
}
for (const b of $$(".plan-tabs [role=tab]")) {
  b.addEventListener("click", () => {
    $("#plan-split").dataset.view = b.dataset.view;
    for (const x of $$(".plan-tabs [role=tab]")) x.setAttribute("aria-selected", String(x === b));
  });
}

// briefMD renders a brief, its open decisions ("_Open:_ …") marked.
function briefMD(el, text, empty) {
  if (!text?.trim()) {
    el.replaceChildren(h("p", { class: "hint" }, empty));
    return;
  }
  el.innerHTML = renderMarkdown(text);
  for (const em of $$("em", el)) {
    if (/^open\b/i.test(em.textContent.trim())) em.closest("p, li")?.classList.add("plan-open-mark");
  }
}

// ---- 3. the brief

function renderBrief() {
  const p = pl.cur, v = p.verdict || {};
  const verdict = $("#plan-verdict");
  verdict.className = `plan-verdict ${v.ready ? "is-ready" : "is-not"}`;
  verdict.replaceChildren(ico(v.ready ? "check" : "alert"),
    h("div", {},
      h("strong", {}, v.ready ? "Enough to start" : p.rounds.length ? "Not enough to start yet" : "No interview yet"),
      v.summary ? h("p", {}, v.summary) : null,
      v.can_wait?.length ? h("div", { class: "plan-can-wait" }, h("span", {}, "Can wait:"), h("ul", {}, ...v.can_wait.map((x) => h("li", {}, x)))) : null,
      !v.ready ? h("button", { type: "button", class: "link", onclick: attempt(() => go("interview")) }, "Back to the interview") : null));
  const ta = $("#plan-brief");
  if (document.activeElement !== ta && !pl.saving) ta.value = p.brief;
  const edit = pl.briefView === "edit";
  ta.hidden = !edit;
  $("#plan-brief-preview").hidden = edit;
  if (!edit) briefMD($("#plan-brief-preview"), ta.value, "The brief is empty.");
  for (const x of $$("[data-brief]")) x.setAttribute("aria-selected", String(x.dataset.brief === pl.briefView));
  $("#plan-open").replaceChildren(...(p.open?.length ? [h("h3", {}, "Open questions"), h("ul", {}, ...p.open.map((q) => h("li", {}, ico("alert"), h("span", {}, q))))] : []));
}
for (const b of $$("[data-brief]")) {
  b.addEventListener("click", () => { pl.briefView = b.dataset.brief; renderBrief(); });
}

// The brief saves itself a moment after each edit.
$("#plan-brief").addEventListener("input", () => {
  clearTimeout(pl.saving?.timer);
  $("#plan-saved").textContent = "";
  pl.saving = { timer: setTimeout(() => flushBrief().catch(fail), 800) };
  $("#plan-next").disabled = !$("#plan-brief").value.trim();
});

async function flushBrief() {
  if (!pl.saving || !pl.cur?.id) return;
  clearTimeout(pl.saving.timer);
  pl.saving = null;
  const brief = $("#plan-brief").value, id = pl.cur.id;
  $("#plan-saved").textContent = "Saving…";
  try {
    const p = await call("PATCH", plansAPI(id), { brief });
    if (pl.cur?.id === id) pl.cur = p; // still open: not after the sheet closed
    $("#plan-saved").textContent = "Saved";
  } catch (e) {
    $("#plan-saved").textContent = "Not saved";
    throw e;
  }
}

// ---- 4. the repository

function renderRepo() {
  const sources = $$("#new-project-nav .np-src").filter((b) => b.dataset.rail !== "plan" && !b.hidden);
  $("#plan-sources").replaceChildren(...sources.map((b) => h("button", {
    type: "button", class: `plan-source${pl.repo?.mode === b.dataset.rail ? " is-picked" : ""}`,
    onclick: () => planSource(b.dataset.rail, pl.cur.name || briefTitle(pl.cur.brief)),
  }, h("span", { class: "plan-source-name" }, b.firstElementChild.textContent), h("span", { class: "plan-source-desc" }, b.lastElementChild.textContent))));
  const picked = $("#plan-repo");
  picked.hidden = !pl.repo;
  if (pl.repo) picked.replaceChildren(ico("repo"), h("span", {}, pl.repo.what, pl.repo.runner ? ` · on ${pl.repo.runner}` : ""), h("em", {}, "for the plan"));
}

// briefTitle is the brief's "# " heading, the project's name when the plan has none.
const briefTitle = (brief) => (brief || "").split("\n").find((l) => l.startsWith("# "))?.slice(2).trim() || "";

// ---- 5. launch

async function loadLaunch() {
  const id = pl.cur.id;
  const v = await call("GET", plansAPI(id, "/launch"));
  if (pl.cur?.id !== id) return;
  pl.launch = v;
  if (pl.step === "launch") render();
}

function renderLaunch() {
  const v = pl.launch, r = pl.repo;
  const row = (k, val) => [h("dt", {}, k), h("dd", {}, val)];
  $("#plan-summary").replaceChildren(
    ...row("Project", r?.name || pl.cur.name || "—"),
    ...row("Repository", r ? r.what : "—"),
    ...row("Runner", r?.runner || "—"),
    ...row("Goal", v ? v.goal.title : "…"));
  briefMD($("#plan-launch-brief"), v?.brief || "", "…");
  briefMD($("#plan-launch-claude"), v?.claude_md || "", "…");
}

async function launch() {
  await flushBrief();
  await loadLaunch();
  if (!startAdd({ plan: pl.cur, mode: pl.repo.mode, launch: pl.launch })) {
    throw new Error("The repository picked for the plan isn't ready any more: choose it again.");
  }
}
