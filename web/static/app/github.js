import { h } from "./dom.js";
import { agoText } from "./branches.js";

// ------------------------------------------------------------------ GitHub repository lists
//
// Add project (projects.js) and a runner's projects on the Runners page
// (runners.js) both list a runner's GitHub repositories with its gh: grouped
// by owner, filtered as you type, and capped so a long list stays quick.

// The GitHub mark (github.com/logos), inline: no request for an icon.
const GH_MARK = "M12 .5C5.65.5.5 5.65.5 12c0 5.08 3.29 9.39 7.86 10.91.58.1.79-.25.79-.56v-2c-3.2.7-3.87-1.37-3.87-1.37-.52-1.33-1.28-1.69-1.28-1.69-1.04-.71.08-.7.08-.7 1.16.08 1.77 1.19 1.77 1.19 1.03 1.76 2.7 1.25 3.36.96.1-.75.4-1.25.73-1.54-2.55-.29-5.24-1.28-5.24-5.69 0-1.26.45-2.29 1.19-3.1-.12-.29-.52-1.46.11-3.05 0 0 .97-.31 3.17 1.18a11 11 0 0 1 5.77 0c2.2-1.49 3.17-1.18 3.17-1.18.63 1.59.23 2.76.11 3.05.74.81 1.19 1.84 1.19 3.1 0 4.42-2.69 5.4-5.25 5.68.41.36.78 1.06.78 2.14v3.17c0 .31.21.67.8.56A11.51 11.51 0 0 0 23.5 12C23.5 5.65 18.35.5 12 .5z";
export const ghMark = (cls = "gh-mark") => {
  const t = document.createElement("template");
  t.innerHTML = `<svg class="${cls}" viewBox="0 0 24 24" aria-hidden="true"><path d="${GH_MARK}" fill="currentColor"/></svg>`;
  return t.content.firstChild;
};
// An owner's avatar from GitHub, or the mark when it doesn't load.
export const ghAvatar = (owner) => {
  const img = h("img", { class: "gh-avatar", alt: "", width: 20, height: 20, loading: "lazy", referrerPolicy: "no-referrer",
    src: `https://github.com/${encodeURIComponent(owner)}.png?size=40` });
  img.addEventListener("error", () => img.replaceWith(ghMark("gh-avatar gh-mark")), { once: true });
  return img;
};

// A repository's "owner/name" title.
export const ghRepoLabel = (x) =>
  h("span", { class: "gh-repo-title" }, h("span", { class: "gh-repo-owner" }, `${x.owner}/`), h("strong", {}, x.name));

// The short facts shown under a repository.
export const ghRepoHints = (x) =>
  [x.isPrivate && "private", x.isFork && "fork", x.isArchived && "archived", x.updatedAt && `updated ${agoText(x.updatedAt)}`].filter(Boolean);

// ghRepoRows filters repos by q (lower case; name, description and what
// also(x) returns), and returns the first limit of them as rows made by
// row(x), with an owner header before each owner's repositories (marked
// "you" for user). more is how many matches were left out.
export function ghRepoRows(repos, { q = "", user = "", limit = 200, also = () => [], row }) {
  const hits = repos.filter((x) => !q || [x.nameWithOwner, x.description, ...also(x)].some((s) => (s || "").toLowerCase().includes(q)));
  const rows = [];
  let owner = null;
  for (const x of hits.slice(0, limit)) {
    if (x.owner !== owner) {
      owner = x.owner;
      rows.push(h("div", { class: "gh-owner", role: "presentation" }, ghAvatar(owner), h("span", {}, owner), owner === user ? h("em", {}, "you") : null));
    }
    rows.push(row(x));
  }
  return { rows, more: Math.max(0, hits.length - limit) };
}
