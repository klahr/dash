import { $, dateText, h, ico, plural } from "./dom.js";
import { api } from "./api.js";
import { ask, attempt, railOf, toast } from "./dialogs.js";
import { state } from "./state.js";
import { href } from "./routes.js";
import { askPassword } from "./myaccount.js";
import { orgSection } from "./accountsheet.js";
import { agoText } from "./branches.js";
import { servesProject } from "../groups.js";

// ------------------------------------------------------------------ set up your team
// Organization → Set up your team: what a team needs before everyone can
// work (a name, people, two-factor sign-in, runners, project labels,
// approved commands), each item with a button to where it is set, which is
// spread over Organization, Runners and Project settings. Owners and
// admins see it; the sheet opens on it until every item is done.

// goTo shows a place: a section of the Organization sheet, Runners, or a
// project's settings at a section (p: the project; the open one without).
async function goTo(sheet, section, p = state.project) {
  if (sheet === "org") { railOf("org-sheet").show(section); return; }
  if (sheet === "settings" && !p) throw new Error("Add a project first.");
  if (p) {
    // router.js imports account.js, which imports this: load it when needed.
    const { go } = await import("./router.js");
    const view = p === state.project && state.view !== "planning" ? state.view : "board";
    await go(href({ view, sheet, section }, p));
    return;
  }
  const { openRunners } = await import("./runners.js");
  $("#org-sheet").close();
  await openRunners();
}

const names = (list) => list.join(", ");
const button = (label, onclick) => h("button", { class: "btn small", type: "button", onclick: attempt(onclick) }, label);

// step is one item: done true (a check), false (to do) or null (nothing to
// check, as for tokens).
function step(done, title, detail, action) {
  const mark = done === true ? "Done" : done === false ? "To do" : "";
  return h("li", { class: `team-step${done === true ? " done" : done === false ? " todo" : ""}` },
    h("span", { class: "team-step-mark", role: mark ? "img" : null, "aria-label": mark || null, "aria-hidden": mark ? null : "true" },
      ico(done === true ? "check" : "circle")),
    h("div", { class: "team-step-text" }, h("strong", {}, title), detail ? h("p", { class: "hint" }, detail) : null),
    action);
}

// unapprovedCommands are projects with commands a member set that wait for
// an admin's approval before runners run them (settings.js: unapproved).
const unapprovedCommands = (p, me) => Object.values(p.commands_by || {}).some((by) => !by.admin && by.user !== me);

// renderTeamSetup draws the checklist from the members list (GET
// /api/orgs/{id}/members) and the viewer's role, and says whether every
// item is done. It is hidden in a personal workspace and for members.
export function renderTeamSetup(org, mine) {
  const box = $("#team-setup");
  const manage = mine === "owner" || mine === "admin";
  box.hidden = !manage || !!org.org.personal;
  if (box.hidden) { $("#team-setup-list").replaceChildren(); return true; }
  const me = state.me?.user?.id;
  const members = org.members || [];
  const steps = [];
  const done = [];
  const add = (ok, ...rest) => { if (ok !== null) done.push(ok); steps.push(step(ok, ...rest)); };

  add(true, "Name the organization", `It's called ${org.org.name}. Owners and admins can rename it under Members.` +
    (org.url ? " Its address is the owners' to change, under Sign-in." : ""),
  button("Rename", () => { railOf("org-sheet").show("members"); $("#org-name").focus(); $("#org-name").select(); }));

  const pending = (org.invitations || []).length;
  add(members.length > 1, "Invite your team",
    `${plural(members.length, "member")}${pending ? `, ${plural(pending, "invitation")} waiting` : ""}. Developers join as members or admins, product owners as planners.`,
    button("Invite", () => { railOf("org-sheet").show("members"); $("#org-invite-email").focus(); }));

  const without2FA = members.filter((m) => !m.totp_enabled).map((m) => m.username);
  add(!!org.org.require_2fa, "Require two-factor sign-in",
    (org.org.require_2fa ? "Required of every member." : mine === "owner" ? "Not required yet." : "Not required yet: an owner turns it on.") +
      (without2FA.length ? ` Not set up yet: ${names(without2FA)}.` : " Every member has it."),
    button("Sign-in", () => goTo("org", "signin")));

  // On a hub: runners and labels. A runner does only its owner's tasks,
  // unless it takes other members' (a shared runner, on an API key).
  if (Array.isArray(state.runners)) {
    const runners = state.runners;
    const devs = members.filter((m) => m.role !== "planner");
    const shared = runners.filter((r) => r.takes_others);
    const none = devs.filter((m) => !runners.some((r) => r.owner?.user_id === m.id)).map((m) => m.username);
    add(runners.length > 0 && (shared.length > 0 || !none.length), "A runner for each developer, or a shared runner",
      !runners.length ? "No runners yet. Each developer adds their own, or one runner with an API key takes everyone's tasks."
        : shared.length ? `${names(shared.map((r) => r.name))} ${shared.length === 1 ? "takes" : "take"} everyone's tasks.` +
          (none.length ? ` Without a runner of their own (no chats or terminals): ${names(none)}.` : "")
          : none.length ? `No runner yet: ${names(none)}. Their tasks wait.` : "Every developer has a runner.",
      button("Runners", () => goTo("runners")));

    const projects = (state.projects || []).filter((p) => !p.prefix);
    const unserved = projects.filter((p) => !runners.some((r) => servesProject(r, p)));
    const target = unserved[0] || state.project;
    add(projects.length > 0 && !unserved.length, "Label projects for their runners",
      !projects.length ? "No projects yet." : unserved.length
        ? `No runner has the labels of ${names(unserved.map((p) => p.name))}: their work waits.`
        : "Every project has a runner with its labels.",
      target && !target.prefix ? button(unserved[0] ? `${target.name}: runners` : "Project settings", () => goTo("settings", "runners", target)) : null);
  }

  const waiting = (state.projects || []).filter((p) => !p.prefix && unapprovedCommands(p, me));
  const cmdTarget = waiting[0] || state.project;
  add(!waiting.length, "Approve members' commands",
    waiting.length ? `Commands members changed wait for an admin in ${names(waiting.map((p) => p.name))}: runners don't run them until then.`
      : "Commands a member sets (test, setup, run, deploy) wait for an admin's approval. None wait now.",
    cmdTarget && !cmdTarget.prefix ? button(waiting[0] ? `${cmdTarget.name}: commands` : "Project settings", () => goTo("settings", "commands", cmdTarget)) : null);

  add(null, "Review members' tokens", "API tokens and connected apps sign in as a member until revoked.",
    button("Tokens", () => goTo("org", "tokens")));

  $("#team-setup-list").replaceChildren(...steps);
  return done.every(Boolean);
}

// ------------------------------------------------------------------ members' tokens
// Organization → Members' tokens (owners and admins): every member's API
// tokens and connected apps in this org, revocable. A member's own list is
// Account → Tokens & apps (tokens.js); runners are in Runners.
orgSection("tokens", loadOrgTokens);
const RANK = { planner: 0, member: 1, admin: 2, owner: 3 };
async function loadOrgTokens() {
  const area = $("#org-tokens-area");
  const org = state.me?.org;
  const mine = org?.role;
  area.hidden = !org || org.personal || RANK[mine] < RANK.admin;
  if (area.hidden) return;
  const list = await api("GET", `/api/orgs/${org.id}/tokens`, null, "");
  const rows = list.map((t) => h("li", { class: "item-row" },
    h("span", { class: "runner-name" }, t.name, t.kind === "api" ? h("code", { class: "hint" }, ` ${t.prefix}…`) : null),
    h("span", { class: "item-sub hint" }, [
      t.username + (t.user_id === state.me.user.id ? " (you)" : ""),
      t.kind === "oauth" ? "connected app" : "API token",
      t.scopes.includes("write") ? "read and write" : "read only",
      t.last_used ? `used ${agoText(t.last_used)}` : "never used",
      `created ${dateText(t.created)}`,
    ].join(" · ")),
    RANK[t.role] <= RANK[mine] ? h("button", { class: "btn small danger", type: "button", onclick: attempt(async () => {
      if (!await ask({ title: `Revoke ${t.username}'s ${t.name}?`, body: t.kind === "oauth"
        ? "The app is signed out at once and has to be connected again." : "Anything using this token loses access at once.", ok: "Revoke", danger: true })) return;
      const pw = await askPassword(`Revoke ${t.username}'s ${t.name}?`);
      if (pw == null) return;
      await api("DELETE", `/api/orgs/${org.id}/tokens/${t.id}`, { password: pw }, "");
      toast(`${t.name} revoked`);
      await loadOrgTokens();
    }) }, "Revoke…") : null));
  $("#org-tokens").replaceChildren(...(rows.length ? rows : [h("li", { class: "hint" }, "No member has an API token or a connected app here.")]));
}
