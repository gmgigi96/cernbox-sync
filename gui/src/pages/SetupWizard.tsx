import { useEffect, useState } from "react";
import {
  AlertCircle,
  ArrowLeft,
  ArrowRight,
  Check,
  CheckCircle2,
  Download,
  FolderPlus,
  FolderSync,
  LayoutDashboard,
  ListTree,
  Timer,
  Upload,
  UserRound,
} from "lucide-react";
import { ipc } from "../ipc";
import { AccountSetup } from "./AccountSetup";
import { LegacyImport } from "./LegacyImport";
import { BandwidthInput } from "./Settings";
import { useUiStore, type ViewMode } from "../store/uiStore";
import type { DaemonState } from "../hooks/useDaemon";
import type { Account, LegacyClient } from "../types";

// ── First-start detection ─────────────────────────────────────────────────────

/** localStorage key set once the user went through the setup wizard. */
export const SETUP_DONE_KEY = "cernbox-sync-setup-done";

export function setupDone(): boolean {
  try {
    return localStorage.getItem(SETUP_DONE_KEY) === "1";
  } catch {
    return false;
  }
}

export function markSetupDone() {
  try {
    localStorage.setItem(SETUP_DONE_KEY, "1");
  } catch {
    // Without storage the wizard is simply offered again next time.
  }
}

/**
 * Whether the app should open on the setup wizard: always without an account,
 * otherwise on first start unless folders are already synced (an install that
 * predates the wizard).
 */
export function needsSetup(account: Account | false, done: boolean, folderCount: number): boolean {
  if (account === false) return true;
  return !done && folderCount === 0;
}

// ── Steps ─────────────────────────────────────────────────────────────────────

export type WizardStep = "welcome" | "account" | "import" | "interface" | "preferences" | "done";

const STEP_LABELS: Record<WizardStep, string> = {
  welcome: "Welcome",
  account: "Account",
  import: "Import",
  interface: "Interface",
  preferences: "Preferences",
  done: "Finish",
};

/** The wizard's steps; the import is offered only when a desktop client syncs folders here. */
export function wizardSteps(legacyFolders: number): WizardStep[] {
  return ["welcome", "account", ...(legacyFolders > 0 ? (["import"] as const) : []), "interface", "preferences", "done"];
}

const INTERVALS = [
  { value: "1m", label: "1 minute" },
  { value: "5m", label: "5 minutes" },
  { value: "15m", label: "15 minutes" },
  { value: "30m", label: "30 minutes" },
  { value: "1h", label: "1 hour" },
];

/** Seconds in a Go duration string such as "5m", "1h0m0s" or "1m30s"; NaN if it is not one. */
export function durationSeconds(d: string): number {
  const units: Record<string, number> = { h: 3600, m: 60, s: 1 };
  const parts = [...d.matchAll(/(\d+(?:\.\d+)?)(h|m|s)/g)];
  if (!parts.length || parts.map((p) => p[0]).join("") !== d) return NaN;
  return parts.reduce((n, [, v, u]) => n + parseFloat(v) * units[u], 0);
}

/** "24h0m0s" → "24 hours"; durations that are not whole minutes are shown as they are. */
function intervalLabel(d: string): string {
  const secs = durationSeconds(d);
  if (secs > 0 && secs % 3600 === 0) return plural(secs / 3600, "hour");
  if (secs > 0 && secs % 60 === 0) return plural(secs / 60, "minute");
  return d;
}

const VIEW_MODES: { id: ViewMode; label: string; description: string; icon: typeof ListTree }[] = [
  {
    id: "simple",
    label: "Simple",
    description: "A list of your synced folders with their status. Recommended for most people.",
    icon: ListTree,
  },
  {
    id: "advanced",
    label: "Advanced",
    description: "A dashboard with transfer statistics, per-folder details, activity logs and conflicts.",
    icon: LayoutDashboard,
  },
];

const legacyFolderCount = (clients: LegacyClient[]) =>
  clients.reduce((n, c) => n + c.accounts.reduce((m, a) => m + a.folders.length, 0), 0);

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

// ── Wizard ────────────────────────────────────────────────────────────────────

interface SetupWizardProps {
  serverUrl: string;
  daemon: DaemonState;
  /** The account already configured, false when there is none yet. */
  account: Account | false;
  /** Called after the user signed in. */
  onAccountChanged: (account: Account) => void;
  /** Called when the user leaves the wizard; addFolder opens the add-folder flow next. */
  onFinish: (addFolder: boolean) => void;
}

/**
 * Guides the user through the first start: sign in, take over the folders of
 * the ownCloud / CERNBox desktop client if there is one, pick the interface
 * and the main sync preferences.
 */
export function SetupWizard({ serverUrl, daemon, account, onAccountChanged, onFinish }: SetupWizardProps) {
  const [step, setStep] = useState<WizardStep>("welcome");
  // Desktop clients syncing with this server, as found by the daemon. null = not detected yet.
  const [legacyClients, setLegacyClients] = useState<LegacyClient[] | null>(null);
  // Folders imported from the desktop client, for the summary.
  const [imported, setImported] = useState(0);

  useEffect(() => {
    if (legacyClients !== null || !daemon.daemonOnline) return;
    ipc.legacyDetect(serverUrl, false)
      .then(setLegacyClients)
      .catch(() => setLegacyClients([]));
  }, [legacyClients, daemon.daemonOnline, serverUrl]);

  const legacyFolders = legacyFolderCount(legacyClients ?? []);
  const steps = wizardSteps(legacyFolders);
  const index = steps.indexOf(step);
  const next = () => setStep(steps[Math.min(index + 1, steps.length - 1)]);
  const back = () => setStep(steps[Math.max(index - 1, 0)]);

  let content: React.ReactNode;
  switch (step) {
    case "welcome":
      content = (
        <WelcomeStep
          legacyClients={legacyClients ?? []}
          // Wait for the detection, which suggests the username to sign in with.
          detecting={daemon.daemonOnline && legacyClients === null}
          onNext={next}
        />
      );
      break;
    case "account":
      content = (
        <AccountStep
          account={account}
          legacyClients={legacyClients ?? []}
          onSignedIn={(acc) => {
            onAccountChanged(acc);
            next();
          }}
          onNext={next}
        />
      );
      break;
    case "import":
      content = (
        <LegacyImport
          embedded
          clients={legacyClients ?? []}
          serverUrl={serverUrl}
          daemon={daemon}
          onDone={(n) => {
            setImported(n);
            next();
          }}
        />
      );
      break;
    case "interface":
      content = <InterfaceStep onNext={next} />;
      break;
    case "preferences":
      content = <PreferencesStep onNext={next} />;
      break;
    case "done":
      content = (
        <DoneStep
          account={account}
          folderCount={daemon.folders.length}
          imported={imported}
          onFinish={onFinish}
        />
      );
      break;
  }

  const canGoBack = index > 0;

  return (
    <div style={s.root}>
      <header style={s.header}>
        <div style={s.headerSide}>
          {canGoBack && (
            <button className="btn-ghost" style={s.backBtn} onClick={back}>
              <ArrowLeft size={14} strokeWidth={1.5} /> Back
            </button>
          )}
        </div>
        <ol style={s.stepper} aria-label="Setup progress">
          {steps.map((st, i) => {
            const current = i === index;
            const complete = i < index;
            return (
              <li key={st} style={s.stepItem} aria-current={current ? "step" : undefined}>
                {i > 0 && <span style={{ ...s.stepLine, ...(i <= index ? s.stepLineDone : {}) }} />}
                <span style={{ ...s.stepDot, ...(current ? s.stepDotCurrent : complete ? s.stepDotDone : {}) }}>
                  {complete ? <Check size={11} strokeWidth={2.5} /> : i + 1}
                </span>
                <span style={{ ...s.stepLabel, ...(current ? s.stepLabelCurrent : {}) }}>{STEP_LABELS[st]}</span>
              </li>
            );
          })}
        </ol>
        <div style={s.headerSide} />
      </header>
      <main style={s.main}>{content}</main>
    </div>
  );
}

// ── Welcome ───────────────────────────────────────────────────────────────────

function WelcomeStep({ legacyClients, detecting, onNext }: { legacyClients: LegacyClient[]; detecting: boolean; onNext: () => void }) {
  const folders = legacyFolderCount(legacyClients);
  return (
    <div style={s.card}>
      <FolderSync size={32} strokeWidth={1.5} style={{ color: "var(--primary)", marginBottom: "1rem" }} />
      <h1 style={s.title}>Welcome to CERNBox Sync</h1>
      <p style={s.subtitle}>
        CERNBox Sync keeps folders on this computer in sync with your CERNBox. A few steps will get you started: sign in,
        {folders > 0 && " import the folders of your desktop client,"} choose how the app looks, and set how it syncs.
      </p>
      {folders > 0 && (
        <div style={s.infoBox}>
          <FolderSync size={14} strokeWidth={1.5} style={{ flexShrink: 0, marginTop: 2 }} />
          <span>
            The {legacyClients[0].app_name} desktop client on this computer syncs {plural(folders, "folder")}. You can take{" "}
            {folders === 1 ? "it" : "them"} over without downloading your files again.
          </span>
        </div>
      )}
      <button className="btn-primary" style={s.wideBtn} onClick={onNext} disabled={detecting}>
        {detecting ? "Checking this computer…" : "Get started"}
        {!detecting && <ArrowRight size={15} strokeWidth={1.5} />}
      </button>
    </div>
  );
}

// ── Account ───────────────────────────────────────────────────────────────────

function AccountStep({
  account,
  legacyClients,
  onSignedIn,
  onNext,
}: {
  account: Account | false;
  legacyClients: LegacyClient[];
  onSignedIn: (account: Account) => void;
  onNext: () => void;
}) {
  const [changing, setChanging] = useState(false);

  if (account && !changing) {
    return (
      <div style={s.card}>
        <UserRound size={32} strokeWidth={1.5} style={{ color: "var(--primary)", marginBottom: "1rem" }} />
        <h1 style={s.title}>Signed in</h1>
        <p style={s.subtitle}>
          CERNBox Sync uses the account <strong style={{ color: "var(--on-surface)" }}>{account.username}</strong> for all
          your sync folders.
        </p>
        <div style={s.actions}>
          <button className="btn-secondary" onClick={() => setChanging(true)}>
            Use a different account
          </button>
          <button className="btn-primary" onClick={onNext}>
            Continue <ArrowRight size={15} strokeWidth={1.5} />
          </button>
        </div>
      </div>
    );
  }

  const legacy = legacyClients.flatMap((c) => c.accounts.map((a) => ({ client: c, account: a })))[0];
  const legacyFolders = legacy?.account.folders.length ?? 0;
  return (
    <AccountSetup
      embedded
      suggestedUsername={legacy?.account.username}
      notice={
        legacy &&
        `The ${legacy.client.app_name} desktop client on this computer syncs ${plural(legacyFolders, "folder")}. Sign in to import ${
          legacyFolders === 1 ? "it" : "them"
        } next.`
      }
      onDone={() =>
        ipc.getAccount().then((acc) => {
          if (acc?.username) onSignedIn(acc);
        })
      }
    />
  );
}

// ── Interface ─────────────────────────────────────────────────────────────────

function InterfaceStep({ onNext }: { onNext: () => void }) {
  const viewMode = useUiStore((st) => st.viewMode);
  const setViewMode = useUiStore((st) => st.setViewMode);
  return (
    <div style={{ ...s.card, width: 560 }}>
      <LayoutDashboard size={32} strokeWidth={1.5} style={{ color: "var(--primary)", marginBottom: "1rem" }} />
      <h1 style={s.title}>Choose the interface</h1>
      <p style={s.subtitle}>You can switch at any time from the settings.</p>
      <div style={s.modeRow} role="radiogroup" aria-label="Interface mode">
        {VIEW_MODES.map(({ id, label, description, icon: Icon }) => {
          const active = viewMode === id;
          return (
            <button
              key={id}
              role="radio"
              aria-checked={active}
              style={{ ...s.modeOption, ...(active ? s.modeOptionActive : {}) }}
              onClick={() => setViewMode(id)}
            >
              <span style={{ ...s.modeIcon, ...(active ? s.modeIconActive : {}) }}>
                <Icon size={18} strokeWidth={1.5} />
              </span>
              <span style={s.modeLabel}>{label}</span>
              <span style={s.hint}>{description}</span>
            </button>
          );
        })}
      </div>
      <div style={s.actions}>
        <button className="btn-primary" onClick={onNext}>
          Continue <ArrowRight size={15} strokeWidth={1.5} />
        </button>
      </div>
    </div>
  );
}

// ── Preferences ───────────────────────────────────────────────────────────────

type DaemonSettings = Awaited<ReturnType<typeof ipc.getSettings>>;

function PreferencesStep({ onNext }: { onNext: () => void }) {
  // Loaded when the step opens: an import may have set the bandwidth limits.
  const [current, setCurrent] = useState<DaemonSettings | null>(null);
  const [syncInterval, setSyncInterval] = useState("5m");
  const [uploadBandwidth, setUploadBandwidth] = useState(0);
  const [downloadBandwidth, setDownloadBandwidth] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    ipc.getSettings()
      .then((v) => {
        setCurrent(v);
        // The daemon answers e.g. "5m0s": show it as the matching choice.
        const si = v.syncInterval || "5m";
        setSyncInterval(INTERVALS.find((i) => durationSeconds(i.value) === durationSeconds(si))?.value ?? si);
        setUploadBandwidth(v.uploadBandwidth ?? 0);
        setDownloadBandwidth(v.downloadBandwidth ?? 0);
      })
      .catch((e) => setError(String(e)));
  }, []);

  const intervals = INTERVALS.some((i) => i.value === syncInterval)
    ? INTERVALS
    : [...INTERVALS, { value: syncInterval, label: intervalLabel(syncInterval) }];

  async function save() {
    setSaving(true);
    setError(null);
    try {
      await ipc.setSettings(
        current?.logRotateMaxAge ?? null,
        syncInterval,
        uploadBandwidth,
        downloadBandwidth,
        current?.transferStreams ?? 0,
        current?.metadataStreams ?? 0,
      );
      onNext();
    } catch (e) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div style={{ ...s.card, width: 480 }}>
      <Timer size={32} strokeWidth={1.5} style={{ color: "var(--primary)", marginBottom: "1rem" }} />
      <h1 style={s.title}>Sync preferences</h1>
      <p style={s.subtitle}>
        How often CERNBox Sync looks for changes, and how much bandwidth it may use. Leave a limit empty for unlimited.
        You can change these and more options in the settings.
      </p>

      <div style={s.form}>
        <label style={s.field}>
          <span style={s.label}>
            <Timer size={13} strokeWidth={1.5} style={{ color: "var(--outline)" }} />
            Check CERNBox for changes every
          </span>
          <select
            style={s.select}
            value={syncInterval}
            onChange={(e) => setSyncInterval(e.target.value)}
            disabled={saving}
            aria-label="Sync interval"
          >
            {intervals.map((i) => (
              <option key={i.value} value={i.value} style={{ background: "var(--surface-container-high)" }}>
                {i.label}
              </option>
            ))}
          </select>
        </label>
        <div style={s.fieldRow}>
          <div style={s.field}>
            <span style={s.label}>
              <Upload size={13} strokeWidth={1.5} style={{ color: "var(--outline)" }} />
              Upload limit
            </span>
            <BandwidthInput bytes={uploadBandwidth} onChange={setUploadBandwidth} disabled={saving} />
          </div>
          <div style={s.field}>
            <span style={s.label}>
              <Download size={13} strokeWidth={1.5} style={{ color: "var(--outline)" }} />
              Download limit
            </span>
            <BandwidthInput bytes={downloadBandwidth} onChange={setDownloadBandwidth} disabled={saving} />
          </div>
        </div>
      </div>

      {error && (
        <div style={s.errorBox}>
          <AlertCircle size={14} strokeWidth={1.5} style={{ flexShrink: 0 }} />
          {error}
        </div>
      )}

      <div style={s.actions}>
        <button className="btn-secondary" onClick={onNext} disabled={saving}>
          Skip
        </button>
        <button className="btn-primary" onClick={save} disabled={saving}>
          {saving ? "Saving…" : "Continue"}
          {!saving && <ArrowRight size={15} strokeWidth={1.5} />}
        </button>
      </div>
    </div>
  );
}

// ── Done ──────────────────────────────────────────────────────────────────────

function DoneStep({
  account,
  folderCount,
  imported,
  onFinish,
}: {
  account: Account | false;
  folderCount: number;
  imported: number;
  onFinish: (addFolder: boolean) => void;
}) {
  const viewMode = useUiStore((st) => st.viewMode);
  return (
    <div style={s.card}>
      <CheckCircle2 size={32} strokeWidth={1.5} style={{ color: "var(--success)", marginBottom: "1rem" }} />
      <h1 style={s.title}>You're all set</h1>
      <ul style={s.summary}>
        {account && <li>Signed in as <strong>{account.username}</strong></li>}
        {imported > 0 && <li>{plural(imported, "folder")} imported from the desktop client</li>}
        <li>{VIEW_MODES.find((m) => m.id === viewMode)?.label} interface</li>
      </ul>
      {folderCount === 0 ? (
        <>
          <p style={s.subtitle}>Choose the CERNBox folders to keep in sync on this computer.</p>
          <div style={s.actions}>
            <button className="btn-secondary" onClick={() => onFinish(false)}>
              Later
            </button>
            <button className="btn-primary" onClick={() => onFinish(true)}>
              <FolderPlus size={15} strokeWidth={1.5} /> Add a folder
            </button>
          </div>
        </>
      ) : (
        <>
          <p style={s.subtitle}>{plural(folderCount, "folder is", "folders are")} kept in sync.</p>
          <div style={s.actions}>
            <button className="btn-secondary" onClick={() => onFinish(true)}>
              <FolderPlus size={15} strokeWidth={1.5} /> Add a folder
            </button>
            <button className="btn-primary" onClick={() => onFinish(false)}>
              Open CERNBox Sync <ArrowRight size={15} strokeWidth={1.5} />
            </button>
          </div>
        </>
      )}
    </div>
  );
}

// ── Styles ────────────────────────────────────────────────────────────────────

const s: Record<string, React.CSSProperties> = {
  root: {
    width: "100vw",
    height: "100vh",
    display: "flex",
    flexDirection: "column",
    background: "var(--background)",
  },
  header: {
    display: "flex",
    alignItems: "center",
    minHeight: "3.75rem",
    padding: "1rem 1.5rem",
    gap: "1rem",
  },
  headerSide: {
    flex: "0 0 6rem",
  },
  backBtn: {
    gap: "0.375rem",
    fontSize: "0.8125rem",
  },
  stepper: {
    flex: 1,
    display: "flex",
    justifyContent: "center",
    alignItems: "center",
    listStyle: "none",
    margin: 0,
    padding: 0,
    gap: "0.5rem",
    flexWrap: "wrap" as const,
  },
  stepItem: {
    display: "flex",
    alignItems: "center",
    gap: "0.5rem",
  },
  stepLine: {
    width: "1.5rem",
    height: 1,
    background: "var(--outline-variant)",
  },
  stepLineDone: {
    background: "var(--primary)",
  },
  stepDot: {
    width: 20,
    height: 20,
    borderRadius: "var(--radius-full)",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    fontSize: "0.6875rem",
    fontWeight: 600,
    background: "var(--surface-container-highest)",
    color: "var(--on-surface-variant)",
  },
  stepDotCurrent: {
    background: "var(--primary-container)",
    color: "var(--on-primary)",
  },
  stepDotDone: {
    background: "rgba(180,197,255,0.15)",
    color: "var(--primary)",
  },
  stepLabel: {
    fontSize: "0.75rem",
    color: "var(--outline)",
  },
  stepLabelCurrent: {
    color: "var(--on-surface)",
    fontWeight: 500,
  },
  main: {
    flex: 1,
    minHeight: 0,
    display: "flex",
    flexDirection: "column",
    padding: "0 1.5rem 1.5rem",
  },
  card: {
    margin: "auto",
    background: "var(--surface-container-high)",
    borderRadius: "var(--radius-xl)",
    padding: "2.5rem",
    width: 440,
    maxWidth: "100%",
    maxHeight: "100%",
    overflowY: "auto" as const,
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
  infoBox: {
    alignSelf: "stretch",
    display: "flex",
    alignItems: "flex-start",
    gap: "0.5rem",
    background: "rgba(180,197,255,0.08)",
    borderRadius: "var(--radius-md)",
    padding: "0.625rem 0.75rem",
    fontSize: "0.8125rem",
    lineHeight: 1.5,
    color: "var(--on-surface-variant)",
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
  modeRow: {
    alignSelf: "stretch",
    display: "grid",
    gridTemplateColumns: "1fr 1fr",
    gap: "0.75rem",
  },
  modeOption: {
    display: "flex",
    flexDirection: "column",
    alignItems: "flex-start",
    gap: "0.375rem",
    textAlign: "left" as const,
    background: "var(--surface-container-highest)",
    border: "1px solid transparent",
    borderRadius: "var(--radius-lg)",
    padding: "1rem",
    cursor: "pointer",
    fontFamily: "var(--font-family)",
    transition: "all var(--transition-base)",
  },
  modeOptionActive: {
    border: "1px solid var(--primary)",
    background: "rgba(180,197,255,0.08)",
  },
  modeIcon: {
    display: "inline-flex",
    padding: "0.5rem",
    borderRadius: "var(--radius-md)",
    background: "var(--surface-container-high)",
    color: "var(--on-surface-variant)",
    marginBottom: "0.25rem",
  },
  modeIconActive: {
    color: "var(--primary)",
    background: "rgba(180,197,255,0.12)",
  },
  modeLabel: {
    fontSize: "0.875rem",
    fontWeight: 600,
    color: "var(--on-surface)",
  },
  hint: {
    fontSize: "0.75rem",
    lineHeight: 1.5,
    color: "var(--on-surface-variant)",
  },
  form: {
    alignSelf: "stretch",
    display: "flex",
    flexDirection: "column",
    gap: "1rem",
  },
  fieldRow: {
    display: "grid",
    gridTemplateColumns: "1fr 1fr",
    gap: "0.75rem",
  },
  field: {
    display: "flex",
    flexDirection: "column",
    gap: "0.375rem",
  },
  label: {
    fontSize: "0.8125rem",
    fontWeight: 500,
    color: "var(--on-surface-variant)",
    display: "flex",
    alignItems: "center",
    gap: "0.375rem",
  },
  select: {
    background: "var(--surface-container-lowest)",
    color: "var(--on-surface)",
    border: "1px solid rgba(68,71,90,0.10)",
    borderRadius: "var(--radius-md)",
    padding: "0.5rem 0.75rem",
    fontSize: "0.875rem",
    outline: "none",
    fontFamily: "var(--font-family)",
  },
  summary: {
    alignSelf: "stretch",
    listStyle: "none",
    margin: 0,
    padding: "0.75rem 1rem",
    background: "var(--surface-container-highest)",
    borderRadius: "var(--radius-lg)",
    display: "flex",
    flexDirection: "column",
    gap: "0.375rem",
    fontSize: "0.8125rem",
    color: "var(--on-surface-variant)",
  },
};
