import type { Space } from "./types";

// GraphSpace is the raw shape returned by the oCIS Graph API
// GET /graph/v1.0/drives
// https://owncloud.dev/apis/http/graph/spaces/
interface GraphDrive {
  id: string;
  name: string;
  driveType: string; // "personal" | "project" | "share" | ...
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
    webdav_url: d.root?.webDavUrl ?? "",
    description: d.description ?? "",
  }));
}
