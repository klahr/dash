// Star Catcher, the game the tutorial videos build (scripts/tutorials/series):
// what fakeclaude.mjs "does" for each of its tasks, the coordinator's plans,
// the two brief interviews and the project chat's answer. Each task's change
// applies to main as the task before it left it, in the order the videos
// queue them; tasks that run side by side touch different files, except
// "lives" and "high score", which both add a line to the HUD in main.js, so
// the board shows a conflict between them (video 7).
//
// A task is {key, title, short, summary, run(write, edit), slow?, rework?}:
// key is a word in its prompt, rework a second change for a rework run
// whose notes match rework.key.

const file = {};

file['package.json'] = `{
  "name": "star-catcher",
  "private": true,
  "type": "module",
  "scripts": {
    "start": "node server.js",
    "test": "node --test"
  }
}
`;

file['server.js'] = `// A small static file server for the game: node server.js, on $PORT (8080).
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { extname, join, normalize } from 'node:path';

const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml' };
const root = import.meta.dirname;

createServer(async (req, res) => {
  const path = normalize(decodeURIComponent(new URL(req.url, 'http://x').pathname)).replace(/^([/\\\\.])+/, '') || 'index.html';
  try {
    const body = await readFile(join(root, path));
    res.writeHead(200, { 'Content-Type': types[extname(path)] || 'application/octet-stream' });
    res.end(body);
  } catch {
    res.writeHead(404).end('not found');
  }
}).listen(Number(process.env.PORT) || 8080);
`;

file['index.html'] = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Star Catcher</title>
  <link rel="stylesheet" href="style.css">
</head>
<body>
  <canvas id="game" width="800" height="500" aria-label="Star Catcher"></canvas>
  <script type="module" src="src/main.js"></script>
</body>
</html>
`;

file['style.css'] = `html, body { height: 100%; margin: 0; }
body { display: grid; place-items: center; background: #0b1026; }
canvas { max-width: 100vw; max-height: 100vh; border-radius: 12px; box-shadow: 0 20px 60px rgba(0, 0, 0, .5); }
`;

file['src/game.js'] = `// The game's state, and one step of it. Drawing happens in main.js.
import { createBasket, updateBasket } from './basket.js';
import { updateStars } from './stars.js';

export const WIDTH = 800;
export const HEIGHT = 500;
export const GROUND = 450;

export function createGame(seed = 7) {
  return { time: 0, rng: random(seed), basket: createBasket(), stars: [], spawnIn: 0.4, caught: 0 };
}

// step moves the game on by dt seconds, with the keys held down.
export function step(game, dt, keys) {
  game.time += dt;
  updateBasket(game.basket, dt, keys);
  updateStars(game, dt);
}

// random is a small seeded random number generator (mulberry32), so a game
// plays the same way each time it starts.
export function random(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
`;

file['src/events.js'] = `// Game events, such as a star caught or a rock hit, for whoever listens.
const listeners = {};

export function listen(name, fn) {
  (listeners[name] ||= []).push(fn);
}

export function emit(name, game) {
  for (const fn of listeners[name] || []) fn(game);
}
`;

file['src/draw.js'] = `// The sky and the ground.
import { WIDTH, HEIGHT, GROUND } from './game.js';

const far = Array.from({ length: 60 }, (_, i) => [(i * 137) % WIDTH, (i * 89) % (GROUND - 40), (i % 3) + 1]);

export function drawSky(ctx, time) {
  const g = ctx.createLinearGradient(0, 0, 0, HEIGHT);
  g.addColorStop(0, '#0b1026');
  g.addColorStop(1, '#2b3a78');
  ctx.fillStyle = g;
  ctx.fillRect(0, 0, WIDTH, HEIGHT);
  for (const [x, y, r] of far) {
    ctx.globalAlpha = 0.35 + 0.25 * Math.sin(time * 2 + x);
    ctx.fillStyle = '#ffffff';
    ctx.fillRect(x, y, r, r);
  }
  ctx.globalAlpha = 1;
}

export function drawGround(ctx) {
  ctx.fillStyle = '#1d6b4f';
  ctx.fillRect(0, GROUND, WIDTH, HEIGHT - GROUND);
  ctx.fillStyle = '#2f8f68';
  ctx.fillRect(0, GROUND, WIDTH, 6);
}
`;

file['src/input.js'] = `// The keys the player holds down.
export const keys = { left: false, right: false };
`;

const basketStub = `// The basket the player moves along the ground.
import { WIDTH, GROUND } from './game.js';

export const BASKET_WIDTH = 110;

export function createBasket() {
  return { x: WIDTH / 2, y: GROUND - 34 };
}

export function updateBasket(basket, dt, keys) {}

export function drawBasket(ctx, basket) {
  const { x, y } = basket, w = BASKET_WIDTH;
  ctx.fillStyle = '#b8743a';
  ctx.beginPath();
  ctx.moveTo(x - w / 2, y);
  ctx.lineTo(x + w / 2, y);
  ctx.lineTo(x + w / 2 - 14, y + 34);
  ctx.lineTo(x - w / 2 + 14, y + 34);
  ctx.closePath();
  ctx.fill();
  ctx.strokeStyle = '#8a5226';
  ctx.lineWidth = 3;
  for (let i = 1; i < 4; i++) {
    ctx.beginPath();
    ctx.moveTo(x - w / 2 + 6 + i * 2, y + i * 8.5);
    ctx.lineTo(x + w / 2 - 6 - i * 2, y + i * 8.5);
    ctx.stroke();
  }
}
`;
file['src/basket.js'] = basketStub;

file['src/stars.js'] = `// The stars that fall from the sky.
export function updateStars(game, dt) {}

export function drawStars(ctx, stars) {
  for (const s of stars) drawStar(ctx, s.x, s.y, s.r, s.spin);
}

function drawStar(ctx, x, y, r, spin) {
  ctx.fillStyle = '#ffd84a';
  ctx.beginPath();
  for (let i = 0; i < 10; i++) {
    const a = spin + (i * Math.PI) / 5 - Math.PI / 2;
    const d = i % 2 ? r * 0.45 : r;
    ctx.lineTo(x + Math.cos(a) * d, y + Math.sin(a) * d);
  }
  ctx.closePath();
  ctx.fill();
}
`;

file['src/main.js'] = `// Runs the game: a step and a frame, about 60 times a second.
import { createGame, step } from './game.js';
import { drawSky, drawGround } from './draw.js';
import { drawBasket } from './basket.js';
import { drawStars } from './stars.js';
import { keys } from './input.js';

const ctx = document.getElementById('game').getContext('2d');
const game = createGame();
let last = performance.now();

function frame(now) {
  step(game, Math.min(0.05, (now - last) / 1000), keys);
  last = now;
  drawSky(ctx, game.time);
  drawStars(ctx, game.stars);
  drawBasket(ctx, game.basket);
  drawGround(ctx);
  requestAnimationFrame(frame);
}
requestAnimationFrame(frame);
`;

file['test/game.test.js'] = `import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createGame, step, random, WIDTH } from '../src/game.js';

test('a new game starts with the basket in the middle', () => {
  const game = createGame();
  assert.equal(game.basket.x, WIDTH / 2);
  assert.equal(game.caught, 0);
});

test('the random numbers repeat for the same seed', () => {
  const a = random(3), b = random(3);
  assert.deepEqual([a(), a(), a()], [b(), b(), b()]);
});

test('a step moves time on', () => {
  const game = createGame();
  step(game, 0.5, { left: false, right: false });
  assert.equal(game.time, 0.5);
});
`;

const BRIEF = `# Star Catcher

A browser game: move a basket with the arrow keys to catch falling stars, and dodge the rocks.

## Users & purpose
Anyone with a browser, for a few minutes of fun. No sign-up.

## Core features
- A basket moves left and right along the ground with the arrow keys.
- Stars fall from the sky; catching one scores a point.
- Three lives, and the game gets faster over time.

## Data & storage
None on a server. The best score is kept in the browser.

## Platform & stack
Plain JavaScript and a canvas, no build step and no dependencies. \`node server.js\` serves it; \`node --test\` tests the game logic.

## Auth & accounts
None.

## Deployment & hosting
Static files on any host.

## Out of scope
Accounts, online leaderboards, music.
`;

export const brief = BRIEF;
export const claudeMD = `# Star Catcher

A browser game in plain JavaScript and a canvas. See BRIEF.md.

- Run it: \`node server.js\` (port \`$PORT\`, 8080 by default).
- Test it: \`node --test\`. Game logic lives in \`src/\` without the DOM, so it can be tested.
- No build step and no dependencies.
`;

// The tasks of the first version (video 3's plan): set up, then basket and
// stars side by side, then catching.
export const tasks = [
  {
    key: /set up the page/i, slow: true, title: 'Set up the page and the canvas', short: 'The page, the canvas and the game loop',
    summary: 'Set up the game: `index.html` with a canvas, a game loop in `src/main.js`, the sky and the ground, a static server (`node server.js`) and the first tests.',
    run(w) { for (const [f, t] of Object.entries(file)) w(f, t); },
  },
  {
    key: /move the basket (with the arrow keys|along)/i, title: 'Move the basket with the arrow keys', short: 'The basket follows the arrow keys',
    summary: 'The basket now moves with the left and right arrow keys, and stops at the edges. The tests cover both.',
    run(w, e) {
      w('src/input.js', `// The keys the player holds down.
export const keys = { left: false, right: false };

const names = { ArrowLeft: 'left', ArrowRight: 'right' };
for (const [type, down] of [['keydown', true], ['keyup', false]]) {
  addEventListener(type, (e) => {
    if (!names[e.key]) return;
    keys[names[e.key]] = down;
    e.preventDefault();
  });
}
`);
      e('src/basket.js', "import { WIDTH, GROUND } from './game.js';\n\nexport const BASKET_WIDTH = 110;\n",
        "import { WIDTH, GROUND } from './game.js';\n\nexport const BASKET_WIDTH = 110;\nexport const BASKET_SPEED = 420; // pixels a second\n");
      e('src/basket.js', 'export function updateBasket(basket, dt, keys) {}\n', `export function updateBasket(basket, dt, keys) {
  const dir = (keys.right ? 1 : 0) - (keys.left ? 1 : 0);
  const half = BASKET_WIDTH / 2;
  basket.x = Math.min(WIDTH - half, Math.max(half, basket.x + dir * BASKET_SPEED * dt));
}
`);
      w('test/basket.test.js', `import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createBasket, updateBasket, BASKET_WIDTH } from '../src/basket.js';

test('the arrow keys move the basket', () => {
  const b = createBasket(), x = b.x;
  updateBasket(b, 0.1, { right: true });
  assert.ok(b.x > x);
  updateBasket(b, 0.2, { left: true });
  assert.ok(b.x < x);
});

test('the basket stops at the edge', () => {
  const b = createBasket();
  updateBasket(b, 10, { left: true });
  assert.equal(b.x, BASKET_WIDTH / 2);
});
`);
    },
  },
  {
    key: /make stars fall/i, title: 'Make stars fall', short: 'Stars fall from the sky',
    summary: 'Stars now fall from the sky at random places, spinning a little, and leave once they reach the ground. The first few fall near the middle, so a new player catches one early.',
    run(w, e) {
      e('src/stars.js', '// The stars that fall from the sky.\nexport function updateStars(game, dt) {}\n', `// The stars that fall from the sky.
import { WIDTH, GROUND } from './game.js';

export const STAR_SPEED = 150; // pixels a second
const EVERY = 0.9; // seconds between two stars

export function updateStars(game, dt) {
  game.spawnIn -= dt;
  if (game.spawnIn <= 0) {
    game.spawnIn = EVERY;
    // The first three fall near the middle: an easy start.
    const spread = game.time < 3 ? 0.15 : 0.9;
    const x = WIDTH / 2 + (game.rng() - 0.5) * spread * WIDTH;
    game.stars.push({ x, y: -20, r: 16, spin: 0 });
  }
  for (const s of game.stars) {
    s.y += STAR_SPEED * dt;
    s.spin += dt;
  }
  game.stars = game.stars.filter((s) => s.y < GROUND + s.r);
}
`);
      w('test/stars.test.js', `import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createGame } from '../src/game.js';
import { updateStars } from '../src/stars.js';

test('stars appear and fall', () => {
  const game = createGame();
  updateStars(game, 0.5);
  assert.equal(game.stars.length, 1);
  const y = game.stars[0].y;
  updateStars(game, 0.1);
  assert.ok(game.stars[0].y > y);
});

test('a star leaves at the ground', () => {
  const game = createGame();
  updateStars(game, 0.5);
  for (let i = 0; i < 40; i++) updateStars(game, 0.1);
  assert.ok(game.stars.every((s) => s.y < 500));
});
`);
    },
  },
  {
    key: /catch stars/i, title: 'Catch stars in the basket', short: 'A star in the basket is caught',
    summary: 'A star that lands in the basket is caught: it disappears and counts in `game.caught`, and a `catch` event is sent for sounds or effects later.',
    run(w, e) {
      w('src/catch.js', `// A star that reaches the basket is caught.
import { BASKET_WIDTH } from './basket.js';
import { emit } from './events.js';

export function catchStars(game) {
  const { x, y } = game.basket;
  const inBasket = (s) => s.y + s.r * 0.5 >= y && s.y - s.r < y + 20 && Math.abs(s.x - x) < BASKET_WIDTH / 2;
  const caught = game.stars.filter(inBasket);
  if (!caught.length) return;
  game.stars = game.stars.filter((s) => !inBasket(s));
  for (let i = 0; i < caught.length; i++) {
    game.caught++;
    emit('catch', game);
  }
}
`);
      e('src/game.js', "import { updateStars } from './stars.js';\n", "import { updateStars } from './stars.js';\nimport { catchStars } from './catch.js';\n");
      e('src/game.js', '  updateStars(game, dt);\n}', '  updateStars(game, dt);\n  catchStars(game);\n}');
      w('test/catch.test.js', `import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createGame } from '../src/game.js';
import { catchStars } from '../src/catch.js';

test('a star in the basket is caught', () => {
  const game = createGame();
  game.stars.push({ x: game.basket.x, y: game.basket.y, r: 16, spin: 0 });
  catchStars(game);
  assert.equal(game.caught, 1);
  assert.equal(game.stars.length, 0);
});

test('a star beside the basket falls past', () => {
  const game = createGame();
  game.stars.push({ x: game.basket.x + 200, y: game.basket.y, r: 16, spin: 0 });
  catchStars(game);
  assert.equal(game.caught, 0);
});
`);
    },
  },
  // Video 5.
  {
    key: /show the score/i, slow: true, title: 'Show the score', short: 'The score in the top left corner',
    summary: 'The score now shows in the top left corner: one point for each star caught.',
    run(w, e) {
      w('src/score.js', `// The score, in the top left corner.
export function drawScore(ctx, game) {
  ctx.font = '600 24px system-ui, sans-serif';
  ctx.fillStyle = '#ffffff';
  ctx.textBaseline = 'top';
  ctx.fillText(\`★ \${game.caught}\`, 20, 18);
}
`);
      e('src/main.js', "import { keys } from './input.js';\n", "import { keys } from './input.js';\nimport { drawScore } from './score.js';\n");
      e('src/main.js', '  drawGround(ctx);\n', '  drawGround(ctx);\n  drawScore(ctx, game);\n');
    },
  },
  // Video 6: far too fast at first; the rework slows them down.
  {
    key: /rocks/i, slow: true, title: 'Add falling rocks to dodge', short: 'Rocks fall too: dodge them',
    summary: 'Rocks now fall among the stars. A rock in the basket costs three stars and shakes the screen, and a `hit` event is sent.',
    run(w, e) {
      w('src/rocks.js', `// Rocks fall among the stars: dodge them.
import { WIDTH, GROUND } from './game.js';
import { BASKET_WIDTH } from './basket.js';
import { emit } from './events.js';

const ROCK_SPEED = 900; // pixels a second
const EVERY = 2.2; // seconds between two rocks

export function updateRocks(game, dt) {
  game.rocks ||= [];
  game.rockIn = (game.rockIn ?? EVERY) - dt;
  if (game.rockIn <= 0) {
    game.rockIn = EVERY;
    game.rocks.push({ x: 40 + game.rng() * (WIDTH - 80), y: -20, r: 18 });
  }
  for (const r of game.rocks) r.y += rockSpeed(game) * dt;
  const { x, y } = game.basket;
  const hit = (r) => r.y + r.r >= y && Math.abs(r.x - x) < BASKET_WIDTH / 2;
  for (const r of game.rocks.filter(hit)) {
    game.caught = Math.max(0, game.caught - 3);
    emit('hit', game);
  }
  game.rocks = game.rocks.filter((r) => !hit(r) && r.y < GROUND + r.r);
}

export function rockSpeed(game) {
  return ROCK_SPEED;
}

export function drawRocks(ctx, rocks = []) {
  ctx.fillStyle = '#7d7f8c';
  for (const r of rocks) {
    ctx.beginPath();
    ctx.ellipse(r.x, r.y, r.r, r.r * 0.8, 0.4, 0, Math.PI * 2);
    ctx.fill();
  }
}
`);
      e('src/game.js', "import { catchStars } from './catch.js';\n", "import { catchStars } from './catch.js';\nimport { updateRocks } from './rocks.js';\n");
      e('src/game.js', '  catchStars(game);\n}', '  catchStars(game);\n  updateRocks(game, dt);\n}');
      e('src/main.js', "import { drawStars } from './stars.js';\n", "import { drawStars } from './stars.js';\nimport { drawRocks } from './rocks.js';\n");
      e('src/main.js', '  drawStars(ctx, game.stars);\n', '  drawStars(ctx, game.stars);\n  drawRocks(ctx, game.rocks);\n');
      w('test/rocks.test.js', `import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createGame } from '../src/game.js';
import { updateRocks } from '../src/rocks.js';

test('a rock in the basket costs three stars', () => {
  const game = createGame();
  game.caught = 5;
  game.rocks = [{ x: game.basket.x, y: game.basket.y, r: 18 }];
  updateRocks(game, 0.01);
  assert.equal(game.caught, 2);
});
`);
    },
    rework: {
      // A run on the rocks task once rocks.js exists is the rework.
      applied: 'src/rocks.js',
      key: /speed|slow|fast/i,
      summary: 'Rocks now start slow, at a third of the old speed, and get faster with each level: `rockSpeed` reads the level, and the tests check the first level\'s speed.',
      run(w, e) {
        e('src/rocks.js', 'const ROCK_SPEED = 900; // pixels a second\n', 'const ROCK_SPEED = 300; // pixels a second, on the first level\n');
        e('src/rocks.js', 'export function rockSpeed(game) {\n  return ROCK_SPEED;\n}', '// rockSpeed is how fast rocks fall: slow at first, 15% faster each level.\nexport function rockSpeed(game) {\n  return ROCK_SPEED * 1.15 ** ((game.level || 1) - 1);\n}');
        w('test/rocks.test.js', `import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createGame } from '../src/game.js';
import { updateRocks, rockSpeed } from '../src/rocks.js';

test('a rock in the basket costs three stars', () => {
  const game = createGame();
  game.caught = 5;
  game.rocks = [{ x: game.basket.x, y: game.basket.y, r: 18 }];
  updateRocks(game, 0.01);
  assert.equal(game.caught, 2);
});

test('rocks start slow and speed up with the level', () => {
  assert.equal(rockSpeed({ level: 1 }), 300);
  assert.ok(rockSpeed({ level: 3 }) > rockSpeed({ level: 2 }));
});
`);
      },
    },
  },
  // Video 7: three at once. Lives and high score both add a line to the HUD.
  {
    key: /sound/i, slow: true, title: 'Sounds for stars and rocks', short: 'A chime for a star, a thud for a rock',
    summary: 'A short chime plays when a star is caught, and a low thud when a rock hits, made with the Web Audio API: no sound files.',
    run(w, e) {
      w('src/sound.js', `// A chime for a star, a thud for a rock, made with the Web Audio API.
import { listen } from './events.js';

let audio;
function tone(freq, seconds, type) {
  audio ||= new AudioContext();
  const osc = audio.createOscillator(), gain = audio.createGain();
  osc.type = type;
  osc.frequency.value = freq;
  gain.gain.setValueAtTime(0.2, audio.currentTime);
  gain.gain.exponentialRampToValueAtTime(0.001, audio.currentTime + seconds);
  osc.connect(gain).connect(audio.destination);
  osc.start();
  osc.stop(audio.currentTime + seconds);
}

listen('catch', () => tone(880, 0.15, 'triangle'));
listen('hit', () => tone(110, 0.3, 'square'));
`);
      e('index.html', '  <script type="module" src="src/main.js"></script>\n', '  <script type="module" src="src/main.js"></script>\n  <script type="module" src="src/sound.js"></script>\n');
    },
  },
  {
    key: /lives|hearts/i, slow: true, title: 'Three lives', short: 'Three hearts; a rock costs one',
    summary: 'The player has three lives, shown as hearts in the top right. A rock costs one, and losing the last starts a new game.',
    run(w, e) {
      w('src/lives.js', `// Three lives, shown as hearts. A rock costs one; the last one starts over.
import { listen } from './events.js';

export const LIVES = 3;

listen('hit', (game) => {
  game.lives = (game.lives ?? LIVES) - 1;
  if (game.lives <= 0) Object.assign(game, { lives: LIVES, caught: 0, stars: [], rocks: [] });
});

export function drawLives(ctx, game) {
  ctx.font = '24px system-ui, sans-serif';
  ctx.textAlign = 'right';
  ctx.textBaseline = 'top';
  ctx.fillStyle = '#ff5d73';
  ctx.fillText('♥'.repeat(game.lives ?? LIVES), 780, 18);
  ctx.textAlign = 'left';
}
`);
      e('src/main.js', "import { drawScore } from './score.js';\n", "import { drawScore } from './score.js';\nimport { drawLives } from './lives.js';\n");
      e('src/main.js', '  drawScore(ctx, game);\n', '  drawScore(ctx, game);\n  drawLives(ctx, game);\n');
    },
  },
  {
    key: /best score|high score/i, slow: true, title: 'Keep the best score', short: 'The best score, kept in the browser',
    summary: 'The best score is kept in the browser (`localStorage`) and shows under the score, so it survives a reload.',
    run(w, e) {
      w('src/best.js', `// The best score, kept in the browser.
const KEY = 'star-catcher.best';

function read() {
  try { return Number(localStorage.getItem(KEY)) || 0; } catch { return 0; }
}

let best = read();

export function drawBest(ctx, game) {
  if (game.caught > best) {
    best = game.caught;
    try { localStorage.setItem(KEY, String(best)); } catch { /* private mode */ }
  }
  ctx.font = '16px system-ui, sans-serif';
  ctx.fillStyle = 'rgba(255, 255, 255, .7)';
  ctx.textBaseline = 'top';
  ctx.fillText(\`Best \${best}\`, 22, 50);
}
`);
      e('src/main.js', "import { drawScore } from './score.js';\n", "import { drawScore } from './score.js';\nimport { drawBest } from './best.js';\n");
      e('src/main.js', '  drawScore(ctx, game);\n', '  drawScore(ctx, game);\n  drawBest(ctx, game);\n');
    },
  },
  // Video 8's goal: levels.
  {
    key: /work out the level/i, title: 'Work out the level', short: 'A level for every 20 stars',
    summary: 'The game now has levels: one more for every 20 stars caught, in `game.level`.',
    run(w, e) {
      w('src/levels.js', `// A level for every 20 stars caught.
export const STARS_PER_LEVEL = 20;

export function levelFor(caught) {
  return 1 + Math.floor(caught / STARS_PER_LEVEL);
}
`);
      e('src/game.js', "import { updateRocks } from './rocks.js';\n", "import { updateRocks } from './rocks.js';\nimport { levelFor } from './levels.js';\n");
      e('src/game.js', '  updateRocks(game, dt);\n}', '  updateRocks(game, dt);\n  game.level = levelFor(game.caught);\n}');
      w('test/levels.test.js', `import { test } from 'node:test';
import assert from 'node:assert/strict';
import { levelFor } from '../src/levels.js';

test('a level for every 20 stars', () => {
  assert.equal(levelFor(0), 1);
  assert.equal(levelFor(19), 1);
  assert.equal(levelFor(20), 2);
});
`);
    },
  },
  {
    key: /speed up each level/i, title: 'Speed up each level', short: 'Stars fall faster each level',
    summary: 'Stars fall 10% faster with each level; rocks already speed up by the level.',
    run(w, e) {
      e('src/stars.js', '    s.y += STAR_SPEED * dt;\n', '    s.y += STAR_SPEED * 1.1 ** ((game.level || 1) - 1) * dt;\n');
    },
  },
  {
    key: /show the level/i, title: 'Show the level', short: 'The level, at the top in the middle',
    summary: 'The level shows at the top in the middle, and flashes for a moment when it goes up.',
    run(w, e) {
      w('src/level-hud.js', `// The level, at the top in the middle.
export function drawLevel(ctx, game) {
  ctx.font = '600 20px system-ui, sans-serif';
  ctx.textAlign = 'center';
  ctx.textBaseline = 'top';
  ctx.fillStyle = '#9fd3ff';
  ctx.fillText(\`Level \${game.level || 1}\`, 400, 20);
  ctx.textAlign = 'left';
}
`);
      e('src/main.js', "import { drawScore } from './score.js';\n", "import { drawScore } from './score.js';\nimport { drawLevel } from './level-hud.js';\n");
      e('src/main.js', '  drawScore(ctx, game);\n', '  drawScore(ctx, game);\n  drawLevel(ctx, game);\n');
    },
  },
  // Video 9: from the project chat.
  {
    key: /finger|touch/i, title: 'Move the basket with a finger', short: 'Touch controls for phones',
    summary: 'On a phone the basket now follows your finger: drag anywhere on the game. The arrow keys work as before.',
    run(w, e) {
      w('src/input.js', touchInput());
      e('src/basket.js', '  const dir = (keys.right ? 1 : 0) - (keys.left ? 1 : 0);\n', '  let dir = (keys.right ? 1 : 0) - (keys.left ? 1 : 0);\n  // A finger on the screen: head for it.\n  if (keys.target != null && Math.abs(keys.target - basket.x) > 4) dir = Math.sign(keys.target - basket.x);\n');
    },
  },
  // Video 10's starting state: a deploy script.
  {
    key: /deploy script/i, title: 'Add a deploy script', short: 'deploy.sh copies the game to the site',
    summary: 'Added `deploy.sh`: it copies the game\'s files to `$SITE_DIR`, the folder the web server serves.',
    run(w) {
      w('deploy.sh', `#!/bin/sh
# Copies the game to the folder the web server serves ($SITE_DIR).
set -eu
mkdir -p "$SITE_DIR"
cp -r index.html style.css src "$SITE_DIR"/
echo "deployed to $SITE_DIR"
`, 0o755);
    },
  },
];

// touchInput is the touch task's input.js, whole.
function touchInput() {
  return `// The keys the player holds down, and a finger on the screen.
export const keys = { left: false, right: false, target: null };

const names = { ArrowLeft: 'left', ArrowRight: 'right' };
for (const [type, down] of [['keydown', true], ['keyup', false]]) {
  addEventListener(type, (e) => {
    if (!names[e.key]) return;
    keys[names[e.key]] = down;
    e.preventDefault();
  });
}

// A finger (or the mouse, held down) on the game: the basket heads for it.
const canvas = document.getElementById('game');
const toGame = (e) => ((e.clientX - canvas.getBoundingClientRect().left) / canvas.clientWidth) * canvas.width;
canvas.style.touchAction = 'none';
canvas.addEventListener('pointerdown', (e) => { keys.target = toGame(e); canvas.setPointerCapture(e.pointerId); });
canvas.addEventListener('pointermove', (e) => { if (e.buttons) keys.target = toGame(e); });
for (const t of ['pointerup', 'pointercancel']) canvas.addEventListener(t, () => { keys.target = null; });
`;
}

// The coordinator's plans, by words in the goal's title.
export const plans = [
  {
    goal: /first version/i,
    why: 'The brief asks for a basket, falling stars and catching them. Set up the page first; the basket and the stars don\'t depend on each other, so they can run side by side; catching needs both.',
    tasks: [
      { title: 'Set up the page and the canvas', prompt: 'Set up the page: index.html with an 800×500 canvas, a game loop in src/main.js, the sky and the ground, a static server (node server.js on $PORT) and node --test tests for the game logic.' },
      { title: 'Move the basket with the arrow keys', prompt: 'Move the basket along the ground with the left and right arrow keys; stop it at the edges.', after: [0] },
      { title: 'Make stars fall', prompt: 'Make stars fall from the sky at random places and leave at the ground. Let the first few fall near the middle.', after: [0] },
      { title: 'Catch stars in the basket', prompt: 'Catch stars in the basket: a star that lands in it disappears and counts as caught.', after: [1, 2] },
    ],
  },
  {
    goal: /levels/i,
    why: 'Work out the level from the stars caught first; then speeding up and showing the level are independent, so they can run side by side.',
    tasks: [
      { title: 'Work out the level', prompt: 'Work out the level from the stars caught: one more for every 20 stars, in game.level.' },
      { title: 'Speed up each level', prompt: 'Speed up each level: stars should fall 10% faster on each one (the rock speed already follows the level).', after: [0] },
      { title: 'Show the level', prompt: 'Show the level at the top in the middle of the screen.', after: [0] },
    ],
  },
];

// The brief interviews: a project's (video 2) and a feature's (video 8), a
// round per call. The brief takes the answers, or the suggestions.
export const interviews = {
  project: {
    rounds: [
      {
        note: 'I wrote a first brief from your idea. A few questions about who it\'s for and how it\'s built.',
        questions: [
          { id: 'r1q1', topic: 'stack', text: 'What should it be built with?', default: 'Plain JavaScript and a canvas', options: ['Phaser', 'React and SVG'] },
          { id: 'r1q2', topic: 'features', text: 'How does a game end?', default: 'Three lives; a rock costs one', options: ['A two-minute timer', 'It never ends'] },
          { id: 'r1q3', topic: 'users', text: 'Who is it for?', default: 'Anyone with a browser, no sign-up', options: ['Children', 'My friends'] },
        ],
        settled: ['users', 'stack'],
      },
      {
        note: 'Thanks. Two more, about saving and putting it online.',
        questions: [
          { id: 'r2q1', topic: 'data', text: 'Should it remember anything between games?', default: 'The best score, in the browser', options: ['Nothing'] },
          { id: 'r2q2', topic: 'deploy', text: 'Where will it be published?', default: 'Static files on any host', options: ['GitHub Pages'] },
        ],
        settled: ['features', 'data', 'auth', 'deploy'],
      },
      { note: 'That\'s enough to build the first version. Review the brief, or tell me what to change.', questions: [], settled: ['scope'], ready: true },
    ],
    topics: ['users', 'features', 'data', 'stack', 'auth', 'deploy', 'scope'],
    brief: () => BRIEF,
    open: 'Online leaderboards',
  },
  feature: {
    rounds: [
      {
        note: 'I read the game\'s code: rocks already speed up by `game.level`, but nothing sets it yet. A few questions about levels.',
        questions: [
          { id: 'r1q1', topic: 'behaviour', text: 'When does the next level start?', default: 'Every 10 stars', options: ['Every 20 stars', 'Every 30 seconds'] },
          { id: 'r1q2', topic: 'behaviour', text: 'What gets harder each level?', default: 'Stars and rocks fall faster', options: ['More rocks', 'A smaller basket'] },
          { id: 'r1q3', topic: 'surface', text: 'How does the player see the level?', default: 'At the top, in the middle', options: ['A banner when it changes'] },
        ],
        settled: ['problem', 'behaviour', 'surface'],
      },
      { note: 'That\'s enough to build it. Review the brief, and choose how to start.', questions: [], settled: ['data', 'edges', 'done', 'scope'], ready: true },
    ],
    topics: ['problem', 'behaviour', 'surface', 'data', 'edges', 'done', 'scope'],
    brief: (a) => `# Levels

The game gets harder as you go: a new level ${a('r1q1').toLowerCase()} caught.

## Problem & who it's for
Players master the first speed in a minute, then the game gets dull.

## Behaviour & scope
- A new level: ${a('r1q1').toLowerCase()}.
- Each level: ${a('r1q2').toLowerCase()}.

## Where it shows
${a('r1q3')}.

## Data changes
None: the level follows from the stars caught.

## Edge cases & errors
Losing the last life starts again at level 1.

## Done when
The level goes up, the game speeds up, and the tests cover both.

## Out of scope
Level select, bosses.
`,
    open: 'Whether the basket shrinks too',
  },
};

// The project chat's answer to "can people play this on a phone?" (video 9).
export const chat = {
  key: /phone|mobile|touch/i,
  answer: 'Not yet: the basket only listens to the arrow keys (`src/input.js`), and a phone has none. Letting the basket follow your finger is a small change: listen for pointer events on the canvas and steer the basket toward the finger. I drafted it as a task, so you can queue it.',
  task: { title: 'Move the basket with a finger', prompt: 'On a phone there are no arrow keys: let the basket follow a finger on the canvas (pointer events), and keep the arrow keys working.' },
};

// keepBoth resolves git's conflict markers in text by keeping both sides,
// ours first: right for two tasks that each added a line in the same place.
export function keepBoth(text) {
  return text.replace(/^<<<<<<< [^\n]*\n([\s\S]*?)^(?:\|\|\|\|\|\|\| [^\n]*\n[\s\S]*?)?^=======\n([\s\S]*?)^>>>>>>> [^\n]*\n/gm, '$1$2');
}
