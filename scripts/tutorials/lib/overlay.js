// The tutorials' overlays, injected into every page like cursor.js: a
// terminal window (the `terminal` action) and a caption in the lower third
// (the `caption` action). Both sit in the top layer, in one manual popover
// raised above open dialogs and below the pointer. The recorder calls
// window.__tutTerminal.open(), .type(command, cps), .print(lines),
// .close() and window.__tutCaption.set(text). A caption is kept in
// sessionStorage, so it stays up when the page changes.
(() => {
  if (window.__tutTerminal) return;
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const ease = 'cubic-bezier(.16,1,.3,1)'; // brand.css's --out
  const PROMPT = 'alex@studio ~ %';
  const KEY = '__tutCaption';
  const store = {
    get() { try { return sessionStorage.getItem(KEY) || ''; } catch { return ''; } },
    set(v) { try { if (v) sessionStorage.setItem(KEY, v); else sessionStorage.removeItem(KEY); } catch { /* data: URLs have none */ } },
  };

  let root, term, caption;
  let fading = 0; // overlays still fading out
  const install = () => {
    root = document.createElement('div');
    root.setAttribute('popover', 'manual');
    root.id = '__tut-overlay';
    const style = document.createElement('style');
    style.textContent = `
      #__tut-overlay { all: initial; position: fixed; inset: 0; width: 100vw; height: 100vh; margin: 0; padding: 0;
        border: 0; background: transparent; pointer-events: none; overflow: visible; display: block; }
      #__tut-overlay * { box-sizing: border-box; }
      #__tut-overlay .term { position: absolute; left: 50%; top: 27vh; width: min(1120px, 90vw); translate: -50% 0;
        border-radius: 12px; overflow: hidden; background: rgba(21, 23, 30, .97);
        border: 1px solid rgba(255, 255, 255, .1);
        box-shadow: 0 0 0 .5px rgba(0, 0, 0, .8), 0 32px 90px rgba(0, 0, 0, .6), 0 10px 28px rgba(0, 0, 0, .35); }
      #__tut-overlay .bar { position: relative; height: 40px; display: flex; align-items: center; gap: 9px; padding: 0 16px;
        background: linear-gradient(#2c2f38, #25282f); border-bottom: 1px solid rgba(0, 0, 0, .55); }
      #__tut-overlay .bar i { width: 13px; height: 13px; border-radius: 50%; display: block; box-shadow: inset 0 0 0 .5px rgba(0, 0, 0, .3); }
      #__tut-overlay .bar span { position: absolute; left: 0; right: 0; text-align: center; color: #a3a9b6;
        font: 500 15px/40px var(--ui, "Instrument Sans", system-ui, sans-serif); letter-spacing: .01em; }
      #__tut-overlay .screen { padding: 18px 22px 24px; min-height: 340px; color: #d8dee9; white-space: pre-wrap; word-break: break-all;
        font: 400 21px/1.55 var(--mono, "JetBrains Mono", ui-monospace, Menlo, monospace); font-variant-ligatures: none; }
      #__tut-overlay .screen .ps { color: #9fb3ff; font-weight: 600; }
      #__tut-overlay .screen .ok { color: #6ee7a0; }
      #__tut-overlay .screen .no { color: #ff7a7a; }
      #__tut-overlay .screen .dim { color: #8b93a3; }
      #__tut-overlay .caret { display: inline-block; width: .6em; height: 1.2em; vertical-align: -.22em; background: #d8dee9; }
      #__tut-overlay .caret.blink { animation: __tut-blink 1.06s steps(1) infinite; }
      @keyframes __tut-blink { 50% { opacity: 0; } }
      #__tut-overlay .cap { position: absolute; left: 50%; bottom: 10.5vh; translate: -50% 0; max-width: 84vw; width: max-content;
        padding: .36em .9em .42em; border-radius: .38em; text-align: center; text-wrap: balance;
        font: 700 min(6vh, 3.6vw)/1.12 var(--ui, "Instrument Sans", system-ui, sans-serif); letter-spacing: -.02em; color: #eef2fa;
        background: rgba(16, 23, 34, .74); -webkit-backdrop-filter: blur(16px) saturate(1.3); backdrop-filter: blur(16px) saturate(1.3);
        border: 1.5px solid color-mix(in srgb, var(--accent, #8aa4ff) 34%, transparent);
        box-shadow: 0 18px 60px rgba(0, 0, 0, .45); text-shadow: 0 1px 2px rgba(0, 0, 0, .35); }
      #__tut-overlay .cap::after { content: ""; position: absolute; left: 50%; bottom: -1.5px; width: 2.2em; height: 4px; translate: -50% 0;
        border-radius: 2px; background: linear-gradient(90deg, var(--accent, #8aa4ff), var(--accent2, #c79bff)); }`;
    root.append(style);
    document.documentElement.append(root);
    new MutationObserver(() => raise()).observe(document.documentElement, { subtree: true, attributeFilter: ['open'] });
    const saved = store.get();
    if (saved) setCaption(saved, true);
  };

  // raise puts the overlay on top of the top layer (dialogs opened later go
  // above it), and the pointer above the overlay. It is shown only while it
  // holds something.
  function raise() {
    if (!root) return;
    const busy = !!(term || caption || fading);
    try {
      if (root.matches(':popover-open')) root.hidePopover();
      if (busy) root.showPopover();
    } catch { /* not connected yet */ }
    if (busy) window.__tutCursor?.raise();
  }

  const el = (tag, cls, text) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  };

  // The terminal: a window with a title bar and a screen of lines; the
  // last line holds the caret.
  let screen, caret;
  const line = () => screen.lastElementChild;
  const newLine = () => { const l = el('div'); screen.append(l); return l; };
  window.__tutTerminal = {
    // open shows the window (once) with an idle prompt.
    async open() {
      if (term) return;
      term = el('div', 'term');
      const bar = el('div', 'bar');
      bar.append(el('i'), el('i'), el('i'), el('span', '', 'alex — -zsh — 100×28'));
      const [r, y, g] = bar.querySelectorAll('i');
      r.style.background = '#ff5f57'; y.style.background = '#febc2e'; g.style.background = '#28c840';
      screen = el('div', 'screen');
      caret = el('span', 'caret blink');
      term.append(bar, screen);
      root.append(term);
      this.prompt();
      raise();
      await term.animate([{ opacity: 0, transform: 'translateY(14px) scale(.965)' }, { opacity: 1, transform: 'none' }],
        { duration: 320, easing: ease }).finished;
    },
    // prompt starts a line with the prompt and the blinking caret.
    prompt() {
      const l = newLine();
      l.append(el('span', 'ps', PROMPT), ' ', caret);
      caret.classList.add('blink');
    },
    // type types command after the prompt, cps characters a second, then
    // presses return.
    async type(command, cps = 22) {
      const l = line();
      const typed = document.createTextNode('');
      l.insertBefore(typed, caret);
      await sleep(350);
      caret.classList.remove('blink');
      let i = 0;
      for (const ch of command) {
        typed.data += ch;
        // A touch of rhythm, as the type action has.
        await sleep(1000 / cps + (/[ |-]/.test(ch) ? 45 : (i++ * 37) % 23));
      }
      await sleep(420);
      caret.remove();
      newLine().append(caret);
    },
    // print prints lines one by one, then a new prompt. A line that starts
    // with ✓ or ✗ gets its mark in green or red.
    async print(lines, gap = 260) {
      for (const text of lines) {
        await sleep(gap + Math.min(500, text.length * 6));
        const l = line();
        const m = text.match(/^(\s*)([✓✔]|[✗✘×])(.*)$/s);
        if (m) l.append(m[1], el('span', /[✓✔]/.test(m[2]) ? 'ok' : 'no', m[2]), m[3]);
        else l.append(text);
        l.append(caret);
        caret.remove();
        newLine().append(caret);
      }
      await sleep(gap);
      line().remove();
      this.prompt();
    },
    // close fades the window out and takes it away.
    async close() {
      if (!term) return;
      const t = term;
      term = null;
      fading++;
      await t.animate([{ opacity: 1, transform: 'none' }, { opacity: 0, transform: 'translateY(8px) scale(.98)' }],
        { duration: 260, easing: 'ease-in', fill: 'forwards' }).finished;
      t.remove();
      fading--;
      raise();
    },
    get isOpen() { return !!term; },
  };

  // The caption: one line or two of big text in the lower third. A new
  // caption rises in as the old one fades out; "" clears it.
  function setCaption(text, still) {
    const old = caption;
    caption = null;
    if (old) {
      fading++;
      old.animate([{ opacity: 1 }, { opacity: 0, transform: 'translateY(-6px)' }], { duration: 280, easing: 'ease-in', fill: 'forwards' })
        .finished.then(() => { old.remove(); fading--; raise(); });
    }
    if (text) {
      caption = el('div', 'cap', text);
      root.append(caption);
      if (!still) {
        caption.animate([{ opacity: 0, transform: 'translateY(22px)', filter: 'blur(6px)' }, { opacity: 1, transform: 'none', filter: 'blur(0)' }],
          { duration: 650, delay: old ? 140 : 0, easing: ease, fill: 'backwards' });
      }
    }
    raise();
  }
  window.__tutCaption = {
    set(text) { store.set(text); setCaption(text, false); },
    get text() { return caption ? caption.textContent : ''; },
  };

  if (document.documentElement) install();
  else document.addEventListener('DOMContentLoaded', install, { once: true });
})();
