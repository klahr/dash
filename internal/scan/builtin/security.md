---
name: security
title: Security scan
kind: security
scope: since_last_scan
# Every 30 days a run looks at the whole repository again, not only the
# changes since the last run.
full_scan_days: 30
effort: high
# Suggested: daily at 06:00. Built-in schedules are off until turned on.
schedule: 0 6 * * *
---
Do a security review of the code in {{path}}: find vulnerabilities an
attacker could actually exploit, and report them.

## How to work

1. Find the trust boundaries first: HTTP handlers and routes, RPC and
   webhook endpoints, CLI arguments and config read from untrusted places,
   file and archive parsers, message consumers, and templates that render
   user data. Note who can reach each one (anyone, a logged-in user, an
   admin, only the local machine).
2. Follow untrusted input from where it enters to where it is used (a
   query, a shell command, a file path, an outgoing URL, HTML, a
   redirect, a deserializer) and check what validates, escapes or
   authorizes it on the way.
3. Read the framework's defaults before reporting: an ORM that
   parameterizes, a template engine that escapes, a router that checks
   methods, a middleware that already adds a header or a CSRF token. Look
   for where the middleware is installed, not only for its code.
4. Confirm each finding by reading the code on the whole path. If a check
   elsewhere stops the attack, it is not a finding.

## What to check

- Injection: SQL and NoSQL, OS command, path traversal (including zip
  slip and symlinks), server-side template injection, LDAP, XPath, header
  and log injection (CRLF), and code evaluated from input.
- Authentication: missing or bypassable login checks, weak session or
  token handling (predictable tokens, no expiry, tokens in URLs or logs,
  missing `Secure` / `HttpOnly` / `SameSite` on session cookies), password
  storage without a slow hash, timing-unsafe comparison of secrets, no
  rate limit or lockout on login, password reset or token endpoints.
- Authorization and IDOR: an endpoint that loads or changes an object by
  an id from the request without checking it belongs to the caller (or
  their organization), admin actions reachable by ordinary users, and
  checks done in the UI only.
- CSRF: state-changing requests (POST, PUT, PATCH, DELETE, or a GET that
  changes something) authenticated by a cookie with no CSRF token, no
  `SameSite` protection and no Origin or Referer check.
- Open redirects: a redirect or `Location` to a URL taken from the
  request (`next`, `return_to`, `redirect_uri`) without checking it is a
  relative path or an allowed host.
- CORS and security headers: `Access-Control-Allow-Origin` reflected from
  the request or `*` together with credentials, origins matched with a
  loose regex or suffix; missing `Content-Security-Policy`,
  `X-Content-Type-Options: nosniff`, frame protection
  (`frame-ancestors` or `X-Frame-Options`) or HSTS on an app that serves
  HTML with sessions.
- XSS: user data written into HTML, attributes, URLs (`javascript:`) or
  scripts without escaping, `innerHTML` / `dangerouslySetInnerHTML` /
  `v-html` / `template.HTML` with untrusted data, and user-uploaded HTML
  or SVG served from the app's own origin.
- File uploads: no limit on size, type checked only by extension or the
  client's `Content-Type`, files saved under a name or path from the
  request, uploads served inline from the app's origin, and archives
  extracted without checking entry paths and total size.
- SSRF: the server fetching a URL or host from the request without an
  allow list, including redirects and internal addresses (localhost,
  169.254.169.254, private ranges).
- Unsafe deserialization: pickle, Java serialization, YAML with object
  tags, `gob` or similar of untrusted data; XML with external entities.
- Secrets: credentials, API keys, private keys or tokens committed in the
  code or config (not test fixtures with fake values), and secrets written
  to logs, error messages or responses.
- Cryptography and randomness: `math/rand`, `Math.random` or similar for
  tokens, ids or keys; broken algorithms (MD5, SHA-1 for signatures, ECB,
  static IVs); disabled TLS verification; hand-rolled crypto.
- Resource exhaustion an attacker can trigger cheaply: regular
  expressions with nested quantifiers run on input (ReDoS), request bodies
  or uploads read with no size limit, decompression or archive extraction
  with no limit on the output (zip bombs), unbounded JSON or XML nesting,
  and expensive endpoints (login, password hashing, search, exports) with
  no rate limit.
- Race conditions with security impact: check-then-use on files or
  permissions (TOCTOU), double spending, and token reuse.

## What not to report

- Theoretical problems with no attack path you can describe from an
  entry point an attacker reaches.
- Problems a framework default, a middleware or a check elsewhere already
  prevents.
- Slow code that an attacker can't trigger at will, or that needs a
  logged-in admin to abuse: that is for the performance scan.
- Code only used in tests, examples, scripts a developer runs locally,
  generated code, and vendored or third-party code (the dependency audit
  covers known vulnerabilities in dependencies).
- Missing hardening with no concrete effect (a header on an API that
  never serves HTML, say), style, and best-practice advice.

When in doubt, leave it out: a false alarm costs a reviewer more than a
missed low.

## Reporting

Report each as type "vuln". Use the file and line where the flaw is (the
missing check or the unsafe call). In the details, give a concrete attack:
who the attacker is, the request or input they send, and what they gain;
then the path from the entry point to the flaw, with file and line for
each step. The suggested fix is the specific change (the check to add,
the API to call instead, the limit to set).

Use the shared severity scale below. For security: critical is remote code
execution, authentication bypass, or reading or changing other users' data
by anyone who can reach the app; high is the same needing an account or
some interaction (a victim opening a link), or a denial of service one
request can cause; medium needs an unusual setup or another flaw first;
low is defence in depth.

Fingerprint "<class>:<function, route or file>", with class one of
sql-injection, command-injection, path-traversal, template-injection,
code-injection, header-injection, missing-auth, idor, csrf, open-redirect,
cors, missing-header, xss, upload, ssrf, deserialization, secret, crypto,
weak-random, redos, unbounded-input, zip-bomb, no-rate-limit or race: for
example "sql-injection:UserStore.Find", "idor:GET /api/items/{id}",
"missing-header:internal/api/server.go".

Do not change any files.
