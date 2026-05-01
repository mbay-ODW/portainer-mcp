package main

import (
	"flag"
	"os"
	"strings"
	"time"

	"github.com/portainer/portainer-mcp/internal/mcp"
	"github.com/portainer/portainer-mcp/internal/tooldef"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const defaultToolsPath = "tools.yaml"

var (
	Version   string
	BuildDate string
	Commit    string
)

func main() {
	configureLogging()

	log.Info().
		Str("version", Version).
		Str("build-date", BuildDate).
		Str("commit", Commit).
		Str("log-level", zerolog.GlobalLevel().String()).
		Msg("Portainer MCP server")

	serverFlag := flag.String("server", "", "The Portainer server URL (or env PORTAINER_URL)")
	tokenFlag := flag.String("token", "", "The authentication token for the Portainer server (or env PORTAINER_TOKEN)")
	toolsFlag := flag.String("tools", "", "The path to the tools YAML file")
	readOnlyFlag := flag.Bool("read-only", false, "Run in read-only mode")
	disableVersionCheckFlag := flag.Bool("disable-version-check", false, "Disable Portainer server version check")
	transportFlag := flag.String("transport", "stdio", "Transport: 'stdio' (default) or 'sse'")
	listenFlag := flag.String("listen", ":8000", "SSE listen address (only used with -transport sse)")

	flag.Parse()

	// Allow env vars as fallback so the docker-compose deployment doesn't
	// need to bake credentials into command-line args.
	resolvedServer := *serverFlag
	if resolvedServer == "" {
		resolvedServer = os.Getenv("PORTAINER_URL")
	}
	resolvedToken := *tokenFlag
	if resolvedToken == "" {
		resolvedToken = os.Getenv("PORTAINER_TOKEN")
	}
	resolvedTransport := *transportFlag
	if envT := os.Getenv("MCP_TRANSPORT"); envT != "" {
		resolvedTransport = envT
	}
	resolvedListen := *listenFlag
	if envL := os.Getenv("PORT"); envL != "" {
		resolvedListen = ":" + envL
	}

	log.Debug().
		Str("portainer_url", resolvedServer).
		Bool("portainer_token_set", resolvedToken != "").
		Int("portainer_token_len", len(resolvedToken)).
		Str("transport", resolvedTransport).
		Str("listen", resolvedListen).
		Bool("read_only", *readOnlyFlag).
		Bool("disable_version_check", *disableVersionCheckFlag).
		Msg("resolved configuration")

	if resolvedServer == "" || resolvedToken == "" {
		log.Fatal().Msg("server (PORTAINER_URL) and token (PORTAINER_TOKEN) are required")
	}

	toolsPath := *toolsFlag
	if toolsPath == "" {
		toolsPath = defaultToolsPath
	}

	// Working directory + path diagnostics – useful when tools.yaml writes fail
	// because the runtime user has no write permissions on the workdir.
	if cwd, err := os.Getwd(); err == nil {
		log.Debug().Str("cwd", cwd).Str("tools_path", toolsPath).Msg("filesystem context")
	}

	// We first check if the tools.yaml file exists
	// We'll create it from the embedded version if it doesn't exist
	exists, err := tooldef.CreateToolsFileIfNotExists(toolsPath)
	if err != nil {
		log.Fatal().Err(err).Str("tools_path", toolsPath).Msg("failed to create tools.yaml file")
	}

	if exists {
		log.Info().Str("tools_path", toolsPath).Msg("using existing tools.yaml file")
	} else {
		log.Info().Str("tools_path", toolsPath).Msg("created tools.yaml file from embedded default")
	}

	log.Info().
		Str("portainer-host", resolvedServer).
		Str("tools-path", toolsPath).
		Str("transport", resolvedTransport).
		Bool("read-only", *readOnlyFlag).
		Bool("disable-version-check", *disableVersionCheckFlag).
		Msg("starting MCP server")

	log.Debug().Msg("calling NewPortainerMCPServer (may probe Portainer for version)")
	startInit := time.Now()
	server, err := mcp.NewPortainerMCPServer(resolvedServer, resolvedToken, toolsPath, mcp.WithReadOnly(*readOnlyFlag), mcp.WithDisableVersionCheck(*disableVersionCheckFlag))
	if err != nil {
		log.Fatal().Err(err).Dur("elapsed", time.Since(startInit)).Msg("failed to create server")
	}
	log.Debug().Dur("elapsed", time.Since(startInit)).Msg("MCP server constructed")

	log.Debug().Msg("registering tool feature groups")
	server.AddEnvironmentFeatures()
	server.AddEnvironmentGroupFeatures()
	server.AddTagFeatures()
	server.AddStackFeatures()
	server.AddLocalStackFeatures()
	server.AddSettingsFeatures()
	server.AddUserFeatures()
	server.AddTeamFeatures()
	server.AddAccessGroupFeatures()
	server.AddDockerProxyFeatures()
	server.AddKubernetesProxyFeatures()
	log.Debug().Msg("all feature groups registered")

	switch resolvedTransport {
	case "sse":
		err = server.StartSSE(resolvedListen)
	case "stdio", "":
		err = server.Start()
	default:
		log.Fatal().Str("transport", resolvedTransport).Msg("unknown transport (use 'stdio' or 'sse')")
	}
	if err != nil {
		log.Fatal().Err(err).Msg("failed to start server")
	}
}

// configureLogging reads LOG_LEVEL (default INFO) and switches zerolog.
// Recognised values: trace, debug, info, warn, error, fatal, panic.
// In debug/trace mode, falls back to a human-readable console writer
// (useful when tailing `docker logs`).
func configureLogging() {
	zerolog.TimeFieldFormat = time.RFC3339
	level := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	if level == "" {
		level = "info"
	}
	parsed, err := zerolog.ParseLevel(level)
	if err != nil {
		parsed = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(parsed)

	if parsed <= zerolog.DebugLevel {
		log.Logger = log.Output(zerolog.ConsoleWriter{
			Out:        os.Stderr,
			TimeFormat: time.RFC3339,
		})
	}
}
