# Flow System Core Library

`flow-system` is the foundational library for all Flow microservices. it provides standardized configuration management, database connectivity, response structures, and common utilities to ensure consistency across the distributed architecture.

## Installation

To install the package in your Go project:

```bash
go get github.com/factory24/flow-system
```

## Key Components

### 1. Configuration Management (`pkg/config`)
Provides a `BaseConfig` interface and implementation to handle environment variables using `godotenv`. It includes helper methods for:
- Environment detection (Production, Development, Testing)
- Database configuration retrieval
- Sentry and Keycloak settings
- Logging levels and standardized `slog` instances

**Usage:**
```go
import "github.com/factory24/flow-system/pkg/config"

cfg := config.NewBaseConfig()
cfg.LoadEnv()
port := cfg.GetAppPort()
```

### 2. Database Connectivity (`pkg/database`)
A wrapper around GORM that supports both SQLite and PostgreSQL. It includes automatic connection retries with exponential backoff and automated migrations.

**Usage:**
```go
import "github.com/factory24/flow-system/pkg/database"

db := database.NewGormDatabase(cfg, models)
db.Connect()
engine := db.GetEngine()
```

### 3. API Response Structures (`pkg/response`)
Standardizes JSON responses for all microservices.
- `ApiResponse[T]`: Standard success response.
- `ErrorResponse`: Standard error response.
- `Event[T]`: Generic structure for events (Pulsar/ServiceBus).
- `NewCSVExport`: Helper for streaming CSV exports via Echo.

### 4. Middleware (`pkg/middleware`)
Standardized Echo middleware, including logging and recovery, tailored for the Flow ecosystem.

### 5. Shared Models and Utils (`pkg/models`, `pkg/utils`)
Common data structures and helper functions (e.g., string manipulation, validation) used across multiple services.

## Development Mandates

- **Framework**: Use **Echo v4** for all web services.
- **Logging**: Standardized via `slog` and `echo/middleware/logger`.
- **Validation**: Ensure `go build ./...` succeeds before pushing changes.
