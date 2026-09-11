// Package main is the entry point for the Prowl CLI.
//
//	@title			Prowl API
//	@version		1.0
//	@description	Prowl is the Ryoku harness for coding and daily Linux work.
//	@contact.name	Ryoku
//	@contact.url	https://github.com/neur0map/PROWL
//	@license.name	FSL-1.1-MIT
//	@license.url	https://github.com/neur0map/PROWL/blob/main/LICENSE.md
//	@BasePath		/v1
package main

import (
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"

	_ "github.com/joho/godotenv/autoload"
	"github.com/neur0map/prowl/internal/cmd"
	_ "github.com/neur0map/prowl/internal/dns"
)

func main() {
	if os.Getenv("PROWL_PROFILE") != "" {
		go func() {
			slog.Info("Serving pprof at localhost:6060")
			if httpErr := http.ListenAndServe("localhost:6060", nil); httpErr != nil {
				slog.Error("Failed to pprof listen", "error", httpErr)
			}
		}()
	}

	cmd.Execute()
}
