# Branding and the landing page

If you host Dash for other people, you can give it your product's name, link your terms
and privacy policy, and show a landing page to visitors who aren't signed in. Dash is
AGPL software that anyone may host, so nothing about one deployment is built in: every
value below comes from a flag or a `DASH_*` variable (in `.env` or the service's
environment).

Leave them all alone and Dash looks as before, except for the **Source code** link in the
footer.

## Settings

| Variable              | Flag             | What                                                                     |
|-----------------------|------------------|--------------------------------------------------------------------------|
| `DASH_BRAND_NAME`     | `-brand-name`    | the product name in page titles, the sign-in pages, emails, the installed app and authenticator apps (default `Dash`) |
| `DASH_SOURCE_URL`     | `-source-url`    | the footer's **Source code** link (default `https://github.com/klahr/dash`) |
| `DASH_TERMS_URL`      | `-terms-url`     | terms of service, linked in the footer when set                          |
| `DASH_PRIVACY_URL`    | `-privacy-url`   | privacy policy, linked in the footer when set                            |
| `DASH_CONTACT_EMAIL`  | `-contact-email` | an address people can write to: a **Contact** link in the footer, and a line at the end of every email |
| `DASH_LOGO`           | `-logo`          | an image file on the server (SVG, PNG, WebP, …) shown instead of Dash's mark next to the name |
| `DASH_LANDING`        | `-landing`       | `1` shows the landing page at `/` to visitors who aren't signed in (off by default) |
| `DASH_TEAM_PRICE`     | `-team-price`    | the team plan's price on the landing page, exactly as shown, e.g. `€8 per seat / month` |
| `DASH_PRICING_URL`    | `-pricing-url`   | where the landing page's **see pricing** links to when there's no price  |

URLs must be `http(s)://…` and the contact a bare address (`help@example.com`). Dash
refuses to start with a value that would make a broken link, or a logo it can't read, and
says which.

The logo is served at `/brand/logo` without signing in, like the sign-in page's other
images. The browser's tab icon stays Dash's `favicon.svg`.

## The footer and the AGPL

Every page ends with a footer: the sign-in, signup and invitation pages, the landing page
and the connector's consent page; in the dashboard, the bottom of the account menu and the
keyboard help. It always has **Source code**, then **Terms**, **Privacy** and **Contact**
when they are set.

Section 13 of the AGPL asks that people who use a modified Dash over the network be
offered its source code. If you run a changed Dash, point `DASH_SOURCE_URL` at where your
changes are published (a fork, a tarball of the running version); the default, the
upstream repository, is right only for an unmodified build.

## The landing page

With `DASH_LANDING=1`, someone who opens `/` without being signed in sees the landing page
instead of being sent to the sign-in page. Signed-in people go straight to the board, as
before; every other page still asks for a session. Self-hosters who leave it off go
straight to the sign-in page.

It says what Dash is and how a task goes from queued to merged, with screenshots of the
board, a task in review and the phone layout; how runners work (your machine, your Claude
subscription, no open ports), what else is included, the pricing, and that the software is free to host yourself under the AGPL, with a
link to `DASH_SOURCE_URL`. Its buttons are **Log in**, and **Sign up** when
[open signup](signup.md) is on (it opens the sign-in page's signup form).

The pricing shows individuals as free, and teams as per seat: with `DASH_TEAM_PRICE` that
string, otherwise "see pricing", linked to `DASH_PRICING_URL` (or to the contact address,
or plain text if neither is set). Dash doesn't bill anyone; these are words on a page.

The page is one HTML file built into the binary, with its styles inline: no JavaScript,
web fonts or anything else from other sites, and it fits a phone's screen. Its screenshots
are in `web/static/img/landing/` (public, like the rest of `/img/`). Its text lives
in `web/landing.html`; change it there and rebuild if yours should say something else.

## Example

A hosted Dash, with [open signup](signup.md):

    DASH_PUBLIC_URL=https://agents.example.com
    DASH_SIGNUP=1
    DASH_BRAND_NAME="Example Agents"
    DASH_LANDING=1
    DASH_TEAM_PRICE="€8 per seat / month"
    DASH_TERMS_URL=https://example.com/terms
    DASH_PRIVACY_URL=https://example.com/privacy
    DASH_CONTACT_EMAIL=help@example.com
    DASH_LOGO=/etc/dash/logo.svg
    DASH_SOURCE_URL=https://github.com/example/dash
