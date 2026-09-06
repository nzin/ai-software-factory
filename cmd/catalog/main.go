// Command catalog runs the AI Software Factory catalog service: the source of
// truth for the agent roster and each agent's model configuration.
package main

import (
	"flag"
	"log"
	"net"
	"strconv"

	"github.com/go-openapi/loads"

	"github.com/nzin/ai-software-factory/internal/catalog"
	"github.com/nzin/ai-software-factory/internal/catalog/gen/restapi"
	"github.com/nzin/ai-software-factory/internal/catalog/gen/restapi/operations"
)

func main() {
	addr := flag.String("addr", ":8080", "host:port to listen on")
	dsn := flag.String("db", "catalog.db", "SQLite DSN (file path or file::memory:?cache=shared)")
	flag.Parse()

	store, err := catalog.Open(*dsn)
	if err != nil {
		log.Fatalf("catalog: open store: %v", err)
	}
	defer store.Close()

	swaggerSpec, err := loads.Analyzed(restapi.SwaggerJSON, "")
	if err != nil {
		log.Fatalf("catalog: load spec: %v", err)
	}

	api := operations.NewCatalogAPI(swaggerSpec)
	catalog.Setup(api, catalog.NewService(store))

	server := restapi.NewServer(api)
	defer func() { _ = server.Shutdown() }()

	host, portStr, err := net.SplitHostPort(*addr)
	if err != nil {
		log.Fatalf("catalog: bad --addr %q: %v", *addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		log.Fatalf("catalog: bad port in --addr %q: %v", *addr, err)
	}
	server.Host = host
	if server.Host == "" {
		server.Host = "0.0.0.0"
	}
	server.Port = port
	server.ConfigureAPI()

	log.Printf("catalog: listening on %s (db=%s)", *addr, *dsn)
	if err := server.Serve(); err != nil {
		log.Fatal(err)
	}
}
