import { $ } from "./dom.js";
import { state } from "./state.js";
import { fail } from "./dialogs.js";
import { loadPlanning, mountPlanning, openCard, planningShown } from "./planning.js";
import { openFeatureBrief } from "./planner.js";
import { selectProject } from "./projects.js";
import { detailBackground, openDetail } from "./detail.js";
import { showGoal } from "./goals.js";
import { showView } from "./nav.js";
import { projectChatFor, toggleChatPane } from "./chatpane.js";
import { chat } from "./chat.js";

// The app's Planning view (members, admins and owners): app/planning.js in
// #view-planning, opening a card's linked task or goal on its project's
// board. (Planners get planning.html, with the same module.)

mountPlanning($("#view-planning"), {
  paneParent: $(".app"),
  hooks: {
    visible: () => state.view === "planning",
    background: () => detailBackground(),
    me: () => (state.me?.user ? { id: state.me.user.id, admin: ["owner", "admin"].includes(state.me.org?.role) } : null),
    openWork: (l) => openWork(l).catch(fail),
    askChat: (c) => askAboutCard(c).catch(fail),
    // The brief lands on the card, which Planning shows open.
    featureBrief: ({ card, project, idea }) => openFeatureBrief({ card, project, idea,
      onDone: (c) => loadPlanning().then(() => openCard(c.id)).catch(fail) }).catch(fail),
  },
});
// A reload that lands on Planning (the saved view) showed it in nav.js
// before it was mounted here, when loading it did nothing: load it now.
if (state.view === "planning") planningShown();

async function openWork(l) {
  const p = (state.projects || []).find((x) => x.id === l.project_id && !x.prefix);
  if (!p) throw new Error(`The ${l.kind}'s project isn't on this dashboard.`);
  if (state.project?.id !== p.id || state.project?.prefix) await selectProject(p);
  if (l.kind === "goal") await showGoal(l.id);
  else { showView("board"); await openDetail(l.id); }
}

// askAboutCard opens the chat about card c: its project's chat (the project
// is picked, Planning's filter stays as it is), or the general chat for a card
// of no project. The message box starts with the card's title and id, for
// Claude to read it with get_card; nothing is sent until you add your question.
async function askAboutCard(c) {
  let target = chat, tab = "general";
  if (c.project_id) {
    const p = (state.projects || []).find((x) => x.id === c.project_id && !x.prefix);
    if (!p) throw new Error("The card's project isn't on this dashboard.");
    if (state.project?.id !== p.id || state.project?.prefix) await selectProject(p, { follow: false });
    target = projectChatFor(p).chat;
    tab = "project";
  }
  toggleChatPane(true, tab);
  const input = target.input;
  const about = `About the planning card "${c.title}" (card ${c.id}): `;
  if (!input.value.includes(`(card ${c.id})`)) input.value = about + input.value;
  input.dispatchEvent(new Event("input"));
  input.focus();
  input.setSelectionRange(input.value.length, input.value.length);
}
