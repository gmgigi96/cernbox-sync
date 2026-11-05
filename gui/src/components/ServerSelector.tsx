import { useState } from "react";
import { AlertTriangle } from "lucide-react";
import { KNOWN_SERVERS, foldersOnOtherServers, normalizeServerUrl, serverName } from "../servers";
import type { Folder } from "../types";

const CUSTOM = "custom";

interface ServerSelectorProps {
  serverUrl: string;
  /** Registered folders, to warn about the ones on another server. */
  folders: Folder[];
  confirmLabel: string;
  onSelect: (url: string) => void;
}

/** Shows the current server, and lets the user pick another one on request. */
export function ServerSelector({ serverUrl, folders, confirmLabel, onSelect }: ServerSelectorProps) {
  const [editing, setEditing] = useState(false);
  const [choice, setChoice] = useState(serverUrl);
  const [custom, setCustom] = useState("");

  function open() {
    const known = KNOWN_SERVERS.some((o) => o.url === serverUrl);
    setChoice(known ? serverUrl : CUSTOM);
    setCustom(known ? "" : serverUrl);
    setEditing(true);
  }

  if (!editing) {
    return (
      <div style={styles.summary}>
        <span>
          Server: <strong style={{ color: "var(--on-surface)" }} title={serverUrl}>{serverName(serverUrl)}</strong>
        </span>
        <button type="button" className="btn-ghost" onClick={open}>Change server</button>
      </div>
    );
  }

  const selected = choice === CUSTOM ? normalizeServerUrl(custom) : choice;
  const affected = selected ? foldersOnOtherServers(folders, selected) : [];

  return (
    <div style={styles.form}>
      <select aria-label="Server" style={styles.field} value={choice} onChange={(e) => setChoice(e.target.value)}>
        {KNOWN_SERVERS.map((o) => <option key={o.url} value={o.url}>{o.label}</option>)}
        <option value={CUSTOM}>Custom server…</option>
      </select>

      {choice === CUSTOM && (
        <input
          aria-label="Server URL"
          style={styles.field}
          placeholder="https://cernbox.example.org"
          value={custom}
          onChange={(e) => setCustom(e.target.value)}
        />
      )}
      {choice === CUSTOM && custom.trim() !== "" && !selected && (
        <p style={styles.invalid}>Enter a full URL starting with https:// or http://.</p>
      )}

      {affected.length > 0 && (
        <div role="alert" style={styles.warning}>
          <AlertTriangle size={14} strokeWidth={1.5} style={{ flexShrink: 0, marginTop: 2 }} />
          <span>
            All folders use the account you sign in with, so these may stop syncing:{" "}
            {affected.map((f) => f.Name).join(", ")}.
          </span>
        </div>
      )}

      <div style={styles.actions}>
        <button type="button" className="btn-ghost" onClick={() => setEditing(false)}>Cancel</button>
        <button
          type="button"
          className="btn-secondary"
          disabled={!selected || selected === serverUrl}
          onClick={() => {
            if (!selected) return;
            setEditing(false);
            onSelect(selected);
          }}
        >
          {confirmLabel}
        </button>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  summary: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: "0.5rem",
    width: "100%",
    fontSize: "0.8125rem",
    color: "var(--on-surface-variant)",
  },
  form: {
    display: "flex",
    flexDirection: "column",
    gap: "0.5rem",
    width: "100%",
  },
  field: {
    width: "100%",
  },
  invalid: {
    fontSize: "0.75rem",
    color: "var(--error)",
  },
  warning: {
    display: "flex",
    gap: "0.5rem",
    background: "rgba(255,107,107,0.1)",
    borderRadius: "var(--radius-md)",
    padding: "0.5rem 0.75rem",
    fontSize: "0.75rem",
    lineHeight: 1.5,
    color: "var(--error)",
  },
  actions: {
    display: "flex",
    justifyContent: "flex-end",
    gap: "0.5rem",
  },
};
