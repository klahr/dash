# Caddy with a DNS provider module, for wildcard certificates (live previews
# and org subdomains). Built by deploy/docker-compose.dns.yml; pick the
# provider with CADDY_DNS_PROVIDER in deploy/.env, as named in
# https://github.com/caddy-dns (cloudflare, digitalocean, hetzner, duckdns, ...).

ARG CADDY_VERSION=2

FROM caddy:${CADDY_VERSION}-builder AS build
ARG CADDY_DNS_PROVIDER
RUN test -n "$CADDY_DNS_PROVIDER" || { echo "set CADDY_DNS_PROVIDER, e.g. cloudflare" >&2; exit 1; } \
 && xcaddy build --with "github.com/caddy-dns/${CADDY_DNS_PROVIDER}"

FROM caddy:${CADDY_VERSION}
COPY --from=build /usr/bin/caddy /usr/bin/caddy
