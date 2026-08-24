# GO LIVE — put HookRelay on the internet, for free

A literal walkthrough. Follow it top to bottom and you end with HookRelay running
on a public HTTPS URL that you own.

**Time:** about 1h45, most of it waiting on Oracle's signup and one Docker
build.
**Cost:** $0. A domain is optional (~$10/year); there is an IP-only path if you
skip it.
**Assumed knowledge:** you can copy-paste into a terminal. Nothing more.

| Part | What | Time |
|---|---|---|
| [1](#part-1--what-is-already-done-for-you) | (already done — just read it) | 2 min |
| [2](#part-2--get-a-free-server) | Get a free server | 30–45 min |
| [3](#part-3--prepare-the-server) | Install Docker, open the firewall | 15 min |
| [4](#part-4--get-the-code-and-set-your-secrets) | Get the code, set secrets | 10 min |
| [5](#part-5--the-production-overrides) | Production overrides | 5 min |
| [6](#part-6--first-launch) | First launch | 15 min |
| [7](#part-7--domain-and-https) | Domain and HTTPS | 20 min |
| [8](#part-8--create-your-account-and-send-a-real-event) | Create your account, send an event | 10 min |
| [9](#part-9--keep-it-alive) | Backups and updates | 15 min |

> **Every command marked `[server]` runs on the cloud server over SSH.**
> Commands marked `[local]` run on your own machine.

---

## Part 1 — What is already done for you

Nothing to do here. This part used to ask you to paste in a security fix; it is
now in the code, so this is just what you are getting.

**The SSRF guard.** Endpoint URLs come from tenants, so without a guard a tenant
can register `http://169.254.169.254/` and have HookRelay read this machine's
cloud credentials on their behalf. Deliveries now refuse to connect to loopback,
link-local, carrier-grade NAT and RFC1918 addresses.

The check runs in the dialler's `Control` hook rather than against the URL,
because `Control` fires *after* DNS resolution with the concrete address about to
be dialled. Checking the hostname string would miss
`evil.example.com → 169.254.169.254`, and there is no resolve-then-connect
window to race.

**A production configuration guard.** The API and worker now refuse to start when
`ENVIRONMENT=production` and any of these is true, reporting every problem at
once:

| Condition | Why it is refused |
|---|---|
| `JWT_SECRET` is the dev default | Anyone could mint a dashboard token for any tenant |
| `JWT_SECRET` shorter than 32 bytes | Brute-forceable |
| `CORS_ALLOW_ORIGIN` is `*` | Any website could make authenticated calls from a visitor's browser |
| `ALLOW_PRIVATE_ENDPOINTS` is set | The SSRF guard would be off |

So a misconfigured deployment fails loudly at boot instead of running exposed.
If the API will not start, read the error — it tells you exactly what to fix.

**`docker-compose.prod.yml` and `Caddyfile.example`** are in the repo, so Parts 5
and 7 are copy-and-edit rather than write-from-scratch.

`ALLOW_PRIVATE_ENDPOINTS` stays `true` in the base compose file so `docker
compose up` can still reach the bundled test receiver, which lives on a private
address. The production overlay sets it to `false` explicitly — not merely
omitting it, since Compose merges environment maps and the development default
would otherwise survive.

You can confirm all of this is live once you are running, in Part 8.4.

---

## Part 2 — Get a free server

Oracle Cloud's Always Free tier gives you 4 ARM cores and 24 GB of RAM,
permanently, which is far more than this needs. It is the best free option that
exists.

### 2.1 Sign up

1. Go to **https://www.oracle.com/cloud/free/**, click **Start for free**.
2. Fill in your details. **Pick your home region carefully — it cannot be
   changed later.** Choose one geographically near you.
3. You must enter a **credit card**. It is an identity check; you are not
   charged as long as you stay on Always Free resources. You will see a
   temporary ~$1 authorisation that reverses.
4. Wait for the provisioning email. Usually minutes, occasionally a few hours.

### 2.2 Create the instance

1. Console → hamburger menu → **Compute** → **Instances** → **Create instance**.
2. **Name:** `hookrelay`
3. **Image and shape** → **Edit** → **Change image** → **Canonical Ubuntu 24.04**.
4. **Change shape** → **Ampere** tab → **VM.Standard.A1.Flex** → set **4 OCPUs**
   and **24 GB memory**. Confirm it says **Always Free eligible**.
5. **Networking:** leave the defaults (it creates a VCN for you). Make sure
   **Assign a public IPv4 address** is on.
6. **Add SSH keys** → **Generate a key pair for me** → **download both keys**.
   You cannot download them again. Save them somewhere you will not lose.
7. **Create**.

> ### If you see "Out of host capacity"
>
> This is common and not your fault — free ARM capacity is heavily contested.
> Options, in order of what actually works:
>
> 1. Change the **availability domain** dropdown (AD-1 / AD-2 / AD-3) and retry.
> 2. Ask for less: **2 OCPUs / 12 GB**. Still plenty for HookRelay.
> 3. Retry at a quiet hour. Capacity frees up constantly; people script this.
> 4. Upgrade to **Pay As You Go**. Counter-intuitive, but it gives PAYG accounts
>    priority for *Always Free* shapes, and you still are not billed for them.
>
> If ARM stays unavailable, the Always Free **AMD** shape (1/8 OCPU, 1 GB RAM)
> is too small to build the frontend on. Build images elsewhere and push them to
> a registry, or use another provider's free tier. Free tiers change often —
> check current terms before relying on one.

### 2.3 Open ports 80 and 443 in the virtual network

This is separate from the server's own firewall. Both must be open.

1. Instance page → under **Primary VNIC**, click the **Subnet** link.
2. Click the **Security List** (usually "Default Security List for ...").
3. **Add Ingress Rules**, and add two:

| Source CIDR | IP Protocol | Destination Port |
|---|---|---|
| `0.0.0.0/0` | TCP | `80` |
| `0.0.0.0/0` | TCP | `443` |

Do **not** open 8080, 3000, 5432, 6379 or 9090. Everything goes through 443.

### 2.4 Connect

Note your instance's **Public IP address**, then:

```bash
[local] chmod 600 ~/Downloads/ssh-key-*.key
[local] ssh -i ~/Downloads/ssh-key-*.key ubuntu@YOUR_PUBLIC_IP
```

Type `yes` at the fingerprint prompt. You are in.

---

## Part 3 — Prepare the server

### 3.1 Update and install Docker

```bash
[server] sudo apt update && sudo apt upgrade -y
[server] sudo apt install -y docker.io docker-compose-v2 git
[server] sudo usermod -aG docker $USER
```

Then **log out and back in** (`exit`, then SSH again) so the group applies.
Verify:

```bash
[server] docker run --rm hello-world
```

If you get a permission error, you skipped the re-login.

### 3.2 Open the OS firewall

**This is the step that catches everyone.** Oracle's Ubuntu images ship with
iptables rules that drop everything except SSH, *regardless* of what you set in
the console. Your site will be unreachable and nothing will tell you why.

```bash
[server] sudo iptables -I INPUT 6 -m state --state NEW -p tcp --dport 80 -j ACCEPT
[server] sudo iptables -I INPUT 6 -m state --state NEW -p tcp --dport 443 -j ACCEPT
[server] sudo netfilter-persistent save
```

Confirm both appear:

```bash
[server] sudo iptables -L INPUT -n --line-numbers | grep -E 'dpt:(80|443)'
```

### 3.3 Add swap

24 GB of RAM does not need swap, but if you ended up on a smaller shape the
frontend build will be killed without it. Harmless either way:

```bash
[server] sudo fallocate -l 4G /swapfile && sudo chmod 600 /swapfile
[server] sudo mkswap /swapfile && sudo swapon /swapfile
[server] echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
```

---

## Part 4 — Get the code and set your secrets

```bash
[server] sudo mkdir -p /srv && sudo chown $USER /srv
[server] git clone https://github.com/Aryan-Jain06/HookRelay /srv/hookrelay
[server] cd /srv/hookrelay
```

### 4.1 Generate your secrets

Generate them **on the server**. Do not paste them into a chat, an email, or a
commit. If a secret has been in a chat window, it is not a secret.

```bash
[server] cat > .env <<EOF
JWT_SECRET=$(openssl rand -base64 48)
POSTGRES_PASSWORD=$(openssl rand -base64 32 | tr -d '/+=')
WORKER_COUNT=50
DELIVERY_MAX_AGE=24h
RETRY_SCHEDULE=
EOF
[server] chmod 600 .env
```

Two things to know:

- `RETRY_SCHEDULE` **must stay empty** in production. Empty means the real
  8-hour backoff. Setting it compresses retries into seconds, which is for tests.
- `.env` is already in `.gitignore`. Keep it that way.

### 4.2 Decide your URLs

If you have a domain, add these to `.env`, substituting yours:

```bash
[server] cat >> .env <<'EOF'
NEXT_PUBLIC_API_URL=https://api.hookrelay.example.com
CORS_ALLOW_ORIGIN=https://hookrelay.example.com
EOF
```

**No domain?** Use your IP for now and skip Part 7:

```bash
[server] IP=$(curl -s ifconfig.me)
[server] cat >> .env <<EOF
NEXT_PUBLIC_API_URL=http://$IP:8080
CORS_ALLOW_ORIGIN=http://$IP:3000
EOF
```

You would then need ingress rules for 8080 and 3000, and you get no HTTPS —
fine for a demo you show someone, not for anything real. Get the domain.

> **`NEXT_PUBLIC_API_URL` is baked into the dashboard at build time**, not read
> when it starts. If you change it later you must rebuild:
> `docker compose up -d --build frontend`. Changing it in `.env` alone does
> nothing, and the symptom is a dashboard whose network tab shows calls to
> `localhost`.

---

## Part 5 — The production overrides

The overlay is already in the repo. It stops the test receiver from running,
takes Postgres and Redis off the public internet, binds the app ports to
loopback so everything goes through the TLS proxy, and tells Redis never to
evict your queue.

Nothing to write. Just check it is there:

```bash
[server] head -20 docker-compose.prod.yml
```

Save yourself typing the long command every time:

```bash
[server] echo "alias hr='docker compose -f /srv/hookrelay/docker-compose.yml -f /srv/hookrelay/docker-compose.prod.yml'" >> ~/.bashrc
[server] source ~/.bashrc
```

Confirm your `.env` satisfies it before building — this resolves the overlay
without starting anything, and names any variable you have missed:

```bash
[server] hr config > /dev/null && echo "config OK"
```

If it complains `required variable POSTGRES_PASSWORD is missing a value`, go
back to Part 4.1.

---

## Part 6 — First launch

```bash
[server] cd /srv/hookrelay
[server] hr up -d --build
```

The first build takes **5–15 minutes** on ARM — it compiles the Go binaries and
builds Next.js. It is not stuck. Watch it if you like: `hr logs -f`.

### 6.1 Check everything is healthy

```bash
[server] hr ps
```

You want `postgres`, `redis`, `api`, `worker` and `frontend` all up, and
**no `receiver`**. Then:

```bash
[server] curl -s localhost:8080/healthz          # {"status":"ok"}
[server] curl -s localhost:8080/readyz           # checks Postgres AND Redis
```

`readyz` returning 200 is the real signal: it means migrations ran and both
dependencies answered.

```bash
[server] hr exec postgres psql -U hookrelay -d hookrelay -c '\dt'
```

Seven tables: `tenants`, `endpoints`, `subscriptions`, `events`, `deliveries`,
`delivery_attempts`, `schema_migrations`.

```bash
[server] hr exec redis redis-cli config get maxmemory-policy   # must say noeviction
```

If any of those fail, jump to [Troubleshooting](#troubleshooting) before
continuing.

---

## Part 7 — Domain and HTTPS

Skip if you are on the IP-only path.

### 7.1 Point DNS at your server

At your registrar (Namecheap, Cloudflare, Porkbun — any), add two **A records**:

| Type | Name | Value |
|---|---|---|
| A | `hookrelay` | your public IP |
| A | `api.hookrelay` | your public IP |

Using Cloudflare, set both to **DNS only** (grey cloud) for now — the orange
proxy interferes with the certificate challenge.

Wait for propagation, then check from the server:

```bash
[server] dig +short hookrelay.example.com
```

It must return your IP before you continue. Certificates will fail otherwise.

### 7.2 Run Caddy

Caddy gets and renews Let's Encrypt certificates automatically. No configuration
beyond this, no cron, no renewal to remember.

```bash
[server] cp /srv/hookrelay/Caddyfile.example /srv/hookrelay/Caddyfile
[server] nano /srv/hookrelay/Caddyfile     # replace both hostnames with yours
```

The example also carries a commented-out block for putting basic auth on
`/auth/register` once you have your own tenant. Then:

```bash
[server] docker run -d --name caddy --restart unless-stopped --network host \
  -v /srv/hookrelay/Caddyfile:/etc/caddy/Caddyfile \
  -v caddy_data:/data -v caddy_config:/config \
  caddy:2-alpine
[server] docker logs -f caddy
```

Watch for `certificate obtained successfully`. Then, from your own machine:

```bash
[local] curl -I https://api.hookrelay.example.com/healthz
```

A `200` over HTTPS means you are live.

### 7.3 Rebuild the dashboard with the real URL

Because the API URL is compiled in, the dashboard is still pointing at whatever
it was built with:

```bash
[server] hr up -d --build frontend
```

Now open **https://hookrelay.example.com** in a browser.

---

## Part 8 — Create your account and send a real event

### 8.1 Register

```bash
[server] API=https://api.hookrelay.example.com
[server] curl -s -X POST $API/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"name":"My Company","email":"you@example.com","password":"a-long-password-you-choose"}' | jq
```

The response contains your **API key**, shown **once**. Save it in your password
manager now.

```json
{
  "tenant": { "id": "...", "name": "My Company", "email": "you@example.com" },
  "api_key": "hrk_live_...",
  "token": "eyJ..."
}
```

> Anyone who can reach `/auth/register` can create a tenant. Once you have your
> account, either put HTTP basic auth on that path in the Caddyfile, or add the
> IP rate limit from [PRODUCTION.md §2.2](PRODUCTION.md#22-rate-limit-login-).

### 8.2 Register an endpoint

Use any URL you control. https://webhook.site gives you a free throwaway URL for
testing — open it and copy your unique address.

```bash
[server] KEY=hrk_live_...paste-yours...
[server] curl -s -X POST $API/endpoints \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"url":"https://webhook.site/your-unique-id","description":"first test","event_types":["order.created"]}' | jq
```

Keep the `secret` from the response — the `whsec_...` value. That is what your
receiver verifies signatures with.

### 8.3 Publish an event

```bash
[server] curl -s -X POST $API/events \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"event_type":"order.created","payload":{"order_id":"ord_1","amount_cents":1999}}' | jq
```

You get `202` and an event id. Within a second, webhook.site shows the request,
with `X-HookRelay-Id`, `X-HookRelay-Timestamp` and `X-HookRelay-Signature`
headers.

### 8.4 Confirm the SSRF fix is actually on

This is the one test worth doing deliberately.

```bash
[server] curl -s -X POST $API/endpoints -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"url":"http://169.254.169.254/","description":"ssrf check","event_types":["ssrf.test"]}' | jq -r .id
[server] curl -s -X POST $API/events -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"event_type":"ssrf.test","payload":{}}' | jq -r .id
[server] sleep 5 && hr logs worker --tail 30 | grep -i "internal address"
```

You **must** see `refusing to deliver to internal address 169.254.169.254`.

If you see nothing, the guard is not active — check that
`ALLOW_PRIVATE_ENDPOINTS` is absent from your production `.env`, and that Part 1
is actually in the code you deployed (`git log --oneline -1`). Delete that test
endpoint when you are done.

### 8.5 Look at the dashboard

Log in at `https://hookrelay.example.com` with the email and password from 8.1.
You should see the event, its delivery, and the attempt with its status code and
latency.

---

## Part 9 — Keep it alive

### 9.1 Back up Postgres

Only Postgres needs backing up. Redis holds work pointers, and the scheduler
rebuilds the queue from Postgres — losing Redis entirely costs latency, not data.

```bash
[server] mkdir -p /srv/hookrelay/backups
[server] cat > /srv/hookrelay/backup.sh <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
cd /srv/hookrelay
STAMP=$(date +%F-%H%M)
docker compose -f docker-compose.yml -f docker-compose.prod.yml \
  exec -T postgres pg_dump -U hookrelay --format=custom --no-owner hookrelay \
  > "backups/hookrelay-$STAMP.dump"
find backups -name '*.dump' -mtime +30 -delete
EOF
[server] chmod +x /srv/hookrelay/backup.sh
[server] (crontab -l 2>/dev/null; echo "0 3 * * * /srv/hookrelay/backup.sh >> /var/log/hookrelay-backup.log 2>&1") | crontab -
```

Run it once now to prove it works:

```bash
[server] /srv/hookrelay/backup.sh && ls -lh /srv/hookrelay/backups/
```

**These backups are on the same machine as the database, which protects you from
nothing that matters.** Copy them off — see
[PRODUCTION.md §5.1](PRODUCTION.md#51-nightly-backups-to-free-object-storage) for
Cloudflare R2 (10 GB free). And once, actually restore one; an untested backup is
a rumour.

### 9.2 Stop the disk filling up

Two things grow without bound. Both are one cron line.

```bash
[server] (crontab -l; echo "*/30 * * * * cd /srv/hookrelay && docker compose -f docker-compose.yml -f docker-compose.prod.yml exec -T redis redis-cli XTRIM deliveries_stream MAXLEN '~' 1000000") | crontab -
[server] (crontab -l; echo "30 3 * * * cd /srv/hookrelay && docker compose -f docker-compose.yml -f docker-compose.prod.yml exec -T postgres psql -U hookrelay -d hookrelay -c \"DELETE FROM delivery_attempts WHERE attempted_at < now() - interval '30 days';\"") | crontab -
```

### 9.3 Know when it breaks

Install **Uptime Kuma** (free, self-hosted, five minutes) and point it at
`https://api.hookrelay.example.com/readyz`:

```bash
[server] docker run -d --restart unless-stopped -p 127.0.0.1:3001:3001 \
  -v uptime-kuma:/app/data --name uptime-kuma louislam/uptime-kuma:1
```

Add a Caddy entry for it, then configure email or Telegram alerts in its UI.

The metric that actually matters is **dead letters appearing**, since that means
deliveries were permanently abandoned. Check it with:

```bash
[server] curl -s -H "Authorization: Bearer $KEY" "$API/deliveries?status=dead&limit=1" | jq .counts
```

For proper metrics and alerting, see
[PRODUCTION.md §3](PRODUCTION.md#stage-3--see-whats-happening).

### 9.4 Deploy an update

```bash
[server] cd /srv/hookrelay && git pull && hr up -d --build
```

Migrations run automatically when the API starts, under an advisory lock, so
this is safe. If you changed `NEXT_PUBLIC_API_URL`, the `--build` is required.

---

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Browser times out on your domain | Oracle's iptables rules | Part 3.2. This is the most common failure by a wide margin. |
| Times out and iptables is right | Security List missing rules | Part 2.3 — console rules and OS rules are separate. |
| Caddy: "could not get certificate" | DNS not propagated, or 80 blocked | `dig +short your.domain` must return your IP; port 80 must be open (Let's Encrypt needs it). |
| Cloudflare users: cert fails | Orange-cloud proxy interfering | Set DNS to "DNS only" (grey), get the cert, then re-enable if you want. |
| Dashboard loads, all API calls fail | `NEXT_PUBLIC_API_URL` baked in wrong | `hr up -d --build frontend`. Check the browser network tab — calls to `localhost` confirm it. |
| Dashboard calls blocked by CORS | `CORS_ALLOW_ORIGIN` mismatch | It must exactly match the dashboard origin, scheme included, no trailing slash. |
| `docker: permission denied` | Group not applied | Log out and back in after `usermod -aG docker`. |
| Frontend build killed | Out of memory on a small shape | Add swap (Part 3.3). |
| `readyz` 503 | Postgres or Redis not up | `hr ps`, then `hr logs postgres redis`. |
| Deliveries stuck `pending` | Worker not running | `hr ps worker`, `hr logs worker`. |
| Everything dead-letters in seconds | `RETRY_SCHEDULE` is set | Must be empty in production (Part 4.1). |
| Redis "OOM command not allowed" | Memory full | Confirm `noeviction`, run the `XTRIM` from 9.2. This is Redis protecting your queue, not losing it. |
| Out of capacity creating instance | Free ARM contention | See the box in Part 2.2. |

Useful commands:

```bash
[server] hr ps                  # what is running
[server] hr logs -f api         # follow API logs
[server] hr logs -f worker      # follow delivery logs
[server] hr restart worker      # safe: loses nothing, the reaper recovers in-flight work
[server] hr down                # stop everything (data survives in volumes)
```

---

## Connecting your application

With HookRelay live, [CONNECTING.md](CONNECTING.md) covers wiring your app to it:
publishing events, verifying signatures, and making your handler idempotent —
with working code in Node, Python and Go.

---

## When you are done

You have:

- HookRelay on a public HTTPS URL with auto-renewing certificates
- The SSRF hole closed and verified closed
- Secrets generated on the box, never transmitted
- Postgres and Redis unreachable from the internet
- The test receiver not running
- Nightly backups, disk-growth control, uptime alerting

For $0/month, plus a domain if you chose one.

### What is still not done

Being straight with you about what this walkthrough does *not* give you:

- **No rate limiting.** One runaway loop from a client fills your database.
  [PRODUCTION.md §2](PRODUCTION.md#stage-2--dont-get-taken-down).
- **No metrics.** You will find out about problems by looking, not by being told.
  [PRODUCTION.md §3](PRODUCTION.md#stage-3--see-whats-happening).
- **Backups are on the same machine.** Copy them off. §9.1.
- **`/auth/register` is open.** Restrict it once you have your account.
- **Signing secrets are stored in plaintext.**
  [PRODUCTION.md §6.1](PRODUCTION.md#61-encrypt-signing-secrets-at-rest-).

None of those block a demo or a portfolio piece. All of them matter if real
traffic arrives.
