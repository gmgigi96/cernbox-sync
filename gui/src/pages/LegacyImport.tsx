import { useCallback, useEffect, useState } from "react";
import { AlertCircle, AlertTriangle, ArrowRight, CheckCircle2, Cloud, FolderSync, HardDrive, RefreshCw } from "lucide-react";
import { ipc } from "../ipc";
import { CheckboxIcon } from "../components/FolderTree";
import type { DaemonState } from "../hooks/useDaemon";
import type { LegacyClient, LegacyFolder, LegacyFolderRef, LegacyImportReport } from "../types";

interface LegacyImportProps {
  /** Desktop clients found at startup, listed while their import is checked. */
  clients: LegacyClient[];
  serverUrl: string;
  daemon: DaemonState;
  /** Called once the user imported folders or chose to skip. */
  onDone: () => void;
}

interface Row {
  key: string;
  ref: LegacyFolderRef;
  folder: LegacyFolder;
}

const keyOf = (r: LegacyFolderRef) => `${r.config_path}|${r.account_id}|${r.folder_id}`;

function rowsOf(clients: LegacyClient[]): Row[] {
  return clients.flatMap((client) =>
    client.accounts.flatMap((account) =>
      account.folders.map((folder) => {
        const ref = { config_path: client.config_path, account_id: account.id, folder_id: folder.id };
        return { key: keyOf(ref), ref, folder };
      }),
    ),
  );
}

function Checkbox({ checked, disabled, label, onChange }: { checked: boolean; disabled?: boolean; label: string; onChange: () => void }) {
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      style={{ ...s.checkbox, cursor: disabled ? "default" : "pointer" }}
      onClick={(e) => {
        e.stopPropagation();
        onChange();
      }}
    >
      <CheckboxIcon state={checked ? "all" : "none"} />
    </button>
  );
}

/**
 * Offers to take over the sync folders of the ownCloud / CERNBox desktop
 * client. The daemon decides whether and how each folder can be imported
 * and performs the import; this page shows its answers and the user's choice.
 */
export function LegacyImport({ clients: initialClients, serverUrl, daemon, onDone }: LegacyImportProps) {
  const [clients, setClients] = useState(initialClients);
  const [checking, setChecking] = useState(true);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [error, setError] = useState<string | null>(null);
  const [importing, setImporting] = useState(false);
  const [report, setReport] = useState<LegacyImportReport | null>(null);
  const [importLimits, setImportLimits] = useState(true);

  const rows = rowsOf(clients);
  const appName = clients[0]?.app_name || "desktop";
  const running = clients.some((c) => c.running);
  const limits = clients.find((c) => c.upload_limit_kbps || c.download_limit_kbps);
  const limitsText = [
    limits?.upload_limit_kbps && `upload ${limits.upload_limit_kbps} KB/s`,
    limits?.download_limit_kbps && `download ${limits.download_limit_kbps} KB/s`,
  ]
    .filter(Boolean)
    .join(", ");

  // Asks the daemon to detect the clients again and check each folder.
  const check = useCallback(async () => {
    setChecking(true);
    setError(null);
    try {
      const planned = await ipc.legacyDetect(serverUrl, true);
      setClients(planned);
      setSelected(new Set(rowsOf(planned).filter((r) => r.folder.plan?.ready).map((r) => r.key)));
    } catch (e) {
      setError(String(e));
    } finally {
      setChecking(false);
    }
  }, [serverUrl]);

  useEffect(() => {
    check();
  }, [check]);

  function toggle(key: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  async function handleImport() {
    setImporting(true);
    setError(null);
    try {
      const refs = rows.filter((r) => selected.has(r.key) && r.folder.plan?.ready).map((r) => r.ref);
      setReport(await ipc.legacyImport(serverUrl, refs, importLimits && !!limits));
    } catch (e) {
      setError(String(e));
    } finally {
      setImporting(false);
    }
  }

  // ── Done ────────────────────────────────────────────────────────────────────

  if (report) {
    const nameOf = (r: LegacyFolderRef) => rows.find((row) => row.key === keyOf(r))?.folder.display_name ?? r.folder_id;
    const imported = report.results.filter((r) => !r.error);
    const failed = report.results.filter((r) => r.error);
    return (
      <div style={s.root}>
        <div style={s.card}>
          <CheckCircle2 size={32} strokeWidth={1.5} style={{ color: "var(--success)", marginBottom: "1rem" }} />
          <h1 style={s.title}>
            {imported.length === 0 ? "No folders imported" : imported.length === 1 ? "1 folder imported" : `${imported.length} folders imported`}
          </h1>
          {imported.length > 0 && (
            <p style={s.subtitle}>
              CERNBox Sync now keeps {imported.length === 1 ? "it" : "them"} in sync, starting from where the {appName} desktop client left off.
            </p>
          )}
          {failed.map((r) => (
            <div key={keyOf(r)} style={s.errorBox}>
              <AlertCircle size={14} strokeWidth={1.5} style={{ flexShrink: 0 }} />
              <span><strong>{nameOf(r)}</strong>: {r.error}</span>
            </div>
          ))}
          {report.limits_error && (
            <div style={s.errorBox}>
              <AlertCircle size={14} strokeWidth={1.5} style={{ flexShrink: 0 }} />
              Bandwidth limits were not imported: {report.limits_error}
            </div>
          )}
          {imported.length > 0 && (
            <div style={s.warningBox}>
              <AlertTriangle size={14} strokeWidth={1.5} style={{ flexShrink: 0, marginTop: 2 }} />
              <span>
                Remove these folders from the {appName} desktop client, or uninstall it, so that it does not sync them again the next time
                it starts.
              </span>
            </div>
          )}
          <button className="btn-primary" style={s.wideBtn} onClick={onDone}>
            Continue <ArrowRight size={15} strokeWidth={1.5} />
          </button>
        </div>
      </div>
    );
  }

  // ── Review ──────────────────────────────────────────────────────────────────

  const readyCount = rows.filter((r) => selected.has(r.key) && r.folder.plan?.ready).length;
  const importDisabled = checking || importing || running || readyCount === 0 || !daemon.daemonOnline;

  return (
    <div style={s.root}>
      <div style={{ ...s.card, width: 640 }}>
        <FolderSync size={32} strokeWidth={1.5} style={{ color: "var(--primary)", marginBottom: "1rem" }} />
        <h1 style={s.title}>Import from the {appName} desktop client</h1>
        <p style={s.subtitle}>
          These folders are synchronized by the {appName} desktop client on this computer. Import them to keep syncing them here, without
          downloading or uploading your files again.
        </p>

        {running && (
          <div style={s.warningBox}>
            <AlertTriangle size={14} strokeWidth={1.5} style={{ flexShrink: 0, marginTop: 2 }} />
            <span style={{ flex: 1 }}>
              The {appName} desktop client is running. Quit it before importing: two sync clients working on the same folders can create
              conflicts.
            </span>
            <button className="btn-secondary" style={{ flexShrink: 0 }} onClick={check} disabled={checking}>
              <RefreshCw size={13} strokeWidth={1.5} /> Check again
            </button>
          </div>
        )}

        {error && (
          <div style={s.errorBox}>
            <AlertCircle size={14} strokeWidth={1.5} style={{ flexShrink: 0 }} />
            <span style={{ flex: 1 }}>{error}</span>
            <button className="btn-secondary" style={{ flexShrink: 0 }} onClick={check} disabled={checking}>Retry</button>
          </div>
        )}

        <div style={s.list} role="list">
          {rows.map(({ key, folder }) => {
            const plan = folder.plan;
            const ready = !!plan?.ready;
            return (
              <div
                key={key}
                role="listitem"
                style={{ ...s.row, opacity: plan && !ready ? 0.65 : 1, cursor: ready && !importing ? "pointer" : "default" }}
                onClick={() => ready && !importing && toggle(key)}
              >
                <Checkbox
                  checked={ready && selected.has(key)}
                  disabled={!ready || importing}
                  label={`Import ${folder.display_name}`}
                  onChange={() => toggle(key)}
                />
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={s.rowTitle}>
                    {folder.display_name}
                    {folder.paused && <span style={s.badge}>PAUSED</span>}
                    {folder.excluded.length > 0 && (
                      <span style={s.badge}>
                        {folder.excluded.length} EXCLUDED {folder.excluded.length === 1 ? "FOLDER" : "FOLDERS"}
                      </span>
                    )}
                  </div>
                  <div style={s.pathLine}>
                    <HardDrive size={12} strokeWidth={1.5} style={s.pathIcon} />
                    <span style={s.pathText}>{folder.local_path}</span>
                  </div>
                  <div style={s.pathLine}>
                    <Cloud size={12} strokeWidth={1.5} style={s.pathIcon} />
                    <span style={s.pathText}>{plan?.location || folder.target_path || "/"}</span>
                  </div>
                  {checking && <p style={s.note}>Checking…</p>}
                  {!checking && plan && !ready && <p style={{ ...s.note, color: "var(--tertiary)" }}>{plan.reason}</p>}
                  {!checking && ready && !!plan.unsynced_files?.length && (
                    <p style={s.note} title={plan.unsynced_files.join("\n")}>
                      {plan.unsynced_files.length === 1 ? "1 file" : `${plan.unsynced_files.length} files`} next to the excluded folders
                      will no longer be synced.
                    </p>
                  )}
                </div>
              </div>
            );
          })}
        </div>

        {limits && (
          <div style={s.limitsRow} onClick={() => !importing && setImportLimits((v) => !v)}>
            <Checkbox
              checked={importLimits}
              disabled={importing}
              label="Also apply its bandwidth limits"
              onChange={() => setImportLimits((v) => !v)}
            />
            Also apply its bandwidth limits ({limitsText})
          </div>
        )}

        {!daemon.daemonOnline && (
          <p style={{ ...s.note, alignSelf: "stretch" }}>The sync daemon is offline; start cernbox-syncd to import.</p>
        )}

        <div style={s.actions}>
          <button className="btn-secondary" onClick={onDone} disabled={importing}>
            Skip
          </button>
          <button className="btn-primary" onClick={handleImport} disabled={importDisabled}>
            {importing ? "Importing…" : readyCount === 1 ? "Import 1 folder" : `Import ${readyCount} folders`}
          </button>
        </div>
      </div>
    </div>
  );
}

const s: Record<string, React.CSSProperties> = {
  root: {
    width: "100vw",
    height: "100vh",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    background: "var(--background)",
  },
  card: {
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "2.5rem",
    width: 440,
    maxWidth: "calc(100vw - 3rem)",
    maxHeight: "calc(100vh - 3rem)",
    boxShadow: "var(--shadow-float)",
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    gap: "0.75rem",
  },
  title: {
    fontSize: "1.25rem",
    fontWeight: 600,
    color: "var(--on-surface)",
    textAlign: "center" as const,
  },
  subtitle: {
    fontSize: "0.8125rem",
    color: "var(--on-surface-variant)",
    textAlign: "center" as const,
    lineHeight: 1.6,
    marginBottom: "0.5rem",
  },
  list: {
    alignSelf: "stretch",
    display: "flex",
    flexDirection: "column",
    gap: "0.5rem",
    overflowY: "auto" as const,
    minHeight: 0,
  },
  row: {
    display: "flex",
    gap: "0.75rem",
    alignItems: "flex-start",
    background: "var(--surface-container-highest)",
    borderRadius: "var(--radius-lg)",
    padding: "0.75rem 1rem",
  },
  checkbox: {
    background: "transparent",
    border: "none",
    padding: 0,
    marginTop: 2,
    display: "flex",
    flexShrink: 0,
  },
  rowTitle: {
    display: "flex",
    alignItems: "center",
    flexWrap: "wrap" as const,
    gap: "0.5rem",
    fontSize: "0.875rem",
    fontWeight: 500,
    color: "var(--on-surface)",
    marginBottom: "0.25rem",
  },
  badge: {
    fontSize: "0.5625rem",
    fontWeight: 700,
    letterSpacing: "0.06em",
    background: "rgba(180,197,255,0.12)",
    color: "var(--primary)",
    padding: "0.125rem 0.5rem",
    borderRadius: "var(--radius-full)",
  },
  pathLine: {
    display: "flex",
    alignItems: "center",
    gap: "0.375rem",
    minWidth: 0,
  },
  pathIcon: {
    color: "var(--outline)",
    flexShrink: 0,
  },
  pathText: {
    fontSize: "0.75rem",
    color: "var(--on-surface-variant)",
    whiteSpace: "nowrap" as const,
    overflow: "hidden",
    textOverflow: "ellipsis",
  },
  note: {
    fontSize: "0.75rem",
    color: "var(--outline)",
    marginTop: "0.375rem",
    lineHeight: 1.5,
  },
  limitsRow: {
    alignSelf: "stretch",
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
    fontSize: "0.8125rem",
    color: "var(--on-surface-variant)",
    cursor: "pointer",
  },
  warningBox: {
    alignSelf: "stretch",
    display: "flex",
    alignItems: "flex-start",
    gap: "0.5rem",
    background: "rgba(255,181,150,0.1)",
    borderRadius: "var(--radius-md)",
    padding: "0.625rem 0.75rem",
    fontSize: "0.8125rem",
    lineHeight: 1.5,
    color: "var(--tertiary)",
  },
  errorBox: {
    alignSelf: "stretch",
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
    background: "rgba(255,107,107,0.1)",
    borderRadius: "var(--radius-md)",
    padding: "0.5rem 0.75rem",
    fontSize: "0.8125rem",
    color: "var(--error)",
  },
  actions: {
    alignSelf: "stretch",
    display: "flex",
    justifyContent: "flex-end",
    gap: "0.5rem",
    marginTop: "0.5rem",
  },
  wideBtn: {
    width: "100%",
    justifyContent: "center",
    gap: "0.5rem",
    marginTop: "0.5rem",
  },
};
