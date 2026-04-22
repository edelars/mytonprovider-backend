# mytonprovider-backend

**[Русская версия](README.ru.md)**

Backend service for mytonprovider.org - a TON Storage providers monitoring service.

## Description

This backend service:
- Communicates with storage providers via ADNL protocol
- Monitors provider performance, availability, do health checks
- Handles telemetry data from providers
- Provides API endpoints for frontend
- Computes provider ratings
- Collect own metrics via **Prometheus**

## Production Server Setup

The automated install scripts target a clean Debian 12 server with root access.

1. Download the SSH bootstrap script on your local machine:

```bash
wget https://raw.githubusercontent.com/dearjohndoe/mytonprovider-backend/refs/heads/master/scripts/init_server_connection.sh
```

2. Copy your SSH key and disable password access:

```bash
USERNAME=root PASSWORD=supersecretpassword HOST=123.45.67.89 bash init_server_connection.sh
```

3. Log into the server and fetch the installer:

```bash
ssh root@123.45.67.89
wget https://raw.githubusercontent.com/dearjohndoe/mytonprovider-backend/refs/heads/master/scripts/setup_server.sh
```

4. Run the full setup:

```bash
PG_USER=appuser PG_PASSWORD=secret PG_DB=providerdb NEWFRONTENDUSER=jdfront NEWSUDOUSER=johndoe NEWUSER_PASSWORD=newsecurepassword bash ./setup_server.sh
```

Notes:
- `PG_USER` is no longer required to be `pguser`; the DB init script templates schema ownership from the env var.
- PostgreSQL is configured for local access by default; the backend connects to `127.0.0.1:5432`.
- `build_frontend.sh` no longer patches frontend source code. If you need an absolute API URL during frontend build, pass `FRONTEND_API_BASE_URL=...`.

## Local Development

### Prerequisites

- Docker and Docker Compose plugin

### 1. Start PostgreSQL

```bash
docker compose up -d postgres
```

### 2. Initialize the database schema

```bash
docker compose run --rm db-init
```

### 3. Run the backend in Docker

```bash
docker compose up backend
```

If you changed the compose file or hit a stale backend container, recreate the service:

```bash
docker compose rm -sf backend
docker compose up --force-recreate backend
```

Useful local endpoints:
- `http://localhost:9090/health`
- `http://localhost:9090/api/v1/providers/filters`
- `http://localhost:9090/metrics`

### Full local stack with frontend

To run PostgreSQL, backend, and frontend together:

```bash
docker compose up -d postgres
docker compose run --rm db-init
docker compose -f docker-compose.yml -f docker-compose.full.yml up backend frontend
```

The frontend will be available at `http://localhost:3000` and uses `http://backend:9090` inside Docker.

If the frontend container previously failed during dependency installation, recreate its volumes before retrying:

```bash
docker compose -f docker-compose.yml -f docker-compose.full.yml down -v
```

### Host-run alternative

If you want to run Go directly on the host instead of in Docker:

```bash
cp .env.example .env
bash scripts/init_local_db.sh
bash scripts/dev_backend.sh
```

This path uses the local PostgreSQL port published by Docker and runs `go run -tags=debug ./cmd` so the app handles CORS and `OPTIONS` requests without nginx.

### VS Code

If you prefer VS Code, use the same env values from `.env` and keep `buildFlags: "-tags=debug"`.

## Project Structure

```
├── cmd/                   # Application entry point, configs, inits
├── pkg/                   # Application packages
│   ├── cache/             # Custom cache
│   ├── httpServer/        # Fiber server handlers
│   ├── models/            # DB and API data models
│   ├── repositories/      # All work with postgres here
│   ├── services/          # Business logic
│   ├── tonclient/         # TON blockchain client, wrap some usefull functions
│   └── workers/           # Workers
├── db/                    # Database schema
├── scripts/               # Setup and utility scripts
```

## API Endpoints

The server provides REST API endpoints for:
- Telemetry data collection
- Provider info and filters tool
- Metrics

## Workers

The application runs several background workers:
- **Providers Master**: Manages provider lifecycle and health checks
- **Telemetry Worker**: Processes incoming telemetry data
- **Cleaner Worker**: Maintains database hygiene and cleanup

## License
 
Apache-2.0



This project was created by order of a TON Foundation community member.
