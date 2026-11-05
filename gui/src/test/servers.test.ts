import { describe, it, expect } from "vitest";
import { KNOWN_SERVERS, foldersOnOtherServers, normalizeServerUrl, serverName } from "../servers";
import type { Folder } from "../types";

function folder(Name: string, RemoteBase: string): Folder {
  return { Name, RemoteBase } as Folder;
}

describe("normalizeServerUrl", () => {
  it("accepts http and https URLs and drops the trailing slash", () => {
    expect(normalizeServerUrl(" https://qa.cernbox.cern.ch/ ")).toBe("https://qa.cernbox.cern.ch");
    expect(normalizeServerUrl("http://localhost:8080")).toBe("http://localhost:8080");
    expect(normalizeServerUrl("https://example.org/cernbox/")).toBe("https://example.org/cernbox");
  });

  it("rejects anything else", () => {
    expect(normalizeServerUrl("")).toBeNull();
    expect(normalizeServerUrl("qa.cernbox.cern.ch")).toBeNull();
    expect(normalizeServerUrl("ftp://qa.cernbox.cern.ch")).toBeNull();
  });

  it("drops credentials embedded in the URL", () => {
    expect(normalizeServerUrl("https://user:secret@example.org")).toBe("https://example.org");
  });
});

describe("serverName", () => {
  it("uses the label of known servers and the host of the others", () => {
    expect(serverName("https://qa.cernbox.cern.ch")).toBe("CERNBox QA");
    expect(serverName("https://example.org:8443/cernbox")).toBe("example.org:8443");
  });
});

describe("KNOWN_SERVERS", () => {
  it("lists production and QA only, even when the build defaults to another server", () => {
    expect(KNOWN_SERVERS.map((o) => o.url)).toEqual(["https://cernbox.cern.ch", "https://qa.cernbox.cern.ch"]);
  });
});

describe("foldersOnOtherServers", () => {
  it("returns the folders whose remote URL is on another host", () => {
    const prod = folder("prod", "https://cernbox.cern.ch/remote.php/dav/spaces/1");
    const qa = folder("qa", "https://qa.cernbox.cern.ch/remote.php/dav/spaces/2");
    expect(foldersOnOtherServers([prod, qa], "https://qa.cernbox.cern.ch")).toEqual([prod]);
  });

  it("ignores folders without a valid URL", () => {
    expect(foldersOnOtherServers([folder("odd", "not a url")], "https://cernbox.cern.ch")).toEqual([]);
  });
});
