// Verifies the Cloudflare Access JWT on every request, as Cloudflare advises
// for anything behind Access: if Access is ever switched off or bypassed for
// this Worker, requests are refused instead of reaching the staging site.

export interface AccessConfig {
  /** Zero Trust team domain, e.g. "webee.cloudflareaccess.com". */
  teamDomain: string;
  /** Application Audience (AUD) tag. */
  audience: string;
}

export interface AccessClaims {
  aud?: string | string[];
  iss?: string;
  exp?: number;
  nbf?: number;
  iat?: number;
}

interface ParsedJwt {
  header: { alg?: string; kid?: string };
  claims: AccessClaims;
  signingInput: Uint8Array;
  signature: Uint8Array;
}

export type AccessKey = JsonWebKey & { kid?: string };
export type KeySource = (refresh: boolean) => Promise<AccessKey[]>;

const LEEWAY_SECONDS = 60;

function base64UrlDecode(part: string): Uint8Array {
  const b64 = part.replace(/-/g, "+").replace(/_/g, "/");
  const bin = atob(b64 + "=".repeat((4 - (b64.length % 4)) % 4));
  return Uint8Array.from(bin, (c) => c.charCodeAt(0));
}

export function parseJwt(token: string): ParsedJwt | null {
  const parts = token.split(".");
  if (parts.length !== 3) return null;
  const [h, p, s] = parts as [string, string, string];
  try {
    const text = new TextDecoder();
    const header: unknown = JSON.parse(text.decode(base64UrlDecode(h)));
    const claims: unknown = JSON.parse(text.decode(base64UrlDecode(p)));
    if (typeof header !== "object" || header === null || typeof claims !== "object" || claims === null) {
      return null;
    }
    return {
      header: header as ParsedJwt["header"],
      claims: claims as AccessClaims,
      signingInput: new TextEncoder().encode(`${h}.${p}`),
      signature: base64UrlDecode(s),
    };
  } catch {
    return null;
  }
}

/** Returns why the claims are unacceptable, or null when they are fine. */
export function claimsProblem(claims: AccessClaims, cfg: AccessConfig, nowSeconds: number): string | null {
  const audiences = Array.isArray(claims.aud) ? claims.aud : claims.aud === undefined ? [] : [claims.aud];
  if (!audiences.includes(cfg.audience)) return "audience";
  if (claims.iss !== `https://${cfg.teamDomain}`) return "issuer";
  if (typeof claims.exp !== "number" || claims.exp + LEEWAY_SECONDS < nowSeconds) return "expired";
  if (typeof claims.nbf === "number" && claims.nbf - LEEWAY_SECONDS > nowSeconds) return "not yet valid";
  return null;
}

async function signatureValid(jwt: ParsedJwt, keys: AccessKey[]): Promise<boolean> {
  const key = keys.find((k) => k.kid === jwt.header.kid);
  if (!key) return false;
  const imported = await crypto.subtle.importKey(
    "jwk",
    { kty: key.kty, n: key.n, e: key.e, alg: "RS256", ext: true },
    { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" },
    false,
    ["verify"],
  );
  return crypto.subtle.verify("RSASSA-PKCS1-v1_5", imported, jwt.signature, jwt.signingInput);
}

/**
 * Checks the token's claims and RS256 signature. An unknown key id triggers
 * one refresh of the key set, which covers Access rotating its keys.
 */
export async function accessTokenValid(
  token: string | null,
  cfg: AccessConfig,
  keys: KeySource,
  nowSeconds: number,
): Promise<boolean> {
  if (!token || !cfg.audience || !cfg.teamDomain) return false;
  const jwt = parseJwt(token);
  if (!jwt || jwt.header.alg !== "RS256" || !jwt.header.kid) return false;
  if (claimsProblem(jwt.claims, cfg, nowSeconds) !== null) return false;
  try {
    if (await signatureValid(jwt, await keys(false))) return true;
    return await signatureValid(jwt, await keys(true));
  } catch {
    return false;
  }
}

const KEY_CACHE_MS = 60 * 60 * 1000;
let cached: { domain: string; keys: AccessKey[]; at: number } | null = null;

/** Fetches the team's signing keys, cached per isolate for an hour. */
export function teamKeys(teamDomain: string): KeySource {
  return async (refresh) => {
    const now = Date.now();
    if (!refresh && cached && cached.domain === teamDomain && now - cached.at < KEY_CACHE_MS) {
      return cached.keys;
    }
    const res = await fetch(`https://${teamDomain}/cdn-cgi/access/certs`, { signal: AbortSignal.timeout(5000) });
    if (!res.ok) throw new Error(`access certs: HTTP ${res.status}`);
    const body = (await res.json()) as { keys?: AccessKey[] };
    cached = { domain: teamDomain, keys: body.keys ?? [], at: now };
    return cached.keys;
  };
}
