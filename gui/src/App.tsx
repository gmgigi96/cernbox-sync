import { useState, useEffect } from "react";
import { Layout } from "./components/Layout";
import { LocalPathModal } from "./components/LocalPathModal";
import { Dashboard } from "./pages/Dashboard";
import { Folders } from "./pages/Folders";
import { Settings } from "./pages/Settings";
import { AccountSetup } from "./pages/AccountSetup";
import { SpacePicker } from "./pages/SpacePicker";
import { useDaemon } from "./hooks/useDaemon";
import { ipc } from "./ipc";
import type { Account, NavPage, Space } from "./types";

const SERVER_URL = import.meta.env.VITE_SERVER_URL;

export function App() {
  const daemon = useDaemon();
  const [page, setPage] = useState<NavPage>("dashboard");
  const [showSpacePicker, setShowSpacePicker] = useState(false);
  const [selectedSpace, setSelectedSpace] = useState<Space | null>(null);
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
    setShowSpacePicker(true);
    setSelectedSpace(null);
  }

  function handleSpaceSelected(space: Space) {
    setShowSpacePicker(false);
    setSelectedSpace(space);
  }

  // Space picker replaces the main content area.
  if (showSpacePicker) {
    return (
      <Layout page={page} onNavigate={setPage} onAddFolder={openAddFolder}>
        <SpacePicker
          serverUrl={SERVER_URL}
          username={account.username}
          password={account.password}
          onBack={() => setShowSpacePicker(false)}
          onSelectSpace={handleSpaceSelected}
        />
      </Layout>
    );
  }

  return (
    <>
      <Layout page={page} onNavigate={setPage} onAddFolder={openAddFolder}>
        {page === "dashboard" && (
          <Dashboard daemon={daemon} onNavigate={(p) => setPage(p)} />
        )}
        {page === "folders" && (
          <Folders daemon={daemon} onAddFolder={openAddFolder} />
        )}
        {page === "settings" && <Settings />}
      </Layout>

      {selectedSpace !== null && (
        <LocalPathModal
          space={selectedSpace}
          daemon={daemon}
          onClose={() => setSelectedSpace(null)}
        />
      )}
    </>
  );
}
