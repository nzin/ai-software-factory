// Command coordinator accepts a PRD and drives it through the factory.
//
// Usage:
//
//	coordinator submit --prd path/to/prd.md [--repo URL] [--out plan.md]
//	coordinator serve  --addr :8090 [--runstore /data/runs.db] [--ui-dir DIR]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/go-openapi/loads"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/coordinator"
	"github.com/nzin/ai-software-factory/internal/coordinator/gen/restapi"
	"github.com/nzin/ai-software-factory/internal/coordinator/gen/restapi/operations"
	"github.com/nzin/ai-software-factory/internal/coordinator/runstore"
	"github.com/nzin/ai-software-factory/internal/coordinator/ui"
	"github.com/nzin/ai-software-factory/internal/prd"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "submit":
		cmdSubmit(os.Args[2:])
	case "serve":
		cmdServe(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: coordinator <submit|serve> [flags]")
	os.Exit(2)
}

func newOrchestrator(catalogURL, wsRoot, storeDSN, baseBranch string, extra ...coordinator.Option) *coordinator.Orchestrator {
	cc, err := agentkit.NewCatalogClient(catalogURL)
	if err != nil {
		log.Fatalf("coordinator: %v", err)
	}
	opts := []coordinator.Option{
		coordinator.WithWorkspace(workspace.NewManager(wsRoot)),
		coordinator.WithBaseBranch(baseBranch),
	}
	if storeDSN != "" && storeDSN != "memory" {
		st, err := runstore.Open(storeDSN)
		if err != nil {
			log.Fatalf("coordinator: open runstore %q: %v", storeDSN, err)
		}
		opts = append(opts, coordinator.WithStore(st))
	}
	opts = append(opts, extra...)
	o := coordinator.New(cc, opts...)
	if err := o.Recover(); err != nil {
		log.Fatalf("coordinator: recover: %v", err)
	}
	return o
}

func cmdSubmit(args []string) {
	fs := flag.NewFlagSet("submit", flag.ExitOnError)
	prdPath := fs.String("prd", "", "path to the PRD (markdown or JSON); '-' for stdin")
	repoURL := fs.String("repo", "", "target repo: '' (new local), file:///path (local), https/git URL (remote)")
	baseBranch := fs.String("base-branch", "main", "branch to base the work on")
	out := fs.String("out", "", "write the plan to this file instead of stdout")
	catalogURL := fs.String("catalog-url", envOr("ASF_CATALOG_URL", "http://127.0.0.1:8080"), "catalog base URL")
	wsRoot := fs.String("workspace-root", envOr("ASF_WORKSPACE_ROOT", "workspace"), "root dir for per-run workspaces")
	budget := fs.Int("budget", 0, "override the iteration budget (0 = default)")
	deadline := fs.Duration("deadline", 0, "override the wall-clock budget (0 = default)")
	autoApprove := fs.Bool("yes", false, "auto-approve the plan at the approval gate")
	_ = fs.Parse(args)

	if *prdPath == "" {
		log.Fatal("coordinator submit: --prd is required")
	}
	doc, err := readPRD(*prdPath)
	if err != nil {
		log.Fatalf("coordinator submit: %v", err)
	}

	orch := newOrchestrator(*catalogURL, *wsRoot, "memory", *baseBranch)
	run, err := orch.Submit(context.Background(), doc, coordinator.SubmitOptions{
		RepoURL: *repoURL, BaseBranch: *baseBranch,
		IterationBudget: *budget, Deadline: *deadline,
	})
	if err != nil {
		log.Fatalf("coordinator submit: %v", err)
	}
	log.Printf("run %s started (repo kind=%s, branch=%s)", run.ID, run.RepoKind, run.WorkBranch)

	// Poll to completion (the CLI is a thin sync wrapper over the async engine).
	for {
		time.Sleep(3 * time.Second)
		r, _ := orch.Get(run.ID)
		if r == nil {
			continue
		}
		if r.Status == coordinator.StatusAwaitingApproval {
			log.Printf("run %s: awaiting approval — %s", r.ID, approvalReason(r))
			if !*autoApprove {
				fmt.Fprintln(os.Stderr, "re-run with --yes to auto-approve, or POST /v1/runs/"+r.ID+"/approve")
				os.Exit(2)
			}
			log.Printf("run %s: auto-approving", r.ID)
			if _, err := orch.Approve(r.ID); err != nil {
				log.Fatalf("approve: %v", err)
			}
			continue
		}
		if r.Status.Terminal() {
			log.Printf("run %s: %s — %s", r.ID, r.Status, r.Reason)
			if r.Plan != "" && *out != "" {
				_ = os.WriteFile(*out, []byte(r.Plan+"\n"), 0o644)
				log.Printf("plan written to %s", *out)
			} else if r.Plan != "" {
				fmt.Println(r.Plan)
			}
			if r.Status == coordinator.StatusFailed {
				os.Exit(1)
			}
			return
		}
	}
}

func approvalReason(r *coordinator.Run) string {
	if r.Approval != nil {
		return r.Approval.Reason
	}
	return r.Reason
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8090", "host:port to listen on")
	catalogURL := fs.String("catalog-url", envOr("ASF_CATALOG_URL", "http://127.0.0.1:8080"), "catalog base URL")
	wsRoot := fs.String("workspace-root", envOr("ASF_WORKSPACE_ROOT", "workspace"), "root dir for per-run workspaces")
	store := fs.String("runstore", envOr("ASF_RUNSTORE", "memory"), "'memory' or a SQLite path for durable runs")
	baseBranch := fs.String("base-branch", envOr("ASF_BASE_BRANCH", "main"), "default branch to base work on")
	budget := fs.Int("iteration-budget", coordinator.DefaultIterationBudget, "default per-run iteration budget")
	deadline := fs.Duration("deadline", coordinator.DefaultDeadline, "default per-run wall-clock budget")
	uiDir := fs.String("ui-dir", envOr("ASF_UI_DIR", "browser/asf-ui/dist"),
		"built Vue SPA to serve at / (skipped when it has no index.html)")
	defaultRepo := fs.String("default-repo", envOr("ASF_DEFAULT_REPO", ""),
		"repo a submission targets when its repoURL is empty (default: scaffold a throwaway repo)")
	_ = fs.Parse(args)

	orch := newOrchestrator(*catalogURL, *wsRoot, *store, *baseBranch,
		coordinator.WithDefaults(*budget, *deadline),
		coordinator.WithDefaultRepo(*defaultRepo))

	// Must happen before ConfigureAPI, which builds the middleware stack.
	ui.SetDir(*uiDir)

	swaggerSpec, err := loads.Analyzed(restapi.SwaggerJSON, "")
	if err != nil {
		log.Fatalf("coordinator serve: load spec: %v", err)
	}
	api := operations.NewCoordinatorAPI(swaggerSpec)
	coordinator.Setup(api, orch)

	server := restapi.NewServer(api)
	defer func() { _ = server.Shutdown() }()

	host, portStr, err := net.SplitHostPort(*addr)
	if err != nil {
		log.Fatalf("coordinator serve: bad --addr %q: %v", *addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		log.Fatalf("coordinator serve: bad port %q: %v", portStr, err)
	}
	server.Host = host
	if server.Host == "" {
		server.Host = "0.0.0.0"
	}
	server.Port = port
	server.ConfigureAPI()

	log.Printf("coordinator: listening on %s (catalog=%s, runstore=%s)", *addr, *catalogURL, *store)
	if err := server.Serve(); err != nil {
		log.Fatal(err)
	}
}

func readPRD(path string) (prd.PRD, error) {
	if path == "-" {
		return prd.Parse(os.Stdin)
	}
	f, err := os.Open(path)
	if err != nil {
		return prd.PRD{}, err
	}
	defer f.Close()
	doc, err := prd.Parse(f)
	if err != nil {
		return prd.PRD{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return doc, nil
}
