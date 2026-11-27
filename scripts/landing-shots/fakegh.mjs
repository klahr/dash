#!/usr/bin/env node
// A stand-in for the GitHub CLI (gh), for the demo: it answers the gh calls
// Dash makes (internal/gitops/github.go, ghrepos.go, git.go, repoinfo.go)
// from local state, so a project whose remote is
// https://github.com/<owner>/<repo>.git can open, review and merge pull
// requests with no network and no account.
//
// Repositories are bare ones at $DEMO_DIR/origin/<owner>/<repo>.git (served
// by gitserve, which git reaches through a url.insteadOf rule); pull requests
// and the rest live in $DEMO_DIR/github.json. The signed-in login is
// $FAKE_GH_LOGIN (default alex). Every call is logged to $DEMO_DIR/gh.log;
// a call it doesn't know fails with a message saying so.
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const DEMO = process.env.DEMO_DIR || path.resolve(here, '..');
const ORIGIN = path.join(DEMO, 'origin');
const STATE = path.join(DEMO, 'github.json');
const LOCK = path.join(DEMO, 'github.lock');
const LOGIN = process.env.FAKE_GH_LOGIN || 'alex';

class GhError extends Error {
	constructor(msg, code = 1) {
		super(msg);
		this.code = code;
	}
}
const fail = (msg) => {
	throw new GhError(msg);
};

// ---- logging -------------------------------------------------------------

function log(entry) {
	try {
		fs.mkdirSync(DEMO, { recursive: true });
		fs.appendFileSync(path.join(DEMO, 'gh.log'), JSON.stringify({ time: new Date().toISOString(), ...entry }) + '\n');
	} catch {
		// logging must never break a call
	}
}

// ---- git -----------------------------------------------------------------

// A clean environment for git run on the bare repositories and temporary
// clones: Dash pins GIT_DIR/GIT_WORK_TREE to the task's worktree, which
// must not leak into them.
function cleanEnv(extra = {}) {
	const env = { ...process.env };
	for (const k of ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_INDEX_FILE', 'GIT_OBJECT_DIRECTORY', 'GIT_ALTERNATE_OBJECT_DIRECTORIES', 'GIT_COMMON_DIR', 'GIT_PREFIX']) {
		delete env[k];
	}
	return { ...env, GIT_TERMINAL_PROMPT: '0', ...extra };
}

// git runs git; opts.cwd, opts.env (default cleanEnv()), opts.ok (return
// the result instead of throwing on a non-zero exit).
function git(args, opts = {}) {
	const r = spawnSync('git', args, { cwd: opts.cwd, env: opts.env || cleanEnv(), encoding: 'utf8', input: opts.input });
	if (r.error) throw r.error;
	if (r.status !== 0 && !opts.ok) {
		fail(`git ${args.join(' ')} failed: ${(r.stderr || r.stdout).trim()}`);
	}
	return opts.ok ? r : r.stdout.trim();
}

// ---- state ---------------------------------------------------------------

const sleep = (ms) => Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);

// withState runs fn on the state under a lock (a directory: mkdir is atomic)
// and, when write, writes the state back atomically (a rename).
function withState(fn, write = true) {
	fs.mkdirSync(DEMO, { recursive: true });
	const deadline = Date.now() + 60_000;
	for (;;) {
		try {
			fs.mkdirSync(LOCK);
			break;
		} catch (e) {
			if (e.code !== 'EEXIST') throw e;
			try {
				// A lock left by a killed process: older than two minutes.
				if (Date.now() - fs.statSync(LOCK).mtimeMs > 120_000) fs.rmSync(LOCK, { recursive: true, force: true });
			} catch {}
			if (Date.now() > deadline) fail(`fakegh: ${LOCK} is held; remove it if no gh is running`);
			sleep(25 + Math.random() * 50);
		}
	}
	try {
		let st = { repos: {}, prs: {}, nextId: 1000 };
		try {
			st = { ...st, ...JSON.parse(fs.readFileSync(STATE, 'utf8')) };
		} catch (e) {
			if (e.code !== 'ENOENT') throw e;
		}
		const out = fn(st);
		if (write) {
			const tmp = `${STATE}.${process.pid}.tmp`;
			fs.writeFileSync(tmp, JSON.stringify(st, null, 2) + '\n');
			fs.renameSync(tmp, STATE);
		}
		return out;
	} finally {
		fs.rmSync(LOCK, { recursive: true, force: true });
	}
}
const readState = (fn) => withState(fn, false);

// ---- repositories --------------------------------------------------------

const ghRe = /github\.com[/:]([A-Za-z0-9_.-]+)\/([A-Za-z0-9_.-]+?)(?:\.git)?\/?$/;

// repoOf turns NAME, OWNER/NAME, github.com/OWNER/NAME or a github.com URL
// into {owner, name}.
function repoOf(spec) {
	spec = String(spec).trim();
	const m = spec.match(ghRe);
	if (m) return { owner: m[1], name: m[2] };
	const parts = spec.replace(/\.git$/, '').split('/');
	if (parts.length === 1 && parts[0]) return { owner: LOGIN, name: parts[0] };
	if (parts.length === 2 && parts[0] && parts[1]) return { owner: parts[0], name: parts[1] };
	fail(`expected the "[HOST/]OWNER/REPO" format, got "${spec}"`);
}

// currentRepo is the GitHub repository of the checkout in the working
// directory (its origin, as configured: not after insteadOf, which
// `git remote get-url` would apply).
function currentRepo(flags) {
	if (flags && flags.repo) return repoOf(flags.repo);
	const r = git(['config', '--get', 'remote.origin.url'], { env: process.env, ok: true });
	const url = r.status === 0 ? r.stdout.trim() : '';
	const m = url.match(ghRe);
	if (!m) {
		fail(url ? `none of the git remotes configured for this repository point to a known GitHub host (origin is ${url})` : 'fatal: not a git repository (or any of the parent directories): .git');
	}
	return { owner: m[1], name: m[2] };
}

const full = (r) => `${r.owner}/${r.name}`;
const bareOf = (r) => path.join(ORIGIN, r.owner, `${r.name}.git`);
const webURL = (r) => `https://github.com/${full(r)}`;
const exists = (r) => fs.existsSync(path.join(bareOf(r), 'HEAD'));

function mustExist(r) {
	if (!exists(r)) fail(`GraphQL: Could not resolve to a Repository with the name '${full(r)}'. (repository)`);
}

function defaultBranch(r) {
	const out = git(['symbolic-ref', '--short', 'HEAD'], { cwd: bareOf(r), ok: true });
	return out.status === 0 ? out.stdout.trim() : 'main';
}

function branchOid(r, branch) {
	const out = git(['rev-parse', '--verify', '-q', `refs/heads/${branch}^{commit}`], { cwd: bareOf(r), ok: true });
	return out.status === 0 ? out.stdout.trim() : '';
}

function initBare(r) {
	const dir = bareOf(r);
	fs.mkdirSync(path.dirname(dir), { recursive: true });
	git(['init', '-q', '--bare', '-b', 'main', dir]);
	git(['config', 'http.receivepack', 'true'], { cwd: dir });
}

function repoMeta(st, r) {
	const key = full(r);
	if (!st.repos[key]) {
		const t = new Date().toISOString();
		st.repos[key] = { private: true, description: '', createdAt: t };
	}
	return st.repos[key];
}

// repoFields is everything gh repo view/list --json can be asked for here.
function repoFields(st, r) {
	const meta = st.repos[full(r)] || { private: true, description: '', createdAt: new Date().toISOString() };
	const prs = st.prs[full(r)] || [];
	let pushedAt = meta.createdAt;
	try {
		pushedAt = fs.statSync(path.join(bareOf(r), 'refs', 'heads')).mtime.toISOString();
		const packed = path.join(bareOf(r), 'packed-refs');
		if (fs.existsSync(packed)) {
			const t = fs.statSync(packed).mtime.toISOString();
			if (t > pushedAt) pushedAt = t;
		}
	} catch {}
	const branch = defaultBranch(r);
	return {
		name: r.name,
		nameWithOwner: full(r),
		owner: { id: 'U_fake', login: r.owner },
		url: webURL(r),
		sshUrl: `git@github.com:${full(r)}.git`,
		description: meta.description || '',
		homepageUrl: '',
		visibility: meta.private ? 'PRIVATE' : 'PUBLIC',
		isPrivate: !!meta.private,
		isFork: false,
		isArchived: false,
		isTemplate: false,
		isEmpty: !branchOid(r, branch),
		parent: null,
		defaultBranchRef: { name: branch },
		licenseInfo: null,
		latestRelease: null,
		primaryLanguage: null,
		repositoryTopics: [],
		stargazerCount: 0,
		forkCount: 0,
		watchers: { totalCount: 1 },
		issues: { totalCount: 0 },
		pullRequests: { totalCount: prs.filter((p) => p.state === 'OPEN').length },
		diskUsage: 0,
		createdAt: meta.createdAt,
		pushedAt,
		updatedAt: pushedAt,
		hasIssuesEnabled: true,
		hasWikiEnabled: false,
		hasDiscussionsEnabled: false,
		hasProjectsEnabled: false,
		mergeCommitAllowed: true,
		squashMergeAllowed: true,
		rebaseMergeAllowed: true,
		deleteBranchOnMerge: false,
		viewerDefaultMergeMethod: 'SQUASH',
		viewerPermission: 'ADMIN',
	};
}

// pick keeps the --json fields asked for, failing like gh on an unknown one.
function pick(obj, fields, what) {
	const out = {};
	for (const f of String(fields).split(',').map((s) => s.trim()).filter(Boolean)) {
		if (!(f in obj)) fail(`Unknown JSON field: "${f}" (fakegh doesn't know this ${what} field)`);
		out[f] = obj[f];
	}
	return out;
}

const print = (v) => process.stdout.write((typeof v === 'string' ? v : JSON.stringify(v)) + '\n');

// ---- arguments -----------------------------------------------------------

const boolFlags = new Set([
	'draft', 'squash', 'merge', 'rebase', 'auto', 'yes', 'private', 'public', 'internal', 'push', 'paginate',
	'required', 'delete-branch', 'admin', 'log-failed', 'failed', 'silent', 'include', 'i', 'web', 'fill',
	'disable-auto', 'watch', 'slurp', 'verbose', 'hostname-only',
]);
const shortFlags = { R: 'repo', b: 'body', t: 'title', B: 'base', H: 'head', d: 'draft', X: 'method', q: 'jq', L: 'limit', s: 'state', c: 'comment' };
const multiFlags = new Set(['f', 'F']);

// parse splits args into positionals and flags ({name: value}, -f/-F as
// lists of key=value).
function parse(args) {
	const pos = [];
	const flags = { f: [], F: [] };
	for (let i = 0; i < args.length; i++) {
		const a = args[i];
		if (a === '--') {
			pos.push(...args.slice(i + 1));
			break;
		}
		if (!a.startsWith('-') || a === '-') {
			pos.push(a);
			continue;
		}
		let name = a.replace(/^--?/, '');
		let value;
		const eq = name.indexOf('=');
		if (eq >= 0) {
			value = name.slice(eq + 1);
			name = name.slice(0, eq);
		}
		if (a.startsWith('--')) {
			if (name === 'field') name = 'F';
			if (name === 'raw-field') name = 'f';
		} else if (name !== 'f' && name !== 'F') {
			name = shortFlags[name] || name;
		}
		if (boolFlags.has(name)) {
			flags[name] = value === undefined ? true : value !== 'false';
			continue;
		}
		if (value === undefined) {
			if (i + 1 >= args.length) fail(`flag needs an argument: ${a}`);
			value = args[++i];
		}
		if (multiFlags.has(name)) flags[name].push(value);
		else flags[name] = value;
	}
	return { pos, flags };
}

// fill replaces gh's {owner}, {repo} and {branch} placeholders.
function fill(s, flags) {
	if (!/\{(owner|repo|branch)\}/.test(s)) return s;
	const r = currentRepo(flags);
	return s.replaceAll('{owner}', r.owner).replaceAll('{repo}', r.name).replaceAll('{branch}', () => git(['symbolic-ref', '--short', 'HEAD'], { env: process.env }));
}

function fieldMap(flags) {
	const out = {};
	for (const kv of [...flags.f, ...flags.F]) {
		const i = kv.indexOf('=');
		if (i < 0) fail(`field "${kv}" requires a value separated by an '=' sign`);
		out[kv.slice(0, i)] = fill(kv.slice(i + 1), flags);
	}
	return out;
}

// ---- pull requests -------------------------------------------------------

function prsOf(st, r) {
	return (st.prs[full(r)] ||= []);
}

function findPR(st, r, spec) {
	const s = String(spec ?? '');
	let n = Number(s.replace(/^#/, ''));
	const m = s.match(/\/pull\/(\d+)/);
	if (m) n = Number(m[1]);
	let pr;
	if (Number.isInteger(n) && n > 0) pr = prsOf(st, r).find((p) => p.number === n);
	else if (s) pr = [...prsOf(st, r)].reverse().find((p) => p.head === s);
	else {
		const b = git(['symbolic-ref', '--short', 'HEAD'], { env: process.env, ok: true }).stdout.trim();
		pr = [...prsOf(st, r)].reverse().find((p) => p.head === b);
		if (!pr) fail(`no pull requests found for branch "${b}"`);
	}
	if (!pr) fail(`GraphQL: Could not resolve to a PullRequest with the number of ${s}. (repository.pullRequest)`);
	return pr;
}

// mergeable is GitHub's verdict on an open pull request: whether head
// merges into base without conflicts.
function mergeable(r, pr) {
	if (pr.state !== 'OPEN') return 'UNKNOWN';
	const base = branchOid(r, pr.base);
	const head = branchOid(r, pr.head);
	if (!base || !head) return 'UNKNOWN';
	const out = git(['merge-tree', '--write-tree', '--no-messages', base, head], { cwd: bareOf(r), ok: true });
	if (out.status === 0) return 'MERGEABLE';
	if (out.status === 1) return 'CONFLICTING';
	return 'UNKNOWN';
}

// prFields is everything gh pr view/list --json can be asked for here.
function prFields(r, pr) {
	const headRefOid = (pr.state === 'OPEN' && branchOid(r, pr.head)) || pr.headRefOid || '';
	return {
		number: pr.number,
		id: `PR_fake${pr.number}`,
		url: `${webURL(r)}/pull/${pr.number}`,
		title: pr.title,
		body: pr.body,
		state: pr.state,
		closed: pr.state !== 'OPEN',
		isDraft: pr.isDraft,
		baseRefName: pr.base,
		headRefName: pr.head,
		headRefOid,
		headRepositoryOwner: { login: r.owner },
		isCrossRepository: false,
		author: { login: pr.author, is_bot: false },
		mergeable: mergeable(r, pr),
		mergeStateStatus: pr.state === 'OPEN' ? (pr.isDraft ? 'DRAFT' : 'CLEAN') : 'UNKNOWN',
		mergeCommit: pr.mergeCommit ? { oid: pr.mergeCommit } : null,
		mergedAt: pr.mergedAt || null,
		mergedBy: pr.mergedAt ? { login: LOGIN } : null,
		closedAt: pr.closedAt || null,
		createdAt: pr.createdAt,
		updatedAt: pr.updatedAt,
		statusCheckRollup: [],
		reviewDecision: '',
		reviews: [],
		reviewRequests: [],
		latestReviews: [],
		comments: (pr.comments || []).map((c) => ({ id: c.id, author: { login: c.author }, body: c.body, createdAt: c.createdAt, url: c.url, authorAssociation: 'OWNER' })),
		labels: [],
		assignees: [],
		additions: 0,
		deletions: 0,
		changedFiles: 0,
	};
}

function touch(pr) {
	pr.updatedAt = new Date().toISOString();
}

function prList(flags) {
	const r = currentRepo(flags);
	const state = String(flags.state || 'open').toUpperCase();
	const head = flags.head ? String(flags.head).replace(/^[^:]+:/, '') : '';
	const list = readState((st) =>
		prsOf(st, r)
			.filter((p) => state === 'ALL' || p.state === state)
			.filter((p) => !head || p.head === head)
			.filter((p) => !flags.base || p.base === flags.base)
			.reverse()
			.slice(0, Number(flags.limit || 30)),
	);
	if (!flags.json) {
		for (const p of list) print(`${p.number}\t${p.title}\t${p.head}\t${p.state}`);
		return;
	}
	print(list.map((p) => pick(prFields(r, p), flags.json, 'pull request')));
}

function prCreate(flags) {
	const r = currentRepo(flags);
	mustExist(r);
	const base = flags.base || defaultBranch(r);
	const head = String(flags.head || git(['symbolic-ref', '--short', 'HEAD'], { env: process.env })).replace(/^[^:]+:/, '');
	if (flags.title === undefined && !flags.fill) fail('must provide `--title` and `--body` (or `--fill`) when not running interactively');
	const headOid = branchOid(r, head);
	if (!headOid) {
		fail(`pull request create failed: GraphQL: Head sha can't be blank, Base sha can't be blank, No commits between ${base} and ${head}, Head ref must be a branch (createPullRequest)`);
	}
	if (!branchOid(r, base)) fail(`pull request create failed: GraphQL: Base ref must be a branch (createPullRequest)`);
	const ahead = git(['rev-list', '--count', `refs/heads/${base}..refs/heads/${head}`], { cwd: bareOf(r) });
	if (ahead === '0') fail(`pull request create failed: GraphQL: No commits between ${base} and ${head} (createPullRequest)`);
	const url = withState((st) => {
		const prs = prsOf(st, r);
		const open = prs.find((p) => p.state === 'OPEN' && p.head === head && p.base === base);
		if (open) fail(`a pull request for branch "${head}" into branch "${base}" already exists:\n${webURL(r)}/pull/${open.number}`);
		const meta = repoMeta(st, r);
		meta.lastNumber = (meta.lastNumber || 0) + 1;
		const t = new Date().toISOString();
		const pr = {
			number: meta.lastNumber,
			title: flags.title ?? head,
			body: flags.body ?? '',
			base,
			head,
			state: 'OPEN',
			// FAKE_GH_NO_DRAFTS: open it ready for review, as GitHub does where it has
			// no drafts (a free plan's private repository).
			isDraft: !!flags.draft && !process.env.FAKE_GH_NO_DRAFTS,
			author: LOGIN,
			createdAt: t,
			updatedAt: t,
			comments: [],
		};
		prs.push(pr);
		return `${webURL(r)}/pull/${pr.number}`;
	});
	print(url);
}

function prReady(pos, flags) {
	const r = currentRepo(flags);
	withState((st) => {
		const pr = findPR(st, r, pos[0]);
		if (pr.state !== 'OPEN') fail(`Pull request ${full(r)}#${pr.number} is closed. Only draft pull requests can be marked as "ready for review"`);
		pr.isDraft = false;
		touch(pr);
	});
}

function prClose(pos, flags) {
	const r = currentRepo(flags);
	withState((st) => {
		const pr = findPR(st, r, pos[0]);
		if (pr.state === 'MERGED') fail(`Pull request ${full(r)}#${pr.number} can't be closed because it was already merged`);
		if (pr.state === 'CLOSED') return;
		if (flags.comment) addComment(st, r, pr, flags.comment);
		pr.headRefOid = branchOid(r, pr.head) || pr.headRefOid;
		pr.state = 'CLOSED';
		pr.closedAt = new Date().toISOString();
		touch(pr);
		if (flags['delete-branch']) git(['update-ref', '-d', `refs/heads/${pr.head}`], { cwd: bareOf(r), ok: true });
	});
}

function addComment(st, r, pr, body) {
	st.nextId = (st.nextId || 1000) + 1;
	const c = { id: st.nextId, author: LOGIN, body, createdAt: new Date().toISOString() };
	c.url = `${webURL(r)}/pull/${pr.number}#issuecomment-${c.id}`;
	(pr.comments ||= []).push(c);
	touch(pr);
	return c;
}

function prComment(pos, flags) {
	const r = currentRepo(flags);
	if (flags.body === undefined) fail('`--body` or `--body-file` required when not running interactively');
	const url = withState((st) => addComment(st, r, findPR(st, r, pos[0]), flags.body).url);
	print(url);
}

// prMerge really merges: in a temporary clone of the bare repository, the
// head branch is squashed, merged or rebased onto the base, which is pushed.
function prMerge(pos, flags) {
	const r = currentRepo(flags);
	const method = flags.squash ? 'squash' : flags.rebase ? 'rebase' : flags.merge ? 'merge' : '';
	if (!method) fail('--merge, --rebase, or --squash required when not running interactively');
	const msg = withState((st) => {
		const pr = findPR(st, r, pos[0]);
		if (pr.state === 'MERGED') fail(`Pull request ${full(r)}#${pr.number} was already merged`);
		if (pr.state === 'CLOSED') fail(`Pull request ${full(r)}#${pr.number} is closed`);
		if (pr.isDraft) fail(`Pull request ${full(r)}#${pr.number} is still a draft`);
		const headOid = branchOid(r, pr.head);
		if (!headOid) fail(`GraphQL: Head branch ${pr.head} was deleted (mergePullRequest)`);
		const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'fakegh-merge-'));
		try {
			const author = git(['log', '-1', '--format=%an%x00%ae', headOid], { cwd: bareOf(r) }).split('\0');
			const env = cleanEnv({
				GIT_AUTHOR_NAME: author[0] || LOGIN,
				GIT_AUTHOR_EMAIL: author[1] || `${LOGIN}@users.noreply.github.com`,
				GIT_COMMITTER_NAME: 'GitHub',
				GIT_COMMITTER_EMAIL: 'noreply@github.com',
			});
			const g = (args, ok) => git(['-c', 'core.hooksPath=/dev/null', '-c', 'commit.gpgsign=false', ...args], { cwd: tmp, env, ok });
			git(['clone', '-q', '--no-tags', '-b', pr.base, bareOf(r), tmp], { env });
			g(['fetch', '-q', 'origin', `+refs/heads/${pr.head}:refs/fakegh/head`]);
			const notMergeable = () => fail(`Pull request ${full(r)}#${pr.number} is not mergeable: the merge commit cannot be cleanly created.`);
			if (method === 'squash') {
				if (g(['merge', '-q', '--squash', 'refs/fakegh/head'], true).status !== 0) notMergeable();
				g(['commit', '-q', '--allow-empty', '-m', `${pr.title} (#${pr.number})`]);
			} else if (method === 'merge') {
				const m = `Merge pull request #${pr.number} from ${r.owner}/${pr.head}\n\n${pr.title}`;
				if (g(['merge', '-q', '--no-ff', '-m', m, 'refs/fakegh/head'], true).status !== 0) notMergeable();
			} else {
				g(['checkout', '-q', '-b', 'fakegh-rebase', 'refs/fakegh/head']);
				if (g(['rebase', '-q', pr.base], true).status !== 0) {
					g(['rebase', '--abort'], true);
					notMergeable();
				}
			}
			const push = g(['push', '-q', 'origin', `HEAD:refs/heads/${pr.base}`], true);
			if (push.status !== 0) fail(`GraphQL: Base branch was modified. Review and try the merge again. (mergePullRequest)`);
			pr.mergeCommit = g(['rev-parse', 'HEAD']);
		} finally {
			fs.rmSync(tmp, { recursive: true, force: true });
		}
		pr.headRefOid = headOid;
		pr.state = 'MERGED';
		pr.isDraft = false;
		pr.mergedAt = pr.closedAt = new Date().toISOString();
		touch(pr);
		if (flags['delete-branch']) git(['update-ref', '-d', `refs/heads/${pr.head}`], { cwd: bareOf(r), ok: true });
		const verb = { squash: 'Squashed and merged', merge: 'Merged', rebase: 'Rebased and merged' }[method];
		return `✓ ${verb} pull request ${full(r)}#${pr.number} (${pr.title})`;
	});
	process.stderr.write(msg + '\n');
}

function prView(pos, flags) {
	const r = currentRepo(flags);
	const f = readState((st) => prFields(r, findPR(st, r, pos[0])));
	if (flags.json) print(pick(f, flags.json, 'pull request'));
	else print(`${f.title} ${full(r)}#${f.number}\n${f.state} • ${f.author.login} wants to merge into ${f.baseRefName} from ${f.headRefName}\n\n${f.body}\n\nView this pull request on GitHub: ${f.url}`);
}

// prChecks: the demo's repositories have no checks. With --json gh prints
// them and exits 0; --required with none required is an error, as in gh.
function prChecks(pos, flags) {
	const r = currentRepo(flags);
	const pr = readState((st) => findPR(st, r, pos[0]));
	if (flags.required) fail(`no required checks reported on the '${pr.head}' branch`);
	if (flags.json) {
		print([]);
		return;
	}
	fail(`no checks reported on the '${pr.head}' branch`);
}

// ---- gh api --------------------------------------------------------------

function jq(value, expr) {
	if (!expr) return print(value);
	expr = expr.trim();
	if (expr === '.') return print(value);
	const each = expr.match(/^\.\[\](?:\.([A-Za-z_]\w*))?$/);
	if (each) {
		for (const v of Array.isArray(value) ? value : []) {
			const x = each[1] ? v?.[each[1]] : v;
			print(typeof x === 'string' ? x : JSON.stringify(x));
		}
		return;
	}
	const field = expr.match(/^\.([A-Za-z_]\w*)$/);
	if (field) {
		const x = value?.[field[1]];
		return print(typeof x === 'string' ? x : JSON.stringify(x ?? null));
	}
	fail(`fakegh: --jq ${expr} isn't supported`);
}

function apiUser(login) {
	return { login, id: 1, type: 'User', name: login, html_url: `https://github.com/${login}`, site_admin: false };
}

function api(pos, flags) {
	const endpoint = pos[0];
	if (!endpoint) fail('accepts 1 arg(s), received 0');
	const method = String(flags.method || (flags.f.length || flags.F.length ? 'POST' : 'GET')).toUpperCase();
	if (endpoint === 'graphql') return graphql(flags);
	const p = fill(endpoint, flags).replace(/^\//, '').replace(/\?.*$/, '');
	if (method === 'GET') {
		if (p === 'user') return jq(apiUser(LOGIN), flags.jq);
		if (p === 'user/orgs') return jq([], flags.jq);
		if (/^repos\/[^/]+\/[^/]+\/(pulls\/\d+\/(reviews|comments|requested_reviewers)|issues\/\d+\/(comments|events|timeline)|pulls\/comments|issues\/comments)$/.test(p)) {
			return jq(p.endsWith('requested_reviewers') ? { users: [], teams: [] } : [], flags.jq);
		}
		const m = p.match(/^repos\/([^/]+)\/([^/]+)$/);
		if (m) {
			const r = { owner: m[1], name: m[2] };
			if (!exists(r)) fail('gh: Not Found (HTTP 404)');
			const f = readState((st) => repoFields(st, r));
			return jq({ id: 1, name: f.name, full_name: f.nameWithOwner, private: f.isPrivate, html_url: f.url, default_branch: f.defaultBranchRef.name, owner: apiUser(r.owner) }, flags.jq);
		}
		fail(`gh: Not Found (HTTP 404) — fakegh doesn't know GET ${p}`);
	}
	// Writes (replies, review requests, …) succeed and change nothing.
	if (method === 'DELETE') return;
	return jq({}, flags.jq);
}

function graphql(flags) {
	const vars = fieldMap(flags);
	const query = vars.query || '';
	if (!query) fail('fakegh: api graphql without a query');
	if (/^\s*mutation\b/.test(query)) {
		if (query.includes('resolveReviewThread')) return print({ data: { resolveReviewThread: { thread: { isResolved: true } } } });
		if (query.includes('unresolveReviewThread')) return print({ data: { unresolveReviewThread: { thread: { isResolved: false } } } });
		return print({ data: {} });
	}
	if (/^\s*query\s*\{\s*viewer\s*\{\s*login\s*\}\s*\}\s*$/.test(query)) return print({ data: { viewer: { login: LOGIN } } });
	const r = vars.owner && vars.repo ? { owner: vars.owner, name: vars.repo } : currentRepo(flags);
	if (query.includes('reviewThreads')) {
		return print({ data: { repository: { pullRequest: { reviewThreads: { pageInfo: { hasNextPage: false, endCursor: null }, nodes: [] } } } } });
	}
	const aliases = [...query.matchAll(/(\w+)\s*:\s*pullRequest\s*\(\s*number\s*:\s*(\d+)\s*\)/g)];
	if (aliases.length) {
		const repo = {};
		const errors = [];
		readState((st) => {
			for (const [, alias, num] of aliases) {
				const pr = exists(r) && prsOf(st, r).find((p) => p.number === Number(num));
				if (!pr) {
					repo[alias] = null;
					errors.push({ type: 'NOT_FOUND', path: ['repository', alias], message: `Could not resolve to a PullRequest with the number of ${num}.` });
					continue;
				}
				const f = prFields(r, pr);
				repo[alias] = {
					state: f.state,
					mergeable: f.mergeable,
					headRefOid: f.headRefOid,
					isDraft: f.isDraft,
					reviewDecision: null,
					updatedAt: f.updatedAt,
					mergeCommit: f.mergeCommit,
					commits: { nodes: [{ commit: { statusCheckRollup: null } }] },
				};
			}
		});
		const body = { data: { repository: repo } };
		if (errors.length) {
			body.errors = errors;
			print(body);
			throw new GhError(`GraphQL: ${errors.map((e) => e.message).join(', ')}`);
		}
		return print(body);
	}
	fail(`fakegh: unsupported GraphQL query: ${query.slice(0, 200)}`);
}

// ---- gh repo -------------------------------------------------------------

function listRepos(owner) {
	const dir = path.join(ORIGIN, owner);
	let ents = [];
	try {
		ents = fs.readdirSync(dir, { withFileTypes: true });
	} catch {}
	return ents.filter((e) => e.isDirectory() && e.name.endsWith('.git')).map((e) => ({ owner, name: e.name.slice(0, -4) }));
}

function repoList(pos, flags) {
	const owner = pos[0] || LOGIN;
	const list = readState((st) => listRepos(owner).map((r) => repoFields(st, r)));
	list.sort((a, b) => (a.updatedAt < b.updatedAt ? 1 : -1));
	const limited = list.slice(0, Number(flags.limit || 30));
	if (flags.json) print(limited.map((f) => pick(f, flags.json, 'repository')));
	else for (const f of limited) print(`${f.nameWithOwner}\t${f.description}\t${f.visibility.toLowerCase()}\t${f.updatedAt}`);
}

function repoCreate(pos, flags) {
	if (!pos[0]) fail('name argument required when not running interactively');
	const r = repoOf(pos[0]);
	if (!flags.private && !flags.public && !flags.internal) fail('`--public`, `--private`, or `--internal` required when not running interactively');
	withState((st) => {
		if (exists(r)) fail(`GraphQL: Name already exists on this account (createRepository)`);
		initBare(r);
		const meta = repoMeta(st, r);
		meta.private = !flags.public;
		if (flags.description) meta.description = flags.description;
	});
	if (flags.source) {
		const src = path.resolve(flags.source);
		const remote = flags.remote || 'origin';
		const env = cleanEnv();
		const inSrc = (args, ok) => git(args, { cwd: src, env: src === process.cwd() ? process.env : env, ok });
		if (inSrc(['remote', 'get-url', remote], true).status === 0) fail(`Unable to add remote "${remote}"`);
		inSrc(['remote', 'add', remote, `${webURL(r)}.git`]);
		if (flags.push) {
			// Straight to the bare repository: no url.insteadOf needed here.
			const branch = inSrc(['symbolic-ref', '--short', 'HEAD']);
			inSrc(['push', '-q', bareOf(r), `HEAD:refs/heads/${branch}`]);
			git(['symbolic-ref', 'HEAD', `refs/heads/${branch}`], { cwd: bareOf(r) });
			inSrc(['update-ref', `refs/remotes/${remote}/${branch}`, 'HEAD']);
			inSrc(['branch', `--set-upstream-to=${remote}/${branch}`, branch]);
		}
	}
	print(webURL(r));
}

function repoClone(pos) {
	if (!pos[0]) fail('cannot clone: repository argument required');
	const r = repoOf(pos[0]);
	mustExist(r);
	const dest = path.resolve(pos[1] || r.name);
	git(['clone', '-q', ...pos.slice(2), bareOf(r), dest]);
	git(['remote', 'set-url', 'origin', `${webURL(r)}.git`], { cwd: dest });
}

function repoDelete(pos, flags) {
	const r = pos[0] ? repoOf(pos[0]) : currentRepo(flags);
	if (!flags.yes) fail('--yes required when not running interactively');
	mustExist(r);
	withState((st) => {
		fs.rmSync(bareOf(r), { recursive: true, force: true });
		delete st.repos[full(r)];
		delete st.prs[full(r)];
	});
}

function repoView(pos, flags) {
	const r = pos[0] ? repoOf(pos[0]) : currentRepo(flags);
	mustExist(r);
	const f = readState((st) => repoFields(st, r));
	if (flags.json) print(pick(f, flags.json, 'repository'));
	else print(`${f.nameWithOwner}\n${f.description}\n\nView this repository on GitHub: ${f.url}`);
}

// ---- main ----------------------------------------------------------------

function main(argv) {
	const [cmd, sub, ...rest] = argv;
	const unknown = () => fail(`fakegh: unsupported command: gh ${argv.join(' ')}`);
	if (cmd === '--version' || cmd === 'version') return print('gh version 2.63.0 (fakegh, the demo stand-in)');
	if (cmd === 'api') {
		const { pos, flags } = parse(argv.slice(1));
		return api(pos, flags);
	}
	const { pos, flags } = parse(rest);
	switch (`${cmd} ${sub}`) {
		case 'auth status':
			return;
		case 'auth token':
			return print('gho_fakegh000000000000000000000000000000');
		case 'auth git-credential': {
			if (pos[0] === 'get') {
				print(`protocol=https\nhost=github.com\nusername=${LOGIN}\npassword=gho_fakegh000000000000000000000000000000`);
			}
			return;
		}
		case 'auth setup-git':
			return;
		case 'repo list':
			return repoList(pos, flags);
		case 'repo create':
			return repoCreate(pos, flags);
		case 'repo clone':
			return repoClone(pos, flags);
		case 'repo delete':
			return repoDelete(pos, flags);
		case 'repo view':
			return repoView(pos, flags);
		case 'pr list':
			return prList(flags);
		case 'pr create':
			return prCreate(flags);
		case 'pr ready':
			return prReady(pos, flags);
		case 'pr close':
			return prClose(pos, flags);
		case 'pr comment':
			return prComment(pos, flags);
		case 'pr merge':
			return prMerge(pos, flags);
		case 'pr view':
			return prView(pos, flags);
		case 'pr checks':
			return prChecks(pos, flags);
		case 'run list':
			if (flags.json) return print([]);
			return;
		case 'release view':
			return fail('release not found');
	}
	unknown();
}

const argv = process.argv.slice(2);
let code = 0;
let error = '';
try {
	main(argv);
} catch (e) {
	code = e instanceof GhError ? e.code : 1;
	error = e instanceof GhError ? e.message : String(e?.stack || e);
	process.stderr.write(error + '\n');
}
log({ args: argv, cwd: process.cwd(), code, ...(error ? { error } : {}) });
process.exitCode = code;
