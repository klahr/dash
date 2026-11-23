// The demo video's five tasks (README.md here), as [title, prompt]: seed.mjs's
// demo-video scene adds them as drafts, fakeclaude.mjs has a canned change
// for each (by title), and demo_test.mjs checks the two agree.
//
// "Add dark mode" and "Update the README" both change the README's line
// about dark mode, so whichever of them merges second conflicts.
// "Validate the webhook URL" keeps the page's layout, so its Save button is
// still too low in review, for Draw and Rework….
export const VIDEO_TASKS = [
  ['Add dark mode', `Add a dark theme to the shop that follows the system setting (\`prefers-color-scheme: dark\`), on every page.

The colours are hard-coded in \`public/style.css\`. Move each one into a CSS custom property on \`:root\`, use the properties in the rules, and give them dark values in a \`@media (prefers-color-scheme: dark)\` block. The light theme must look exactly as it does now, and text must stay readable in the dark one (at least 4.5:1 contrast).

Then, in README.md's Features list, replace the line \`- Light theme only: dark mode is planned\` with one that says the shop has a dark theme that follows the system setting. Change nothing else in the README: another task rewrites it.`],
  ['Validate the webhook URL', `The webhook settings page (\`/settings/webhooks\`: the form is \`src/views/webhooks.html\`, \`src/server.js\` handles it and \`src/webhooks.js\` keeps the URL) saves anything as the endpoint URL. Validate it:

- Allow only absolute \`https://\` URLs, and not \`localhost\` or private network addresses (127.x, 10.x, 172.16–31.x, 192.168.x).
- In the form, make the URL field \`type="url"\` and \`required\`, so the browser checks it too.
- Check it again on the server. A bad URL isn't saved: show the form again with what was typed, and "Enter an https:// URL" under the field with the existing \`.error\` class.
- Add tests for the check in \`test/webhooks.test.js\`.

Keep the page's layout and its styles as they are.`],
  ['Fix the date bug in exports', `The orders export (\`/export/orders.csv\`, written by \`src/export.js\`) puts the wrong month in every date: an order from 14 January 2026 comes out as \`2026-00-14\`.

First add a test to \`test/export.test.js\` that shows the bug (it should fail), then fix the bug so it passes. Make sure January and December both come out right.`],
  ['Add tests for the billing page', `The billing page (\`/billing\`, worked out by \`src/billing.js\`) has no tests. Add \`test/billing.test.js\`, with \`node:test\` and \`node:assert/strict\` as the other tests use, covering:

- the plan prices for a full month,
- prorating a month that started late (e.g. on the 11th of a 30-day month),
- VAT and its rounding to the cent,
- extra seats,
- an unknown plan.

Don't change \`src/billing.js\`. If a test finds a real bug, mark that test \`todo\` and say so in your summary.`],
  ['Update the README', `README.md is out of date. It says the shop needs Node 14 and Yarn, runs with \`yarn dev\` on port 3000 and tests with Jest. In fact it has no dependencies, starts with \`npm start\` (on \`$PORT\`, else 4100) and tests with \`npm test\` (Node's own test runner). Its Features list names PayPal, a PDF export and gift cards, which the shop doesn't have, and misses the webhook settings, billing and the CSV orders export.

Rewrite README.md to match the code in \`src/\`: a one-line description, the Features list (correct every item, and drop the line saying dark mode is planned: the "Add dark mode" task replaces it) and how to run and test it. Keep it short, and change no other file.`],
];

