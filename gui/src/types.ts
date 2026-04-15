export interface Folder {
  Name: string;
  LocalRoot: string;
  RemoteBase: string;
}

export interface Account {
  username: string;
  password: string;
}

export interface SyncStatus {
  syncing: string[];
  last_sync: Record<string, string>; // folder name → RFC 3339 timestamp
}

export type NavPage = "dashboard" | "folders" | "settings";
