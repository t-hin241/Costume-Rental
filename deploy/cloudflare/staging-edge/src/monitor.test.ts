import { describe, expect, it } from "vitest";
import { alerts, INITIAL_STATE, nextState, parseState, sameState, type MonitorState } from "./monitor";

const hour = 3_600_000;
const now = 1_800_000_000_000;
const maxAge = 26 * hour;
const fresh = now - 2 * hour;

function step(prev: MonitorState, originReady: boolean, newestBackupAt: number | null = fresh) {
  const obs = { originReady, newestBackupAt };
  const next = nextState(prev, obs, maxAge, now);
  return { next, messages: alerts(prev, next, obs, now) };
}

describe("origin readiness", () => {
  it("stays quiet on a single miss", () => {
    const { next, messages } = step(INITIAL_STATE, false);
    expect(next.origin).toBe("up");
    expect(messages).toEqual([]);
  });

  it("alerts once after two misses in a row, then stays quiet", () => {
    const first = step(INITIAL_STATE, false).next;
    const second = step(first, false);
    expect(second.next.origin).toBe("down");
    expect(second.messages).toHaveLength(1);
    const third = step(second.next, false);
    expect(third.messages).toEqual([]);
    expect(sameState(second.next, third.next)).toBe(true);
  });

  it("announces the recovery once", () => {
    const down: MonitorState = { originFailures: 2, origin: "down", backup: "fresh" };
    const { next, messages } = step(down, true);
    expect(next).toEqual(INITIAL_STATE);
    expect(messages).toHaveLength(1);
    expect(messages[0]).toContain("trở lại");
  });

  it("forgets a single miss after a success", () => {
    const missed = step(INITIAL_STATE, false).next;
    expect(step(missed, true).next.originFailures).toBe(0);
  });
});

describe("backup freshness", () => {
  it("is fresh within the window and stale past it", () => {
    expect(step(INITIAL_STATE, true, now - 25 * hour).next.backup).toBe("fresh");
    const stale = step(INITIAL_STATE, true, now - 27 * hour);
    expect(stale.next.backup).toBe("stale");
    expect(stale.messages[0]).toContain("27 giờ");
  });

  it("treats no backup at all as stale", () => {
    const { next, messages } = step(INITIAL_STATE, true, null);
    expect(next.backup).toBe("stale");
    expect(messages[0]).toContain("chưa có bản nào");
  });
});

describe("parseState", () => {
  it("falls back to the initial state on missing or broken data", () => {
    expect(parseState(null)).toEqual(INITIAL_STATE);
    expect(parseState("{not json")).toEqual(INITIAL_STATE);
    expect(parseState('{"origin":"weird"}')).toEqual(INITIAL_STATE);
  });

  it("round-trips a stored state", () => {
    const s: MonitorState = { originFailures: 2, origin: "down", backup: "stale" };
    expect(parseState(JSON.stringify(s))).toEqual(s);
  });
});
