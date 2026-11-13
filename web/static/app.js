// Dash web client. REST for state, /ws/events for live updates,
// /ws/chat for streaming answers, /ws/proc/:id for terminals.
//
// This file boots the app; the rest is in app/, one module per part of it,
// all imported here. Shared state is in app/state.js, DOM helpers in
// app/dom.js, the REST client in app/api.js and toasts and dialogs in
// app/dialogs.js. Modules import each other in cycles, so a module's
// top-level code can run before a module it imports has: what it uses as it
// loads should be in those four or be function declarations (hoisted), not
// other modules' constants. In event handlers, anything may be used.

import { $ } from "./app/dom.js";
import { desktop } from "./app/viewport.js";
import "./app/api.js";
import { fail } from "./app/dialogs.js";
import "./app/attachments.js";
import "./app/todoform.js";
import { state } from "./app/state.js";
import "./app/nav.js";
import { connectAll } from "./app/events.js";
import "./app/projects.js";
import "./app/board.js";
import { renderBoard } from "./app/branches.js";
import "./app/activity.js";
import "./app/todos.js";
import "./app/detail.js";
import "./app/inlineedit.js";
import "./app/preview.js";
import "./app/drawmode.js";
import "./app/diffedit.js";
import "./app/accountsheet.js";
import "./app/prefs.js";
import "./app/runners.js";
import "./app/account.js";
import "./app/billing.js";
import "./app/tokens.js";
import "./app/wallview.js";
import "./app/notifications.js";
import "./app/review.js";
import "./app/runstats.js";
import { loadUsage, renderUsage } from "./app/usage.js";
import "./app/actions.js";
import "./app/origin.js";
import "./app/settings.js";
import "./app/claudefiles.js";
import "./app/webhooks.js";
import "./app/sharednotes.js";
import "./app/backend.js";
import { chatRunner } from "./app/chat.js";
import "./app/chatpane.js";
import "./app/timeline.js";
import "./app/terminal.js";
import "./app/try.js";
import "./app/history.js";
import "./app/releases.js";
import "./app/files.js";
import "./app/dnd.js";
import "./app/hovercard.js";
import "./app/issues.js";
import "./app/scans.js";
import "./app/checklist.js";
import { setMode } from "./app/vim.js";
import "./app/palette.js";
import "./app/updates.js";

// ------------------------------------------------------------------ boot
desktop.addEventListener("change", () => { if (state.detail) $("#scrim").hidden = !desktop.matches; });

renderBoard();
setMode();
setInterval(() => { if (!document.hidden) renderBoard(); }, 30000); // keep "4m"/"2h" current
// Keep the usage pace current; on a hub that doesn't run Claude, fetch your
// runner's latest report too (runners report without an event).
setInterval(() => {
  if (document.hidden) return;
  if (chatRunner.info?.local === false) loadUsage().catch(() => renderUsage());
  else renderUsage();
}, 60000);
connectAll().catch(fail);
// Pick up computers that were offline at start (or came back) every minute.
setInterval(() => { if (!document.hidden) connectAll().catch(() => {}); }, 60000);
