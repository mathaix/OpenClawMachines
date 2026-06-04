package main

import (
	"flag"
	"log/slog"

	"github.com/mathaix/openclawmachines/backend/internal/ptyd"
	"github.com/mathaix/openclawmachines/backend/pkg/logging"
	"github.com/mathaix/openclawmachines/backend/pkg/version"
)

func main() {
	port := flag.String("port", "7681", "PTY server port")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		slog.Info("ocmptyd.version", "version", version.Version, "commit", version.GitCommit, "built", version.BuildTime)
		return
	}

	logging.Init("ocmptyd")
	slog.Info("ocmptyd.start", "version", version.Version)
	ptyd.Run(*port)
}
