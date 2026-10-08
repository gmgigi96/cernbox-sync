import { useState, useEffect } from "react";
import { listen } from "@tauri-apps/api/event";
import { invoke } from "@tauri-apps/api/core";
import { useSyncStore, type DaemonSnapshot, type DaemonEvent } from "./store/syncStore";
import { Layout } from "./components/Layout";
import { Dashboard } from "./pages/Dashboard";
import { Conflicts } from "./pages/Conflicts";
import { Folders } from "./pages/Folders";
import { FolderDetail } from "./pages/FolderDetail";
import { Settings } from "./pages/Settings";
import { AccountSetup } from "./pages/AccountSetup";
import { SpacePicker } from "./pages/SpacePicker";
import { FolderPicker } from "./pages/FolderPicker";
import { LocalFolderPicker } from "./pages/LocalFolderPicker";
import { LegacyImport } from "./pages/LegacyImport";
import { useDaemon } from "./hooks/useDaemon";
import { ipc } from "./ipc";
import type { Account, Folder, LegacyClient, NavPage, Space } from "./types";

const SERVER_URL = import.meta.env.VITE_SERVER_URL;

// Set once the user imported or skipped the folders of an ownCloud / CERNBox
// desktop client, so the offer is made only on first start.
const LEGACY_IMPORT_DONE_KEY = "legacyImportDone";

function legacyImportDone(): boolean {
  try {
    return localStorage.getItem(LEGACY_IMPORT_DONE_KEY) === "1";
  } catch {
    return false;
  }
}

type FlowStep =
  | { step: "none" }
  | { step: "spacePicker" }
  | { step: "folderPicker"; space: Space; existingFolder?: Folder }
  | { step: "localPath"; space: Space; remoteUrls: string[]; existingFolder?: Folder };

export function App() {
  const daemon = useDaemon();
  const [page, setPage] = useState<NavPage>("dashboard");

  // ── Subscribe to daemon push events ─────────────────────────────────────────
  useEffect(() => {
    const store = useSyncStore.getState();
    const unlisteners: Array<() => void> = [];

    const setup = async () => {
      // Register incremental-event listener BEFORE fetching the snapshot so
      // no event is missed between the two steps.
      unlisteners.push(
        await listen<DaemonEvent>("daemon-event", (e) => {
          store._handleEvent(e.payload);
        }),
      );

      // Daemon went offline — background subscriber will retry automatically.
      unlisteners.push(
        await listen<DaemonEvent>("daemon-offline", () => {
          store._setDaemonOffline("Daemon offline — reconnecting…");
        }),
      );

      // Re-snapshot on reconnect (background subscriber still emits this).
      unlisteners.push(
        await listen<DaemonSnapshot>("daemon-snapshot", (e) => {
          const { folders, status } = e.payload;
          store._applySnapshot(folders, status ?? { syncing: [], last_sync: {}, counts: {} });
        }),
      );

      // Pull the initial state via a direct command call — avoids the race
      // where the background subscriber emits the snapshot before the JS
      // listener above is registered.
      try {
        const snap = await invoke<DaemonSnapshot>("ipc_get_snapshot");
        store._applySnapshot(snap.folders, snap.status ?? { syncing: [], last_sync: {}, counts: {} });
      } catch {
        store._setDaemonOffline("Daemon offline — reconnecting…");
      }
    };

    setup();
    return () => { unlisteners.forEach((u) => u()); };
  }, []);
  const [flow, setFlow] = useState<FlowStep>({ step: "none" });
  const [selectedFolder, setSelectedFolder] = useState<Folder | null>(null);
  // null = show all conflicts; string = show conflicts for that folder name
  const [conflictFolder, setConflictFolder] = useState<string | null>(null);
  // null = still checking, false = no account, Account = loaded
  const [account, setAccount] = useState<Account | null | false>(null);

  useEffect(() => {
    ipc.getAccount()
      .then((acc) => setAccount(acc?.username ? acc : false))
      .catch(() => setAccount(false));
  }, []);

  // Folders synced by a desktop client this app can take over, as found by
  // the daemon. null = not detected yet.
  const [legacyClients, setLegacyClients] = useState<LegacyClient[] | null>(null);
  const [legacyImportOpen, setLegacyImportOpen] = useState(false);

  useEffect(() => {
    if (legacyClients !== null || !daemon.daemonOnline) return;
    if (legacyImportDone()) {
      setLegacyClients([]);
      return;
    }
    ipc.legacyDetect(SERVER_URL, false)
      .then(setLegacyClients)
      .catch(() => setLegacyClients([]));
  }, [legacyClients, daemon.daemonOnline]);

  // Offered on first start: signed in, nothing synced yet. Stays open while
  // importing adds folders, until the user leaves it.
  useEffect(() => {
    if (legacyClients?.length && account && !daemon.loading && daemon.daemonOnline && daemon.folders.length === 0) {
      setLegacyImportOpen(true);
    }
  }, [legacyClients, account, daemon.loading, daemon.daemonOnline, daemon.folders.length]);

  if (account === null) return null;

  if (account === false) {
    // Wait for the detection to suggest the username of the desktop client.
    if (daemon.loading || (daemon.daemonOnline && legacyClients === null)) return null;
    const legacyAccount = (legacyClients ?? []).flatMap((c) => c.accounts.map((a) => ({ client: c, account: a })))[0];
    return (
      <AccountSetup
        suggestedUsername={legacyAccount?.account.username}
        notice={
          legacyAccount &&
          `The ${legacyAccount.client.app_name} desktop client on this computer syncs ${
            legacyAccount.account.folders.length === 1 ? "1 folder" : `${legacyAccount.account.folders.length} folders`
          }. Sign in to import ${legacyAccount.account.folders.length === 1 ? "it" : "them"} next.`
        }
        onDone={() =>
          ipc.getAccount().then((acc) => setAccount(acc ?? false))
        }
      />
    );
  }

  if (legacyImportOpen && legacyClients?.length) {
    return (
      <LegacyImport
        clients={legacyClients}
        serverUrl={SERVER_URL}
        daemon={daemon}
        onDone={() => {
          try {
            localStorage.setItem(LEGACY_IMPORT_DONE_KEY, "1");
          } catch {
            // Without storage the offer is simply made again next time.
          }
          setLegacyClients([]);
          setLegacyImportOpen(false);
        }}
      />
    );
  }

  function openAddFolder() {
    setFlow({ step: "spacePicker" });
  }

  function cancelFlow(p: NavPage) {
    setFlow({ step: "none" });
    setPage(p);
  }

  // Space picker replaces the main content area.
  if (flow.step === "spacePicker") {
    return (
      <Layout page={page} onNavigate={cancelFlow} onAddFolder={openAddFolder}>
        <SpacePicker
          serverUrl={SERVER_URL}
          username={account.username}
          password={account.password}
          existingFolders={daemon.folders}
          onBack={() => setFlow({ step: "none" })}
          onSelectSpace={(space) => setFlow({ step: "folderPicker", space })}
          onEditSpace={(space, existingFolder) => setFlow({ step: "folderPicker", space, existingFolder })}
        />
      </Layout>
    );
  }

  // Folder picker replaces the main content area.
  if (flow.step === "folderPicker") {
    const { space, existingFolder } = flow;
    return (
      <Layout page={page} onNavigate={cancelFlow} onAddFolder={openAddFolder}>
        <FolderPicker
          space={space}
          username={account.username}
          password={account.password}
          initialUrls={existingFolder?.Folders}
          onBack={() => setFlow({ step: "spacePicker" })}
          onConfirm={(remoteUrls) => setFlow({ step: "localPath", space, remoteUrls, existingFolder })}
        />
      </Layout>
    );
  }

  // Local folder picker replaces the main content area.
  if (flow.step === "localPath") {
    const { space, remoteUrls, existingFolder } = flow;
    return (
      <Layout page={page} onNavigate={cancelFlow} onAddFolder={openAddFolder}>
        <LocalFolderPicker
          space={space}
          remoteUrls={remoteUrls}
          existingFolder={existingFolder}
          daemon={daemon}
          onBack={() => setFlow({ step: "folderPicker", space, existingFolder })}
          onDone={() => setFlow({ step: "none" })}
        />
      </Layout>
    );
  }

  function navigate(p: NavPage) {
    if (p !== "folderDetail") setSelectedFolder(null);
    if (p !== "conflicts") setConflictFolder(null);
    setPage(p);
  }

  function openConflicts(folderName?: string) {
    setConflictFolder(folderName ?? null);
    setPage("conflicts");
  }

  // Map folderDetail/conflicts → folders so sidebar highlights correctly
  const sidebarPage: NavPage = (page === "folderDetail" || page === "conflicts") ? "folders" : page;

  return (
    <Layout page={sidebarPage} onNavigate={navigate} onAddFolder={openAddFolder}>
      {page === "dashboard" && (
        <Dashboard
          daemon={daemon}
          onNavigate={(p) => navigate(p)}
          onOpenFolder={(folder) => { setSelectedFolder(folder); setPage("folderDetail"); }}
          onOpenConflicts={() => openConflicts()}
        />
      )}
      {page === "folders" && (
        <Folders
          daemon={daemon}
          onAddFolder={openAddFolder}
          onOpenFolder={(folder) => { setSelectedFolder(folder); setPage("folderDetail"); }}
        />
      )}
      {page === "folderDetail" && selectedFolder && (
        <FolderDetail
          folder={selectedFolder}
          daemon={daemon}
          account={account}
          onBack={() => { setSelectedFolder(null); setPage("folders"); }}
          onFolderUpdated={(f) => setSelectedFolder(f)}
          onRemove={() => { daemon.removeFolder(selectedFolder.Name); setSelectedFolder(null); setPage("folders"); }}
          onOpenConflicts={() => openConflicts(selectedFolder.Name)}
        />
      )}
      {page === "conflicts" && (
        <Conflicts
          folderName={conflictFolder ?? undefined}
          onBack={() => {
            if (conflictFolder && selectedFolder) {
              setPage("folderDetail");
            } else {
              setConflictFolder(null);
              setPage("dashboard");
            }
          }}
        />
      )}
      {page === "settings" && <Settings />}
    </Layout>
  );
}
