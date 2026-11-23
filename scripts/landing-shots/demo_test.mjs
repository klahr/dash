// Checks the demo video's setup without a Dash (see README.md here): the
// seed repository from repo.sh passes its tests, fakeclaude.mjs has a
// change for each of video-tasks.mjs's tasks, "Add dark mode" and "Update
// the README" conflict whichever merges first and the stand-in resolves
// it, and the webhook task leaves the Save button too low until reworked.
//
//   node --test scripts/landing-shots/demo_test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { VIDEO_TASKS } from './video-tasks.mjs';

const here = import.meta.dirname;
const fake = join(here, 'fakeclaude.mjs');
const root = mkdtempSync(join(tmpdir(), 'demo-video-'));
const repo = join(root, 'acme-shop');
// The demo's own git settings, as demo.sh writes them, none of yours.
const env = { ...process.env, HOME: root, GIT_CONFIG_GLOBAL: join(root, 'gitconfig'), GIT_CONFIG_NOSYSTEM: '1', DEMO_DIR: root, FAKECLAUDE_PACE_MS: '0' };
execFileSync('git', ['config', '--file', env.GIT_CONFIG_GLOBAL, 'merge.conflictStyle', 'diff3']);
execFileSync('sh', [join(here, 'repo.sh'), repo], { env });

const git = (...args) => execFileSync('git', ['-c', 'user.name=Demo', '-c', 'user.email=demo@example.com', ...args], { cwd: repo, env, encoding: 'utf8' });
const read = (f) => readFileSync(join(repo, f), 'utf8');
const nodeTest = () => spawnSync(process.execPath, ['--test'], { cwd: repo, env, encoding: 'utf8' });
// claude runs the stand-in in the repository, as Dash would.
function claude(input, ...args) {
  const r = spawnSync(process.execPath, [fake, '-p', ...args], { cwd: repo, env, input, encoding: 'utf8' });
  assert.equal(r.status, 0, r.stderr);
  const res = r.stdout.trim().split('\n').map((l) => JSON.parse(l)).find((e) => e.type === 'result');
  assert.ok(res, 'no result');
  return res;
}
const prompt = (title) => `# Task: ${title}\n\n${VIDEO_TASKS.find(([t]) => t === title)[1]}`;
// branch makes a branch off main with the task's canned change.
function branch(name, title) {
  git('checkout', '-q', '-b', name, 'main');
  const res = claude(prompt(title));
  git('add', '-A');
  git('commit', '-q', '-m', title);
  git('checkout', '-q', 'main');
  return res;
}

test.after(() => rmSync(root, { recursive: true, force: true }));

test('the seed passes its tests and has what the tasks need', () => {
  const r = nodeTest();
  assert.equal(r.status, 0, r.stdout);
  assert.match(read('src/export.js'), /pad\(d\.getMonth\(\)\)/, 'the date bug');
  assert.match(read('README.md'), /^- Light theme only: dark mode is planned$/m);
  assert.match(read('public/webhooks.css'), /\.save \{ margin-top: 22px/, 'the low Save button');
  assert.match(read('src/server.js'), /process\.env\.PORT/);
  assert.doesNotMatch(read('src/views/webhooks.html'), /type="url"/);
});

test('every task has a canned change that keeps the tests passing', () => {
  assert.deepEqual(VIDEO_TASKS.map(([t]) => t), ['Add dark mode', 'Validate the webhook URL', 'Fix the date bug in exports', 'Add tests for the billing page', 'Update the README']);
  for (const [title] of VIDEO_TASKS) {
    const name = title.toLowerCase().replaceAll(' ', '-');
    const res = branch(name, title);
    assert.doesNotMatch(res.result, /Nothing to change/, title);
    assert.notEqual(git('diff', '--stat', 'main', name), '', title);
    git('checkout', '-q', name);
    const r = nodeTest();
    git('checkout', '-q', 'main');
    assert.equal(r.status, 0, `${title}: ${r.stdout}`);
  }
});

test('the webhook task leaves the button low until it is reworked', () => {
  git('checkout', '-q', 'validate-the-webhook-url');
  assert.match(read('public/webhooks.css'), /margin-top: 22px/);
  assert.match(read('src/views/webhooks.html'), /type="url" required/);
  const res = claude('The Save button sits lower than the field: line it up.', '--resume', 'demo-webhook-url');
  assert.match(res.result, /Lined the Save button up/);
  assert.match(read('public/webhooks.css'), /align-items: center/);
  git('checkout', '-q', '--', '.');
  git('checkout', '-q', 'main');
});

for (const [first, second, session] of [['add-dark-mode', 'update-the-readme', 'demo-readme'], ['update-the-readme', 'add-dark-mode', 'demo-dark-mode']]) {
  test(`${second} conflicts once ${first} is merged, and is resolved`, () => {
    git('checkout', '-q', '-b', `main-${first}`, 'main');
    git('merge', '-q', '--squash', first);
    git('commit', '-q', '-m', first);
    git('checkout', '-q', '-b', `try-${second}`, second);
    const merge = spawnSync('git', ['merge', '--no-commit', '--no-ff', `main-${first}`], { cwd: repo, env, encoding: 'utf8' });
    assert.notEqual(merge.status, 0, 'no conflict');
    assert.equal(git('diff', '--name-only', '--diff-filter=U').trim(), 'README.md');
    claude('`main` has moved on, and merging it into your work conflicts in:\n- README.md\n', '--resume', session);
    const readme = read('README.md');
    assert.doesNotMatch(readme, /^(<<<<<<<|=======|>>>>>>>)/m);
    assert.match(readme, /^- A dark theme that follows the system setting$/m);
    assert.match(readme, /^- Orders export as CSV$/m);
    assert.doesNotMatch(readme, /PayPal|dark mode is planned/);
    git('merge', '--abort');
    git('checkout', '-q', 'main');
  });
}
