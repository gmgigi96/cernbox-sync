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
  status        Show the daemon's current sync status
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
	case "status":
		cmdStatus(os.Args[2:])
	case "stop":
		cmdStop(os.Args[2:])
	case "set-settings":
		cmdSetSettings(os.Args[2:])
	case "get-settings":
		cmdGetSettings(os.Args[2:])
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
	fs.Parse(args)

	if *name == "" || *localDir == "" || *remoteURL == "" {
		fmt.Fprintf(os.Stderr, "Usage: cernbox-sync add -name <n> -local <dir> -remote <url> [-folders f1,f2]\n\n")
		fs.PrintDefaults()
		os.Exit(1)
	}

	var folders []string
	if *foldersRaw != "" {
		for _, f := range strings.Split(*foldersRaw, ",") {
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
		},
	})
	fmt.Printf("Registered sync folder %q\n", *name)
}

// ── list ─────────────────────────────────────────────────────────────────────

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	fs.Parse(args)

	resp := send(ipc.Request{Cmd: ipc.CmdList})
	if len(resp.Folders) == 0 {
		fmt.Println("No sync folders registered. Use 'cernbox-sync add' to add one.")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tLOCAL\tREMOTE\tFOLDERS")
	for _, f := range resp.Folders {
		foldersSummary := "(all)"
		if len(f.Folders) > 0 {
			foldersSummary = strings.Join(f.Folders, ",")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", f.Name, f.LocalRoot, f.RemoteBase, foldersSummary)
	}
	w.Flush()
}

// ── remove ───────────────────────────────────────────────────────────────────

func cmdRemove(args []string) {
	fs := flag.NewFlagSet("remove", flag.ExitOnError)
	name := fs.String("name", "", "Name of the sync pair to remove (required)")
	fs.Parse(args)

	if *name == "" {
		fmt.Fprintf(os.Stderr, "Usage: cernbox-sync remove -name <n>\n\n")
		fs.PrintDefaults()
		os.Exit(1)
	}

	send(ipc.Request{Cmd: ipc.CmdRemove, Name: *name})
	fmt.Printf("Removed sync folder %q\n", *name)
}

// ── run ──────────────────────────────────────────────────────────────────────

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	name := fs.String("name", "", "Name of the sync pair to run (omit to run all)")
	fs.Parse(args)

	send(ipc.Request{Cmd: ipc.CmdSync, Name: *name})
	if *name != "" {
		fmt.Printf("Sync triggered for %q\n", *name)
	} else {
		fmt.Println("Sync triggered for all registered folders")
	}
}

// ── status ───────────────────────────────────────────────────────────────────

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	fs.Parse(args)

	resp := send(ipc.Request{Cmd: ipc.CmdStatus})
	s := resp.Status

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
	fmt.Fprintln(w, "FOLDER\tLAST SYNC")
	for _, n := range names {
		fmt.Fprintf(w, "%s\t%s\n", n, s.LastSync[n])
	}
	w.Flush()
}

// ── stop ─────────────────────────────────────────────────────────────────────

func cmdStop(args []string) {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	fs.Parse(args)

	send(ipc.Request{Cmd: ipc.CmdStop})
	fmt.Println("Daemon stopped")
}

// ── set-settings ─────────────────────────────────────────────────────────────

func cmdSetSettings(args []string) {
	fs := flag.NewFlagSet("set-settings", flag.ExitOnError)
	maxAge := fs.String("log-max-age", "", "Maximum age of per-folder log entries (e.g. 168h, 720h). Empty string disables rotation.")
	fs.Parse(args)

	send(ipc.Request{
		Cmd:      ipc.CmdSetSettings,
		Settings: ipc.SettingsPayload{LogRotateMaxAge: *maxAge},
	})
	fmt.Println("Settings updated")
}

// ── get-settings ─────────────────────────────────────────────────────────────

func cmdGetSettings(args []string) {
	fs := flag.NewFlagSet("get-settings", flag.ExitOnError)
	fs.Parse(args)

	resp := send(ipc.Request{Cmd: ipc.CmdGetSettings})
	if resp.Settings == nil || resp.Settings.LogRotateMaxAge == "" {
		fmt.Println("log-max-age: (disabled)")
	} else {
		fmt.Printf("log-max-age: %s\n", resp.Settings.LogRotateMaxAge)
	}
}
