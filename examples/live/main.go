// Explicitly run this executable to opt in to credit-consuming live requests.
// Offline tests never invoke main or read real credentials.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/January-ai/january-server-sdk-go/january"
)

func main() {
	root, err := findRoot()
	if err != nil {
		fmt.Println("configuration NOT_RUN code=sdk_root_not_found")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(runCommand(ctx, root, os.LookupEnv, os.Stdout, january.NewClient))
}

func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", safeError("sdk_root_not_found")
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(data), "module github.com/January-ai/january-server-sdk-go") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", safeError("sdk_root_not_found")
		}
		dir = parent
	}
}

func runCommand(ctx context.Context, root string, lookup func(string) (string, bool), out io.Writer, newClient func(january.Config) (*january.Client, error)) int {
	c, err := loadConfig(root, lookup)
	var report runReport
	if err != nil {
		report = newReport()
		report.Status = "NOT_RUN"
		report.Checks = append(report.Checks, result{Operation: "configuration", Status: "FAIL", Code: errorCode(err)})
		for i := range report.Operations {
			report.Operations[i].Reason = "configuration_not_ready"
		}
		report.finish()
	} else {
		report = runWorkflow(ctx, c, func(r result) { printResult(out, r) }, newClient)
	}
	if err != nil {
		for _, r := range report.Checks {
			printResult(out, r)
		}
	}
	if err := saveReport(root, report); err != nil {
		fmt.Fprintln(out, "report FAIL code=report_write_failed")
		return 1
	}
	fmt.Fprintf(out, "go %s operations_passed=%d operations_failed=%d operations_blocked=%d cleanup_failed=%d\n", report.Status, report.Counts.Passed, report.Counts.Failed, report.Counts.Blocked, report.CleanupFailed)
	if report.Status != "PASS" {
		return 1
	}
	return 0
}

func printResult(w io.Writer, r result) {
	fmt.Fprintf(w, "%s %s", r.Operation, r.Status)
	if r.Code != "" {
		fmt.Fprintf(w, " code=%s", r.Code)
	}
	if r.RequestID != "" {
		fmt.Fprintf(w, " requestID=%s", r.RequestID)
	}
	if r.Reason != "" {
		fmt.Fprintf(w, " reason=%s", r.Reason)
	}
	fmt.Fprintln(w)
}

func saveReport(root string, r runReport) error {
	dir := filepath.Join(root, ".e2e-results")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".result-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "latest.json"))
}
