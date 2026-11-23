#!/usr/bin/env node
// Starts the live preview of a demo task in review on the runner that ran
// it, and prints its address. The hub's task pane doesn't offer Preview for
// a task on a runner yet, but the hub serves the runner's preview at
// <task>-<runner>.<preview domain> (see README.md here).
//
//   node scripts/landing-shots/preview.mjs "Validate the webhook URL"
const [title] = process.argv.slice(2);
if (!title) throw new Error('usage: preview.mjs "<task title>"');
const base = process.env.DEMO_HUB || 'http://127.0.0.1:7791';
let cookie = '';

async function api(method, path, body) {
  const res = await fetch(base + path, {
    method, headers: { 'Content-Type': 'application/json', Cookie: cookie },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const set = res.headers.get('set-cookie');
  if (set) cookie = set.split(';')[0];
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${text}`);
  return text ? JSON.parse(text) : null;
}

await api('POST', '/api/login', { username: process.env.DASH_USERNAME || 'demo', password: process.env.DASH_PASSWORD || 'demo-password-for-shots' });
const runners = (await api('GET', '/api/runners')).runners;
let task;
for (const p of await api('GET', '/api/projects')) {
  task = (await api('GET', `/api/projects/${p.id}/todos`)).find((t) => t.title === title && t.status === 'review');
  if (task) break;
}
if (!task) throw new Error(`no task "${title}" in review`);
const runner = runners.find((r) => r.id === task.runner);
if (!runner) throw new Error(`"${title}" didn't run on a runner`);
const pv = await api('POST', `/peer/${encodeURIComponent(runner.name)}/api/todos/${task.remote_id}/preview`, {});
console.log(pv.url);
