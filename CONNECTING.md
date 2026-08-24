# CONNECTING — wire your app to HookRelay

How to connect your application, in both directions:

- **Publishing** — your app tells HookRelay something happened.
- **Receiving** — your subscriber accepts a signed webhook and verifies it.

Working code in Node, Python and Go for both. Copy it, change the URL, done.

- [The two sides](#the-two-sides)
- [Step 1 — get your credentials](#step-1--get-your-credentials)
- [Step 2 — register an endpoint](#step-2--register-an-endpoint)
- [Step 3 — publish an event](#step-3--publish-an-event)
- [Step 4 — receive and verify](#step-4--receive-and-verify)
- [Step 5 — make your handler idempotent](#step-5--make-your-handler-idempotent)
- [Testing before you go live](#testing-before-you-go-live)
- [What HookRelay sends](#what-hookrelay-sends)
- [Full API reference](#full-api-reference)
- [Mistakes that cost people hours](#mistakes-that-cost-people-hours)

---

## The two sides

```
YOUR APP                    HOOKRELAY                  YOUR SUBSCRIBER
(publisher)                                            (receiver)

order placed
     │
     │  POST /events
     │  Authorization: Bearer hrk_...
     └──────────────────────────►  stores it
                                   fans out
                                   signs each one
                                        │
                                        │  POST https://your-app.com/webhooks
                                        │  X-HookRelay-Signature: v1=...
                                        └───────────────────────►  verify HMAC
                                                                   process
                                        ◄───────────────────────   200 OK
```

Both sides may well be your own code. Publishing uses your **API key**;
receiving uses that endpoint's **signing secret**. They are different secrets
with different jobs — the API key proves *you* to HookRelay, the signing secret
proves *HookRelay* to your receiver.

Throughout, `$API` is your API base URL — `http://localhost:8080` locally, or
`https://api.yourdomain.com` once deployed.

---

## Step 1 — get your credentials

If you have not created a tenant yet:

```bash
curl -s -X POST $API/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"name":"My Company","email":"you@example.com","password":"a-long-password"}'
```

```json
{
  "tenant": { "id": "f81d5aca-…", "name": "My Company", "api_key_prefix": "hrk_lDNvjQLQ" },
  "api_key": "hrk_lDNvjQLQxDma88W0ivhSHxv27ZUMHC42OGhIly64hkA",
  "token": "eyJhbGciOiJIUzI1NiIs…"
}
```

- **`api_key`** — shown **once**. Store it now. It is only ever stored hashed,
  so it cannot be recovered, only replaced.
- **`token`** — a JWT for the dashboard, valid 24h. Not needed for publishing.

Keep the key in your secret manager or environment, never in source:

```bash
export HOOKRELAY_API_KEY=hrk_...
```

Already have a tenant? Log in instead:

```bash
curl -s -X POST $API/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"a-long-password"}'
```

Login returns a dashboard token but **not** the API key. Lost your key? Nobody
can recover it — see [Rotating your API key](#rotating-your-api-key).

---

## Step 2 — register an endpoint

An endpoint is a URL plus the event types it wants.

```bash
curl -s -X POST $API/endpoints \
  -H "Authorization: Bearer $HOOKRELAY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "url": "https://your-app.com/webhooks/hookrelay",
    "description": "production order pipeline",
    "event_types": ["order.created", "order.refunded"]
  }'
```

```json
{
  "id": "0f7398fe-f6ce-4ff9-8285-c41a6d217737",
  "url": "https://your-app.com/webhooks/hookrelay",
  "secret": "whsec_iMaCN4Wh29fnQ0FOiqXKznMdCQU_c-gi",
  "active": true,
  "event_types": ["order.created", "order.refunded"]
}
```

**Save the `secret`.** That `whsec_` value is what your receiver verifies
signatures with. Unlike the API key it stays retrievable, but it is hidden by
default and you have to ask for it explicitly:

```bash
curl -s "$API/endpoints/{id}?reveal_secret=true" -H "Authorization: Bearer $HOOKRELAY_API_KEY"
```

Without `?reveal_secret=true` the `secret` field is omitted, and it is never
included in `GET /endpoints` listings. The dashboard's reveal button calls the
same flag.

Notes:

- `event_types` can contain `"*"` to subscribe to everything.
- An endpoint with **no** matching subscription simply receives nothing. If
  deliveries are not arriving, check this first.
- `"active": false` pauses an endpoint without deleting its history.
- The URL must be publicly reachable, and **must not** be a private or loopback
  address — deliveries to those are refused deliberately. For local testing see
  [Testing before you go live](#testing-before-you-go-live).

---

## Step 3 — publish an event

### curl

```bash
curl -s -X POST $API/events \
  -H "Authorization: Bearer $HOOKRELAY_API_KEY" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: order-1234-created" \
  -d '{"event_type":"order.created","payload":{"order_id":"ord_1234","amount_cents":1999}}'
```

```json
{
  "event_id": "01M0THYYDGWH7PV8F09G06Z9HH",
  "event_type": "order.created",
  "deliveries": 2,
  "delivery_ids": ["4c27a301-…", "8b91f0de-…"],
  "duplicate": false,
  "created_at": "2026-08-24T18:54:10.352791Z"
}
```

`202 Accepted` means **durably stored and queued**, not delivered. `deliveries`
is how many endpoints matched — **if it is `0`, nothing was subscribed** and no
webhook will ever arrive.

### Always send an Idempotency-Key

Your own POST can fail *after* HookRelay committed the event — a dropped
connection, a timeout, a pod restart. Retrying without a key publishes the event
twice and your subscribers see it twice.

With a key, the retry returns the original event and fans out nothing:

```json
{ "event_id": "01M0THYYDGWH7PV8F09G06Z9HH", "deliveries": 0, "duplicate": true }
```

Use something derived from the thing that happened — `order-1234-created`,
`invoice-99-paid` — not a random UUID per attempt, which defeats the purpose.
Keys are scoped per tenant.

### Node

```js
// publish.js — no dependencies, Node 18+
export async function publish(eventType, payload, idempotencyKey) {
  const res = await fetch(`${process.env.HOOKRELAY_API_URL}/events`, {
    method: "POST",
    headers: {
      "Authorization": `Bearer ${process.env.HOOKRELAY_API_KEY}`,
      "Content-Type": "application/json",
      // Lets HookRelay collapse our own retries into one event.
      ...(idempotencyKey && { "Idempotency-Key": idempotencyKey }),
    },
    body: JSON.stringify({ event_type: eventType, payload }),
  });

  if (!res.ok) {
    throw new Error(`hookrelay publish failed: ${res.status} ${await res.text()}`);
  }
  const body = await res.json();
  if (body.deliveries === 0 && !body.duplicate) {
    // Not an error, but almost always a misconfiguration: nothing subscribed.
    console.warn(`hookrelay: no endpoint subscribed to ${eventType}`);
  }
  return body;
}

await publish("order.created", { order_id: "ord_1234" }, "order-1234-created");
```

### Python

```python
# publish.py — requires: pip install requests
import os
import requests

API = os.environ["HOOKRELAY_API_URL"]
KEY = os.environ["HOOKRELAY_API_KEY"]


def publish(event_type: str, payload: dict, idempotency_key: str | None = None) -> dict:
    headers = {
        "Authorization": f"Bearer {KEY}",
        "Content-Type": "application/json",
    }
    if idempotency_key:
        # Lets HookRelay collapse our own retries into one event.
        headers["Idempotency-Key"] = idempotency_key

    res = requests.post(
        f"{API}/events",
        headers=headers,
        json={"event_type": event_type, "payload": payload},
        timeout=10,
    )
    res.raise_for_status()
    body = res.json()
    if body["deliveries"] == 0 and not body["duplicate"]:
        # Not an error, but almost always a misconfiguration.
        print(f"hookrelay: no endpoint subscribed to {event_type}")
    return body


publish("order.created", {"order_id": "ord_1234"}, "order-1234-created")
```

### Go

```go
// publish.go — standard library only
package hookrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

type PublishResult struct {
	EventID    string `json:"event_id"`
	Deliveries int    `json:"deliveries"`
	Duplicate  bool   `json:"duplicate"`
}

// Publish sends one event. idempotencyKey may be empty, but supplying one
// derived from the underlying change lets HookRelay collapse retries of this
// call into a single event.
func (c *Client) Publish(ctx context.Context, eventType string, payload any, idempotencyKey string) (*PublishResult, error) {
	body, err := json.Marshal(map[string]any{"event_type": eventType, "payload": payload})
	if err != nil {
		return nil, fmt.Errorf("marshal event: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/events", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("publish event: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusAccepted {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2<<10))
		return nil, fmt.Errorf("publish event: %s: %s", res.Status, msg)
	}

	var out PublishResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}
```

---

## Step 4 — receive and verify

HookRelay signs every request. **Verify the signature before trusting the body**,
or anyone who learns your URL can post fake events at you.

The signed string is `{id}.{timestamp}.{rawBody}` and the algorithm is
HMAC-SHA256, hex-encoded, sent as `v1=<hex>`.

Two rules that matter more than the code:

1. **Verify against the raw body bytes.** Parsing to JSON and re-serialising
   changes whitespace and key order, and the signature will not match. Get the
   raw body first.
2. **Reject old timestamps.** The timestamp is inside the signed material, so it
   cannot be forged — which is what makes a replay window enforceable. Without
   this check a captured request stays valid forever.

### Node / Express

```js
// webhook.js — Express, no extra dependencies
import crypto from "node:crypto";
import express from "express";

const app = express();
const SECRET = process.env.HOOKRELAY_SIGNING_SECRET; // whsec_...
const TOLERANCE_SECONDS = 300;

// The raw body is required: re-serialised JSON will not match the signature.
app.post("/webhooks/hookrelay", express.raw({ type: "*/*" }), (req, res) => {
  const id = req.get("X-HookRelay-Id");
  const timestamp = req.get("X-HookRelay-Timestamp");
  const signature = req.get("X-HookRelay-Signature");

  if (!id || !timestamp || !signature) {
    return res.status(400).send("missing signature headers");
  }

  // Bound how long a captured request stays replayable.
  const age = Math.abs(Math.floor(Date.now() / 1000) - Number(timestamp));
  if (!Number.isFinite(age) || age > TOLERANCE_SECONDS) {
    return res.status(400).send("timestamp outside tolerance");
  }

  const expected = crypto
    .createHmac("sha256", SECRET)
    .update(`${id}.${timestamp}.${req.body}`)
    .digest("hex");

  // timingSafeEqual needs equal lengths, so compare fixed-length digests.
  const provided = signature.replace(/^v1=/, "");
  const ok =
    provided.length === expected.length &&
    crypto.timingSafeEqual(Buffer.from(provided), Buffer.from(expected));

  if (!ok) return res.status(401).send("bad signature");

  const event = JSON.parse(req.body);

  // Deliveries are at-least-once: this may be a repeat. See Step 5.
  if (alreadyProcessed(event.id)) return res.status(200).send("duplicate");

  handle(event);          // your business logic
  markProcessed(event.id);

  // Answer 2xx promptly. Slow handlers get retried as failures.
  res.status(200).send("ok");
});

app.listen(3000);
```

### Python / Flask

```python
# webhook.py — requires: pip install flask
import hashlib
import hmac
import json
import os
import time

from flask import Flask, request

app = Flask(__name__)
SECRET = os.environ["HOOKRELAY_SIGNING_SECRET"].encode()  # whsec_...
TOLERANCE_SECONDS = 300


@app.post("/webhooks/hookrelay")
def hookrelay():
    event_id = request.headers.get("X-HookRelay-Id")
    timestamp = request.headers.get("X-HookRelay-Timestamp")
    signature = request.headers.get("X-HookRelay-Signature", "")

    if not (event_id and timestamp and signature):
        return "missing signature headers", 400

    # Bound how long a captured request stays replayable.
    try:
        age = abs(int(time.time()) - int(timestamp))
    except ValueError:
        return "bad timestamp", 400
    if age > TOLERANCE_SECONDS:
        return "timestamp outside tolerance", 400

    # get_data() is the raw body; re-serialised JSON would not match.
    raw = request.get_data()
    signed = f"{event_id}.{timestamp}.".encode() + raw
    expected = hmac.new(SECRET, signed, hashlib.sha256).hexdigest()

    provided = signature.removeprefix("v1=")
    if not hmac.compare_digest(provided, expected):
        return "bad signature", 401

    event = json.loads(raw)

    # Deliveries are at-least-once: this may be a repeat. See Step 5.
    if already_processed(event["id"]):
        return "duplicate", 200

    handle(event)
    mark_processed(event["id"])
    return "ok", 200
```

### Go

```go
// webhook.go — standard library only
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxBodyBytes = 1 << 20
	tolerance    = 5 * time.Minute
)

type Event struct {
	ID        string          `json:"id"`
	EventType string          `json:"event_type"`
	Timestamp int64           `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

func webhookHandler(secret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-HookRelay-Id")
		ts := r.Header.Get("X-HookRelay-Timestamp")
		sig := r.Header.Get("X-HookRelay-Signature")
		if id == "" || ts == "" || sig == "" {
			http.Error(w, "missing signature headers", http.StatusBadRequest)
			return
		}

		// Bound how long a captured request stays replayable. The timestamp is
		// inside the signed material, so it cannot be altered to defeat this.
		sec, err := strconv.ParseInt(ts, 10, 64)
		if err != nil || time.Since(time.Unix(sec, 0)).Abs() > tolerance {
			http.Error(w, "timestamp outside tolerance", http.StatusBadRequest)
			return
		}

		// Verify against the raw bytes: re-encoded JSON would not match.
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}

		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(id + "." + ts + "."))
		mac.Write(body)
		expected := hex.EncodeToString(mac.Sum(nil))

		if !hmac.Equal([]byte(strings.TrimPrefix(sig, "v1=")), []byte(expected)) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}

		var ev Event
		if err := json.Unmarshal(body, &ev); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		// Deliveries are at-least-once: this may be a repeat. See Step 5.
		if alreadyProcessed(ev.ID) {
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := handle(ev); err != nil {
			// A 5xx tells HookRelay to retry on its backoff schedule.
			http.Error(w, "processing failed", http.StatusInternalServerError)
			return
		}
		markProcessed(ev.ID)
		w.WriteHeader(http.StatusOK)
	}
}
```

### Handling a rotated secret

Rotation keeps the previous secret valid for 24 hours, so during that window
either signature is legitimate. Accept both:

```js
const secrets = [process.env.HOOKRELAY_SIGNING_SECRET, process.env.HOOKRELAY_SIGNING_SECRET_OLD]
  .filter(Boolean);

const ok = secrets.some((secret) => {
  const expected = crypto.createHmac("sha256", secret)
    .update(`${id}.${timestamp}.${req.body}`).digest("hex");
  return provided.length === expected.length &&
    crypto.timingSafeEqual(Buffer.from(provided), Buffer.from(expected));
});
```

Rotate with `POST /endpoints/{id}/rotate-secret`, deploy the new secret as
`HOOKRELAY_SIGNING_SECRET` while moving the old one to `..._OLD`, then drop the
old one after 24 hours.

---

## Step 5 — make your handler idempotent

**This is not optional, and it is the one thing HookRelay cannot do for you.**

Delivery is at-least-once. The unavoidable case: a worker POSTs, your server
processes it and answers 200, and the worker is killed before it records the
success. HookRelay has no idea it worked, so it retries. You get the event twice.

Exactly-once delivery over a network is impossible — see
[EXPLANATION.md](EXPLANATION.md#1-why-at-least-once-and-not-exactly-once). What
*is* possible is making a repeat harmless, in the one place a transaction that
spans "record it" and "act on it" actually exists: your own database.

**Deduplicate on `X-HookRelay-Id`.** It is the event's ULID, byte-identical
across every retry of that event.

```sql
CREATE TABLE processed_webhooks (
    event_id     TEXT PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Do the insert and the work in **one transaction**, letting the primary key be
the arbiter:

```python
def handle_webhook(event):
    with db.transaction():
        try:
            db.execute(
                "INSERT INTO processed_webhooks (event_id) VALUES (%s)",
                (event["id"],),
            )
        except UniqueViolation:
            return "duplicate", 200   # already done; nothing to do
        do_the_actual_work(event)      # rolls back with the insert on failure
    return "ok", 200
```

Checking "have I seen this?" and then inserting as two statements leaves a race
where two concurrent retries both pass the check. Let the database decide.

Prune the table on whatever window exceeds your retry budget — 30 days is
comfortable against HookRelay's ~8 hours.

---

## Testing before you go live

### The bundled receiver

`docker compose up` starts a deliberately misbehaving receiver on port 9090, so
you can exercise every path without writing anything:

| Route | Behaviour | Proves |
|---|---|---|
| `/ok` | always 200 | the happy path |
| `/flaky?rate=0.5` | 500s half the time | retries eventually succeed |
| `/slow?ms=15000` | sleeps past the 10s timeout | timeout handling |
| `/fail?code=500` | always fails | dead-lettering |
| `/verify` | checks the HMAC, 401s on a bad one | your signing is correct |

```bash
curl -X POST $API/endpoints -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"url":"http://receiver:9090/flaky?rate=0.5","description":"flaky test","event_types":["test.event"]}'
```

Then watch the attempts land in the dashboard at `/events/{id}`.

To run the whole suite — fan-out, idempotency, signing, rotation, retries, DLQ,
replay, circuit breaker — `./scripts/verify.sh`.

### Testing your own receiver locally

Deliveries to private and loopback addresses are refused deliberately, so
pointing an endpoint at `http://localhost:3000` will not work. Two options:

**A tunnel** (works against a deployed HookRelay):

```bash
npx localtunnel --port 3000        # or: cloudflared tunnel --url http://localhost:3000
```

Register the public URL it prints.

**Same Docker network** (local stack only) — use the compose service name, which
the local stack permits via `ALLOW_PRIVATE_ENDPOINTS=true`:

```bash
-d '{"url":"http://host.docker.internal:3000/webhooks","...":"..."}'
```

### Verifying your signature check is right

Point an endpoint at the bundled `/verify` route. It performs the same HMAC your
code should and 401s a bad signature. If deliveries to `/verify` succeed, your
signing is correct and any failure in your own handler is your verification
logic, not HookRelay's signing.

---

## What HookRelay sends

### Headers

| Header | Example | Meaning |
|---|---|---|
| `X-HookRelay-Id` | `01M0THYYDGWH7PV8F09G06Z9HH` | Event ULID. **Dedupe on this.** Identical across retries. |
| `X-HookRelay-Timestamp` | `1787597650` | Unix seconds when signed. Inside the signature. |
| `X-HookRelay-Signature` | `v1=3f2a9c…` | `HMAC-SHA256({id}.{timestamp}.{body})`, hex. |
| `X-HookRelay-Attempt` | `3` | Which attempt this is. Useful in logs. |
| `Content-Type` | `application/json` | Always. |

### Body

```json
{
  "id": "01M0THYYDGWH7PV8F09G06Z9HH",
  "event_type": "order.created",
  "timestamp": 1787597650,
  "data": { "order_id": "ord_1234", "amount_cents": 1999 }
}
```

Your original payload is under **`data`**, not at the top level.

### What your response means

| You answer | HookRelay does |
|---|---|
| `2xx` | Marks it delivered. Done. |
| `4xx` | Treats it as a failure and retries. A bad signature is `401`, and retrying will not help — but it also cannot tell your `401` from a transient one. |
| `5xx` | Retries on the backoff schedule. |
| timeout (>10s) | Retries. |
| connection refused | Retries. |

Retries: `5s → 30s → 2m → 10m → 30m → 2h → 5h`, ±20% jitter, then dead-lettered.

**Answer quickly.** The timeout is 10 seconds; queue slow work rather than doing
it inline, or a heavy handler turns into retries and duplicate processing.

---

## Full API reference

Every route needs `Authorization: Bearer <api_key>` except `/auth/*`,
`/healthz` and `/readyz`.

### Publishing

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/events` | Publish. Accepts `Idempotency-Key`. |
| `GET` | `/events` | List. `?event_type=`, `?limit=`, `?cursor=`. |
| `GET` | `/events/{id}` | One event with all deliveries and attempts. |
| `POST` | `/events/{id}/replay` | Re-queue every delivery of the event. |

### Endpoints

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/endpoints` | Create. Returns the signing secret. |
| `GET` | `/endpoints` | List. |
| `GET` | `/endpoints/{id}` | One endpoint. Add `?reveal_secret=true` for the secret. |
| `PATCH` | `/endpoints/{id}` | Update `url`, `description`, `active`, `event_types`. |
| `DELETE` | `/endpoints/{id}` | Delete, cascading to its history. |
| `POST` | `/endpoints/{id}/rotate-secret` | New secret; old valid 24h. |
| `GET` | `/endpoints/{id}/stats` | 24h success rate and volume. |

### Deliveries

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/deliveries` | List. `?status=dead` for the DLQ. |
| `GET` | `/deliveries/{id}` | One delivery with its attempts. |
| `POST` | `/deliveries/{id}/replay` | Reset attempts and re-queue. |
| `POST` | `/deliveries/replay` | Bulk replay: `{"delivery_ids":[…]}`, or `{"status":"dead","limit":1000}` to drain the DLQ. |

### Dashboard and health

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/auth/register` | Create a tenant. Returns the API key **once**. |
| `POST` | `/auth/login` | Email + password → JWT. |
| `GET` | `/auth/me` | Current tenant. |
| `POST` | `/auth/api-key/rotate` | New API key; old one dies immediately. Needs the dashboard token. |
| `GET` | `/stats/overview` | Counts and success rate. |
| `GET` | `/stats/timeseries` | Deliveries/min, success rate, p95. |
| `GET` | `/healthz` | Process alive. |
| `GET` | `/readyz` | Postgres and Redis reachable. |

### Rotating your API key

```bash
# Requires a dashboard token, not the API key itself.
TOKEN=$(curl -s -X POST $API/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"a-long-password"}' | jq -r .token)

curl -s -X POST $API/auth/api-key/rotate -H "Authorization: Bearer $TOKEN"
```

```json
{
  "api_key": "hrk_f9Net3MJ8G…",
  "api_key_prefix": "hrk_f9Net3M",
  "warning": "the previous API key stopped working immediately; this value is shown only once"
}
```

Two deliberate design points:

**It requires the dashboard token, not the API key.** Presenting the API key
gets a `403`. If a key leaks, whoever holds it must not be able to rotate it and
lock you out — so rotation always needs the password-derived credential, which
an attacker holding only the key does not have.

**There is no grace period.** The old key stops working the instant this
returns. Endpoint signing secrets keep the previous value valid for 24 hours
because a receiver can try two secrets; an API key is the thing being *looked
up*, so honouring both would just mean two live credentials — the opposite of
what rotation is for. **Update every publisher before you call this.**

Keys are stored only as a SHA-256 digest, so a lost key cannot be recovered,
only replaced.

---

## Mistakes that cost people hours

| Symptom | Cause | Fix |
|---|---|---|
| `"deliveries": 0` on publish | No endpoint subscribed to that event type | Check `event_types` matches exactly. It is case-sensitive, and `order.created` ≠ `order_created`. |
| Signature never matches | Verifying re-serialised JSON | Verify the **raw body bytes**. In Express that means `express.raw()`, not `express.json()`. |
| Signature never matches | Signing only the body | The signed string is `{id}.{timestamp}.{body}` — all three, dot-separated. |
| Signature never matches | Leaving the `v1=` prefix on | Strip it before comparing. |
| Works locally, fails deployed | Proxy rewriting the body | Ensure your proxy passes the body through byte-for-byte. |
| Everything retries once then succeeds | Handler slower than 10s | Queue the work, answer 2xx immediately. |
| Duplicate processing | No idempotency | [Step 5](#step-5--make-your-handler-idempotent). This will happen eventually. |
| Delivery dead: "refusing to deliver to internal address" | Endpoint points at a private/loopback IP | Working as intended. Use a tunnel — see [Testing](#testing-before-you-go-live). |
| Endpoint stopped receiving | Circuit breaker opened after 20 consecutive failures | Fix the receiver; it resumes after 5 minutes. Replay clears the breaker immediately. |
| `401` publishing | Wrong key, or `whsec_` used instead of `hrk_` | The API key starts `hrk_`; `whsec_` is for verifying. |
| `401` on every request after a rotation | Publisher still holds the old key | Rotation has no grace period. Roll out the new key everywhere. |
| `403` rotating the API key | Presented the API key, not a dashboard token | Sign in at `/auth/login` and use that token. |
| `429` with `Retry-After` | Rate limited | 200 req/s per tenant, 5/s per IP on the auth routes. Tune with `RATE_LIMIT_*`. |
| CORS errors in the dashboard | `CORS_ALLOW_ORIGIN` mismatch | Must match the dashboard origin exactly, scheme included, no trailing slash. |

---

## Further reading

- **[README.md](README.md)** — how HookRelay works internally, with a walkthrough
  of one event's whole life.
- **[GO_LIVE.md](GO_LIVE.md)** — deploy it to a public HTTPS URL for $0.
- **[EXPLANATION.md](EXPLANATION.md)** — design decisions, alternatives rejected,
  and why exactly-once is not on offer.
- **[PRODUCTION.md](PRODUCTION.md)** — hardening checklist before real traffic.
