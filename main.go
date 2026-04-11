package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/gmgigi96/cernbox-sync/config"
	"github.com/gmgigi96/cernbox-sync/engine"
)

const usage = `cernbox-sync — bidirectional WebDAV sync client

Usage:
  cernbox-sync <command> [flags]

Commands:
  add     Register a new sync folder pair
  list    List registered sync folder pairs
  remove  Remove a registered sync folder pair
  run     Run a sync cycle (one folder or all)

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
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(1)
	}
}

// ── add ──────────────────────────────────────────────────────────────────────

func cmdAdd(args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	name := fs.String("name", "", "Unique name for this sync pair (required)")
	localDir := fs.String("local", "", "Local directory to sync (required)")
	remoteURL := fs.String("remote", "", "Remote WebDAV URL (required)")
	username := fs.String("user", "", "WebDAV username (required)")
	password := fs.String("pass", "", "WebDAV password (required)")
	fs.Parse(args)

	if *name == "" || *localDir == "" || *remoteURL == "" || *username == "" || *password == "" {
		fmt.Fprintf(os.Stderr, "Usage: cernbox-sync add -name <n> -local <dir> -remote <url> -user <u> -pass <p>\n\n")
		fs.PrintDefaults()
		os.Exit(1)
	}

	absLocal, err := filepath.Abs(*localDir)
	if err != nil {
		log.Fatalf("invalid local path: %v", err)
	}
	if err := os.MkdirAll(absLocal, 0o755); err != nil {
		log.Fatalf("cannot create local dir: %v", err)
	}

	db := openConfigDB()
	defer db.Close()

	if err := db.Add(config.Folder{
		Name:       *name,
		LocalRoot:  absLocal,
		RemoteBase: *remoteURL,
		Username:   *username,
		Password:   *password,
	}); err != nil {
		log.Fatalf("add: %v", err)
	}
	fmt.Printf("Registered sync folder %q\n", *name)
}

// ── list ─────────────────────────────────────────────────────────────────────

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	fs.Parse(args)

	db := openConfigDB()
	defer db.Close()

	folders, err := db.All()
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	if len(folders) == 0 {
		fmt.Println("No sync folders registered. Use 'cernbox-sync add' to add one.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tLOCAL\tREMOTE\tUSER")
	for _, f := range folders {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", f.Name, f.LocalRoot, f.RemoteBase, f.Username)
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

	db := openConfigDB()
	defer db.Close()

	if err := db.Remove(*name); err != nil {
		log.Fatalf("remove: %v", err)
	}
	fmt.Printf("Removed sync folder %q\n", *name)
}

// ── run ──────────────────────────────────────────────────────────────────────

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	name := fs.String("name", "", "Name of the sync pair to run (omit to run all)")
	fs.Parse(args)

	db := openConfigDB()
	defer db.Close()

	var folders []config.Folder
	if *name != "" {
		f, err := db.Get(*name)
		if err != nil {
			log.Fatalf("run: %v", err)
		}
		if f == nil {
			log.Fatalf("run: folder %q not found", *name)
		}
		folders = []config.Folder{*f}
	} else {
		var err error
		folders, err = db.All()
		if err != nil {
			log.Fatalf("run: %v", err)
		}
		if len(folders) == 0 {
			fmt.Println("No sync folders registered. Use 'cernbox-sync add' to add one.")
			return
		}
	}

	var failed int
	for _, f := range folders {
		cfg := engine.Config{
			LocalRoot:  f.LocalRoot,
			RemoteBase: f.RemoteBase,
			Username:   f.Username,
			Password:   f.Password,
			DBPath:     filepath.Join(f.LocalRoot, ".sync.db"),
		}
		if err := engine.Run(cfg); err != nil {
			log.Printf("sync %q failed: %v", f.Name, err)
			failed++
		}
	}
	if failed > 0 {
		os.Exit(1)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func openConfigDB() *config.DB {
	path, err := config.DefaultPath()
	if err != nil {
		log.Fatalf("config path: %v", err)
	}
	db, err := config.Open(path)
	if err != nil {
		log.Fatalf("open config db: %v", err)
	}
	return db
}
