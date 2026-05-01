package main

import (
	"flag"
	"os"

	"github.com/portainer/portainer-mcp/internal/mcp"
	"github.com/portainer/portainer-mcp/internal/tooldef"
	"github.com/rs/zerolog/log"
)

const defaultToolsPath = "tools.yaml"

var (
	Version   string
	BuildDate string
	Commit    string
)

func main() {
	log.Info().
		Str("version", Version).
		Str("build-date", BuildDate).
		Str("commit", Commit).
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

	if resolvedServer == "" || resolvedToken == "" {
		log.Fatal().Msg("server (PORTAINER_URL) and token (PORTAINER_TOKEN) are required")
	}

	toolsPath := *toolsFlag
	if toolsPath == "" {
		toolsPath = defaultToolsPath
	}

	// We first check if the tools.yaml file exists
	// We'll create it from the embedded version if it doesn't exist
	exists, err := tooldef.CreateToolsFileIfNotExists(toolsPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create tools.yaml file")
	}

	if exists {
		log.Info().Msg("using existing tools.yaml file")
	} else {
		log.Info().Msg("created tools.yaml file")
	}

	log.Info().
		Str("portainer-host", resolvedServer).
		Str("tools-path", toolsPath).
		Str("transport", resolvedTransport).
		Bool("read-only", *readOnlyFlag).
		Bool("disable-version-check", *disableVersionCheckFlag).
		Msg("starting MCP server")

	server, err := mcp.NewPortainerMCPServer(resolvedServer, resolvedToken, toolsPath, mcp.WithReadOnly(*readOnlyFlag), mcp.WithDisableVersionCheck(*disableVersionCheckFlag))
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create server")
	}

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
