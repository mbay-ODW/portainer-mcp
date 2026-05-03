# Local Development

A `docker-compose.local.yml` is included in this repo for running the
server on a developer laptop without Traefik, Authelia or TLS. The
container binds to `127.0.0.1` so it is **never** reachable from the
network – auth is intentionally disabled to keep the workflow simple.

## Quick start

```bash
# Configure upstream credentials (DB / API tokens / IMAP …)
cp .env.example .env       # then edit it
$EDITOR .env

# Start the stack on localhost
docker compose -f docker-compose.local.yml up

# Logs
docker compose -f docker-compose.local.yml logs -f
```

The server listens at:

| Repo | URL |
|------|-----|
| `hero-mcp-server`  | http://localhost:8001/sse |
| `mail-mcp`         | http://localhost:8002/sse |
| `paperless-mcp`    | http://localhost:8003/sse |
| `portainer-mcp`    | http://localhost:8004/sse |
| `whatsapp-mcp`     | http://localhost:8005/sse |

(Each repo's `docker-compose.local.yml` only exposes its own port – run
multiple repos in parallel without conflicts.)

## Connecting from Claude Desktop

Edit `~/Library/Application Support/Claude/claude_desktop_config.json`:

```jsonc
{
  "mcpServers": {
    "hero-local": {
      "url": "http://localhost:8001/sse"
    },
    "mail-local":      { "url": "http://localhost:8002/sse" },
    "paperless-local": { "url": "http://localhost:8003/sse" },
    "portainer-local": { "url": "http://localhost:8004/sse" },
    "whatsapp-local":  { "url": "http://localhost:8005/sse" }
  }
}
```

Restart Claude Desktop, the connectors appear in the MCP picker.

## Connecting from MCP Inspector

```bash
npx @modelcontextprotocol/inspector
```
Set `Transport` to `SSE`, URL to e.g. `http://localhost:8001/sse`.

## How auth is disabled in local mode

The server's middleware passes every request through when **all** of
these env vars are unset / empty:

- `MCP_API_KEY`
- `OIDC_INTROSPECTION_URL`
- `OIDC_CLIENT_ID`
- `OIDC_CLIENT_SECRET`

The `docker-compose.local.yml` deliberately does not set them. **Do
not bind the container to a public interface in this mode** – the
`127.0.0.1:PORT:CONTAINERPORT` mapping in the file is the safety belt.

## Switching between local and production deployment

The same image is used in both flows; the difference is purely env
vars + port mapping.

- `docker compose -f docker-compose.local.yml up`     → laptop, no auth
- `docker compose -f docker-compose.yml up -d`        → server, auth via
                                                        Authelia OIDC
