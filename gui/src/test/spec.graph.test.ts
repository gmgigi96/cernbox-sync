import { describe, it, expect, vi, beforeEach } from "vitest";
import { listSpaces } from "../graph";

function mockDrives(value: object[]) {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    text: () => Promise.resolve(JSON.stringify({ value })),
    json: () => Promise.resolve({ value }),
  }));
}

const dav = "https://cern.ch/remote.php/dav/spaces";

describe("space list (plan 1.4, 8.6)", () => {
  beforeEach(() => vi.restoreAllMocks());

  // Plan 1.4: personal space, project spaces and received shares are all listed.
  it("1.4 lists the personal space, project spaces and received shares", async () => {
    mockDrives([
      { id: "p1", name: "Personal", driveType: "personal", root: { webDavUrl: `${dav}/p1` } },
      { id: "proj1", name: "Project A", driveType: "project", root: { webDavUrl: `${dav}/proj1` } },
      { id: "proj2", name: "Project B", driveType: "project", root: { webDavUrl: `${dav}/proj2` } },
      { id: "sh1", name: "Shared folder", driveType: "mountpoint", root: { webDavUrl: `${dav}/sh1` } },
    ]);
    const spaces = await listSpaces("https://cern.ch", "u", "p");
    expect(spaces.map((s) => s.drive_type).sort()).toEqual(["mountpoint", "personal", "project", "project"]);
    expect(spaces.map((s) => s.name)).toEqual(["Personal", "Project A", "Project B", "Shared folder"]);
    for (const s of spaces) expect(s.webdav_url).toContain(`${dav}/${s.id}`);
  });

  // Plan 8.6: a share received both directly and via a group is shown once.
  it("8.6 shows a share received directly and via a group only once", async () => {
    mockDrives([
      { id: "p1", name: "Personal", driveType: "personal", root: { webDavUrl: `${dav}/p1` } },
      { id: "sh1", name: "Shared folder", driveType: "mountpoint", root: { webDavUrl: `${dav}/sh1` } },
      // same share, second entry (received through a group)
      { id: "sh1", name: "Shared folder", driveType: "mountpoint", root: { webDavUrl: `${dav}/sh1` } },
    ]);
    const spaces = await listSpaces("https://cern.ch", "u", "p");
    expect(spaces.filter((s) => s.id === "sh1")).toHaveLength(1);
    expect(spaces).toHaveLength(2);
  });
});
