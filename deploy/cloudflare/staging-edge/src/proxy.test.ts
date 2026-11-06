import { describe, expect, it } from "vitest";
import { edgeResponseHeaders, errorResponse, originRequestHeaders, publicLocation, stripCookie } from "./proxy";

const host = "webee-staging-edge.example.workers.dev";

describe("originRequestHeaders", () => {
  const incoming = new Headers({
    Host: host,
    Cookie: "CF_Authorization=jwt; webee_session=abc; theme=dark",
    "Content-Type": "application/json",
    Origin: `https://${host}`,
    "Sec-Fetch-Site": "same-origin",
    "X-Forwarded-For": "6.6.6.6",
    "X-Forwarded-Host": "evil.example",
    Forwarded: "for=6.6.6.6",
    "X-Real-IP": "6.6.6.6",
    "CF-Connecting-IP": "203.0.113.7",
    "Cf-Access-Jwt-Assertion": "jwt",
    "Cf-Access-Authenticated-User-Email": "tester@example.test",
  });
  const out = originRequestHeaders(incoming, "203.0.113.7");

  it("replaces client-sent forwarding headers with the real client address", () => {
    expect(out.get("X-Forwarded-For")).toBe("203.0.113.7");
    expect(out.get("X-Forwarded-Proto")).toBe("https");
    expect(out.has("X-Forwarded-Host")).toBe(false);
    expect(out.has("Forwarded")).toBe(false);
    expect(out.has("X-Real-IP")).toBe(false);
  });

  it("removes Access artefacts and Cloudflare headers", () => {
    expect(out.has("Cf-Access-Jwt-Assertion")).toBe(false);
    expect(out.has("Cf-Access-Authenticated-User-Email")).toBe(false);
    expect(out.has("CF-Connecting-IP")).toBe(false);
    expect(out.get("Cookie")).toBe("webee_session=abc; theme=dark");
  });

  it("keeps what the API checks", () => {
    expect(out.get("Origin")).toBe(`https://${host}`);
    expect(out.get("Sec-Fetch-Site")).toBe("same-origin");
    expect(out.get("Content-Type")).toBe("application/json");
    expect(out.has("Host")).toBe(false);
  });

  it("drops the Cookie header when only the Access cookie was there", () => {
    expect(originRequestHeaders(new Headers({ Cookie: "CF_Authorization=jwt" }), null).has("Cookie")).toBe(false);
  });

  it("sends no X-Forwarded-For without a client address", () => {
    expect(originRequestHeaders(new Headers({ "X-Forwarded-For": "6.6.6.6" }), null).has("X-Forwarded-For")).toBe(false);
  });
});

describe("stripCookie", () => {
  it("does not match on a name prefix", () => {
    expect(stripCookie("CF_Authorization_x=1; CF_Authorization=2", "CF_Authorization")).toBe("CF_Authorization_x=1");
  });
});

describe("publicLocation", () => {
  it("turns the origin's absolute http redirect into the public https URL", () => {
    expect(publicLocation(`http://${host}:8080/assets/?q=1`, host)).toBe(`https://${host}/assets/?q=1`);
  });
  it("leaves relative and foreign redirects alone", () => {
    expect(publicLocation("/login", host)).toBe("/login");
    expect(publicLocation("http://other.example/x", host)).toBe("http://other.example/x");
  });
});

describe("edgeResponseHeaders", () => {
  it("adds HSTS and noindex and keeps every Set-Cookie", () => {
    const origin = new Headers();
    origin.append("Set-Cookie", "a=1; Secure; HttpOnly");
    origin.append("Set-Cookie", "b=2; Secure; HttpOnly");
    const out = edgeResponseHeaders(origin, host);
    expect(out.get("Strict-Transport-Security")).toBe("max-age=86400");
    expect(out.get("X-Robots-Tag")).toBe("noindex, nofollow");
    expect(out.getSetCookie()).toEqual(["a=1; Secure; HttpOnly", "b=2; Secure; HttpOnly"]);
  });
});

describe("errorResponse", () => {
  it("uses the API error envelope", async () => {
    const res = errorResponse(403, "edge.access_denied", "no", "ray-1");
    expect(res.status).toBe(403);
    expect(await res.json()).toEqual({ error: { code: "edge.access_denied", message: "no" }, request_id: "ray-1" });
  });
});
