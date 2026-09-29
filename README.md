# Nx Caching Server

A custom remote caching server for [Nx](https://nx.dev), written in Go.  
This implementation follows the Nx OpenAPI specification (v20.8+) and provides a lightweight, self-hosted remote cache solution.

Since Nx 20.8, you can use your own caching server by implementing their OpenAPI spec.  
This server handles storage, retrieval, and optional bearer-token authentication.

## Configuration

Configure the server using environment variables:

| Variable             | Description                                          | Default              | Example values       |
|----------------------|------------------------------------------------------|----------------------|----------------------|
| `STORAGE_DIR`        | Directory for storing cache artifacts                | System temp dir      | `/data/nx-cache`     |
| `CLEANUP_THRESHOLD`  | Duration after which unused cache entries are removed (hours only) | `1h`                 | `1h`, `24h`, `168h`  |
| `PORT`               | Port the server listens on                           | `8090`               | `8080`               |
| `AUTH_TOKEN`         | Bearer token required for all requests (optional)     | (unset = no auth)    | `my-secure-token`    |
| `AUTH_TOKENS`        | Comma-separated `name:token` pairs, one per repository; each token only sees `STORAGE_DIR/<name>/` (optional) | (unset) | `repoA:tokA,repoB:tokB` |

**Note:** `CLEANUP_THRESHOLD` uses hour-based durations (e.g. `24h` = 1 day, `168h` = 1 week).

**Note:** `AUTH_TOKEN` and `AUTH_TOKENS` cannot be combined; the server exits at startup if both are set.

## Getting Started

### Running Locally (from source)

1. Build the binary:

   ```bash
   go build -o nx-caching-server ./main.go
   ```

2. Run it:

   ```bash
   ./nx-caching-server
   ```

   With custom settings:

   ```bash
   STORAGE_DIR=./cache-data \
   PORT=8090 \
   AUTH_TOKEN=super-secret-123 \
   CLEANUP_THRESHOLD=24h \
   ./nx-caching-server
   ```

### Using Docker (published image)

Pull the pre-built image from Docker Hub:

```bash
docker pull enxtur/nx-caching-server
```

Run example (with persistent storage and authentication):

```bash
docker run -d \
  --name nx-cache \
  -p 8090:8090 \
  -v $(pwd)/nx-cache-data:/data \
  -e STORAGE_DIR=/data \
  -e AUTH_TOKEN=your-secure-token-here \
  -e CLEANUP_THRESHOLD=48h \
  enxtur/nx-caching-server
```

Or build your own image from source (if preferred):

```bash
docker build -t nx-caching-server .
```

### Using Docker Compose

Create a `docker-compose.yml` file:

```yaml
version: '3.8'

services:
  nx-cache:
    image: enxtur/nx-caching-server:latest
    container_name: nx-caching-server
    restart: unless-stopped
    ports:
      - "8090:8090"
    volumes:
      - ./nx-cache-data:/data
    environment:
      - STORAGE_DIR=/data
      - PORT=8090
      - AUTH_TOKEN=your-secure-token-here          # remove this line if you want no authentication
      - CLEANUP_THRESHOLD=24h
```

Start it:

```bash
docker compose up -d
```

### Multiple repositories

Several repositories can share one server while keeping their caches separate. Give each repository its own name and token with `AUTH_TOKENS`:

```yaml
services:
  nx-cache:
    image: enxtur/nx-caching-server:latest
    restart: unless-stopped
    ports:
      - "8090:8090"
    volumes:
      - ./nx-cache-data:/data
    environment:
      - STORAGE_DIR=/data
      - AUTH_TOKENS=web-app:token-for-web-app,api:token-for-api
      - CLEANUP_THRESHOLD=24h
```

Each token can only read and write its own subdirectory:

```
nx-cache-data/
├── web-app/
│   └── <hash>.cache
└── api/
    └── <hash>.cache
```

- Names may contain only letters, digits, `_` and `-`. Tokens may contain `:`; only the first `:` separates the name from the token.
- Names and tokens must be unique.
- Each repository sets its own token in `NX_SELF_HOSTED_REMOTE_CACHE_ACCESS_TOKEN`; no other Nx changes are needed.
- Switching from `AUTH_TOKEN` to `AUTH_TOKENS` moves cache files into per-name subdirectories, so Nx misses the cache once and rebuilds. The old flat files expire through normal cleanup.

## Configuring Nx Workspace

Point your Nx workspace to the caching server by setting these environment variables (or add them to `.env` / CI config):

```bash
# No authentication
export NX_SELF_HOSTED_REMOTE_CACHE_SERVER=http://localhost:8090

# With authentication
export NX_SELF_HOSTED_REMOTE_CACHE_SERVER=http://localhost:8090
export NX_SELF_HOSTED_REMOTE_CACHE_ACCESS_TOKEN=your-secure-token-here
```

Then run your tasks as usual:

```bash
npx nx run-many --target=build --all
```

Nx will automatically read from / write to your self-hosted cache.

## Contributing

Contributions, bug reports, and feature suggestions are welcome!  
Feel free to open an issue or submit a pull request.