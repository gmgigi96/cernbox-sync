import { describe, it, expect, vi, beforeEach } from "vitest";
import { listSpaces, listRemoteResources } from "../graph";

// ── listSpaces ─────────────────────────────────────────────────────────────────

describe("listSpaces", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  function mockFetch(body: object, status = 200) {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
      ok: status >= 200 && status < 300,
      status,
      text: () => Promise.resolve(JSON.stringify(body)),
      json: () => Promise.resolve(body),
    }));
  }

  it("returns mapped Space objects from the Graph API response", async () => {
    mockFetch({
      value: [
        {
          id: "abc-123",
          name: "My Files",
          driveType: "personal",
          root: { webDavUrl: "https://cern.ch/remote.php/dav/spaces/abc-123" },
          description: "Personal space",
        },
      ],
    });

    const spaces = await listSpaces("https://cern.ch", "user", "pass");
    expect(spaces).toHaveLength(1);
    expect(spaces[0]).toMatchObject({
      id: "abc-123",
      name: "My Files",
      drive_type: "personal",
      webdav_url: "https://cern.ch/remote.php/dav/spaces/abc-123",
      description: "Personal space",
    });
  });

  it("strips driveAlias suffix and appends id to build a stable WebDAV URL", async () => {
    mockFetch({
      value: [
        {
          id: "eoshome-g$XXXX",
          name: "EOS Home",
          driveType: "personal",
          driveAlias: "eos/user/g/gdelmont",
          root: { webDavUrl: "https://cern.ch/remote.php/dav/spaces/eos/user/g/gdelmont" },
        },
      ],
    });

    const spaces = await listSpaces("https://cern.ch", "user", "pass");
    expect(spaces[0].webdav_url).toBe("https://cern.ch/remote.php/dav/spaces/eoshome-g$XXXX");
  });

  it("handles trailing slash on serverUrl", async () => {
    mockFetch({ value: [] });
    await listSpaces("https://cern.ch/", "user", "pass");
    const url = (fetch as ReturnType<typeof vi.fn>).mock.calls[0][0];
    expect(url).toBe("https://cern.ch/graph/v1beta1/me/drives");
  });

  it("throws when the API returns a non-ok status", async () => {
    mockFetch({ message: "Unauthorized" }, 401);
    await expect(listSpaces("https://cern.ch", "user", "wrongpass")).rejects.toThrow("401");
  });

  it("returns empty array when value is missing from response", async () => {
    mockFetch({});
    const spaces = await listSpaces("https://cern.ch", "user", "pass");
    expect(spaces).toEqual([]);
  });

  it("uses Basic auth header with base64-encoded credentials", async () => {
    mockFetch({ value: [] });
    await listSpaces("https://cern.ch", "alice", "s3cr3t");
    const headers = (fetch as ReturnType<typeof vi.fn>).mock.calls[0][1].headers;
    expect(headers.Authorization).toBe("Basic " + btoa("alice:s3cr3t"));
  });
});

// ── listRemoteResources ────────────────────────────────────────────────────────

const PROPFIND_207 = `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:OC="http://owncloud.org/ns">
  <D:response>
    <D:href>/remote.php/dav/spaces/abc-123/</D:href>
    <D:propstat>
      <D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/remote.php/dav/spaces/abc-123/Documents/</D:href>
    <D:propstat>
      <D:prop>
        <D:displayname>Documents</D:displayname>
        <D:resourcetype><D:collection/></D:resourcetype>
        <OC:size>8192</OC:size>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/remote.php/dav/spaces/abc-123/readme.txt</D:href>
    <D:propstat>
      <D:prop>
        <D:displayname>readme.txt</D:displayname>
        <D:resourcetype/>
        <D:getcontentlength>1234</D:getcontentlength>
        <D:getlastmodified>Mon, 01 Jan 2024 00:00:00 GMT</D:getlastmodified>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`;

describe("listRemoteResources", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  function mockPropfind(body: string, status = 207) {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
      ok: status >= 200 && status < 300 || status === 207,
      status,
      text: () => Promise.resolve(body),
    }));
  }

  it("parses PROPFIND 207 response and skips the root collection", async () => {
    mockPropfind(PROPFIND_207);
    const resources = await listRemoteResources("https://cern.ch/remote.php/dav/spaces/abc-123/", "user", "pass");
    expect(resources).toHaveLength(2);
  });

  it("correctly identifies collections vs files", async () => {
    mockPropfind(PROPFIND_207);
    const resources = await listRemoteResources("https://cern.ch/remote.php/dav/spaces/abc-123/", "user", "pass");
    const dir = resources.find((r) => r.name === "Documents");
    const file = resources.find((r) => r.name === "readme.txt");
    expect(dir?.isCollection).toBe(true);
    expect(file?.isCollection).toBe(false);
  });

  it("parses collection size from OC:size", async () => {
    mockPropfind(PROPFIND_207);
    const resources = await listRemoteResources("https://cern.ch/remote.php/dav/spaces/abc-123/", "user", "pass");
    expect(resources.find((r) => r.name === "Documents")?.size).toBe(8192);
  });

  it("parses file size from getcontentlength", async () => {
    mockPropfind(PROPFIND_207);
    const resources = await listRemoteResources("https://cern.ch/remote.php/dav/spaces/abc-123/", "user", "pass");
    expect(resources.find((r) => r.name === "readme.txt")?.size).toBe(1234);
  });

  it("parses lastModified for files", async () => {
    mockPropfind(PROPFIND_207);
    const resources = await listRemoteResources("https://cern.ch/remote.php/dav/spaces/abc-123/", "user", "pass");
    expect(resources.find((r) => r.name === "readme.txt")?.lastModified).toBe("Mon, 01 Jan 2024 00:00:00 GMT");
  });

  it("constructs full href from host + path", async () => {
    mockPropfind(PROPFIND_207);
    const resources = await listRemoteResources("https://cern.ch/remote.php/dav/spaces/abc-123/", "user", "pass");
    expect(resources[0].href).toMatch(/^https:\/\/cern\.ch/);
  });

  it("appends trailing slash to URL before fetching", async () => {
    mockPropfind(PROPFIND_207);
    await listRemoteResources("https://cern.ch/remote.php/dav/spaces/abc-123", "user", "pass");
    const url = (fetch as ReturnType<typeof vi.fn>).mock.calls[0][0];
    expect(url).toMatch(/\/$/);
  });

  it("throws when server returns non-207 error", async () => {
    mockPropfind("Forbidden", 403);
    await expect(
      listRemoteResources("https://cern.ch/remote.php/dav/spaces/abc-123/", "user", "pass")
    ).rejects.toThrow("403");
  });

  it("uses PROPFIND method and Depth:1 header", async () => {
    mockPropfind(PROPFIND_207);
    await listRemoteResources("https://cern.ch/remote.php/dav/spaces/abc-123/", "user", "pass");
    const [, init] = (fetch as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(init.method).toBe("PROPFIND");
    expect(init.headers.Depth).toBe("1");
  });
});
