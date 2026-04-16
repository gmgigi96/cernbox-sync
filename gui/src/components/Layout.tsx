import React from "react";
import { Sidebar } from "./Sidebar";
import type { NavPage } from "../types";

interface LayoutProps {
  page: NavPage;
  onNavigate: (p: NavPage) => void;
  onAddFolder: () => void;
  children: React.ReactNode;
}

export function Layout({ page, onNavigate, onAddFolder, children }: LayoutProps) {
  return (
    <div style={styles.root}>
      <Sidebar
        page={page}
        onNavigate={onNavigate}
        onAddFolder={onAddFolder}
      />
      <main style={styles.main}>{children}</main>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
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
