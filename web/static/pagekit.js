// Helpers shared by the standalone pages (login, invitation, wall) and
// twofactor.js, which are classic scripts outside app.js's modules:
// window.DashPage.
(() => {
  const CANT_REACH = "Can't reach the Dash server.";

  // el builds an element. Attributes that are null or false are left out,
  // true is an empty one, style is CSS text and on<event> a listener.
  // Strings become text nodes (never HTML); null and false kids are skipped.
  function el(tag, attrs, ...kids) {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v == null || v === false) continue;
      if (k === "style") e.style.cssText = v;
      else if (k.startsWith("on") && typeof v === "function") e.addEventListener(k.slice(2), v);
      else e.setAttribute(k, v === true ? "" : v);
    }
    for (const k of kids.flat()) {
      if (k == null || k === false) continue;
      e.append(k instanceof Node ? k : String(k));
    }
    return e;
  }

  // ServerError is a request the server answered with an error: its
  // message is the server's, fit to show.
  class ServerError extends Error {}

  // postJSON posts body (none if undefined) as JSON to path, and returns
  // the response with its JSON ({} if there's none).
  async function postJSON(path, body) {
    const res = await fetch(path, {
      method: "POST",
      headers: body === undefined ? {} : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    return { res, data: await res.json().catch(() => ({})) };
  }

  // post is postJSON that returns only the JSON, and throws a ServerError
  // when the server says no.
  async function post(path, body) {
    const { res, data } = await postJSON(path, body);
    if (!res.ok) throw new ServerError(data.error || `Request failed (${res.status})`);
    return data;
  }

  // busy runs fn with button disabled and error cleared. What fn throws
  // ends up in error: a ServerError's message, else that the server is out
  // of reach. It returns what fn returns (undefined after an error).
  async function busy({ button, error }, fn) {
    if (error) error.textContent = "";
    if (button) button.disabled = true;
    try {
      return await fn();
    } catch (e) {
      if (!error) throw e;
      error.textContent = e instanceof ServerError ? e.message : CANT_REACH;
    } finally {
      if (button) button.disabled = false;
    }
  }

  // busyForm handles form's submits with fn, busy with the form's primary
  // button and its .login-error.
  function busyForm(form, fn) {
    form.addEventListener("submit", (e) => {
      e.preventDefault();
      busy(formParts(form), fn);
    });
  }

  function formParts(form) {
    return {
      button: form.querySelector("button.primary") || form.querySelector("button"),
      error: form.querySelector(".login-error"),
    };
  }

  // optionalImages removes decorative pictures that fail to load (not with
  // an inline onerror: the Content-Security-Policy allows no inline script).
  function optionalImages() {
    for (const img of document.querySelectorAll("img[data-optional]")) {
      if (img.complete && !img.naturalWidth && img.loading !== "lazy") img.remove(); // failed already (a lazy one may not have started)
      else img.addEventListener("error", () => img.remove());
    }
  }

  window.DashPage = { el, postJSON, post, ServerError, busy, busyForm, formParts, optionalImages, CANT_REACH };
})();
