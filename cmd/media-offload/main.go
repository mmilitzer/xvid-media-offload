package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mmilitzer/xvid-media-offload/pkg/config"
	"github.com/mmilitzer/xvid-media-offload/pkg/daemon"
	"github.com/mmilitzer/xvid-media-offload/pkg/db"
	"github.com/mmilitzer/xvid-media-offload/pkg/download"
	"github.com/mmilitzer/xvid-media-offload/pkg/restore"
	"github.com/mmilitzer/xvid-media-offload/pkg/scanner"
	"github.com/mmilitzer/xvid-media-offload/pkg/shrink"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: media-offload <command> [options]")
		fmt.Fprintln(os.Stderr, "Commands:")
		fmt.Fprintln(os.Stderr, "  scan    Scan for offload candidates")
		fmt.Fprintln(os.Stderr, "  shrink  Offload eligible files by punching holes")
		fmt.Fprintln(os.Stderr, "  restore Restore offloaded files from remote storage")
		fmt.Fprintln(os.Stderr, "  daemon  Run continuous daemon with periodic scans and inotify")
		os.Exit(1)
	}

	command := os.Args[1]
	switch command {
	case "scan":
		os.Exit(runScan(os.Args[2:]))
	case "shrink":
		os.Exit(runShrink(os.Args[2:]))
	case "restore":
		os.Exit(runRestore(os.Args[2:]))
	case "daemon":
		os.Exit(runDaemon(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		os.Exit(1)
	}
}

func runScan(args []string) int {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config file (required)")
	verbose := fs.Bool("verbose", false, "Enable verbose output with per-file skip and error details")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		return 1
	}

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "Error: --config is required")
		return 1
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		return 1
	}
	cfg.Verbose = *verbose

	res, err := scanner.Scan(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error scanning: %v\n", err)
		return 1
	}

	printScanReport(res, *verbose)
	return 0
}

func printScanReport(res *scanner.Result, verbose bool) {
	fmt.Println("=== Scan Report ===")
	fmt.Println()

	if len(res.Candidates) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PATH\tREMOTE ID\tSIZE\tMTIME\tAGE\tGLOB\tMARKER")
		for _, c := range res.Candidates {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
				c.Path,
				c.RemoteID,
				c.Size,
				c.ModTime.Format(time.RFC3339),
				c.Age.Round(time.Minute),
				c.Glob,
				c.MarkerPath,
			)
		}
		w.Flush()
		fmt.Println()
	}

	if verbose && len(res.SkippedDetails) > 0 {
		fmt.Println("=== Skipped Files ===")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PATH\tREASON\tMARKER")
		for _, s := range res.SkippedDetails {
			fmt.Fprintf(w, "%s\t%s\t%s\n", s.Path, s.Reason, s.MarkerPath)
		}
		w.Flush()
		fmt.Println()
	}

	if verbose && len(res.ErrorDetails) > 0 {
		fmt.Println("=== Errors ===")
		for _, e := range res.ErrorDetails {
			fmt.Println(e)
		}
		fmt.Println()
	}

	fmt.Println("=== Summary ===")
	fmt.Printf("managed folders found:      %d\n", res.ManagedFolders)
	fmt.Printf("valid marker files:         %d\n", res.ValidMarkers)
	fmt.Printf("invalid marker files:       %d\n", res.InvalidMarkers)
	fmt.Printf("candidate files found:      %d\n", len(res.Candidates))
	fmt.Printf("total candidate files size: %d\n", res.TotalCandidateSize)
	fmt.Printf("skipped already offloaded:  %d\n", res.SkippedOffloaded)
	fmt.Printf("skipped too young:          %d\n", res.SkippedTooYoung)
	fmt.Printf("skipped file not found:     %d\n", res.SkippedMissingRemote)
	fmt.Printf("errors:                     %d\n", res.Errors)

	if len(res.Candidates) == 0 {
		fmt.Println()
		fmt.Println("No candidate files found.")
	}
}

func runShrink(args []string) int {
	fs := flag.NewFlagSet("shrink", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config file (required)")
	dryRun := fs.Bool("dry-run", false, "Print candidates without modifying files")
	verbose := fs.Bool("verbose", false, "Enable verbose output with per-file skip and error details")
	yes := fs.Bool("yes", false, "Skip confirmation prompt")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		return 1
	}

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "Error: --config is required")
		return 1
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		return 1
	}
	cfg.Verbose = *verbose

	var database *db.DB
	if cfg.DatabasePath != "" {
		database, err = db.Open(cfg.DatabasePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
			return 1
		}
		defer database.Close()
	}

	if !*dryRun && !*yes {
		fmt.Println("WARNING: This will permanently modify media files by punching holes.")
		fmt.Println("Files will be kept sparse with a preserved logical size.")
		fmt.Println("This operation is NOT reversible without restoring from remote storage.")
		fmt.Println()
		fmt.Print("Continue? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		response, _ := reader.ReadString('\n')
		response = strings.TrimSpace(response)
		if response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			return 0
		}
	}

	res, err := shrink.Run(cfg, *dryRun, database)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error shrinking: %v\n", err)
		return 1
	}

	printShrinkReport(res, *dryRun, *verbose)
	return 0
}

func printShrinkReport(res *shrink.Result, dryRun bool, verbose bool) {
	if dryRun {
		fmt.Println("=== Dry-Run Shrink Report ===")
	} else {
		fmt.Println("=== Shrink Apply Report ===")
	}
	fmt.Println()

	if dryRun && len(res.Candidates) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PATH\tREMOTE ID\tAUTOGRAPH\tSIZE\tMTIME\tAGE\tGLOB\tMARKER")
		for _, c := range res.Candidates {
			if c.Size <= 0 {
				continue
			}
			autographStr := "missing"
			if c.Autograph >= 0 {
				autographStr = fmt.Sprintf("%d", c.Autograph)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
				c.Path,
				c.RemoteID,
				autographStr,
				c.Size,
				c.ModTime.Format(time.RFC3339),
				c.Age.Round(time.Minute),
				c.Glob,
				c.MarkerPath,
			)
		}
		w.Flush()
		fmt.Println()
	}

	if !dryRun && len(res.Offloaded) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PATH\tREMOTE ID\tAUTOGRAPH\tORIGINAL SIZE\tALLOC BEFORE\tALLOC AFTER\tBYTES SAVED\tMARKER\tDB STATUS")
		for _, o := range res.Offloaded {
			bytesSaved := o.AllocatedBefore - o.AllocatedAfter
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n",
				o.Path,
				o.RemoteID,
				o.Autograph,
				o.OriginalSize,
				o.AllocatedBefore,
				o.AllocatedAfter,
				bytesSaved,
				o.MarkerPath,
				o.DBStatus,
			)
		}
		w.Flush()
		fmt.Println()
	}

	if verbose && len(res.SkippedDetails) > 0 {
		fmt.Println("=== Skipped Files ===")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PATH\tREASON\tMARKER")
		for _, s := range res.SkippedDetails {
			fmt.Fprintf(w, "%s\t%s\t%s\n", s.Path, s.Reason, s.MarkerPath)
		}
		w.Flush()
		fmt.Println()
	}

	if verbose && len(res.ErrorDetails) > 0 {
		fmt.Println("=== Errors ===")
		for _, e := range res.ErrorDetails {
			fmt.Println(e)
		}
		fmt.Println()
	}

	fmt.Println("=== Summary ===")
	fmt.Printf("managed folders found:      %d\n", res.ManagedFolders)
	if dryRun {
		fmt.Printf("valid marker files:         %d\n", res.ValidMarkers)
		fmt.Printf("invalid marker files:       %d\n", res.InvalidMarkers)
	}
	fmt.Printf("candidate files found:      %d\n", len(res.Candidates))
	if !dryRun {
		fmt.Printf("files offloaded:            %d\n", len(res.Offloaded))
	}
	fmt.Printf("skipped already offloaded:  %d\n", res.SkippedOffloaded)
	fmt.Printf("skipped already sparse:     %d\n", res.SkippedSparse)
	fmt.Printf("skipped too young:          %d\n", res.SkippedTooYoung)
	fmt.Printf("skipped too small:          %d\n", res.SkippedTooSmall)
	fmt.Printf("skipped no autograph:       %d\n", res.SkippedNoAutograph)
	fmt.Printf("skipped file not found:     %d\n", res.SkippedMissingRemote)
	fmt.Printf("errors:                     %d\n", res.Errors)
	fmt.Printf("total logical files size:   %d\n", res.TotalCandidateSize)
	if !dryRun && res.TotalCandidateSize > 0 {
		fmt.Printf("bytes saved:                %d (%.1f%%)\n", res.BytesSaved, float64(res.BytesSaved)/float64(res.TotalCandidateSize)*100)
	} else if !dryRun {
		fmt.Printf("bytes saved:                %d\n", res.BytesSaved)
	}

	if dryRun && len(res.Candidates) == 0 {
		fmt.Println()
		fmt.Println("No candidate files found.")
	} else if !dryRun && len(res.Offloaded) == 0 {
		fmt.Println()
		fmt.Println("No files were offloaded.")
	}
}

func runRestore(args []string) int {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config file (required)")
	dryRun := fs.Bool("dry-run", false, "Report candidates without downloading or modifying files")
	verbose := fs.Bool("verbose", false, "Enable verbose output with per-file skip and error details")
	workers := fs.Int("workers", 4, "Number of parallel restore workers")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		return 1
	}

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "Error: --config is required")
		return 1
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		return 1
	}
	cfg.Verbose = *verbose

	var database *db.DB
	if cfg.DatabasePath != "" {
		database, err = db.Open(cfg.DatabasePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
			return 1
		}
		defer database.Close()
	}

	downloader := &download.HTTPDownloader{}
	res, err := restore.Run(cfg, *dryRun, *workers, database, downloader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error restoring: %v\n", err)
		return 1
	}

	printRestoreReport(res, *dryRun, *verbose)
	return 0
}

func printRestoreReport(res *restore.Result, dryRun bool, verbose bool) {
	if dryRun {
		fmt.Println("=== Dry-Run Restore Report ===")
	} else {
		fmt.Println("=== Restore Report ===")
	}
	fmt.Println()

	fmt.Printf("restore candidates found: %d\n", res.Candidates)
	fmt.Printf("jobs queued:              %d\n", res.Queued)
	if !dryRun {
		fmt.Printf("restored successfully:    %d\n", len(res.Restored))
		fmt.Printf("failed:                   %d\n", res.Failed)
	}
	fmt.Printf("skipped no remote id:     %d\n", res.SkippedNoRemoteID)
	if !dryRun {
		fmt.Printf("skipped no credentials:   %d\n", res.SkippedNoCreds)
		fmt.Printf("skipped no disk space:    %d\n", res.SkippedNoSpace)
	}
	fmt.Printf("errors:                   %d\n", res.Errors)
	fmt.Println()

	if !dryRun && len(res.Restored) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PATH\tREMOTE ID\tAUTOGRAPH\tSIZE\tMARKER")
		for _, r := range res.Restored {
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\n",
				r.Path,
				r.RemoteID,
				r.Autograph,
				r.Size,
				r.MarkerPath,
			)
		}
		w.Flush()
		fmt.Println()
	}

	if verbose && len(res.ErrorDetails) > 0 {
		fmt.Println("=== Errors ===")
		for _, e := range res.ErrorDetails {
			fmt.Println(e)
		}
		fmt.Println()
	}
}

func runDaemon(args []string) int {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config file (required)")
	dryRun := fs.Bool("dry-run", false, "Report planned actions without modifying files")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		return 1
	}

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "Error: --config is required")
		return 1
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		return 1
	}

	// Default lock file to the same directory as the config file so it is
	// writable by the unprivileged user running the daemon.
	if cfg.LockFile == "" {
		cfg.LockFile = filepath.Join(filepath.Dir(*configPath), "media-offload.lock")
	}

	var database *db.DB
	if cfg.DatabasePath != "" {
		database, err = db.Open(cfg.DatabasePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
			return 1
		}
	}

	d := daemon.NewDaemon(cfg, *dryRun, database, &download.HTTPDownloader{})
	if err := d.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running daemon: %v\n", err)
		return 1
	}
	return 0
}
