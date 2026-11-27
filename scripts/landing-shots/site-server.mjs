// The game's "host" for video 10 (Publish the game): serves $SITE_DIR, where
// deploy.sh copies the game, for any host name, with no DNS:
// - over HTTP on <port>, which the runner's health check reaches
//   http://star-catcher.r8.rs through as a proxy (HTTP_PROXY);
// - over HTTPS on <tls-port> with a self-signed certificate, which the
//   recording's Chrome maps star-catcher.r8.rs to (--host-resolver-rules,
//   --ignore-certificate-errors): Chrome blocks plain-HTTP public sites.
//   node site-server.mjs <dir> <port> [<tls-port> <cert> <key>]
import { createServer } from 'node:http';
import { createServer as createTLSServer } from 'node:https';
import { readFileSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
import { extname, join, normalize } from 'node:path';

const [dir, port, tlsPort, cert, key] = process.argv.slice(2);
const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css' };
async function serve(req, res) {
  // A proxy request has the whole URL; a direct one only the path.
  const path = normalize(decodeURIComponent(new URL(req.url, 'http://x').pathname)).replace(/^([/\\.])+/, '') || 'index.html';
  try {
    const body = await readFile(join(dir, path));
    res.writeHead(200, { 'Content-Type': types[extname(path)] || 'application/octet-stream' });
    res.end(body);
  } catch {
    res.writeHead(404, { 'Content-Type': 'text/plain' }).end('Nothing deployed here yet.');
  }
}
createServer(serve).listen(Number(port), '127.0.0.1');
if (tlsPort) createTLSServer({ cert: readFileSync(cert), key: readFileSync(key) }, serve).listen(Number(tlsPort), '127.0.0.1');
