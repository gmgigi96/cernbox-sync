import type { Folder } from "./types";

export interface ServerOption {
  label: string;
  url: string;
}

/** Server used until the user picks another one. */
export const DEFAULT_SERVER_URL = import.meta.env.VITE_SERVER_URL;

/** The servers offered by the selector. */
export const KNOWN_SERVERS: ServerOption[] = [
  { label: "CERNBox", url: "https://cernbox.cern.ch" },
  { label: "CERNBox QA", url: "https://qa.cernbox.cern.ch" },
];

/** Returns raw as a server URL without trailing slash, or null if it is not one. */
export function normalizeServerUrl(raw: string): string | null {
  let url: URL;
  try {
    url = new URL(raw.trim());
  } catch {
    return null;
  }
  if ((url.protocol !== "http:" && url.protocol !== "https:") || !url.hostname) return null;
  return (url.origin + url.pathname).replace(/\/+$/, "");
}

function hostOf(url: string): string | null {
  try {
    return new URL(url).host;
  } catch {
    return null;
  }
}

/** The name shown for a server: its label if known, otherwise its host. */
export function serverName(url: string): string {
  return KNOWN_SERVERS.find((s) => s.url === url)?.label ?? hostOf(url) ?? url;
}

/**
 * The folders syncing with another server than serverUrl. The daemon has one
 * account for all folders, so signing in to serverUrl affects them too.
 */
export function foldersOnOtherServers(folders: Folder[], serverUrl: string): Folder[] {
  const host = hostOf(serverUrl);
  return folders.filter((f) => {
    const folderHost = hostOf(f.RemoteBase);
    return folderHost !== null && folderHost !== host;
  });
}
