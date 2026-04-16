import { useState, useEffect } from "react";
import { Layout } from "./components/Layout";
import { Dashboard } from "./pages/Dashboard";
import { Folders } from "./pages/Folders";
import { Settings } from "./pages/Settings";
import { AccountSetup } from "./pages/AccountSetup";
import { SpacePicker } from "./pages/SpacePicker";
import { FolderPicker } from "./pages/FolderPicker";
import { LocalFolderPicker } from "./pages/LocalFolderPicker";
import { useDaemon } from "./hooks/useDaemon";
import { ipc } from "./ipc";
import type { Account, NavPage, Space } from "./types";

const SERVER_URL = import.meta.env.VITE_SERVER_URL;

type FlowStep =
  | { step: "none" }
  | { step: "spacePicker" }
  | { step: "folderPicker"; space: Space }
  | { step: "localPath"; space: Space; remoteUrls: string[] };

export function App() {
  const daemon = useDaemon();
  const [page, setPage] = useState<NavPage>("dashboard");
  const [flow, setFlow] = useState<FlowStep>({ step: "none" });
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

  // Space picker replaces the main content area.
  if (flow.step === "spacePicker") {
    return (
      <Layout page={page} onNavigate={setPage} onAddFolder={openAddFolder}>
        <SpacePicker
          serverUrl={SERVER_URL}
          username={account.username}
          password={account.password}
          existingFolders={daemon.folders}
          onBack={() => setFlow({ step: "none" })}
          onSelectSpace={(space) => setFlow({ step: "folderPicker", space })}
        />
      </Layout>
    );
  }

  // Folder picker replaces the main content area.
  if (flow.step === "folderPicker") {
    const { space } = flow;
    return (
      <Layout page={page} onNavigate={setPage} onAddFolder={openAddFolder}>
        <FolderPicker
          space={space}
          username={account.username}
          password={account.password}
          onBack={() => setFlow({ step: "spacePicker" })}
          onConfirm={(remoteUrls) => setFlow({ step: "localPath", space, remoteUrls })}
        />
      </Layout>
    );
  }

  // Local folder picker replaces the main content area.
  if (flow.step === "localPath") {
    const { space, remoteUrls } = flow;
    return (
      <Layout page={page} onNavigate={setPage} onAddFolder={openAddFolder}>
        <LocalFolderPicker
          space={space}
          remoteUrls={remoteUrls}
          daemon={daemon}
          onBack={() => setFlow({ step: "folderPicker", space })}
          onDone={() => setFlow({ step: "none" })}
        />
      </Layout>
    );
  }

  return (
    <Layout page={page} onNavigate={setPage} onAddFolder={openAddFolder}>
      {page === "dashboard" && (
        <Dashboard daemon={daemon} onNavigate={(p) => setPage(p)} />
      )}
      {page === "folders" && (
        <Folders daemon={daemon} onAddFolder={openAddFolder} />
      )}
      {page === "settings" && <Settings />}
    </Layout>
  );
}
