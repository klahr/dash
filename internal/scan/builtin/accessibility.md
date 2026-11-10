---
name: accessibility
title: Accessibility
kind: accessibility
scope: since_last_scan
# Suggested: Wednesdays at 07:00. Built-in schedules are off until turned on.
schedule: 0 7 * * 3
# Web front ends only: a repository with none of these files is skipped.
requires_files: *.html, *.htm, *.jsx, *.tsx, *.vue, *.svelte, *.tmpl, *.gohtml, *.erb, *.hbs, *.handlebars, *.njk, *.jinja, *.jinja2, *.twig, *.liquid, *.mustache, *.cshtml, *.razor, *.astro
---
Review the web front end of the project in {{path}} for accessibility
problems: UI that people using a screen reader, a keyboard only, or low
vision can't use. Look at the HTML, JSX/TSX, Vue, Svelte and server-side
HTML templates (Go html/template and others), and the CSS they use.

Report:

- Missing labels and text alternatives: images without alt text (or with
  alt text on purely decorative ones), form fields with no label, icon-only
  buttons and links with no accessible name, and a page or frame without a
  title or lang.
- Poor colour contrast: text below 4.5:1 against its background (3:1 for
  large text, icons and focus outlines), and information given by colour
  alone. Work out the ratio from the actual colours, including theme
  variables, and quote them.
- Keyboard traps and dead ends: dialogs and menus that focus can't leave
  or never reaches, handlers on mouse events only, positive tabindex,
  and content that only appears on hover.
- Missing focus styles: outline removed (outline: none or 0, a reset)
  with no visible :focus or :focus-visible replacement.
- Wrong ARIA: invalid roles or attributes, aria-hidden on focusable
  content, aria-labelledby or aria-controls pointing at ids that don't
  exist, states never updated (aria-expanded, aria-selected), and ARIA
  that repeats or contradicts the native element.
- Non-semantic interactive elements: div or span with a click handler
  instead of a button or link, without role, tabindex and key handling;
  links used as buttons and buttons used as links; headings skipped or
  faked with styling.

Start from what changed, but follow a changed component into where it is
used and the styles it gets. Each finding names the file and line of the
element, says who it stops (keyboard, screen reader, low vision) and what
WCAG 2.2 criterion it fails, and the suggested fix is the corrected markup
or style. Use "<file>:<component or element>:<problem>" as the fingerprint.

Only report problems you can see in the code. Leave out things that depend
on content you can't see (the text of a user-supplied image), generated and
vendored files, and test fixtures. Do not change any files.
