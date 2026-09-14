# imot-mcp deployment

`imot-mcp` exposes imot.bg market data to MCP clients (ChatGPT) as three
read-only tools. It is the interactive counterpart to the `imot` CLI: same
scraping core, but cache-first, rate limited, and read-only. Market Radar keeps
using the CLI.

Target: the Hostinger VPS (`76.13.137.79`), alongside the Broker Essentials
workers, behind nginx with Let's Encrypt.

## Why it runs the way it does

- **Docker, not systemd.** The scraping egress is the `imot-vpn` container, which
  is only reachable on the Docker network. Joining `imot-vpn_default` reuses the
  established scraping path instead of scraping from the VPS IP.
- **Cache-first.** A repeat question inside `IMOT_MCP_SEARCH_TTL` never touches
  imot.bg. This is what keeps several colleagues from multiplying requests on an
  egress that Market Radar also uses.
- **Radar-first inside its catalogue.** When `IMOT_MCP_RADAR_DSN` is set, a
  Sofia sale search or detail read that falls inside the shared Market Radar
  scope is answered from the authoritative Radar Postgres store and makes zero
  imot.bg requests. Rentals and out-of-catalogue areas keep the live fallback,
  labelled with `source`, `coverage` and `observed_at`. SQLite remains the
  MCP's own cache and usage store only; it is never authoritative.
- **One token per colleague.** Identity is pinned per token, so quotas are per
  person and revocation is a one-line edit. It also avoids depending on request
  context reaching the MCP tool handler.
- **Read-only.** All tools carry `readOnlyHint`, so clients do not gate them
  behind write confirmations.

## Market Radar read path

Phase U1 of
`broker-essentials/docs/plans/market-radar-mcp-unified-serving-plan-2026-09-13.md`
adds a read-only Radar reader to the MCP. It is optional and off until
`IMOT_MCP_RADAR_DSN` is configured.

- The reader uses its own least-privilege Postgres role over a dedicated DSN.
  It never accepts SQL, URLs, roles or paths from the model, every query is a
  static statement with bind parameters, and the session sets
  `default_transaction_read_only=on` as defence in depth. The collector writer
  credential and the CRM credential are never used here.
- In scope are Sofia sales whose neighbourhood resolves to an active row in the
  Radar catalogue (`RadarNeighborhood`). Scope, price and size filters are
  applied in the reader. A complete, fresh zero-match scope is a verified empty
  result; `never_collected`, `partial`, `stale` and `unavailable` are not.
- Coverage is classified conservatively: it is `complete` only when the latest
  attempt finished `ok`, the coverage receipt does not say incomplete, and the
  last complete scrape is inside `IMOT_MCP_RADAR_FRESHNESS` (default 26h).
- Rentals, other cities, citywide queries and non-catalogue neighbourhoods keep
  the existing live imot.bg fallback, labelled `source: live`,
  `coverage: out_of_scope` (or `unavailable` when Radar could not be read). No
  answer silently mixes the two sources.
- Every answer now carries `source`, `coverage`, `readiness`, `observed_at` and
  `empty_verified`, and `readiness` reports committed and expected detail/media
  counts. The three existing tools and the Streamable HTTP connector are
  unchanged.

The MCP container needs network reachability to the DSN host. Setting the
private Radar network path and issuing the read-only role are deployment steps,
not part of this source change.

## Deploy

```bash
# 1. Build a static binary on the workstation.
cd ~/projects/imot-cli
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o deploy/imot-mcp ./cmd/imot-mcp/

# 2. Copy the runtime files to the VPS.
ssh -i ~/.ssh/hostinger_vps root@76.13.137.79 'mkdir -p /opt/imot-mcp/data'
scp -i ~/.ssh/hostinger_vps deploy/imot-mcp deploy/Dockerfile deploy/docker-compose.yml \
    root@76.13.137.79:/opt/imot-mcp/

# 3. Create the env file on the VPS, then lock it down.
ssh -i ~/.ssh/hostinger_vps root@76.13.137.79 \
  'cp /opt/imot-mcp/imot-mcp.env.example /opt/imot-mcp/imot-mcp.env && chmod 600 /opt/imot-mcp/imot-mcp.env && $EDITOR /opt/imot-mcp/imot-mcp.env'

# 4. Build the image and start the service.
ssh -i ~/.ssh/hostinger_vps root@76.13.137.79 \
  'cd /opt/imot-mcp && docker build -t imot-mcp:current . && docker compose up -d'

# 5. Publish it through nginx.
scp -i ~/.ssh/hostinger_vps deploy/nginx-imot-mcp.conf root@76.13.137.79:/etc/nginx/sites-available/imot-mcp
ssh -i ~/.ssh/hostinger_vps root@76.13.137.79 \
  'ln -sf /etc/nginx/sites-available/imot-mcp /etc/nginx/sites-enabled/imot-mcp && nginx -t && systemctl reload nginx'

# 6. Issue the certificate (requires the DNS record to exist first).
ssh -i ~/.ssh/hostinger_vps root@76.13.137.79 \
  'certbot --nginx -d imot-mcp.pixelautomate.com --non-interactive --agree-tos --redirect'
```

## DNS

`pixelautomate.com` is on Cloudflare, in the personal account
(`Apsis.victor@gmail.com's Account`). The record is already in place:

| Type | Name | Content | Proxy |
|------|------|---------|-------|
| A | `imot-mcp` | `76.13.137.79` | DNS only (grey cloud) |

DNS-only keeps certificate issuance with Let's Encrypt on the VPS. Proxying
would also work but adds a second place SSE buffering could break.

To manage it programmatically, the toolbelt needs a scoped token stored as
`CLOUDFLARE_API_TOKEN_PERSONAL` (Zone → DNS → Edit for `pixelautomate.com`).
`wrangler` cannot do this: it has no DNS-record command and its OAuth grant
carries no `dns_records` scope.

## Two host-specific gotchas

Both cost real debugging time on first deploy.

1. **The 443 listener must name this host's IP.** Other vhosts on this box bind
   `76.13.137.79:443`; nginx prefers a specific-address listener over a wildcard
   one, so a `listen 443` block is never reached for traffic on that address and
   nginx serves the default vhost's certificate instead. The site config here
   already pins the address.
2. **Verify listeners, not reload status.** After changing nginx config, `ss -lntp
   | grep nginx` must show `76.13.137.79:443`. A `systemctl reload` reported
   success while the old listeners stayed in place; `systemctl restart nginx`
   applied the change.

Also note nginx itself was found `failed` on this host (down since 2026-08-27,
which had taken `pdf.pixelautomate.com` down with it). It was started during this
deploy.

## Verify

```bash
# Local health check from the VPS.
ssh -i ~/.ssh/hostinger_vps root@76.13.137.79 'curl -s localhost:8099/healthz'

# Public HTTPS, the path ChatGPT uses.
curl -s https://imot-mcp.pixelautomate.com/healthz
curl -s -o /dev/null -w '%{http_code}\n' https://imot-mcp.pixelautomate.com/mcp/wrong   # 401

# Certificate must be the imot-mcp one, not another vhost's.
echo | openssl s_client -connect 76.13.137.79:443 -servername imot-mcp.pixelautomate.com 2>/dev/null | grep subject=

# A full protocol and live-data check over the public URL.
cd ~/projects/imot-cli
IMOT_MCP_REMOTE_URL="https://imot-mcp.pixelautomate.com/mcp/<secret>" \
  go test -tags live ./internal/mcpserver/ -run TestLiveRemote -v -timeout 300s
```

## Connecting it to ChatGPT

1. Generate a token per colleague (see the env example) and restart the service.
2. Each colleague: ChatGPT → **Settings → Security and login → Developer mode**.
3. **Plugins → plus button** → create a developer-mode app.
4. Server URL: `https://imot-mcp.pixelautomate.com/mcp/<their secret>`
5. Authentication: **No authentication** — the secret is the path itself.
6. In a conversation, pick the app from the **Developer mode** menu in the
   composer, then ask a market question naming the app.

This posture is deliberate for the pilot and is *not* the end state: the URL is
the only secret. Before opening it beyond the pilot, move to OAuth (the MCP Go
SDK ships `auth` and `oauthex` packages, including dynamic client registration),
which lets ChatGPT authenticate a colleague properly and removes the secret from
the URL.

## Operating notes

- Usage is recorded per identity in the cache database. Inspect it with:
  `sqlite3 /opt/imot-mcp/data/mcp-cache.db 'SELECT identity, tool, cache_hit, datetime(created_at,"unixepoch") FROM usage_log ORDER BY id DESC LIMIT 50;'`
  `cache_hit=1` means the call consumed no live imot.bg fetch, which includes
  both MCP cache hits and Radar-served reads.
- Raise `IMOT_MCP_SEARCH_TTL` to reduce live scraping; raise `IMOT_MCP_MAX_PAGES`
  to widen coverage at the cost of latency per question.
- If the shared budget trips during heavy use, the endpoint returns a clear
  tool error instead of scraping; cached answers keep working.
- The MCP endpoint and Market Radar share one VPN egress. If Market Radar starts
  seeing blocks, cut `IMOT_MCP_GLOBAL_QUOTA` first.
