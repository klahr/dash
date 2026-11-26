# Caddy with a DNS provider module, for wildcard certificates (live previews
# and org subdomains). Built by deploy/docker-compose.dns.yml; pick the
# provider with CADDY_DNS_PROVIDER in deploy/.env, as named in
# https://github.com/caddy-dns (cloudflare, digitalocean, hetzner, duckdns, ...),
# and pin its version with CADDY_DNS_VERSION (e.g. v0.2.4; "latest" if unset).
#
# Both images are pinned by digest. To move to a new Caddy release, change the
# tag and digest on both FROM lines, and CADDY_IMAGE's default in
# deploy/docker-compose.yml, to the values from
#   docker buildx imagetools inspect caddy:2.X.Y-builder
#   docker buildx imagetools inspect caddy:2.X.Y

FROM caddy:2.11.9-builder@sha256:cda07fe9d82728c7ac44d844306d435e98131423dc7b114f05a8a8ecfff94707 AS build
ARG CADDY_DNS_PROVIDER
ARG CADDY_DNS_VERSION=latest
RUN test -n "$CADDY_DNS_PROVIDER" || { echo "set CADDY_DNS_PROVIDER, e.g. cloudflare" >&2; exit 1; } \
 && xcaddy build --with "github.com/caddy-dns/${CADDY_DNS_PROVIDER}@${CADDY_DNS_VERSION:-latest}"

FROM caddy:2.11.9@sha256:4851e54a46b206517c574821bb5cc67f170d2156041e9fbc4c9193b45e84a75b
COPY --from=build /usr/bin/caddy /usr/bin/caddy
