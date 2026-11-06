// Request and response rewriting between the browser and the staging origin.
// Pure functions, so the rules can be tested without the Workers runtime.

const ACCESS_COOKIE = "CF_Authorization";

// Client-supplied proxy headers are never passed on: the API believes the
// right-most X-Forwarded-For entry its trusted proxies did not add, so the
// Worker writes that entry itself.
const DROPPED_PREFIXES = ["cf-", "x-forwarded-"];
// Host comes from the origin URL, which carries the public host name.
const DROPPED = new Set(["host", "forwarded", "x-real-ip", "true-client-ip"]);

/** Headers for the origin request: proxy headers rebuilt, Access artefacts removed. */
export function originRequestHeaders(incoming: Headers, clientIp: string | null): Headers {
  const out = new Headers();
  incoming.forEach((value, name) => {
    const lower = name.toLowerCase();
    if (DROPPED.has(lower) || DROPPED_PREFIXES.some((p) => lower.startsWith(p))) return;
    if (lower === "cookie") {
      const kept = stripCookie(value, ACCESS_COOKIE);
      if (kept) out.set("Cookie", kept);
      return;
    }
    out.append(name, value);
  });
  if (clientIp) out.set("X-Forwarded-For", clientIp);
  out.set("X-Forwarded-Proto", "https");
  return out;
}

export function stripCookie(header: string, name: string): string {
  return header
    .split(";")
    .map((c) => c.trim())
    .filter((c) => c !== "" && c.split("=", 1)[0] !== name)
    .join("; ");
}

/**
 * nginx answers some redirects with an absolute http://<host>:8080 URL. On the
 * public side the same host is https on the default port.
 */
export function publicLocation(location: string, publicHost: string): string {
  try {
    const url = new URL(location);
    if (url.protocol === "http:" && url.hostname === publicHost) {
      url.protocol = "https:";
      url.port = "";
      return url.toString();
    }
  } catch {
    // relative or malformed: leave it to the browser
  }
  return location;
}

/** Response headers with the staging edge additions. */
export function edgeResponseHeaders(origin: Headers, publicHost: string): Headers {
  const out = new Headers(origin);
  out.set("Strict-Transport-Security", "max-age=86400");
  out.set("X-Robots-Tag", "noindex, nofollow");
  const location = out.get("Location");
  if (location) out.set("Location", publicLocation(location, publicHost));
  return out;
}

/** Same envelope as the API (internal/httpapi/respond.go). */
export function errorResponse(status: number, code: string, message: string, requestId: string | null): Response {
  return new Response(JSON.stringify({ error: { code, message }, request_id: requestId ?? "" }), {
    status,
    headers: {
      "Content-Type": "application/json; charset=utf-8",
      "Cache-Control": "no-store",
      "X-Robots-Tag": "noindex, nofollow",
    },
  });
}
