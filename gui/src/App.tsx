import { useState } from "react";
import { Layout } from "./components/Layout";
import { AddFolderModal } from "./components/AddFolderModal";
import { Dashboard } from "./pages/Dashboard";
import { Folders } from "./pages/Folders";
import { Settings } from "./pages/Settings";
import { useDaemon } from "./hooks/useDaemon";
import type { NavPage } from "./types";

export function App() {
  const daemon = useDaemon();
  const [page, setPage] = useState<NavPage>("dashboard");
  const [showAdd, setShowAdd] = useState(false);

  function navigate(p: NavPage) {
    setPage(p);
  }

  return (
    <>
      <Layout
        page={page}
        onNavigate={navigate}
        daemonOnline={daemon.daemonOnline}
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
