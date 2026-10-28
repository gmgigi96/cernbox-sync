import React, { useState } from "react";
import { AlertCircle } from "lucide-react";
import { AccountSetup } from "../pages/AccountSetup";
import { useSyncStore } from "../store/syncStore";
import { Sidebar } from "./Sidebar";
import type { NavPage } from "../types";

interface LayoutProps {
  page: NavPage;
  onNavigate: (p: NavPage) => void;
  onAddFolder: () => void;
  children: React.ReactNode;
}

export function Layout({ page, onNavigate, onAddFolder, children }: LayoutProps) {
  const authRequired = useSyncStore((s) => s.authRequired);
  const clearAuthRequired = useSyncStore((s) => s.clearAuthRequired);
  const [loggingIn, setLoggingIn] = useState(false);

  if (authRequired && loggingIn) {
    return (
      <AccountSetup
        onDone={() => {
          setLoggingIn(false);
          clearAuthRequired();
        }}
      />
    );
  }

  return (
    <div style={styles.root}>
      <Sidebar
        page={page}
        onNavigate={onNavigate}
        onAddFolder={onAddFolder}
      />
      <main style={styles.main}>
        {authRequired && (
          <div role="alert" style={styles.banner}>
            <AlertCircle size={16} />
            <span style={{ flex: 1 }}>Authentication failed — log in again</span>
            <button style={styles.bannerBtn} onClick={() => setLoggingIn(true)}>
              Log in
            </button>
          </div>
        )}
        {children}
      </main>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  banner: {
    display: "flex",
    alignItems: "center",
    gap: 10,
    padding: "10px 16px",
    background: "rgba(255,180,171,0.12)",
    color: "var(--error, #ffb4ab)",
    fontSize: 13,
    flexShrink: 0,
  },
  bannerBtn: {
    padding: "4px 12px",
    borderRadius: 6,
    border: "1px solid currentColor",
    background: "transparent",
    color: "inherit",
    cursor: "pointer",
  },
  root: {
    display: "flex",
    height: "100%",
    background: "var(--surface)",
    overflow: "hidden",
  },
  main: {
    flex: 1,
    overflow: "hidden auto",
    display: "flex",
    flexDirection: "column",
  },
};
