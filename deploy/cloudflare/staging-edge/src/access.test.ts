import { beforeAll, describe, expect, it } from "vitest";
import { accessTokenValid, claimsProblem, type AccessKey, type KeySource } from "./access";

const cfg = { teamDomain: "webee-test.cloudflareaccess.com", audience: "aud-tag-123" };
const now = 1_800_000_000;

function b64url(data: Uint8Array | string): string {
  const bytes = typeof data === "string" ? new TextEncoder().encode(data) : data;
  return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

let privateKey: CryptoKey;
let publicJwk: AccessKey;
let otherJwk: AccessKey;

async function rsaPair() {
  return (await crypto.subtle.generateKey(
    { name: "RSASSA-PKCS1-v1_5", modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-256" },
    true,
    ["sign", "verify"],
  )) as CryptoKeyPair;
}

beforeAll(async () => {
  const pair = await rsaPair();
  privateKey = pair.privateKey;
  publicJwk = { ...((await crypto.subtle.exportKey("jwk", pair.publicKey)) as JsonWebKey), kid: "k1" };
  otherJwk = { ...((await crypto.subtle.exportKey("jwk", (await rsaPair()).publicKey)) as JsonWebKey), kid: "k1" };
});

async function sign(claims: object, header: object = { alg: "RS256", kid: "k1" }): Promise<string> {
  const input = `${b64url(JSON.stringify(header))}.${b64url(JSON.stringify(claims))}`;
  const sig = await crypto.subtle.sign("RSASSA-PKCS1-v1_5", privateKey, new TextEncoder().encode(input));
  return `${input}.${b64url(new Uint8Array(sig))}`;
}

const good = { aud: [cfg.audience], iss: `https://${cfg.teamDomain}`, exp: now + 600, iat: now };
const keys = (list: () => AccessKey[]): KeySource => async () => list();

describe("claimsProblem", () => {
  it("accepts matching claims", () => expect(claimsProblem(good, cfg, now)).toBeNull());
  it("accepts a single-string audience", () => expect(claimsProblem({ ...good, aud: cfg.audience }, cfg, now)).toBeNull());
  it("rejects another application's audience", () => expect(claimsProblem({ ...good, aud: ["other"] }, cfg, now)).toBe("audience"));
  it("rejects another team", () =>
    expect(claimsProblem({ ...good, iss: "https://evil.cloudflareaccess.com" }, cfg, now)).toBe("issuer"));
  it("rejects an expired token", () => expect(claimsProblem({ ...good, exp: now - 120 }, cfg, now)).toBe("expired"));
  it("rejects a token without exp", () => expect(claimsProblem({ ...good, exp: undefined }, cfg, now)).toBe("expired"));
  it("rejects a token not valid yet", () => expect(claimsProblem({ ...good, nbf: now + 600 }, cfg, now)).toBe("not yet valid"));
});

describe("accessTokenValid", () => {
  it("accepts a correctly signed token", async () => {
    expect(await accessTokenValid(await sign(good), cfg, keys(() => [publicJwk]), now)).toBe(true);
  });

  it("refuses a missing token", async () => {
    expect(await accessTokenValid(null, cfg, keys(() => [publicJwk]), now)).toBe(false);
  });

  it("refuses everything when the audience is not configured", async () => {
    expect(await accessTokenValid(await sign(good), { ...cfg, audience: "" }, keys(() => [publicJwk]), now)).toBe(false);
  });

  it("refuses a token signed by another key", async () => {
    expect(await accessTokenValid(await sign(good), cfg, keys(() => [otherJwk]), now)).toBe(false);
  });

  it("refuses alg none and HS256", async () => {
    for (const alg of ["none", "HS256"]) {
      expect(await accessTokenValid(await sign(good, { alg, kid: "k1" }), cfg, keys(() => [publicJwk]), now)).toBe(false);
    }
  });

  it("refuses a tampered payload", async () => {
    const [h, , s] = (await sign(good)).split(".");
    const forged = `${h}.${b64url(JSON.stringify({ ...good, exp: now + 99_999 }))}.${s}`;
    expect(await accessTokenValid(forged, cfg, keys(() => [publicJwk]), now)).toBe(false);
  });

  it("refuses garbage", async () => {
    expect(await accessTokenValid("not.a.jwt", cfg, keys(() => [publicJwk]), now)).toBe(false);
  });

  it("refreshes the key set once for a rotated key", async () => {
    const calls: boolean[] = [];
    const source: KeySource = async (refresh) => {
      calls.push(refresh);
      return refresh ? [publicJwk] : [];
    };
    expect(await accessTokenValid(await sign(good), cfg, source, now)).toBe(true);
    expect(calls).toEqual([false, true]);
  });

  it("fails closed when the key set cannot be fetched", async () => {
    const source: KeySource = async () => {
      throw new Error("network");
    };
    expect(await accessTokenValid(await sign(good), cfg, source, now)).toBe(false);
  });
});
