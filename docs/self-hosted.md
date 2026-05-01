# Self-hosted Deployment

This document explains how to run `portainer-mcp` as a long-running
service exposed over Server-Sent Events behind **Traefik** with
**Authelia** as the OIDC identity provider, so that
[Claude.ai](https://claude.ai) can connect to it as an MCP connector.

The setup mirrors the pattern used by sibling repos (`hero-mcp`,
`mail-mcp`, `paperless-mcp`) so a single Authelia instance can protect
all of them.

## Architecture

```
                  ┌────────────────────────────────────────────┐
                  │  https://portainer-mcp.example.com         │
                  │  (Traefik, public TLS)                     │
                  ├────────────────────────────────────────────┤
                  │  /sse, /messages/   ──► portainer-mcp:8000 │
                  │  /.well-known/*     ──► authelia:9091      │
                  │  /api/oidc/*        ──► authelia:9091      │
                  │  /authorize, /consent, /static, /  ──► …   │
                  └────────────────────────────────────────────┘
                              │                 │
                              ▼                 ▼
                  ┌────────────────────┐  ┌──────────────┐
                  │ portainer-mcp      │  │ authelia     │
                  │ (this repo)        │  │ (OIDC + 2FA) │
                  │  introspects token │◄─┤              │
                  │  via Authelia      │  └──────────────┘
                  └────────┬───────────┘
                           │
                           ▼
                  ┌────────────────────┐
                  │ portainer:9000     │
                  │ (managed instance) │
                  └────────────────────┘
```

Two roles for Authelia, both reachable under the MCP host:

* **Authorization server** – Claude.ai redirects the user to
  `/authorize`, `/api/oidc/*`, etc., for the OAuth Authorization Code
  flow with PKCE.
* **Token introspection** (RFC 7662) – `portainer-mcp` itself calls
  `/api/oidc/introspection` for every incoming Bearer token to check
  `active=true` before serving SSE.

The MCP server intentionally does **not** publish RFC 9728 / RFC 8414
discovery documents on its own – it relies on Traefik to expose
Authelia's documents under the MCP host instead. This matches the rest
of the stack and avoids a class of subtle "wrong URL in metadata"
issues when Authelia is reached via different hostnames internally and
externally.

## Image

GitHub Actions builds and pushes the image on every push to `main`:

```
ghcr.io/mbay-odw/portainer-mcp:latest
ghcr.io/mbay-odw/portainer-mcp:<short-sha>
```

The Dockerfile is a multi-stage Go build, runs as a non-root user, and
defaults to:

```
ENV MCP_TRANSPORT=sse PORT=8000
CMD ["-disable-version-check"]
```

`-disable-version-check` is enabled by default because the upstream
Portainer-version pin is quite strict; remove it from the Compose
`command:` if you want strict matching.

## Required environment variables

| Variable | Required | Description |
|---|---|---|
| `DOMAIN` | yes (compose) | Public domain – the MCP host becomes `portainer-mcp.${DOMAIN}` |
| `PORTAINER_URL` | yes | Internal URL of your Portainer instance, e.g. `http://portainer:9000` |
| `PORTAINER_TOKEN` | yes | Portainer **admin** API access token |
| `MCP_TRANSPORT` | no (default `sse`) | `sse` or `stdio` |
| `PORT` | no (default `8000`) | TCP port the SSE server binds to |
| `MCP_API_KEY` | no | Static fallback Bearer token (Claude Desktop direct connect) |
| `OIDC_INTROSPECTION_URL` | yes for OAuth | e.g. `http://authelia:9091/api/oidc/introspection` |
| `OIDC_CLIENT_ID` | yes for OAuth | Client id registered in Authelia (default `portainer-mcp`) |
| `OIDC_CLIENT_SECRET` | yes for OAuth | **Plaintext** of the secret whose bcrypt hash is stored in Authelia |
| `LOG_LEVEL` | no | `trace`, `debug`, `info` (default), `warn`, `error` |

If neither `MCP_API_KEY` nor a complete OIDC triple is configured the
server falls open and accepts every request – do **not** run that mode
in production.

## Authelia client

```yaml
identity_providers:
  oidc:
    clients:
      - client_id: portainer-mcp
        client_name: Claude Portainer MCP
        authorization_policy: one_factor
        client_secret: $2b$12$REPLACE_ME           # bcrypt hash of plaintext
        redirect_uris:
          - https://claude.ai/api/mcp/auth_callback
        scopes: [openid, profile, email, offline_access, address, phone, groups]
        grant_types: [authorization_code, refresh_token]
        response_types: [code]
        token_endpoint_auth_method: client_secret_post
        introspection_endpoint_auth_method: client_secret_basic
```

Generate the bcrypt hash:

```bash
docker run --rm authelia/authelia:latest \
  authelia crypto hash generate bcrypt --password 'your-plaintext-secret'
```

The plaintext goes into `OIDC_CLIENT_SECRET` on the MCP container; the
hash goes into `client_secret` in Authelia's config.

## Traefik wiring

### 1. Container labels (`docker-compose.yml`)

Already in [`docker-compose.yml`](../docker-compose.yml):

* `Host(`portainer-mcp.${DOMAIN}`) && PathPrefix(`/sse`)` → SSE
* `Host(`portainer-mcp.${DOMAIN}`) && PathPrefix(`/messages`)` → POST messages

Both use a shared `portainer-mcp-svc` LB pointing at the container's
`8000/tcp`.

### 2. File-provider config

Drop [`traefik/portainer-mcp-oauth.yml`](../traefik/portainer-mcp-oauth.yml)
into Traefik's file-provider directory (typically
`/etc/traefik/dynamic/`). Update every `Host(`portainer-mcp.example.com`)`
rule to your actual host.

The file defines:

* `portainer-mcp-authorize` – rewrites `/authorize` to
  `/api/oidc/authorization` and forwards to Authelia.
* `portainer-mcp-{root,api,oidc,consent,static,wellknown}` – pass-through
  routers that send the matching path on the MCP host to Authelia.
* `authelia-oidc` service – shared LB pointing at `http://authelia:9091`.

> **Heads up:** if you already define `authelia-oidc` in another file
> (e.g. for `hero-mcp`), drop the `services` block from one of them, or
> Traefik will log "service already defined" warnings on startup.

### 3. Networks

The MCP container needs to be on a network that:

* Traefik can reach (`traefik`)
* Portainer can be reached on (`portainer_default` or wherever your
  Portainer container lives)

Compose snippet:

```yaml
networks:
  - traefik
  - portainer

networks:
  traefik:
    external: true
  portainer:
    external: true
    name: portainer_default
```

Authelia must also be reachable from the MCP container's network so
that introspection requests succeed; it usually lives on the `traefik`
network.

## Claude.ai connector

1. Settings → Connectors → Add custom connector.
2. URL: `https://portainer-mcp.${DOMAIN}/sse`
3. Submit. Claude.ai discovers Authelia (via the Traefik-mirrored
   `/.well-known/oauth-protected-resource`), redirects you to your IdP,
   you log in, Authelia issues a code, Claude.ai exchanges it for a
   Bearer token, then connects to `/sse` with that token. Subsequent
   tool calls travel over POSTs to `/messages/{session_id}`.

## Debugging

Set `LOG_LEVEL=debug` and tail logs:

```bash
docker logs -f portainer-mcp
```

You'll see:

* Resolved configuration (with token lengths, never values)
* Initialisation timing for the Portainer client + version probe
* Per-request: method, path, masked Authorization header
* OIDC introspection: HTTP status, sub, scope on success – or the
  exact reason for a 401

Common failure modes:

| Symptom | Likely cause |
|---|---|
| Container restart loop with `permission denied` writing tools.yaml | You're on an old image without the `chown -R mcp:mcp /app` fix; pull `:latest` again. |
| `failed to get Portainer server version` | `PORTAINER_URL` unreachable from the container, or token wrong. |
| Claude.ai shows `invalid_request_error: code: Field required` | OAuth discovery is talking to the wrong host. Check your Traefik file-provider rules and that Authelia is reachable on `:9091` from Traefik. Also verify the OIDC client is registered in Authelia and Authelia has been restarted. |
| `OIDC token not active – denying` in logs | Token expired, wrong audience, or LDAP/user backend down behind Authelia. |

## Tools available

All upstream tool groups are registered:

* `Environment`, `EnvironmentGroup`, `Tag`
* `Stack` (Edge stacks), `LocalStack` (regular Docker Compose stacks: list/get/create/update/start/stop/delete)
* `Settings`, `User`, `Team`, `AccessGroup`
* `DockerProxy` – full Docker API access (image pull, container restart, network create, …)
* `KubernetesProxy` – full Kubernetes API access for managed clusters

If you only want a read-only deployment, add `-read-only` to the
container `command:` in `docker-compose.yml`.
