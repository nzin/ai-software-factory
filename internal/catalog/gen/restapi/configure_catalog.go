// This file is safe to edit. Once it exists it will not be overwritten

package restapi

import (
	"crypto/tls"
	"net/http"

	"github.com/go-openapi/errors"
	"github.com/go-openapi/runtime"
	"github.com/go-openapi/runtime/middleware"
	"github.com/nzin/ai-software-factory/internal/catalog/gen/restapi/operations"
	"github.com/nzin/ai-software-factory/internal/catalog/gen/restapi/operations/agents"
	"github.com/nzin/ai-software-factory/internal/catalog/gen/restapi/operations/health"
)

//go:generate swagger generate server --target ../../gen --name Catalog --spec ../../../../api/catalog.swagger.yml --principal any --exclude-main

func configureFlags(api *operations.CatalogAPI) {
	// api.CommandLineOptionsGroups = []cmdutils.CommandLineOptionsGroup{ ... }
	_ = api
}

func configureAPI(api *operations.CatalogAPI) http.Handler {
	// configure the api here
	api.ServeError = errors.ServeError

	// Set your custom logger if needed. Default one is log.Printf
	// Expected interface func(string, ...any)
	//
	// Example:
	// api.Logger = log.Printf

	api.UseSwaggerUI()
	// To continue using redoc as your UI, uncomment the following line
	// api.UseRedoc()

	api.JSONConsumer = runtime.JSONConsumer()

	api.JSONProducer = runtime.JSONProducer()

	if api.AgentsDeleteAgentHandler == nil {
		api.AgentsDeleteAgentHandler = agents.DeleteAgentHandlerFunc(func(params agents.DeleteAgentParams) middleware.Responder {
			_ = params

			return middleware.NotImplemented("operation agents.DeleteAgent has not yet been implemented")
		})
	}
	if api.AgentsGetAgentHandler == nil {
		api.AgentsGetAgentHandler = agents.GetAgentHandlerFunc(func(params agents.GetAgentParams) middleware.Responder {
			_ = params

			return middleware.NotImplemented("operation agents.GetAgent has not yet been implemented")
		})
	}
	if api.AgentsGetAgentModelHandler == nil {
		api.AgentsGetAgentModelHandler = agents.GetAgentModelHandlerFunc(func(params agents.GetAgentModelParams) middleware.Responder {
			_ = params

			return middleware.NotImplemented("operation agents.GetAgentModel has not yet been implemented")
		})
	}
	if api.HealthHealthHandler == nil {
		api.HealthHealthHandler = health.HealthHandlerFunc(func(params health.HealthParams) middleware.Responder {
			_ = params

			return middleware.NotImplemented("operation health.Health has not yet been implemented")
		})
	}
	if api.AgentsListAgentsHandler == nil {
		api.AgentsListAgentsHandler = agents.ListAgentsHandlerFunc(func(params agents.ListAgentsParams) middleware.Responder {
			_ = params

			return middleware.NotImplemented("operation agents.ListAgents has not yet been implemented")
		})
	}
	if api.AgentsPatchAgentModelHandler == nil {
		api.AgentsPatchAgentModelHandler = agents.PatchAgentModelHandlerFunc(func(params agents.PatchAgentModelParams) middleware.Responder {
			_ = params

			return middleware.NotImplemented("operation agents.PatchAgentModel has not yet been implemented")
		})
	}
	if api.AgentsPutAgentHandler == nil {
		api.AgentsPutAgentHandler = agents.PutAgentHandlerFunc(func(params agents.PutAgentParams) middleware.Responder {
			_ = params

			return middleware.NotImplemented("operation agents.PutAgent has not yet been implemented")
		})
	}

	api.PreServerShutdown = func() {}

	api.ServerShutdown = func() {}

	return setupGlobalMiddleware(api.Serve(setupMiddlewares))
}

// The TLS configuration before HTTPS server starts.
func configureTLS(tlsConfig *tls.Config) {
	// Make all necessary changes to the TLS configuration here.
	_ = tlsConfig
}

// As soon as server is initialized but not run yet, this function will be called.
// If you need to modify a config, store server instance to stop it individually later, this is the place.
// This function can be called multiple times, depending on the number of serving schemes.
// scheme value will be set accordingly: "http", "https" or "unix".
func configureServer(server *http.Server, scheme, addr string) {
	_ = server
	_ = scheme
	_ = addr
}

// The middleware configuration is for the handler executors. These do not apply to the swagger.json document.
// The middleware executes after routing but before authentication, binding and validation.
func setupMiddlewares(handler http.Handler) http.Handler {
	return handler
}

// The middleware configuration happens before anything, this middleware also applies to serving the swagger.json document.
// So this is a good place to plug in a panic handling middleware, logging and metrics.
func setupGlobalMiddleware(handler http.Handler) http.Handler {
	return handler
}
