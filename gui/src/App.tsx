import { useState, useEffect } from "react";
import { Layout } from "./components/Layout";
import { Dashboard } from "./pages/Dashboard";
import { Folders } from "./pages/Folders";
import { FolderDetail } from "./pages/FolderDetail";
import { Settings } from "./pages/Settings";
import { AccountSetup } from "./pages/AccountSetup";
import { SpacePicker } from "./pages/SpacePicker";
import { FolderPicker } from "./pages/FolderPicker";
import { LocalFolderPicker } from "./pages/LocalFolderPicker";
import { useDaemon } from "./hooks/useDaemon";
import { ipc } from "./ipc";
import type { Account, Folder, NavPage, Space } from "./types";

const SERVER_URL = import.meta.env.VITE_SERVER_URL;

type FlowStep =
  | { step: "none" }
  | { step: "spacePicker" }
  | { step: "folderPicker"; space: Space; existingFolder?: Folder }
  | { step: "localPath"; space: Space; remoteUrls: string[]; existingFolder?: Folder };

export function App() {
  const daemon = useDaemon();
  const [page, setPage] = useState<NavPage>("dashboard");
  const [flow, setFlow] = useState<FlowStep>({ step: "none" });
  const [selectedFolder, setSelectedFolder] = useState<Folder | null>(null);
  // null = still checking, false = no account, Account = loaded
  const [account, setAccount] = useState<Account | null | false>(null);

  useEffect(() => {
    ipc.getAccount()
      .then((acc) => setAccount(acc?.username ? acc : false))
      .catch(() => setAccount(false));
  }, []);

  if (account === null) return null;

  if (account === false) {
    return (
      <AccountSetup
        onDone={() =>
          ipc.getAccount().then((acc) => setAccount(acc ?? false))
        }
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
    setPage(p);
  }

  // Map folderDetail → folders so sidebar highlights "Syncing Folders"
  const sidebarPage: NavPage = page === "folderDetail" ? "folders" : page;

  return (
    <Layout page={sidebarPage} onNavigate={navigate} onAddFolder={openAddFolder}>
      {page === "dashboard" && (
        <Dashboard daemon={daemon} onNavigate={(p) => navigate(p)} />
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
        />
      )}
      {page === "settings" && <Settings />}
    </Layout>
  );
}
