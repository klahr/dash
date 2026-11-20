import { $, $$, h, ib, ico, plural, setLabel, dateTimeText } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, busy, fail, railOf, toast } from "./dialogs.js";
import { renderMarkdown } from "../editor.js";
import * as store from "./store.js";

// The Add project sheet (projects.js's SHEET). projects.js is the app's, so
// it is loaded only for a project's brief: the planners' Planning page
// (planning-page.js) uses this module for feature briefs without it.
const SHEET = "new-project-sheet";
const projects = () => import("./projects.js");

// ------------------------------------------------------------- write a brief
// The Add project sheet's first section: the user describes an idea and
// Claude interviews them about it, a few questions at a time, each with a
// suggested answer, writing the project's brief as it goes (internal/api/
// plans.go keeps the plan, so a draft survives a reload). Five steps:
// the idea, the interview (the brief beside it on a wide screen), the
// brief to read and edit, the repository (one of the sheet's own sources,
// picked with "Use for the brief"), and Launch, which adds the project as
// the sheet does (startAdd) and goes on to commit the brief and start a goal.
//
// The same planner writes a feature brief, for a project, onto a Planning
// card (openFeatureBrief): in the Feature brief sheet, where the planner's
// markup is moved (mount), with four steps: the idea, the interview, the
// brief, and Start, which puts the brief on the card and starts a task or
// a goal from it, or saves it to Planned. What differs by kind is KINDS's
// (the server's is model.PlanKinds); the markup marks its own with data-kind.

const STEP_TITLES = { idea: "Idea", interview: "Interview", brief: "Brief", repo: "Repository", launch: "Launch", start: "Start" };

// KINDS: each kind's steps, the sheet it shows in (its scrolling part, the
// slot the planner's markup goes in and the footer for Back and Next),
// and its own words.
const KINDS = {
  project: {
    steps: ["idea", "interview", "brief", "repo", "launch"],
    sheet: () => $(`#${SHEET}`), main: () => $("#new-project-main"), slot: () => $("#plan-slot"),
    footer: () => $("#new-project-go").parentElement, before: () => $("#new-project-go"),
    briefNext: "Choose the repository",
  },
  feature: {
    steps: ["idea", "interview", "brief", "start"],
    sheet: () => $("#feature-sheet"), main: () => $("#feature-main"), slot: () => $("#feature-slot"),
    footer: () => $("#feature-actions"), before: () => null,
    briefNext: "Choose how to start",
  },
};
const kindOf = (p) => (p?.kind === "feature" ? "feature" : "project");
const steps = () => KINDS[pl.kind].steps.map((key) => ({ key, title: STEP_TITLES[key] }));
const stepIndex = (key) => KINDS[pl.kind].steps.indexOf(key);

// pl is the planner's state: the drafts, the open plan (as the server has
// it), the step shown, the answers being given to the latest round, and
// what launching needs.
const pl = {
  kind: "project", // the kind of brief the planner shows: KINDS's
  feature: null, // the feature sheet's: { onDone(card), card (the card it started from) }
  projects: null, // the projects a feature may be for: GET /api/planning/projects
  start: "task", // the feature's Start: task, goal or planned
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
  // open reports whether a project's plan is open: the sources then pick its repository.
  open: () => !!pl.cur && pl.kind === "project",
  title: () => pl.cur?.name || pl.cur?.idea?.slice(0, 60) || "new brief",
  // shown is called when the Add project sheet's section changes: on (the
  // planner's) or off.
  shown(on) {
    if (on) mount("project");
    if (pl.kind !== "project") return;
    pl.visible = on;
    // An idea typed but not saved yet (the sheet closed on it) opens again.
    if (on && !pl.cur && typedIdea()) return newProject();
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
  Object.assign(pl, { cur: null, step: "idea", message: "", thinking: false, error: "", retry: null, repo: null, launch: null, saving: null, start: "task" });
  pl.answers.clear();
  render();
}

// mount moves the planner's markup into kind's sheet (its Back and Next
// into the sheet's footer), and shows kind's own parts of it.
function mount(kind) {
  if (pl.kind !== kind && pl.cur) { flushBrief().catch(fail); reset(); }
  const k = KINDS[kind];
  pl.kind = kind;
  const slot = k.slot();
  if ($("#plan-home").parentElement !== slot) slot.append($("#plan-home"), $("#plan-work"));
  if ($("#plan-next").parentElement !== k.footer()) k.footer().insertBefore($("#plan-back"), k.before()), k.footer().insertBefore($("#plan-next"), k.before());
  for (const el of $$("[data-kind]", slot)) el.hidden = el.dataset.kind !== kind;
  for (const el of [$("#plan-idea"), $("#plan-name")]) {
    el.dataset.projectPlaceholder ??= el.placeholder;
    el.placeholder = kind === "feature" ? el.dataset.featurePlaceholder : el.dataset.projectPlaceholder;
  }
}

// ---- a feature brief

// openFeatureBrief opens the Feature brief sheet on a new brief for a
// feature of project (an id; "" lets the user pick it), from idea, or for
// card (a Planning card: its brief then refines the card's title and
// description, and a draft for it already is opened instead). onDone(card)
// is called with the card once the brief is on it.
export async function openFeatureBrief({ card = null, project = "", idea = "", onDone = null } = {}) {
  mount("feature");
  pl.feature = { onDone, card };
  pl.visible = true;
  reset();
  const sheet = KINDS.feature.sheet();
  sheet.returnValue = "";
  if (!sheet.open) sheet.showModal();
  const [drafts, projects] = await Promise.all([call("GET", plansAPI()), pl.projects ? pl.projects : call("GET", "/api/planning/projects")]);
  pl.drafts = drafts;
  pl.projects = projects;
  const draft = card && drafts.find((p) => p.card_id === card.id);
  if (draft) return openPlan(draft.id);
  newFeature({ card, project: card?.project_id || project, idea: idea.slice(0, 2000) || card?.description?.trim() || card?.title || "" });
}

// newFeature opens a feature brief not saved yet: it is made with the first
// save (saveIdea), with what the user changed of these.
function newFeature({ card = null, project = "", idea = "" }) {
  reset();
  const name = card?.title?.slice(0, 100) || "";
  pl.cur = { id: "", kind: "feature", name, idea, project_id: project || (pl.projects?.length === 1 ? pl.projects[0].id : ""), card_id: card?.id || "",
    card_title: card?.title || "", rounds: [], topics: [], brief: "", open: [], verdict: {}, step: "idea", from: { name, idea } };
  render();
  focus();
}

KINDS.feature.sheet().addEventListener("close", () => {
  if (pl.kind !== "feature") return;
  pl.visible = false;
  flushBrief().catch(fail);
  reset();
});

const projectName = (id) => (pl.projects || []).find((p) => p.id === id)?.name || "a project";

// ---- drafts

async function loadDrafts() {
  pl.drafts = await call("GET", plansAPI());
  render();
}

function renderHome() {
  const list = $("#plan-drafts");
  const drafts = (pl.drafts || []).filter((p) => kindOf(p) === pl.kind);
  $("#plan-drafts-head").hidden = !drafts.length;
  list.replaceChildren(...drafts.map((p) => h("li", { class: "plan-draft" },
    h("button", { type: "button", class: "plan-draft-open", onclick: attempt(() => openPlan(p.id)) },
      h("span", { class: "plan-draft-title" }, p.name || p.idea || "Untitled brief"),
      h("span", { class: "plan-draft-meta" }, [
        pl.kind === "feature" ? `Feature · ${projectName(p.project_id)}` : "",
        steps()[Math.max(0, stepIndex(p.step))].title, plural(p.rounds?.length || 0, "round"), dateTimeText(p.updated_at),
      ].filter(Boolean).join(" · "))),
    ib({ type: "button", class: "icon-btn", "aria-label": `Delete ${p.name || "brief"}`, title: "Delete", onclick: attempt(() => deleteDraft(p)) }, "trash"))));
}

async function deleteDraft(p) {
  if (!await ask({ title: `Delete ${p.name || "this brief"}?`, body: "Its interview and brief go with it.", ok: "Delete", danger: true })) return;
  await call("DELETE", plansAPI(p.id));
  store.remove(`dash.planIdea.${kindOf(p)}.${p.id}`);
  pl.drafts = (pl.drafts || []).filter((x) => x.id !== p.id);
  render();
  $("#plan-new").focus();
}

async function openPlan(id) {
  const p = await call("GET", plansAPI(id));
  reset();
  pl.cur = p;
  // The repository isn't kept with the plan: picked again after a reload.
  pl.step = ["repo", "launch"].includes(p.step) ? "repo" : stepIndex(p.step) >= 0 ? p.step : "idea";
  if (pl.step !== "idea" && !p.rounds.length && !p.brief) pl.step = "idea";
  render();
  focus();
}

function newProject() {
  reset();
  pl.cur = { id: "", name: "", idea: "", rounds: [], topics: [], brief: "", open: [], verdict: {}, step: "idea" };
  render();
  focus();
}

$("#plan-new").addEventListener("click", () => {
  if (pl.kind === "feature") return newFeature({ project: pl.cur?.project_id });
  newProject();
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
  return {
    idea: true, interview: !!p.id && !!p.idea, brief: !!p.brief, repo: !!p.brief, launch: !!p.brief && !!pl.repo, start: !!p.brief,
  }[steps()[i]?.key] ?? false;
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
  if (pl.step === "idea" && pl.kind === "feature" && !pl.cur.id && !pl.cur.project_id) return $("#plan-project").focus();
  const pane = $(`.plan-pane[data-step="${pl.step}"]`);
  const el = {
    idea: () => $("#plan-idea"),
    interview: () => $(".plan-q-card .plan-chip", pane) || $("#plan-say") || $(".plan-error .btn", pane),
    brief: () => pl.briefView === "edit" ? $("#plan-brief") : $('[data-brief="preview"]'),
    repo: () => $(".plan-source", pane),
    launch: () => $("#plan-next"),
    start: () => $("input[name=plan-start]:checked", pane),
  }[pl.step]?.();
  (el || $(".plan-stepper [aria-current=step]"))?.focus({ preventScroll: pl.step === "interview" });
}

function render() {
  const home = !pl.cur;
  if (pl.kind === "project") $(`#${SHEET}`).classList.toggle("np-planning", !home);
  $("#plan-home").hidden = !home;
  $("#plan-work").hidden = home;
  if (home) renderHome();
  else {
    renderStepper();
    for (const pane of $$(".plan-pane")) pane.hidden = pane.dataset.step !== pl.step;
    ({ idea: renderIdea, interview: renderInterview, brief: renderBrief, repo: renderRepo, launch: renderLaunch, start: renderStart })[pl.step]?.();
  }
  renderFooter();
}

function renderStepper() {
  const cur = stepIndex(pl.step);
  const STEPS = steps();
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
  if (pl.kind === "project") $("#new-project-go").hidden = pl.visible;
  if (!on) return;
  const p = pl.cur;
  const [icon, label, ok] = {
    idea: ["spark", p.rounds.length ? "Continue the interview" : "Start the interview", !pl.thinking],
    interview: ["right", "Review the brief", !!p.brief && !pl.thinking],
    brief: ["right", KINDS[pl.kind].briefNext, !!p.brief],
    repo: ["right", "Continue", !!pl.repo],
    launch: ["rocket", "Launch", !!pl.launch && !!pl.repo],
    start: { task: ["play", "Start the task"], goal: ["target", "Start the goal"], planned: ["check", "Save to Planned"] }[pl.start].concat(!!p.brief),
  }[pl.step];
  setLabel(next, icon, label);
  next.disabled = !ok;
}

$("#plan-back").addEventListener("click", attempt(() => go(steps()[Math.max(0, stepIndex(pl.step) - 1)].key)));
$("#plan-next").addEventListener("click", attempt(async (e) => {
  const btn = e.currentTarget;
  if (pl.step === "idea") return busy(btn, startInterview);
  if (pl.step === "launch") return busy(btn, launch);
  if (pl.step === "start") return busy(btn, startFeature);
  await go(steps()[stepIndex(pl.step) + 1].key);
}));

// ---- 1. the idea

let whereText = null;
// ideaFor is the plan the idea's fields were filled in from: they are filled
// in again only for another plan (or the same one saved), so a render while
// the user types (the drafts arriving, say) keeps what they typed.
let ideaFor = null;
function renderIdea() {
  const f = { idea: $("#plan-idea"), name: $("#plan-name"), project: $("#plan-project") };
  if (ideaFor !== pl.cur) {
    // What the user typed and didn't save (the sheet closed on it) wins,
    // except over an idea a new brief was opened with.
    const typed = typedIdea(), fresh = !pl.cur.id && !pl.cur.card_id;
    const use = typed && (!fresh || !pl.cur.idea);
    f.idea.value = use ? typed.idea : pl.cur.idea;
    f.name.value = use ? typed.name : pl.cur.name;
    ideaFor = pl.cur;
  }
  if (pl.kind === "feature") {
    // The project is the plan's from its first save.
    const cur = pl.cur.project_id;
    f.project.replaceChildren(
      ...(cur ? [] : [h("option", { value: "" }, pl.projects?.length ? "Choose a project" : "No projects to plan for")]),
      ...(pl.projects || []).map((p) => h("option", { value: p.id, selected: p.id === cur }, p.name)));
    f.project.disabled = !!pl.cur.id;
    const card = pl.cur.card_id ? (pl.cur.card_title || pl.feature?.card?.title || "its card") : "";
    $("#plan-card").textContent = card ? `The brief goes onto the card “${card}”.` : "The brief goes onto a new card in Planning.";
  }
  if (whereText === null) {
    whereText = "";
    // Like the other Claude calls without a project: the first of the user's runners online.
    api("GET", "/api/claude", null, "").then((c) => {
      const r = (c.runners || []).find((x) => x.online);
      whereText = c.local ? "" : r ? `Claude interviews you on your runner ${r.name}.` : "None of your runners is online: start `dash runner` on your computer to write the brief with Claude.";
      $("#plan-where").textContent = whereText;
      $("#plan-where").classList.toggle("warn", !r && !c.local);
    }).catch(() => { whereText = null; });
  }
  $("#plan-where").textContent = whereText || "";
}

// The idea and name the user types are kept in localStorage until they are
// saved, so closing the sheet (Esc, say) doesn't lose them: one draft each
// for a plan, a card, and a new brief of each kind.
const typedKey = () => `dash.planIdea.${pl.kind}.${pl.cur?.id || (pl.cur?.card_id ? `card.${pl.cur.card_id}` : "new")}`;
function typedIdea() {
  const t = store.getJSON(pl.cur ? typedKey() : `dash.planIdea.${pl.kind}.new`);
  return t && typeof t.idea === "string" && t.idea.trim() ? { idea: t.idea, name: String(t.name || "") } : null;
}
function keepTyped() {
  if (!pl.cur || pl.step !== "idea") return;
  const idea = $("#plan-idea").value, name = $("#plan-name").value;
  const saved = idea.trim() === (pl.cur.idea || "").trim() && name.trim() === (pl.cur.name || "").trim();
  if (saved || !idea.trim()) store.remove(typedKey());
  else store.setJSON(typedKey(), { idea, name });
}

// saveIdea saves the idea and name: the plan is made with the first save.
// A feature's is made for its project and card (a new card if none): what
// the user didn't change of the card's words, the server takes from the card.
async function saveIdea() {
  const idea = $("#plan-idea").value.trim(), name = $("#plan-name").value.trim();
  const key = typedKey();
  if (!idea) {
    $("#plan-idea").focus();
    throw new Error("Describe the idea first: a sentence or two is enough.");
  }
  if (!pl.cur.id && pl.kind === "feature") {
    const c = pl.cur, project = $("#plan-project").value;
    if (!project) {
      $("#plan-project").focus();
      throw new Error("Choose the project the feature is for.");
    }
    const body = { kind: "feature", project, step: "interview" };
    if (c.card_id) body.card = c.card_id;
    if (!c.card_id || idea !== c.from.idea.trim()) body.idea = idea;
    if (!c.card_id || name !== c.from.name.trim()) body.name = name;
    pl.cur = await call("POST", plansAPI(), body);
  } else if (!pl.cur.id) pl.cur = await call("POST", plansAPI(), { idea, name, step: "interview" });
  else if (idea !== pl.cur.idea || name !== pl.cur.name) pl.cur = { ...await call("PATCH", plansAPI(pl.cur.id), { idea, name }) };
  store.remove(key);
}

async function startInterview() {
  await saveIdea();
  pl.drafts = null; // listed again next time
  await go("interview");
  if (!pl.cur.rounds.length) await interview({});
}

for (const el of ["#plan-idea", "#plan-name"]) {
  $(el).addEventListener("input", keepTyped);
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
  const main = KINDS[pl.kind].main();
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
    onclick: attempt(async () => (await projects()).planSource(b.dataset.rail, pl.cur.name || briefTitle(pl.cur.brief))),
  }, h("span", { class: "plan-source-name" }, b.firstElementChild.textContent), h("span", { class: "plan-source-desc" }, b.lastElementChild.textContent))));
  const picked = $("#plan-repo");
  picked.hidden = !pl.repo;
  if (pl.repo) picked.replaceChildren(ico("repo"), h("span", {}, pl.repo.what, pl.repo.runner ? ` · on ${pl.repo.runner}` : ""), h("em", {}, "for the brief"));
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
  if (!(await projects()).startAdd({ plan: pl.cur, mode: pl.repo.mode, launch: pl.launch })) {
    throw new Error("The repository picked for the brief isn't ready any more: choose it again.");
  }
}

// ---- 4. a feature's Start

function renderStart() {
  const p = pl.cur;
  const row = (k, val) => [h("dt", {}, k), h("dd", {}, val)];
  $("#plan-start-summary").replaceChildren(
    ...row("Project", projectName(p.project_id)),
    ...row("Card", p.card_id ? (pl.feature?.card?.title || p.name || "its card") : "a new card, in Ideas"));
  for (const r of $$("input[name=plan-start]")) r.checked = r.value === pl.start;
}
for (const r of $$("input[name=plan-start]")) {
  r.addEventListener("change", () => { if (r.checked) { pl.start = r.value; renderFooter(); } });
}

// startFeature puts the brief on the card and starts what was chosen.
async function startFeature() {
  await flushBrief();
  const v = await call("POST", plansAPI(pl.cur.id, "/feature"), { start: pl.start });
  toast({ task: "Task queued and linked to the card", goal: "Goal started and linked to the card", planned: "The brief is on the card, in Planned" }[pl.start], { tone: "ok" });
  const done = pl.feature?.onDone;
  pl.drafts = null;
  KINDS.feature.sheet().close("done");
  done?.(v.card);
}
