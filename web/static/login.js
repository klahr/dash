document.getElementById("login").addEventListener("submit", async (e) => {
  e.preventDefault();
  const form = e.target;
  const err = document.getElementById("login-error");
  const button = form.querySelector("button");
  err.textContent = "";
  button.disabled = true;
  try {
    const res = await fetch("/api/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: form.username.value, password: form.password.value }),
    });
    if (res.ok) {
      // Only back to a live preview or an invitation link: anything else
      // could be an open redirect.
      const next = new URLSearchParams(location.search).get("next") || "";
      const ok = /^\/preview\/[a-z0-9-]+(\?|$)/.test(next) || /^\/invite\.html#[A-Za-z0-9_-]+$/.test(next);
      location.replace(ok ? next : "/");
      return;
    }
    const data = await res.json().catch(() => ({}));
    err.textContent = data.error || `Sign-in failed (${res.status})`;
    form.password.select();
  } catch {
    err.textContent = "Can't reach the Dash server.";
  } finally {
    button.disabled = false;
  }
});
