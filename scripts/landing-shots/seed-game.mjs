// Starting states for the Star Catcher tutorial series
// (scripts/tutorials/series): each scene is where one video starts, built
// the way the videos before it built it, through Dash's API, with
// fakeclaude.mjs doing the work and fakegh.mjs standing in for GitHub.
//
//   node seed-game.mjs http://127.0.0.1:7791 <scene>
//
// game-overview is game-v2 with three drafts, for the overview.
// Scenes, in order (each includes the ones before it): runner (no project),
// game-plan, game-review, game-v1, game-rocks, game-v2, game-v3, game-v4,
// game-done. "fresh" (no runner) needs nothing here.
import { execFileSync } from 'node:child_process';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { brief, claudeMD } from './starcatcher.mjs';

const [base, scene] = process.argv.slice(2);
const SCENES = ['fresh', 'runner', 'game-plan', 'game-review', 'game-v1', 'game-rocks', 'game-v2', 'game-v3', 'game-v4', 'game-done'];
// game-overview (the landing page's overview): game-v2 with video 7's three
// tasks as drafts, to queue with one click.
const overview = scene === 'game-overview';
const upTo = overview ? SCENES.indexOf('game-v2') : SCENES.indexOf(scene);
if (upTo < 0) throw new Error(`unknown scene ${scene}: ${SCENES.join(', ')}`);
const reach = (s) => SCENES.indexOf(s) <= upTo;
const login = process.env.FAKE_GH_LOGIN || 'alex';

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
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(what, fn, secs = 240) {
  for (let i = 0; i < secs * 2; i++) {
    const v = await fn();
    if (v) return v;
    await sleep(500);
  }
  throw new Error(`timed out waiting for ${what}`);
}
const log = (s) => console.error(`seed: ${s}`);
const todo = (id) => api('GET', `/api/todos/${id}`);
const status = (id, ...want) => until(`${id} to be ${want.join(' or ')}`, async () => want.includes((await todo(id)).status));
// inReview waits until task id is in review with its pull request open.
const inReview = (id) => until(`${id} in review with a pull request`, async () => {
  const t = await todo(id);
  if (t.status === 'failed') throw new Error(`task ${t.title} failed: ${t.error || ''}`);
  return t.status === 'review' && t.pr?.number ? t : null;
});
async function merge(id) {
  const t = await inReview(id);
  log(`merging #${t.pr.number} ${t.title}`);
  await api('POST', `/api/todos/${id}/merge-pr`, {});
  await status(id, 'merged');
}
let pid = '';
const add = (title, prompt, opts = {}) => api('POST', `/api/projects/${pid}/todos`, { title, prompt, ...opts });
const goalTasks = async (gid) => (await api('GET', `/api/goals/${gid}`)).tasks || [];
const goalTask = (gid, title) => until(title, async () => (await goalTasks(gid)).find((t) => t.title === title));

await api('POST', '/api/login', { username: process.env.DASH_USERNAME || 'alex', password: process.env.DASH_PASSWORD || 'demo-password-for-shots' });
const out = {};

if (reach('game-plan')) {
  // What Launch leaves (video 2): a new GitHub repository with the brief,
  // the project, and its goal's plan waiting.
  const seed = mkdtempSync(join(tmpdir(), 'star-catcher-'));
  const git = (...a) => execFileSync('git', a, { cwd: seed, stdio: 'pipe' });
  git('init', '-q', '-b', 'main');
  writeFileSync(join(seed, 'BRIEF.md'), brief);
  writeFileSync(join(seed, 'CLAUDE.md'), claudeMD);
  git('add', '-A');
  git('commit', '-q', '-m', 'Add the brief and CLAUDE.md');
  execFileSync('gh', ['repo', 'create', 'star-catcher', '--private', '--source=.', '--remote=origin', '--push'], { cwd: seed, stdio: 'pipe' });
  const runners = (await api('GET', '/api/runners')).runners;
  const p = await api('POST', '/api/projects', {
    name: 'star-catcher', remote_url: `https://github.com/${login}/star-catcher.git`, runners: [runners[0].id],
    test_command: 'node --test', run_command: 'node server.js', merge_mode: 'pr',
  });
  pid = out.project = p.id;
  const goal = await api('POST', '/api/goals', { project: pid, title: 'Build the first version of star-catcher', description: brief, mode: 'ask' });
  out.goal = goal.id;
  await until('the plan', async () => ((await api('GET', `/api/goals/${goal.id}`)).plan || []).length > 0);
  log('the plan waits');

  if (reach('game-review')) {
    await api('POST', `/api/goals/${goal.id}/plan/accept`, { queue: true });
    await merge((await goalTask(goal.id, 'Set up the page and the canvas')).id);
    const basket = await goalTask(goal.id, 'Move the basket with the arrow keys');
    const stars = await goalTask(goal.id, 'Make stars fall');
    await inReview(basket.id);
    await inReview(stars.id);
    if (reach('game-v1')) {
      await merge(basket.id);
      await merge(stars.id);
      await merge((await goalTask(goal.id, 'Catch stars in the basket')).id);
      await until('the goal to be done', async () => (await api('GET', `/api/goals/${goal.id}`)).status === 'done', 120).catch(() => log('the goal isn\'t done yet; going on'));
    }
  }
}

if (reach('game-rocks')) {
  await merge((await add('Show the score', 'Show the score in the top left corner, one point per star caught.', { enqueue: true })).id);
  const rocks = await add('Add falling rocks to dodge', 'Add rocks that fall among the stars: a rock in the basket costs three stars.', { enqueue: true });
  await inReview(rocks.id);
  out.rocks = rocks.id;
  if (reach('game-v2')) {
    await api('POST', `/api/todos/${rocks.id}/rework`, { feedback: 'Rocks should fall at half this speed at the start. Make the speed depend on the level, starting slow.' });
    await status(rocks.id, 'in_progress', 'queued');
    await merge(rocks.id);
  }
}

if (overview) {
  for (const [title, prompt] of [
    ['Sounds for stars and rocks', 'Play a short sound when a star is caught, and another for a rock.'],
    ['Three lives', 'Show three hearts for lives, and lose one when a rock hits.'],
    ['Keep the best score', 'Keep the best score in the browser, and show it on the start screen.'],
  ]) await add(title, prompt);
}

if (reach('game-v3')) {
  const ids = [];
  for (const [title, prompt] of [
    ['Sounds for stars and rocks', 'Play a short sound when a star is caught, and another for a rock.'],
    ['Three lives', 'Show three hearts for lives, and lose one when a rock hits.'],
    ['Keep the best score', 'Keep the best score in the browser, and show it on the start screen.'],
  ]) ids.push((await add(title, prompt, { enqueue: true })).id);
  for (const id of ids) await inReview(id);
  await merge(ids[0]);
  await merge(ids[1]);
  // The best score now conflicts with main: Claude merges main in, and it
  // comes back to Review.
  await status(ids[2], 'in_progress', 'queued');
  log('the best score is fixing its conflict');
  await merge(ids[2]);
  for (const [title, description, column] of [
    ['Levels', 'The game gets harder as you go: faster stars and rocks.', 'ideas'],
    ['Online leaderboard', 'See the best scores of everyone who plays.', 'ideas'],
    ['Pause with P', 'Press P to pause and resume the game.', 'planned'],
    ['Sounds', 'A chime for a star, a thud for a rock.', 'done'],
  ]) await api('POST', '/api/planning/cards', { title, description, project: pid, column });
}

if (reach('game-v4')) {
  const goal = await api('POST', '/api/goals', { project: pid, title: 'Levels', description: 'A new level every 20 stars; stars and rocks fall faster each level; the level shows at the top.', mode: 'ask' });
  await until('the levels plan', async () => ((await api('GET', `/api/goals/${goal.id}`)).plan || []).length > 0);
  await api('POST', `/api/goals/${goal.id}/plan/accept`, { queue: true });
  await merge((await goalTask(goal.id, 'Work out the level')).id);
  await merge((await goalTask(goal.id, 'Speed up each level')).id);
  await merge((await goalTask(goal.id, 'Show the level')).id);
}

if (reach('game-done')) {
  await merge((await add('Move the basket with a finger', 'On a phone there are no arrow keys: let the basket follow a finger on the canvas (pointer events), and keep the arrow keys working.', { enqueue: true })).id);
  await merge((await add('Add a deploy script', 'Add a deploy script that copies the game to $SITE_DIR.', { enqueue: true })).id);
}

console.log(JSON.stringify(out));
