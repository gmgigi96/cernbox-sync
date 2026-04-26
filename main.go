// cernbox-sync is the CLI client for the cernbox-sync daemon.
//
// All commands are forwarded to the cernbox-syncd daemon over a Unix socket.
// The daemon must be running before any CLI command is issued.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/gmgigi96/cernbox-sync/config"
	"github.com/gmgigi96/cernbox-sync/ipc"
)

const usage = `cernbox-sync — bidirectional WebDAV sync client

Usage:
  cernbox-sync <command> [flags]

Commands:
  add           Register a new sync folder pair
  list          List registered sync folder pairs
  remove        Remove a registered sync folder pair
  run           Trigger a sync cycle in the daemon (non-blocking)
  pause         Pause syncing globally or for a specific folder
  resume        Resume syncing globally or for a specific folder
  status        Show the daemon's current sync status
  conflicts     List unresolved sync conflicts
  stop          Ask the daemon to shut down
  set-settings  Configure daemon settings (e.g. log rotation)
  get-settings  Show current daemon settings

The cernbox-syncd daemon must be running for these commands to work.
Start it with: cernbox-syncd [-interval 5m]

Run 'cernbox-sync <command> -help' for command-specific flags.
`

func main() {
	log.SetFlags(log.Ltime | log.Lmsgprefix)
	log.SetPrefix("")

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "add":
		cmdAdd(os.Args[2:])
	case "list":
		cmdList(os.Args[2:])
	case "remove":
		cmdRemove(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "pause":
		cmdPause(os.Args[2:])
	case "resume":
		cmdResume(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
	case "conflicts":
		cmdConflicts(os.Args[2:])
	case "stop":
		cmdStop(os.Args[2:])
	case "set-settings":
		cmdSetSettings(os.Args[2:])
	case "get-settings":
		cmdGetSettings(os.Args[2:])
	case "pin":
		cmdPin(os.Args[2:])
	case "unpin":
		cmdUnpin(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(1)
	}
}

// send dispatches req to the daemon and returns the response.
// It logs a fatal error and exits on any failure or daemon-reported error.
func send(req ipc.Request) *ipc.Response {
	sockPath, err := ipc.SocketPath()
	if err != nil {
		log.Fatalf("socket path: %v", err)
	}
	resp, err := ipc.Send(sockPath, req)
	if err != nil {
		log.Fatalf("%v", err)
	}
	if !resp.OK {
		log.Fatalf("daemon error: %s", resp.Error)
	}
	return resp
}

// ── add ──────────────────────────────────────────────────────────────────────

func cmdAdd(args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	name := fs.String("name", "", "Unique name for this sync pair (required)")
	localDir := fs.String("local", "", "Local directory to sync (required)")
	remoteURL := fs.String("remote", "", "Remote WebDAV URL of the space (required)")
	foldersRaw := fs.String("folders", "", "Comma-separated list of sub-folders to sync (omit to sync entire space)")
	onDemand := fs.Bool("on-demand", false, "Enable on-demand sync (Cloud Files API on Windows): files appear as placeholders and download on access")
	_ = fs.Parse(args)

	if *name == "" || *localDir == "" || *remoteURL == "" {
		fmt.Fprintf(os.Stderr, "Usage: cernbox-sync add -name <n> -local <dir> -remote <url> [-folders f1,f2] [-on-demand]\n\n")
		fs.PrintDefaults()
		os.Exit(1)
	}

	var folders []string
	if *foldersRaw != "" {
		for f := range strings.SplitSeq(*foldersRaw, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				folders = append(folders, f)
			}
		}
	}

	send(ipc.Request{
		Cmd: ipc.CmdAdd,
		Folder: config.Folder{
			Name:       *name,
			LocalRoot:  *localDir,
			RemoteBase: *remoteURL,
			Folders:    folders,
			Settings: config.FolderSettings{
				OnDemand: *onDemand,
			},
		},
	})
	fmt.Printf("Registered sync folder %q\n", *name)
}

// ── list ─────────────────────────────────────────────────────────────────────

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	_ = fs.Parse(args)

	resp := send(ipc.Request{Cmd: ipc.CmdList})
	if len(resp.Folders) == 0 {
		fmt.Println("No sync folders registered. Use 'cernbox-sync add' to add one.")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tLOCAL\tREMOTE\tFOLDERS")
	for _, f := range resp.Folders {
		foldersSummary := "(all)"
		if len(f.Folders) > 0 {
			foldersSummary = strings.Join(f.Folders, ",")
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", f.Name, f.LocalRoot, f.RemoteBase, foldersSummary)
	}
	_ = w.Flush()
}

// ── remove ───────────────────────────────────────────────────────────────────

func cmdRemove(args []string) {
	fs := flag.NewFlagSet("remove", flag.ExitOnError)
	name := fs.String("name", "", "Name of the sync pair to remove (required)")
	_ = fs.Parse(args)

	if *name == "" {
		fmt.Fprintf(os.Stderr, "Usage: cernbox-sync remove -name <n>\n\n")
		fs.PrintDefaults()
		os.Exit(1)
	}

	send(ipc.Request{Cmd: ipc.CmdRemove, Name: *name})
	fmt.Printf("Removed sync folder %q\n", *name)
}

// ── pin / unpin ──────────────────────────────────────────────────────────────

func cmdPin(args []string)   { pinCommand(args, ipc.CmdPin, "pin") }
func cmdUnpin(args []string) { pinCommand(args, ipc.CmdUnpin, "unpin") }

func pinCommand(args []string, cmd, verb string) {
	fs := flag.NewFlagSet(verb, flag.ExitOnError)
	name := fs.String("name", "", "Name of the on-demand sync folder (required)")
	fs.Parse(args)

	if *name == "" || fs.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "Usage: cernbox-sync %s -name <folder> <relative-path>\n\n", verb)
		fs.PrintDefaults()
		os.Exit(1)
	}
	send(ipc.Request{Cmd: cmd, Name: *name, Path: fs.Arg(0)})
	fmt.Printf("%sed %q in folder %q\n", strings.Title(verb), fs.Arg(0), *name)
}

// ── run ──────────────────────────────────────────────────────────────────────

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	name := fs.String("name", "", "Name of the sync pair to run (omit to run all)")
	_ = fs.Parse(args)

	send(ipc.Request{Cmd: ipc.CmdSync, Name: *name})
	if *name != "" {
		fmt.Printf("Sync triggered for %q\n", *name)
	} else {
		fmt.Println("Sync triggered for all registered folders")
	}
}

// ── pause ─────────────────────────────────────────────────────────────────────

func cmdPause(args []string) {
	fs := flag.NewFlagSet("pause", flag.ExitOnError)
	name := fs.String("name", "", "Name of the folder to pause (omit to pause globally)")
	_ = fs.Parse(args)

	send(ipc.Request{Cmd: ipc.CmdPause, Name: *name})
	if *name != "" {
		fmt.Printf("Paused folder %q\n", *name)
	} else {
		fmt.Println("Syncing paused globally")
	}
}

// ── resume ────────────────────────────────────────────────────────────────────

func cmdResume(args []string) {
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	name := fs.String("name", "", "Name of the folder to resume (omit to resume globally)")
	_ = fs.Parse(args)

	send(ipc.Request{Cmd: ipc.CmdResume, Name: *name})
	if *name != "" {
		fmt.Printf("Resumed folder %q\n", *name)
	} else {
		fmt.Println("Syncing resumed globally")
	}
}

// ── status ───────────────────────────────────────────────────────────────────

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	_ = fs.Parse(args)

	resp := send(ipc.Request{Cmd: ipc.CmdStatus})
	s := resp.Status

	if s.GlobalPaused {
		fmt.Println("Status:            PAUSED (globally)")
	}
	if len(s.PausedFolders) > 0 {
		sort.Strings(s.PausedFolders)
		fmt.Printf("Paused folders:    %s\n", strings.Join(s.PausedFolders, ", "))
	}
	sort.Strings(s.Syncing)
	if len(s.Syncing) > 0 {
		fmt.Printf("Currently syncing: %s\n", strings.Join(s.Syncing, ", "))
	} else {
		fmt.Println("Currently syncing: (none)")
	}

	if len(s.LastSync) == 0 {
		fmt.Println("Last sync:         (never)")
		return
	}

	names := make([]string, 0, len(s.LastSync))
	for n := range s.LastSync {
		names = append(names, n)
	}
	sort.Strings(names)

	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "FOLDER\tLAST SYNC")
	for _, n := range names {
		_, _ = fmt.Fprintf(w, "%s\t%s\n", n, s.LastSync[n])
	}
	_ = w.Flush()
}

// ── stop ─────────────────────────────────────────────────────────────────────

func cmdStop(args []string) {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	_ = fs.Parse(args)

	send(ipc.Request{Cmd: ipc.CmdStop})
	fmt.Println("Daemon stopped")
}

// ── set-settings ─────────────────────────────────────────────────────────────

func cmdSetSettings(args []string) {
	fs := flag.NewFlagSet("set-settings", flag.ExitOnError)
	maxAge := fs.String("log-max-age", "", "Maximum age of per-folder log entries (e.g. 168h, 720h). Empty string disables rotation.")
	_ = fs.Parse(args)

	send(ipc.Request{
		Cmd:      ipc.CmdSetSettings,
		Settings: ipc.SettingsPayload{LogRotateMaxAge: *maxAge},
	})
	fmt.Println("Settings updated")
}

// ── get-settings ─────────────────────────────────────────────────────────────

func cmdGetSettings(args []string) {
	fs := flag.NewFlagSet("get-settings", flag.ExitOnError)
	_ = fs.Parse(args)

	resp := send(ipc.Request{Cmd: ipc.CmdGetSettings})
	if resp.Settings == nil || resp.Settings.LogRotateMaxAge == "" {
		fmt.Println("log-max-age: (disabled)")
	} else {
		fmt.Printf("log-max-age: %s\n", resp.Settings.LogRotateMaxAge)
	}
}

// ── conflicts ────────────────────────────────────────────────────────────────

func cmdConflicts(args []string) {
	fs := flag.NewFlagSet("conflicts", flag.ExitOnError)
	name := fs.String("name", "", "Show conflicts for a specific folder only (omit for all)")
	_ = fs.Parse(args)

	resp := send(ipc.Request{Cmd: ipc.CmdListConflicts, Name: *name})
	if len(resp.Conflicts) == 0 {
		fmt.Println("No unresolved conflicts.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "FOLDER\tFILE\tCONFLICT COPY\tDETECTED AT")
	for _, c := range resp.Conflicts {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.Folder, c.Path, c.ConflictPath, c.CreatedAt)
	}
	_ = w.Flush()
}
