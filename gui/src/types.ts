export interface Folder {
  Name: string;
  LocalRoot: string;
  RemoteBase: string;
  /** Sub-folder names relative to RemoteBase. Empty means sync entire space. */
  Folders: string[];
}

export interface Account {
  username: string;
  password: string;
}

export interface FileCounts {
  files: number;
  dirs: number;
}

export interface SyncStatus {
  syncing: string[];
  last_sync: Record<string, string>; // folder name → RFC 3339 timestamp
  counts: Record<string, FileCounts>; // folder name → local file/dir counts after last sync
}

export interface Space {
  id: string;
  name: string;
  drive_type: string; // "personal" | "project" | "share" | ...
  webdav_url: string;
  description: string;
}

export type NavPage = "dashboard" | "folders" | "settings" | "folderDetail";

export interface RemoteResource {
  href: string;           // full WebDAV href
  name: string;           // display name
  isCollection: boolean;
  size: number;           // bytes (0 for collections)
  lastModified: string;   // RFC 1123 date string, empty for collections
}
