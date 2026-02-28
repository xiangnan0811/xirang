# Xirang (息壤) - Project Context for AI Assistant

## 1. Project Overview
Xirang (息壤) is a centralized backup management platform based on **Rsync**. It is designed to manage multiple VPS nodes, configure backup policies and scheduled tasks, execute backups (manually or automatically), and stream real-time task logs via WebSockets. 

### Key Features:
- Node management and connection testing.
- Backup policy and task scheduling (via Cron).
- Real-time task logging via WebSocket.
- Alerting & notifications (Email, Webhook, Slack, Telegram) with retry mechanisms.
- Audit logging and CSV exports.
- Secure credential management (encrypted SSH keys and passwords).

## 2. Tech Stack & Architecture
- **Backend**: Go (Golang), Gin framework, GORM, JWT auth, `robfig/cron` (scheduling), `gorilla/websocket`.
- **Database**: SQLite (default, `xirang.db`) or PostgreSQL (configurable via `DB_TYPE=postgres`).
- **Frontend**: React 18, TypeScript, Vite, Tailwind CSS, shadcn UI, Lucide Icons.
- **Infrastructure**: Docker & Docker Compose (dev and production with Nginx reverse proxy).

## 3. Directory Structure
- `backend/`: Go source code.
  - `cmd/server/main.go`: Backend entry point.
  - `internal/`: Core application logic (api, auth, middleware, model, task, ws).
- `web/`: Frontend React application.
  - `src/`: React components, pages, hooks, context, and lib utilities.
- `deploy/`: Production deployment configurations (Nginx configs, certs).
- `scripts/`: Shell scripts for database backups, restores, and E2E testing.
- `docs/`: Additional documentation and plans.
- `docker-compose.yml` / `docker-compose.prod.yml`: Docker orchestration files.

## 4. Building and Running

### Prerequisites
For local development, you must set an initial admin password:
```bash
export ADMIN_INITIAL_PASSWORD='your-secure-password'
```

### Quick Start (Using Make)
The project includes a `Makefile` for streamlined development:
- **Run Full Dev Stack (Manual)**: Run `make backend-run` and `make web-dev` in separate terminals.
- **Docker Compose (Dev)**: `docker compose up`

### Backend Manual Commands
```bash
cd backend
go mod tidy
go run ./cmd/server
# Listens on http://localhost:8080 by default
```

### Frontend Manual Commands
```bash
cd web
npm install
npm run dev
# Listens on http://localhost:5173 by default
```

## 5. Testing
The project features comprehensive tests across both ends, including E2E smoke tests.
- **Backend Tests**: `make backend-test` or `cd backend && go test ./...`
- **Frontend Tests**: `make web-test` or `cd web && npm test`
- **Frontend Typecheck & Build**: `make web-build` or `cd web && npm run build`
- **E2E Smoke Test**: `make e2e-check` (requires backend and frontend running, and `ADMIN_PASSWORD` exported).
- **E2E Alert Demo**: `make e2e-alert-demo`

## 6. Development Conventions
- **Code Formatting**: Use standard `gofmt` for backend code (`gofmt -w ./cmd ./internal`). Prettier/ESLint rules apply to the frontend.
- **Authentication**: JWT tokens are used. The first launch creates the `admin` user with the password supplied by the `ADMIN_INITIAL_PASSWORD` environment variable. Additional roles (operator, viewer) must be created manually.
- **Security**: The backend encrypts sensitive node credentials using `DATA_ENCRYPTION_KEY`. Integration endpoints block private networks/loopback addresses by default (configurable).
- **CI/CD**: GitHub Actions are configured in `.github/workflows/ci.yml` triggering on `push` and `pull_request`.
