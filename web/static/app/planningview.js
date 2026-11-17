import { $ } from "./dom.js";
import { state } from "./state.js";
import { fail } from "./dialogs.js";
import { mountPlanning } from "./planning.js";
import { selectProject } from "./projects.js";
import { detailBackground, openDetail } from "./detail.js";
import { showGoal } from "./goals.js";
import { showView } from "./nav.js";

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
  },
});

async function openWork(l) {
  const p = (state.projects || []).find((x) => x.id === l.project_id && !x.prefix);
  if (!p) throw new Error(`The ${l.kind}'s project isn't on this dashboard.`);
  if (state.project?.id !== p.id || state.project?.prefix) await selectProject(p);
  if (l.kind === "goal") await showGoal(l.id);
  else { showView("board"); await openDetail(l.id); }
}
