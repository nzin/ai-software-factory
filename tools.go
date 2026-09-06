//go:build tools

// Package tools pins build-time tool dependencies so `go install` picks the
// version recorded in go.mod. Not compiled into any binary.
package tools

import (
	_ "github.com/go-swagger/go-swagger/cmd/swagger"
)
