package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/mmilitzer/xvid-media-offload/pkg/config"
	"github.com/mmilitzer/xvid-media-offload/pkg/scanner"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: media-offload <command> [options]")
		fmt.Fprintln(os.Stderr, "Commands:")
		fmt.Fprintln(os.Stderr, "  scan    Scan for offload candidates")
		os.Exit(1)
	}

	command := os.Args[1]
	switch command {
	case "scan":
		os.Exit(runScan(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		os.Exit(1)
	}
}

func runScan(args []string) int {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config file (required)")
	dryRun := fs.Bool("dry-run", false, "Print candidates without modifying files (required for milestone 1)")
	verbose := fs.Bool("verbose", false, "Enable verbose output with per-file skip and error details")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		return 1
	}

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "Error: --config is required")
		return 1
	}

	if !*dryRun {
		fmt.Fprintln(os.Stderr, "Error: --dry-run is required for the scan command in milestone 1")
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

	printDryRunReport(res, *verbose)
	return 0
}

func printDryRunReport(res *scanner.Result, verbose bool) {
	fmt.Println("=== Dry-Run Scan Report ===")
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
	fmt.Printf("skipped missing remote id:  %d\n", res.SkippedMissingRemote)
	fmt.Printf("errors:                     %d\n", res.Errors)

	if len(res.Candidates) == 0 {
		fmt.Println()
		fmt.Println("No candidate files found.")
	}
}
