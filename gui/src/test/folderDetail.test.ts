import { describe, it, expect } from "vitest";
import { parseSyncLog, collapseSessions, sessionToActivity } from "../pages/FolderDetail";

// Log lines: "<ISO timestamp> <message>"
function ts(minutesAgo: number): string {
  return new Date(Date.now() - minutesAgo * 60 * 1000).toISOString();
}

// Fake log with (oldest → newest):
//   t=60m  sync with 1 download + 1 folder created
//   t=50m  idle
//   t=40m  idle
//   t=30m  idle
//   t=20m  sync with 1 upload
//   t=10m  idle
//   t=5m   sync with 1 download
const fakeLog = `
${ts(60)} [sync] starting
${ts(60)} [sync] download "docs/report.pdf"
${ts(60)} [sync] mkdir local "docs"
${ts(50)} [sync] starting
${ts(40)} [sync] starting
${ts(30)} [sync] starting
${ts(20)} [sync] starting
${ts(20)} [sync] upload "notes.txt"
${ts(10)} [sync] starting
${ts(5)} [sync] starting
${ts(5)} [sync] download "photo.jpg"
`.trim();

describe("parseSyncLog", () => {
  it("parses all sessions", () => {
    const sessions = parseSyncLog(fakeLog);
    expect(sessions).toHaveLength(7);
  });

  it("parses active session fields correctly", () => {
    const sessions = parseSyncLog(fakeLog);
    const first = sessions[0]; // t=60m
    expect(first.downloads).toEqual(["docs/report.pdf"]);
    expect(first.mkdirLocals).toEqual(["docs"]);
    expect(first.uploads).toHaveLength(0);

    const upload = sessions[4]; // t=20m
    expect(upload.uploads).toEqual(["notes.txt"]);

    const last = sessions[6]; // t=5m
    expect(last.downloads).toEqual(["photo.jpg"]);
  });
});

describe("collapseSessions + sessionToActivity", () => {
  it("collapses consecutive idle sessions into one", () => {
    // newest-first for display
    const collapsed = collapseSessions([...parseSyncLog(fakeLog)].reverse());
    // expected: [active@5m, idle(10m), active@20m, idle(30m+40m+50m), active@60m]
    expect(collapsed).toHaveLength(5);
  });

  it("leading (most recent) idle uses 'since'", () => {
    // inject a trailing idle at t=1m so the leading entry is idle
    const withLeadingIdle = `${fakeLog}\n${ts(1)} [sync] starting`;
    const s2 = parseSyncLog(withLeadingIdle);
    const collapsed = collapseSessions([...s2].reverse());
    const items = collapsed.map((s, i) => sessionToActivity(s, i === 0));
    expect(items[0].title).toMatch(/^Nothing changed since /);
  });

  it("non-leading single idle between two active syncs uses 'for' without 'ago'", () => {
    const sessions = parseSyncLog(fakeLog);
    const collapsed = collapseSessions([...sessions].reverse());
    // collapsed[1] is the single idle at t=10m (between active@5m and active@20m)
    const items = collapsed.map((s, i) => sessionToActivity(s, i === 0));
    expect(items[1].title).toMatch(/^Nothing changed for /);
    expect(items[1].title).not.toMatch(/since/);
    expect(items[1].title).not.toMatch(/ago/);
  });

  it("non-leading collapsed idle run uses 'for' with merged duration", () => {
    const collapsed = collapseSessions([...parseSyncLog(fakeLog)].reverse());
    // collapsed[3] is the merged idle run (t=30m–t=50m)
    expect(collapsed[3].idleNewest).toBeDefined();
    const items = collapsed.map((s, i) => sessionToActivity(s, i === 0));
    expect(items[3].title).toMatch(/^Nothing changed for /);
    expect(items[3].title).not.toMatch(/since/);
  });

  it("active sessions show file/folder counts in title", () => {
    const sessions = parseSyncLog(fakeLog);
    const collapsed = collapseSessions([...sessions].reverse());
    const items = collapsed.map((s, i) => sessionToActivity(s, i === 0));
    const activeAtEnd = items[4]; // active@60m: 1 download + 1 folder
    expect(activeAtEnd.title).toMatch(/downloaded/);
    expect(activeAtEnd.title).toMatch(/folder/);
  });
});
