// Command coordinator accepts a PRD and drives it through the factory.
//
// Usage:
//
//	coordinator submit --prd path/to/prd.md [--out plan.md] [--budget N] [--deadline 2h]
//	coordinator serve  --addr :8090 [--catalog-url URL] [--max-runs N]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"

	"github.com/go-openapi/loads"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/coordinator"
	"github.com/nzin/ai-software-factory/internal/coordinator/gen/restapi"
	"github.com/nzin/ai-software-factory/internal/coordinator/gen/restapi/operations"
	"github.com/nzin/ai-software-factory/internal/prd"
)

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

func cmdSubmit(args []string) {
	fs := flag.NewFlagSet("submit", flag.ExitOnError)
	prdPath := fs.String("prd", "", "path to the PRD (markdown or JSON); '-' for stdin")
	out := fs.String("out", "", "write the plan to this file instead of stdout")
	catalogURL := fs.String("catalog-url", "http://127.0.0.1:8080", "catalog base URL")
	budget := fs.Int("budget", 0, "override the iteration budget (0 = default)")
	deadline := fs.Duration("deadline", 0, "override the wall-clock budget (0 = default)")
	_ = fs.Parse(args)

	if *prdPath == "" {
		log.Fatal("coordinator submit: --prd is required")
	}
	doc, err := readPRD(*prdPath)
	if err != nil {
		log.Fatalf("coordinator submit: %v", err)
	}

	cc, err := agentkit.NewCatalogClient(*catalogURL)
	if err != nil {
		log.Fatalf("coordinator submit: %v", err)
	}
	orch := coordinator.New(cc)

	run, err := orch.Submit(context.Background(), doc, coordinator.SubmitOptions{
		IterationBudget: *budget,
		Deadline:        *deadline,
	})
	if err != nil {
		log.Fatalf("coordinator submit: %v", err)
	}

	log.Printf("run %s: status=%s stage=%s iterations_left=%d", run.ID, run.Status, run.Stage, run.Budget.IterationsRemaining)
	if run.Reason != "" {
		log.Printf("run %s: reason: %s", run.ID, run.Reason)
	}
	if run.Status == coordinator.StatusFailed {
		os.Exit(1)
	}

	plan := run.Plan
	if plan == "" {
		log.Print("run produced no plan")
		return
	}
	if *out != "" {
		if err := os.WriteFile(*out, []byte(plan+"\n"), 0o644); err != nil {
			log.Fatalf("coordinator submit: write %s: %v", *out, err)
		}
		log.Printf("plan written to %s (%d bytes)", *out, len(plan))
		return
	}
	fmt.Println(plan)
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8090", "host:port to listen on")
	catalogURL := fs.String("catalog-url", "http://127.0.0.1:8080", "catalog base URL")
	budget := fs.Int("iteration-budget", coordinator.DefaultIterationBudget, "default per-run iteration budget")
	deadline := fs.Duration("deadline", coordinator.DefaultDeadline, "default per-run wall-clock budget")
	_ = fs.Parse(args)

	cc, err := agentkit.NewCatalogClient(*catalogURL)
	if err != nil {
		log.Fatalf("coordinator serve: %v", err)
	}
	orch := coordinator.New(cc, coordinator.WithDefaults(*budget, *deadline))

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

	log.Printf("coordinator: listening on %s (catalog=%s)", *addr, *catalogURL)
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
