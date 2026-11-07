// Setting up two-factor sign-in, shared by the login page (when an org
// requires it) and the account menu: scan a QR code, confirm with a code,
// then save the recovery codes.
(() => {
  function el(tag, attrs = {}, ...kids) {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (v == null || v === false) continue;
      if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
      else e.setAttribute(k, v === true ? "" : v);
    }
    e.append(...kids.flat().filter((k) => k != null));
    return e;
  }

  async function post(path, body) {
    const res = await fetch(path, {
      method: "POST",
      headers: body ? { "Content-Type": "application/json" } : {},
      body: body ? JSON.stringify(body) : undefined,
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || `Request failed (${res.status})`);
    return data;
  }

  // recoveryCodes shows codes in box, with copy and download, and calls
  // done when the user says they've saved them.
  function recoveryCodes(box, codes, done, doneLabel = "I've saved them") {
    const text = codes.join("\n");
    const copy = el("button", { type: "button", class: "btn small", onclick: async (e) => {
      try { await navigator.clipboard.writeText(text); e.target.textContent = "Copied"; } catch { e.target.textContent = "Select and copy them"; }
    } }, "Copy");
    const download = el("a", { class: "btn small", download: "dash-recovery-codes.txt",
      href: "data:text/plain;charset=utf-8," + encodeURIComponent(`Dash recovery codes (each works once)\n\n${text}\n`) }, "Download");
    box.replaceChildren(
      el("h3", {}, "Save your recovery codes"),
      el("p", { class: "hint" }, "If you lose your phone, sign in with one of these instead of a code. Each works once. Keep them somewhere safe, like a password manager: this is the only time they're shown."),
      el("ul", { class: "recovery-codes" }, codes.map((c) => el("li", {}, el("code", {}, c)))),
      el("div", { class: "recovery-actions" }, copy, download),
      el("button", { type: "button", class: "btn primary", onclick: done }, doneLabel),
    );
    box.querySelector(".btn.primary").focus();
  }

  // enrol builds the setup steps in box. done(codes) runs after the codes
  // are saved.
  async function enrol(box, { intro = "", done }) {
    const s = await post("/api/account/2fa/setup");
    const err = el("div", { class: "login-error", role: "alert" });
    const input = el("input", { name: "code", inputmode: "numeric", autocomplete: "one-time-code", pattern: "[0-9 ]*",
      maxlength: "7", required: true, placeholder: "123456", "aria-label": "Code from the app" });
    const button = el("button", { class: "btn primary" }, "Turn on");
    const form = el("form", { class: "totp-enrol" },
      intro ? el("p", { class: "hint" }, intro) : null,
      el("ol", { class: "totp-steps" },
        el("li", {}, "Open an authenticator app (1Password, Google Authenticator, Authy, …) and scan this code."),
        el("li", {}, "Type the 6-digit code it shows."),
      ),
      el("img", { class: "totp-qr", src: s.qr, alt: "QR code for your authenticator app", width: "220", height: "220" }),
      el("details", { class: "totp-secret" }, el("summary", {}, "Can't scan it?"),
        el("p", { class: "hint" }, "Enter this key in the app instead (time-based, 6 digits):"),
        el("code", {}, s.secret.replace(/(.{4})/g, "$1 ").trim())),
      el("label", { class: "field" }, el("span", {}, "Code from the app"), input),
      button, err);
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      err.textContent = "";
      button.disabled = true;
      try {
        const r = await post("/api/account/2fa/enable", { code: input.value });
        recoveryCodes(box, r.recovery_codes, () => done(r.recovery_codes));
      } catch (x) {
        err.textContent = x.message;
        input.select();
      } finally {
        button.disabled = false;
      }
    });
    box.replaceChildren(form);
    input.focus();
  }

  window.DashTwoFactor = { enrol, recoveryCodes, post };
})();
