// Staging monitor (decision S-08): origin readiness and backup freshness,
// announced on Telegram only when a state changes.

export interface MonitorState {
  /** Consecutive failed readiness checks, capped at DOWN_AFTER. */
  originFailures: number;
  origin: "up" | "down";
  backup: "fresh" | "stale";
}

export interface Observation {
  originReady: boolean;
  /** Upload time (ms since epoch) of the newest db.dump, or null if none. */
  newestBackupAt: number | null;
}

/** Two misses in a row (about ten minutes) before calling the origin down. */
export const DOWN_AFTER = 2;

export const INITIAL_STATE: MonitorState = { originFailures: 0, origin: "up", backup: "fresh" };

export function nextState(prev: MonitorState, obs: Observation, maxBackupAgeMs: number, now: number): MonitorState {
  const originFailures = obs.originReady ? 0 : Math.min(prev.originFailures + 1, DOWN_AFTER);
  const origin = originFailures >= DOWN_AFTER ? "down" : obs.originReady ? "up" : prev.origin;
  const backup = obs.newestBackupAt !== null && now - obs.newestBackupAt <= maxBackupAgeMs ? "fresh" : "stale";
  return { originFailures, origin, backup };
}

export function alerts(prev: MonitorState, next: MonitorState, obs: Observation, now: number): string[] {
  const out: string[] = [];
  if (prev.origin !== next.origin) {
    out.push(
      next.origin === "down"
        ? "🔴 Webee staging: /readyz lỗi 2 lần liên tiếp (API, DB hoặc tunnel có vấn đề)."
        : "🟢 Webee staging: /readyz đã trả lời bình thường trở lại.",
    );
  }
  if (prev.backup !== next.backup) {
    if (next.backup === "stale") {
      const age =
        obs.newestBackupAt === null ? "chưa có bản nào trên R2" : `bản mới nhất cách đây ${Math.floor((now - obs.newestBackupAt) / 3_600_000)} giờ`;
      out.push(`🟠 Webee staging: backup quá hạn (${age}). Xem ~/webee-backup.log trên VM.`);
    } else {
      out.push("🟢 Webee staging: đã có backup mới trên R2.");
    }
  }
  return out;
}

export function sameState(a: MonitorState, b: MonitorState): boolean {
  return a.originFailures === b.originFailures && a.origin === b.origin && a.backup === b.backup;
}

export function parseState(raw: string | null): MonitorState {
  if (!raw) return INITIAL_STATE;
  try {
    const v = JSON.parse(raw) as Partial<MonitorState>;
    return {
      originFailures: typeof v.originFailures === "number" ? v.originFailures : 0,
      origin: v.origin === "down" ? "down" : "up",
      backup: v.backup === "stale" ? "stale" : "fresh",
    };
  } catch {
    return INITIAL_STATE;
  }
}
