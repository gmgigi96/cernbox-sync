import { useState, useEffect } from "react";
import { Layout } from "./components/Layout";
import { AddFolderModal } from "./components/AddFolderModal";
import { Dashboard } from "./pages/Dashboard";
import { Folders } from "./pages/Folders";
import { Settings } from "./pages/Settings";
import { AccountSetup } from "./pages/AccountSetup";
import { useDaemon } from "./hooks/useDaemon";
import { ipc } from "./ipc";
import type { NavPage } from "./types";

export function App() {
  const daemon = useDaemon();
  const [page, setPage] = useState<NavPage>("dashboard");
  const [showAdd, setShowAdd] = useState(false);
  // null = still checking, false = no account, true = account exists
  const [hasAccount, setHasAccount] = useState<boolean | null>(null);

  useEffect(() => {
    ipc.getAccount()
      .then((acc) => setHasAccount(!!acc?.username))
      .catch(() => setHasAccount(true)); // if daemon unreachable, let the main UI handle it
  }, []);

  if (hasAccount === null) {
    // Still loading — render nothing to avoid flash
    return null;
  }

  if (!hasAccount) {
    return <AccountSetup onDone={() => setHasAccount(true)} />;
  }

  function navigate(p: NavPage) {
    setPage(p);
  }

  return (
    <>
      <Layout
        page={page}
        onNavigate={navigate}
        onAddFolder={() => setShowAdd(true)}
      >
        {page === "dashboard" && (
          <Dashboard daemon={daemon} onNavigate={(p) => setPage(p)} />
        )}
        {page === "folders" && (
          <Folders daemon={daemon} onAddFolder={() => setShowAdd(true)} />
        )}
        {page === "settings" && <Settings />}
      </Layout>

      {showAdd && (
        <AddFolderModal daemon={daemon} onClose={() => setShowAdd(false)} />
      )}
    </>
  );
}
