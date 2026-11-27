import { $, dateTimeText, docsLink, fill, h, ico, plural } from "./dom.js";
import { api } from "./api.js";
import { attempt, busy, toast } from "./dialogs.js";
import { copyBlock } from "./prefs.js";
import { state } from "./state.js";

// Admin → Incident response (platform admins): after a breach, such as a
// leaked database, end every way in at once. A checkbox per step, a summary
// of what will happen, and LOCKDOWN typed to confirm; then what is left to
// do outside Dash. dash lockdown on the server does the same (docs/breach.md).
// The admin who runs it stays signed in here.

const root = () => $("#admin-lockdown");
let poll = 0; // the notice's emails, while they go out

export async function loadLockdown() {
  const st = await api("GET", "/api/admin/lockdown", undefined, "");
  renderForm(st);
}

// step is one checkbox with what it does.
function step(name, label, text, on = true) {
  const box = h("input", { type: "checkbox", name, checked: on });
  return { box, el: h("label", { class: "lockdown-step" }, box, h("span", {}, h("strong", {}, label), h("span", { class: "hint" }, text))) };
}

function renderForm(st) {
  const p = st.preview;
  const steps = {
    sessions: step("sessions", "Sign out everyone",
      `Ends ${plural(p.sessions, "session")} (yours here stays), revokes ${plural(p.api_tokens, "API token")} and connected apps and ${plural(p.runners, "runner")} (they register again), and cancels ${plural(p.invitations, "pending invitation")}, every email link and ${plural(p.wall_links, "wall link")}.`),
    passwords: step("passwords", "Ask for new passwords",
      `${plural(p.passwords, "account")} must choose a new password at their next sign-in before they can do anything else. Accounts that sign in only with Google aren't asked.`),
    totp: step("totp", "Reset two-factor sign-in",
      `${plural(p.totp, "account")} set up two-factor sign-in again at their next sign-in.`),
    secrets: step("secrets", "Rotate secrets",
      `New secrets for ${plural(p.webhooks, "webhook")}, ${plural(p.github_hooks, "GitHub webhook")} and ${plural(p.trackers, "board backend")}, and a new push key (${plural(p.push_subscriptions, "browser")} turn notifications on again). Webhook tokens and board credentials are cleared. You get a checklist of what to update outside Dash.`),
    notice: step("notice", "Tell everyone", "A message, by email and after sign-in."),
  };
  const text = h("textarea", { name: "text", rows: 5, maxlength: 10000, placeholder: "What happened, and what people need to do: choose a new password at their next sign-in, set up two-factor sign-in again…" });
  const email = h("input", { type: "checkbox", name: "email", checked: st.mail, disabled: !st.mail });
  const banner = h("input", { type: "checkbox", name: "banner", checked: true });
  const csv = h("a", { href: "/api/admin/lockdown/users.csv", download: "dash-users.csv" }, "Download names and email addresses (CSV)");
  const noticeBox = h("div", { class: "lockdown-notice" },
    h("label", { class: "field" }, h("span", {}, "Message"), text),
    h("label", { class: "org-require-2fa" }, email, st.mail ? ` Email it to ${plural(p.emails, "person", "people")}` : " Email it"),
    st.mail ? null : h("p", { class: "hint" }, "This Dash doesn't send email (no SMTP set up), so nobody is emailed. Send the message yourself: ", csv, "."),
    h("label", { class: "org-require-2fa" }, banner, " Show it after sign-in, until each person dismisses it"));

  const summary = h("ul", { class: "lockdown-summary", "aria-live": "polite" });
  const confirm = h("input", { name: "confirm", autocomplete: "off", spellcheck: "false", autocapitalize: "characters", placeholder: st.confirm });
  const noPassword = state.me?.has_password === false; // a recent Google sign-in confirms
  const pw = h("input", { type: "password", name: "password", autocomplete: "current-password" });
  const run = h("button", { class: "btn danger", type: "submit" }, ico("lock"), "Run incident response");
  const error = h("p", { class: "lockdown-error", role: "alert" });

  const refresh = () => {
    noticeBox.hidden = !steps.notice.box.checked;
    const lines = [];
    if (steps.sessions.box.checked) lines.push("Everyone else is signed out; tokens, runners, invitations and links stop working.");
    if (steps.passwords.box.checked) lines.push(`${plural(p.passwords, "account")} choose a new password at their next sign-in.`);
    if (steps.totp.box.checked) lines.push(`${plural(p.totp, "account")} set up two-factor sign-in again.`);
    if (steps.secrets.box.checked) lines.push("Dash's stored secrets are replaced: update the receivers from the checklist you get next.");
    if (steps.notice.box.checked) {
      if (email.checked) lines.push(`Your message is emailed to ${plural(p.emails, "person", "people")}, a few a second.`);
      if (banner.checked) lines.push("Your message shows after sign-in until dismissed.");
    }
    if (steps.passwords.box.checked || steps.totp.box.checked) lines.push(`You (${state.me?.username}) stay signed in here, and keep your password and two-factor sign-in: the checklist says how to change them.`);
    fill(summary, lines.length ? lines.map((l) => h("li", {}, l)) : h("li", {}, "Nothing: choose a step."));
  };
  for (const el of [...Object.values(steps).map((s) => s.box), email, banner]) el.addEventListener("change", refresh);
  refresh();

  const form = h("form", { class: "lockdown-form", novalidate: true },
    h("fieldset", { class: "lockdown-steps" }, h("legend", {}, "Steps"), ...Object.values(steps).map((s) => s.el), noticeBox),
    h("h4", {}, "What will happen"), summary,
    h("label", { class: "field" }, h("span", {}, `Type ${st.confirm} to confirm`), confirm),
    noPassword ? null : h("label", { class: "field" }, h("span", {}, "Your password"), pw),
    error, run);
  form.addEventListener("submit", attempt(async (e) => {
    e.preventDefault();
    error.textContent = "";
    const tell = steps.notice.box.checked;
    const body = {
      sessions: steps.sessions.box.checked, passwords: steps.passwords.box.checked, totp: steps.totp.box.checked, secrets: steps.secrets.box.checked,
      notice: tell ? text.value.trim() : "", email: tell && email.checked, banner: tell && banner.checked,
      confirm: confirm.value.trim(), password: noPassword ? "" : pw.value,
    };
    if (tell && (body.email || body.banner) && !body.notice) { error.textContent = "Write the message, or turn off “Tell everyone”."; text.focus(); return; }
    if (body.confirm !== st.confirm) { error.textContent = `Type ${st.confirm} to confirm.`; confirm.focus(); return; }
    try {
      const res = await busy(run, () => api("POST", "/api/admin/lockdown", body, ""), { label: "Running…" });
      if (res) renderResult(res);
    } catch (err) {
      error.textContent = err.message;
    }
  }));

  fill(root(),
    h("h3", {}, "Incident response"),
    h("p", { class: "hint" }, "After a breach, such as a leaked database, end every way in at once instead of going through each account. ",
      "On the server, ", h("code", {}, "dash lockdown"), " does the same, even with the server stopped. ", docsLink("breach.md")),
    currentNotice(st),
    emailsStatus(st.emails),
    form,
    history(st.events || []));
}

// history is the audit log's entries about incident responses.
function history(events) {
  if (!events.length) return null;
  return h("details", { class: "lockdown-history" },
    h("summary", {}, "Earlier incident responses"),
    h("ul", {}, events.map((e) => h("li", {},
      h("span", { class: "hint" }, `${dateTimeText(e.at)} · ${e.actor || "on the server"} · `),
      `${e.kind.replace(/^admin\.(lockdown|notice)\./, "")}${e.detail ? `: ${e.detail}` : ""}`))));
}

// currentNotice is the message shown after sign-in now, with Clear.
function currentNotice(st) {
  if (!st.notice) return null;
  const n = st.notice;
  return h("div", { class: "lockdown-current" },
    h("p", {}, h("strong", {}, "Shown after sign-in now"), ` (since ${dateTimeText(n.created)}${n.by ? `, by ${n.by}` : ""}):`),
    h("blockquote", {}, n.body),
    h("button", { class: "btn small", type: "button", onclick: attempt(async () => {
      await api("DELETE", "/api/admin/notice", undefined, "");
      toast("The message no longer shows", { tone: "ok" });
      await loadLockdown();
    }) }, ico("x"), "Clear it"));
}

// emailsStatus is how the message's emails go, refreshed until done.
function emailsStatus(e) {
  clearTimeout(poll);
  if (!e) return null;
  if (e.unavailable) return h("p", { class: "lockdown-emails" }, "Nobody was emailed: this Dash doesn't send email. ",
    h("a", { href: "/api/admin/lockdown/users.csv", download: "dash-users.csv" }, "Download names and email addresses (CSV)"), " and send the message yourself.");
  const el = h("p", { class: "lockdown-emails", role: "status" },
    e.done ? `Emails sent: ${e.sent} of ${e.total}${e.failed ? `, ${e.failed} failed (see the server log)` : ""}.`
      : `Emailing the message: ${e.sent} sent${e.failed ? `, ${e.failed} failed` : ""} of ${e.total}…`);
  if (!e.done) {
    poll = setTimeout(async () => {
      if (!el.isConnected) return;
      try {
        const st = await api("GET", "/api/admin/lockdown", undefined, "");
        el.replaceWith(emailsStatus(st.emails) || "");
      } catch {}
    }, 2000);
  }
  return el;
}

function renderResult(res) {
  const r = res.result;
  const done = [];
  if (r.steps.sessions) done.push(`Signed out: ${plural(r.sessions, "session")}, ${plural(r.api_tokens, "token")}, ${plural(r.runners.length, "runner")}, ${plural(r.invitations, "invitation")}, ${plural(r.email_links, "email link")}, ${plural(r.wall_links, "wall link")}.`);
  if (r.steps.passwords) done.push(`${plural(r.passwords, "account")} choose a new password at their next sign-in.`);
  if (r.steps.totp) done.push(`${plural(r.totp, "account")} set up two-factor sign-in again at their next sign-in.`);
  if (r.steps.secrets) done.push(`New secrets for ${plural(r.webhooks.length, "webhook")}, ${plural(r.github_hooks.length, "GitHub webhook")} and ${plural(r.trackers.length, "board backend")}; a new push key.`);
  if (res.notice) done.push("Your message shows after sign-in.");
  const list = res.checklist || [];
  const all = list.map((c, i) => `${i + 1}. ${c.text}${c.secret ? `\n   ${c.secret}` : ""}`).join("\n");
  fill(root(),
    h("h3", { tabindex: "-1" }, "Incident response done"),
    done.length ? h("ul", { class: "lockdown-summary" }, done.map((d) => h("li", {}, d))) : null,
    emailsStatus(res.emails),
    list.length ? [
      h("h4", {}, "Still to do outside Dash"),
      h("p", { class: "hint" }, "New secrets show only now: copy them before you leave this page."),
      h("ol", { class: "lockdown-checklist" }, list.map((c) => h("li", {}, h("span", {}, c.text), c.secret ? copyBlock(c.secret, "Secret copied") : null))),
      h("button", { class: "btn small", type: "button", onclick: () => navigator.clipboard?.writeText(all).then(() => toast("Checklist copied", { tone: "ok" })) }, ico("copy"), "Copy the checklist"),
    ] : h("p", {}, "Nothing is left to do outside Dash."),
    h("p", {}, h("button", { class: "btn", type: "button", onclick: attempt(loadLockdown) }, "Back to incident response")));
  root().querySelector("h3").focus();
}
