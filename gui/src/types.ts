export interface FolderSettings {
  /** Include dotfiles and system items whose names begin with a dot. */
  sync_hidden_files: boolean;
  /** Trigger an immediate sync whenever a local filesystem change is detected. */
  auto_sync_on_change: boolean;
  /** Suspend automatic and manual syncing for this folder. */
  paused?: boolean;
}

export interface Folder {
  Name: string;
  LocalRoot: string;
  RemoteBase: string;
  /** Sub-folder names relative to RemoteBase. Empty means sync entire space. */
  Folders: string[];
  /** Per-folder sync settings. */
  Settings: FolderSettings;
}

export interface Account {
  username: string;
  password: string;
}

export interface FileCounts {
  files: number;
  dirs: number;
  size: number; // total size in bytes of all local files
}

export interface SyncStatus {
  syncing: string[];
  last_sync: Record<string, string>; // folder name → RFC 3339 timestamp
  counts: Record<string, FileCounts>; // folder name → local file/dir counts after last sync
  global_paused?: boolean;
  paused_folders?: string[];
}

export interface Space {
  id: string;
  name: string;
  drive_type: string; // "personal" | "project" | "share" | ...
  webdav_url: string;
  description: string;
}

export type NavPage = "dashboard" | "folders" | "settings" | "folderDetail" | "conflicts";

export interface ConflictEntry {
  folder: string;
  path: string;
  conflict_path: string;
  created_at: string;
}

export interface RemoteResource {
  href: string;           // full WebDAV href
  name: string;           // display name
  isCollection: boolean;
  size: number;           // bytes (0 for collections)
  lastModified: string;   // RFC 1123 date string, empty for collections
}

// ── Import from the ownCloud / CERNBox desktop client (mirror Go package migrate) ──

export interface LegacyPlan {
  ready: boolean;
  /** Why the folder cannot be imported. */
  reason?: string;
  remote_base?: string;
  /** Where the folder is on the server, e.g. "einstein / Projects/x". */
  location?: string;
  folders?: string[];
  /** Files next to excluded folders, which will no longer be synced. */
  unsynced_files?: string[];
}

export interface LegacyFolder {
  id: string;
  display_name: string;
  local_path: string;
  dav_url: string;
  /** Plain (not URL-encoded) remote path, relative to dav_url. */
  target_path: string;
  paused: boolean;
  ignore_hidden_files: boolean;
  virtual_files: boolean;
  /** Plain sub-folder paths excluded from sync. */
  excluded: string[];
  baseline_entries: number;
  /** The journal is locked by the running desktop client. */
  in_use: boolean;
  local_is_empty: boolean;
  /** Present when detection was asked to plan the import. */
  plan?: LegacyPlan;
}

export interface LegacyAccount {
  id: string;
  server_url: string;
  username: string;
  display_name: string;
  folders: LegacyFolder[];
}

export interface LegacyClient {
  app_name: string;
  config_path: string;
  running: boolean;
  upload_limit_kbps?: number;
  download_limit_kbps?: number;
  accounts: LegacyAccount[];
}

export interface LegacyFolderRef {
  config_path: string;
  account_id: string;
  folder_id: string;
}

export interface LegacyImportReport {
  results: Array<LegacyFolderRef & { name?: string; error?: string }>;
  limits_error?: string;
}
