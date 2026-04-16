import type { Space, RemoteResource } from "./types";

// GraphSpace is the raw shape returned by the oCIS Graph API
// GET /graph/v1.0/drives
// https://owncloud.dev/apis/http/graph/spaces/
interface GraphDrive {
  id: string;
  name: string;
  driveType: string; // "personal" | "project" | "share" | ...
  driveAlias?: string;
  webUrl?: string;
  root?: {
    webDavUrl?: string;
  };
  description?: string;
  special?: Array<{ specialFolder: { name: string }; webDavUrl: string }>;
}

interface GraphDrivesResponse {
  value: GraphDrive[];
}

// buildWebDavUrl constructs the canonical WebDAV URL for a drive.
// When driveAlias is present, the server puts the alias at the end of the
// webDavUrl path instead of the space id. We strip the alias and append the
// id so that the URL is stable and works for PROPFIND.
// e.g. webDavUrl = "https://host/remote.php/dav/spaces/eos/user/g/gdelmont"
//      driveAlias = "eos/user/g/gdelmont"
//      id         = "eoshome-g$XXXX"
//   → "https://host/remote.php/dav/spaces/eoshome-g$XXXX"
function buildWebDavUrl(d: GraphDrive): string {
  const raw = d.root?.webDavUrl ?? "";
  if (!d.driveAlias || !raw) return raw;

  const alias = d.driveAlias.replace(/\/$/, "");
  const suffix = "/" + alias;
  const base = raw.endsWith(suffix) ? raw.slice(0, raw.length - suffix.length) : raw.replace(/\/$/, "");
  return base + "/" + d.id;
}

// listSpaces calls the oCIS Graph API with basic auth and returns the list of
// available spaces normalised into our Space type.
export async function listSpaces(
  serverUrl: string,
  username: string,
  password: string,
): Promise<Space[]> {
  const base = serverUrl.replace(/\/$/, "");
  const url = `${base}/graph/v1beta1/me/drives`;

  const resp = await fetch(url, {
    headers: {
      Authorization: "Basic " + btoa(`${username}:${password}`),
      Accept: "application/json",
    },
  });

  if (!resp.ok) {
    throw new Error(`Graph API error ${resp.status}: ${await resp.text()}`);
  }

  const data: GraphDrivesResponse = await resp.json();

  return (data.value ?? []).map((d) => ({
    id: d.id,
    name: d.name,
    drive_type: d.driveType,
    webdav_url: buildWebDavUrl(d),
    description: d.description ?? "",
  }));
}

// listRemoteResources issues a WebDAV PROPFIND Depth:1 against the given URL
// and returns the child resources (the collection itself is excluded).
// The request mirrors dev/scripts/webdav.sh cmd_list.
export async function listRemoteResources(
  url: string,
  username: string,
  password: string,
): Promise<RemoteResource[]> {
  // Ensure directory URL ends with / (matches the shell script behaviour)
  const dirUrl = url.endsWith("/") ? url : url + "/";

  const resp = await fetch(dirUrl, {
    method: "PROPFIND",
    headers: {
      Authorization: "Basic " + btoa(`${username}:${password}`),
      Depth: "1",
      "Content-Type": "application/xml",
    },
    body: `<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:displayname/><D:resourcetype/><D:getcontentlength/><D:getlastmodified/></D:prop></D:propfind>`,
  });

  if (!resp.ok && resp.status !== 207) {
    throw new Error(`PROPFIND ${resp.status}: ${await resp.text()}`);
  }

  const text = await resp.text();
  const parser = new DOMParser();
  const doc = parser.parseFromString(text, "application/xml");

  // The first <response> is always the requested collection itself; skip it.
  const responses = Array.from(doc.getElementsByTagNameNS("DAV:", "response")).slice(1);

  const parsed = new URL(dirUrl);
  const resources: RemoteResource[] = [];

  for (const r of responses) {
    const href = r.getElementsByTagNameNS("DAV:", "href")[0]?.textContent?.trim() ?? "";

    const displayname =
      r.getElementsByTagNameNS("DAV:", "displayname")[0]?.textContent?.trim() ||
      decodeURIComponent(href).replace(/\/$/, "").split("/").pop() ||
      "";
    const isCollection =
      r.getElementsByTagNameNS("DAV:", "collection").length > 0;
    const sizeText =
      r.getElementsByTagNameNS("DAV:", "getcontentlength")[0]?.textContent?.trim() ?? "0";
    const lastModified =
      r.getElementsByTagNameNS("DAV:", "getlastmodified")[0]?.textContent?.trim() ?? "";

    // Reconstruct full URL from the href path returned by the server
    const fullUrl = `${parsed.protocol}//${parsed.host}${href}`;

    resources.push({
      href: fullUrl,
      name: displayname,
      isCollection,
      size: parseInt(sizeText, 10) || 0,
      lastModified,
    });
  }

  return resources;
}
