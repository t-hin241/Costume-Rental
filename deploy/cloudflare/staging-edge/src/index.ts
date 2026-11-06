// webee-staging-edge: the only public entrance to staging (deploy/staging/README.md).
//   fetch:     Access JWT check, then proxy to the VM through the Workers VPC
//              binding (Cloudflare Tunnel, no public hostname, no open port).
//   scheduled: readiness and backup-freshness monitor, alerts on Telegram.

import { accessTokenValid, teamKeys } from "./access";
import { alerts, nextState, parseState, sameState, type Observation } from "./monitor";
import { edgeResponseHeaders, errorResponse, originRequestHeaders } from "./proxy";

export interface Env {
  ORIGIN: Fetcher;
  BACKUPS: R2Bucket;
  STATE: KVNamespace;
  ACCESS_TEAM_DOMAIN: string;
  ACCESS_AUD: string;
  BACKUP_PREFIX: string;
  BACKUP_MAX_AGE_HOURS: string;
  TELEGRAM_BOT_TOKEN?: string;
  TELEGRAM_CHAT_ID?: string;
}

const STATE_KEY = "monitor-state";

async function handleFetch(request: Request, env: Env): Promise<Response> {
  const requestId = request.headers.get("cf-ray");
  const cfg = { teamDomain: env.ACCESS_TEAM_DOMAIN, audience: env.ACCESS_AUD };
  if (!cfg.audience) console.error("ACCESS_AUD is not set: refusing every request");
  const token = request.headers.get("cf-access-jwt-assertion");
  if (!(await accessTokenValid(token, cfg, teamKeys(cfg.teamDomain), Math.floor(Date.now() / 1000)))) {
    return errorResponse(403, "edge.access_denied", "sign in through Cloudflare Access first", requestId);
  }

  const url = new URL(request.url);
  // The VPC Service decides where this goes; the URL's host only becomes the
  // Host header, which must be the public one for the API's origin checks.
  const target = `http://${url.host}${url.pathname}${url.search}`;
  const hasBody = request.method !== "GET" && request.method !== "HEAD";
  let res: Response;
  try {
    res = await env.ORIGIN.fetch(target, {
      method: request.method,
      headers: originRequestHeaders(request.headers, request.headers.get("cf-connecting-ip")),
      body: hasBody ? request.body : null,
      redirect: "manual",
    });
  } catch (err) {
    console.error("origin unreachable", String(err));
    return errorResponse(502, "edge.origin_unavailable", "staging is not reachable right now", requestId);
  }
  return new Response(res.body, {
    status: res.status,
    statusText: res.statusText,
    headers: edgeResponseHeaders(res.headers, url.hostname),
  });
}

async function originReady(env: Env): Promise<boolean> {
  try {
    const res = await env.ORIGIN.fetch("http://staging-origin/readyz", { signal: AbortSignal.timeout(10_000) });
    await res.body?.cancel();
    return res.ok;
  } catch {
    return false;
  }
}

async function newestBackupAt(env: Env): Promise<number | null> {
  let newest: number | null = null;
  let cursor: string | undefined;
  do {
    const page = await env.BACKUPS.list({ prefix: env.BACKUP_PREFIX || undefined, cursor, limit: 1000 });
    for (const obj of page.objects) {
      if (obj.key.endsWith("/db.dump") || obj.key === "db.dump") {
        const at = obj.uploaded.getTime();
        if (newest === null || at > newest) newest = at;
      }
    }
    cursor = page.truncated ? page.cursor : undefined;
  } while (cursor);
  return newest;
}

async function sendTelegram(env: Env, text: string): Promise<boolean> {
  if (!env.TELEGRAM_BOT_TOKEN || !env.TELEGRAM_CHAT_ID) {
    console.warn("telegram not configured; alert:", text);
    return false;
  }
  try {
    const res = await fetch(`https://api.telegram.org/bot${env.TELEGRAM_BOT_TOKEN}/sendMessage`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ chat_id: env.TELEGRAM_CHAT_ID, text }),
      signal: AbortSignal.timeout(10_000),
    });
    await res.body?.cancel();
    if (!res.ok) console.error("telegram send failed", res.status);
    return res.ok;
  } catch (err) {
    console.error("telegram send failed", String(err));
    return false;
  }
}

async function runMonitor(env: Env): Promise<void> {
  const now = Date.now();
  const maxAgeHours = Number(env.BACKUP_MAX_AGE_HOURS) || 26;
  let backupAt: number | null;
  try {
    backupAt = await newestBackupAt(env);
  } catch (err) {
    console.error("listing backups failed", String(err));
    backupAt = null;
  }
  const obs: Observation = { originReady: await originReady(env), newestBackupAt: backupAt };

  const prev = parseState(await env.STATE.get(STATE_KEY));
  const next = nextState(prev, obs, maxAgeHours * 3_600_000, now);
  if (sameState(prev, next)) return;

  let delivered = true;
  for (const text of alerts(prev, next, obs, now)) {
    delivered = (await sendTelegram(env, text)) && delivered;
  }
  // An undelivered alert keeps the old state, so the next run (five minutes
  // later) tries again instead of losing it; nothing retries in between.
  if (delivered) await env.STATE.put(STATE_KEY, JSON.stringify(next));
}

export default {
  fetch: (request, env) => handleFetch(request, env),
  scheduled: (_controller, env, ctx) => {
    ctx.waitUntil(runMonitor(env));
  },
} satisfies ExportedHandler<Env>;
