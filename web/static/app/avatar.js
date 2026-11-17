import { $, h } from "./dom.js";
import { api } from "./api.js";
import { attempt, toast } from "./dialogs.js";
import { state } from "./state.js";
import { accountSection } from "./accountsheet.js";

// ------------------------------------------------------------------ profile pictures
// A user's avatar is their picture's version (0 or missing: none). Its URL
// carries the version, so the browser may keep it for good.
export const avatarURL = (u) => u?.avatar ? `/api/users/${u.id}/avatar?v=${u.avatar}` : "";

// fillAvatar shows u's picture in el, or their initial when they have none
// (or it doesn't load).
export function fillAvatar(el, u) {
  const initial = () => el.replaceChildren([...(u?.username || "?")][0].toUpperCase());
  const url = avatarURL(u);
  if (!url) { initial(); return el; }
  const img = h("img", { src: url, alt: "", decoding: "async", draggable: false });
  img.addEventListener("error", initial, { once: true });
  el.replaceChildren(img);
  return el;
}

// avatar is a small round picture (or initial) of u.
export const avatar = (u, cls = "") => fillAvatar(h("span", { class: `avatar${cls ? ` ${cls}` : ""}`, "aria-hidden": "true" }), u);

// showOwnAvatar redraws the signed-in user's picture: the account menu and
// the Account sheet.
export function showOwnAvatar() {
  const u = state.me?.user && { ...state.me.user, username: state.me.username };
  if (!u) return;
  fillAvatar($("#foot-avatar"), u);
  fillAvatar($("#avatar-preview"), u);
  $("#avatar-clear").hidden = !u.avatar;
}

// SIDE is the width and height of the square Dash keeps: twice the largest
// it is shown at, for sharp screens.
const SIDE = 256;

// square crops the middle square of the image file and scales it to SIDE,
// as WebP where the browser can make it (else PNG).
async function square(file) {
  let bmp;
  try { bmp = await createImageBitmap(file); } catch { throw new Error("That file isn't a picture this browser can read."); }
  const side = Math.min(bmp.width, bmp.height);
  const out = Math.min(SIDE, side);
  const canvas = h("canvas", { width: out, height: out });
  const ctx = canvas.getContext("2d");
  ctx.imageSmoothingQuality = "high";
  ctx.drawImage(bmp, (bmp.width - side) / 2, (bmp.height - side) / 2, side, side, 0, 0, out, out);
  bmp.close?.();
  const blob = await new Promise((resolve) => canvas.toBlob(resolve, "image/webp", 0.9));
  if (!blob) throw new Error("Couldn't read that picture.");
  return blob;
}

async function upload(blob) {
  const res = await fetch("/api/account/avatar", { method: "PUT", headers: { "Content-Type": blob.type || "application/octet-stream" }, body: blob });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data.avatar;
}

accountSection("picture", showOwnAvatar);

$("#avatar-pick").addEventListener("click", () => $("#avatar-file").click());
$("#avatar-file").addEventListener("change", attempt(async (e) => {
  const file = e.target.files[0];
  e.target.value = "";
  if (!file) return;
  if (!/^image\/(png|jpeg|gif|webp)$/.test(file.type)) throw new Error("Choose a PNG, JPEG, GIF or WebP picture.");
  const v = await upload(await square(file));
  state.me.user.avatar = v;
  showOwnAvatar();
  toast("Profile picture saved", { tone: "ok" });
}));
$("#avatar-clear").addEventListener("click", attempt(async () => {
  await api("DELETE", "/api/account/avatar", null, "");
  state.me.user.avatar = 0;
  showOwnAvatar();
  toast("Profile picture removed");
}));
