# Code style and structure

This document defines the code style and structure patterns to follow for the Orchestrator project, covering both `./backend` and `./agent` directories.

## Architecture Patterns

### Clean Architecture / Hexagonal Architecture

The project follows Clean Architecture principles with clear separation of concerns:

```
domain/          - Pure business logic, no external dependencies
application/     - Use cases, services, ports (interfaces)
infrastructure/  - External adapters (HTTP, gRPC, database, hardware)
```

**Layer Rules:**
- **Domain layer** (`domain/`): Contains pure business entities and logic. NO external dependencies allowed (no database, no HTTP, no gRPC).
- **Application layer** (`application/`): Contains use cases, services, and port interfaces. Can depend on domain only.
- **Infrastructure layer** (`infrastructure/`): Contains implementations of ports. Can depend on application and domain.

### Port/Adapter Pattern

**Ports (Interfaces):**
- `ports/in/` - Incoming ports (commands, queries)
  - `commands/` - Write operations (e.g., `user_register`, `node_create`)
  - `queries/` - Read operations (e.g., `node_get`, `node_list`)
- `ports/out/` - Outgoing ports (repositories, external services)
  - `repository/` - Data access interfaces
  - Other external service interfaces

**Adapters (Implementations):**
- Repository implementations in `infrastructure/database/sql/`
- HTTP handlers in `infrastructure/http_server/handlers/`
- gRPC clients/servers in `infrastructure/grpc/`

### Dependency Injection

- All dependencies are injected via constructors
- Use constructor functions: `NewXService()`, `NewXHandler()`, `NewXRepository()`
- Never use global variables or singletons for dependencies
- Pass context explicitly to all operations that need it

## Naming Conventions

### Packages
- **Lowercase, single word**: `user`, `node`, `account`, `config`
- **No underscores**: Use `userhandler` not `user_handler` (but see file naming below)
- **Descriptive but concise**: `auth`, `grpc`, `http_server`

### Types
- **PascalCase**: `User`, `Node`, `AuthService`, `UserRepository`
- **Interfaces**: End with descriptive name (e.g., `UserService`, `NodeRepository`)
- **Structs**: Use clear domain names (e.g., `User`, `Node`, `HardwareFingerprint`)

### Functions
- **Exported functions**: PascalCase (e.g., `NewUserService`, `GetByID`)
- **Private functions**: camelCase (e.g., `hashPassword`, `validateNode`)
- **Constructor pattern**: Always use `NewX()` format (e.g., `NewUserService()`)

### Files
- **snake_case**: `user_repository.go`, `auth_handler.go`, `command_handler.go`
- **One main type per file**: The file name should match the primary type (e.g., `user_service.go` contains `Service` struct)
- **Platform-specific files**: Use build tags and descriptive suffixes:
  - `command_handler_linux.go` (with `//go:build linux`)
  - `command_handler_darwin.go` (with `//go:build darwin`)
  - `command_handler_windows.go` (with `//go:build windows`)

### Constants
- **PascalCase**: `StatusOnline`, `StatusOffline`, `JWTClaimUserID`
- **Group related constants**: Use `const ()` blocks
- **Status constants**: Use descriptive prefixes (e.g., `Status*`, `Err*`)

### Variables
- **camelCase**: `userID`, `accountID`, `nodeRepo`
- **Short names in small scope**: `ctx`, `err`, `cmd`
- **Descriptive names in larger scope**: `existingUser`, `updatedUser`

## Code Structure Patterns

### Service Layer

**Structure:**
```go
package user

type Service struct {
    userRepo repository.UserRepository
    logger   *slog.Logger
}

func NewUserService(userRepo repository.UserRepository, logger *slog.Logger) *Service {
    return &Service{
        userRepo: userRepo,
        logger:   logger.With("service", "user_service"),
    }
}

func (s *Service) GetByID(ctx context.Context, userID uuid.UUID) (*user.User, error) {
    // Implementation
}
```

**Rules:**
- Service struct contains dependencies (repositories, logger, config)
- Constructor function `NewXService()` initializes with dependency injection
- Logger should be scoped with context: `logger.With("service", "service_name")`
- All methods accept `context.Context` as first parameter
- Return domain entities, not infrastructure models

### Repository Pattern

**Interface (in `ports/repository/`):**
```go
package repository

type UserRepository interface {
    Create(ctx context.Context, user *user.User) error
    GetByID(ctx context.Context, id uuid.UUID) (*user.User, error)
    Update(ctx context.Context, user *user.User) error
    Delete(ctx context.Context, id uuid.UUID) error
}
```

**Implementation (in `infrastructure/database/sql/`):**
```go
package sql

var _ repository.UserRepository = (*UserRepository)(nil)

type UserRepository struct {
    db *gorm.DB
}

func NewUserRepository(db *gorm.DB) *UserRepository {
    return &UserRepository{db: db}
}

func (r *UserRepository) Setup() error {
    return r.autoMigrate()
}
```

**Rules:**
- Always verify interface implementation: `var _ repository.XRepository = (*XRepository)(nil)`
- Include `Setup()` method for database migrations
- Use model conversion: `FromDomain()` and `ToDomain()` methods
- All methods accept `context.Context` as first parameter
- Handle `gorm.ErrRecordNotFound` and convert to domain errors

### HTTP Handler Pattern

**Structure:**
```go
package user

type AuthHandler struct {
    logger      *slog.Logger
    authService services.AuthService
    cfg         *config.Config
}

func NewAuthHandler(logger *slog.Logger, authService services.AuthService, cfg *config.Config) *AuthHandler {
    return &AuthHandler{
        logger:      logger.With("handler", "auth"),
        authService: authService,
        cfg:         cfg,
    }
}

func (h *AuthHandler) Register(e *echo.Echo) {
    auth := e.Group("/auth")
    auth.POST("/signup", h.Signup)
    auth.POST("/login", h.Login)
}

func (h *AuthHandler) Signup(c echo.Context) error {
    var cmd user_register.RegisterCommand
    if err := c.Bind(&cmd); err != nil {
        h.logger.Error("failed to bind signup request", "error", err)
        return results.SendValidationError(c, "invalid request")
    }
    
    if err := cmd.Validate(); err != nil {
        return results.SendValidationError(c, err.Error())
    }
    
    tokens, user, err := h.authService.Register(cmd)
    if err != nil {
        // Error handling with switch on app_errors
        switch err {
        case app_errors.ErrUserAlreadyExists:
            return results.SendError(c, http.StatusConflict, "user already exists", err)
        default:
            return results.SendInternalServerError(c, "registration failed", err)
        }
    }
    
    authResult := results.NewAuthResult(tokens, user)
    return results.SendCreatedResponse(c, authResult, "user registered successfully")
}
```

**Rules:**
- Handler struct contains logger, service, and config dependencies
- Constructor function `NewXHandler()` with dependency injection
- Logger scoped: `logger.With("handler", "handler_name")`
- Route registration in `Register()` or `RegisterRoutes()` method
- **MANDATORY: Always include and update Swagger annotations for all HTTP handler methods**
- Bind request to command/query struct
- Validate command/query before processing
- Use `results` package helpers for responses
- Map domain errors to HTTP status codes using switch statements — EXCEPT a
  typed refusal, which declares its own status on the type by implementing
  `results.OperatorFacingError` (`HTTPStatus()` + `ErrorCode()`). Those are
  raised at one choke point and surface through a dozen handlers, so a
  per-handler arm does not work: the arm that is missing is
  invisible and the refusal reaches the user as a generic 500.

**Swagger Annotation Requirements:**
- Every HTTP handler method MUST have complete Swagger annotations
- Include `@Summary`, `@Description`, `@Tags`, `@Accept`, `@Produce`
- Document all request parameters with `@Param` (body, path, query)
- Document all response codes with `@Success` and `@Failure`
- Include `@Security` tag for protected routes
- Include `@Router` with the route path and HTTP method
- Update annotations when changing request/response structures, status codes, or route paths

### Domain Model Pattern

**Structure:**
```go
package user

type User struct {
    ID             uuid.UUID
    AccountID      uuid.UUID
    Name           string
    Email          string
    HashedPassword string
    CreatedAt      time.Time
    UpdatedAt      time.Time
}

func NewUser(accountID uuid.UUID, email, name, hashedPassword string) *User {
    now := time.Now()
    return &User{
        ID:             uuid.New(),
        AccountID:       accountID,
        Name:           name,
        Email:          email,
        HashedPassword: hashedPassword,
        CreatedAt:      now,
        UpdatedAt:      now,
    }
}

func (u *User) CanRegister() bool {
    // Business logic
}
```

**Rules:**
- Domain entities are pure structs with business logic methods
- Constructor functions: `NewX()` for default creation, `NewXWithID()` if ID needs to be set
- Business logic methods on domain types (e.g., `CanRegister()`, `IsAdopted()`)
- No external dependencies in domain layer
- Use `time.Time` for timestamps, `uuid.UUID` for IDs

### Configuration Pattern

**Structure:**
```go
package config

type Config struct {
    Server   ServerConfig   `yaml:"server"`
    Database DatabaseConfig `yaml:"database"`
    JWT      JWTConfig      `yaml:"jwt"`
}

func LoadConfig(r io.Reader) (*Config, error) {
    cfg := getDefaultConfig()
    decoder := yaml.NewDecoder(r)
    err := decoder.Decode(cfg)
    return cfg, err
}

func Load() (*Config, error) {
    cfg := getDefaultConfig()
    return ApplyEnvironment(cfg)
}

func getDefaultConfig() *Config {
    return &Config{
        Server: ServerConfig{
            Port: 8080,
            Host: "0.0.0.0",
        },
    }
}

func ApplyEnvironment(cfg *Config) (*Config, error) {
    // Override with environment variables
    return cfg, nil
}
```

**Rules:**
- Structured config with nested types
- YAML tags for configuration files
- `LoadConfig()` for file-based config
- `Load()` for environment-only config
- `getDefaultConfig()` provides sensible defaults
- `ApplyEnvironment()` overrides with environment variables
- Use environment variable names like `SERVER_PORT`, `DB_HOST`, etc.

### Platform-Specific Code

**Structure:**
```go
//go:build linux

package handlers

func (h *CommandHandler) handleShutdown(commandID string, cmd *proto.ShutdownCommand) error {
    // Linux-specific implementation
}
```

**Rules:**
- Use build tags: `//go:build linux`, `//go:build darwin`, `//go:build windows`
- Platform-specific implementations in separate files
- Common interface/struct in main file (e.g., `command_handler.go`)
- Platform-specific methods in platform files (e.g., `command_handler_linux.go`)

## Error Handling

### Centralized Errors

**Structure:**
```go
package app_errors

var (
    ErrUserNotFound      = errors.New("user not found")
    ErrAccountNotFound   = errors.New("account not found")
    ErrUnauthorized      = errors.New("unauthorized")
    ErrInvalidCredentials = errors.New("invalid credentials")
)
```

**Rules:**
- Define all application errors in `app_errors` package
- Use `errors.New()` for error definitions
- Group related errors together
- Use descriptive, user-friendly error messages
- Prefix with `Err` (e.g., `ErrUserNotFound`)

### Error Wrapping

**Pattern:**
```go
if err != nil {
    return fmt.Errorf("failed to create user: %w", err)
}
```

**Rules:**
- Always wrap errors with context using `fmt.Errorf("...: %w", err)`
- Provide context about what operation failed
- Use `%w` verb to preserve error chain
- Check for specific errors using `errors.Is()`:
  ```go
  if errors.Is(err, gorm.ErrRecordNotFound) {
      return nil, app_errors.ErrUserNotFound
  }
  ```

### Error Handling in Handlers

**Pattern:**
```go
result, err := h.service.DoSomething(cmd)
if err != nil {
    h.logger.Error("operation failed", "error", err)
    switch err {
    case app_errors.ErrNotFound:
        return results.SendNotFoundError(c, "resource not found")
    case app_errors.ErrUnauthorized:
        return results.SendUnauthorizedError(c, "unauthorized")
    default:
        return results.SendInternalServerError(c, "operation failed", err)
    }
}
```

**Rules:**
- Log errors before handling
- Use switch statements to map domain errors to HTTP status codes; for a typed
  refusal implement `results.OperatorFacingError` instead and let
  `SendInternalServerError` answer it as itself (see the rule above)
- Use `results` package helpers for consistent error responses
- Never expose internal error details to clients (log them instead)

## Logging

### Structured Logging with slog

**Pattern:**
```go
logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
    Level: slog.LevelInfo,
}))

logger.Info("user created", "user_id", user.ID, "email", user.Email)
logger.Error("failed to create user", "error", err, "email", email)
```

**Rules:**
- Use `log/slog` package for structured logging
- Create logger in main with appropriate handler (TextHandler for dev, JSONHandler for prod)
- Scope loggers with context: `logger.With("service", "user_service")`
- Include relevant context in log messages (IDs, emails, etc.)
- Use appropriate log levels:
  - `Debug`: Detailed information for debugging
  - `Info`: General informational messages
  - `Warn`: Warning messages for unusual situations
  - `Error`: Error messages for failures

### Logger Setup

**Pattern:**
```go
func setupLogger() *slog.Logger {
    isDev := os.Getenv("APP_ENV") != "production"
    
    var logLevel slog.Level
    if isDev {
        logLevel = slog.LevelDebug
    } else {
        logLevel = slog.LevelInfo
    }
    
    opts := &slog.HandlerOptions{Level: logLevel}
    
    var handler slog.Handler
    if isDev {
        handler = slog.NewTextHandler(os.Stdout, opts)
    } else {
        handler = slog.NewJSONHandler(os.Stdout, opts)
    }
    
    return slog.New(handler)
}
```

## Context Usage

**Rules:**
- Always pass `context.Context` as the first parameter to functions that need it
- Use context for cancellation, timeouts, and request-scoped values
- Create derived contexts when needed: `context.WithTimeout()`, `context.WithCancel()`
- Check context cancellation in loops: `select { case <-ctx.Done(): return }`
- Never store context in structs; pass it explicitly

**Pattern:**
```go
func (s *Service) GetByID(ctx context.Context, id uuid.UUID) (*User, error) {
    user, err := s.repo.GetByID(ctx, id)
    if err != nil {
        return nil, fmt.Errorf("failed to get user: %w", err)
    }
    return user, nil
}
```

## Response Handling

### Standardized API Responses

**Pattern:**
```go
// Success response
authResult := results.NewAuthResult(tokens, user)
return results.SendCreatedResponse(c, authResult, "user registered successfully")

// Error response
return results.SendValidationError(c, "invalid email format")
return results.SendUnauthorizedError(c, "invalid credentials")
return results.SendNotFoundError(c, "user not found")
return results.SendInternalServerError(c, "registration failed", err)
```

**Rules:**
- Use `results` package helpers for all HTTP responses
- Use appropriate response functions:
  - `SendCreatedResponse()` for 201 Created
  - `SendOKResponse()` for 200 OK
  - `SendNoContentResponse()` for 204 No Content
  - `SendValidationError()` for 400 Bad Request
  - `SendUnauthorizedError()` for 401 Unauthorized
  - `SendForbiddenError()` for 403 Forbidden
  - `SendNotFoundError()` for 404 Not Found
  - `SendInternalServerError()` for 500 Internal Server Error

## Import Organization

**Rules:**
- Group imports: standard library, third-party, local packages
- Use blank lines between groups
- Sort imports alphabetically within groups
- Use aliases for clarity when needed (e.g., `proto`, `nodeDomain`)

**Pattern:**
```go
import (
    "context"
    "fmt"
    "log/slog"
    "time"
    
    "github.com/google/uuid"
    "github.com/labstack/echo/v4"
    "gorm.io/gorm"
    
    "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
    "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)
```

## Testing Patterns

**Rules:**
- Test files end with `_test.go`
- Test functions: `TestX()` for unit tests
- Use table-driven tests for multiple scenarios
- Mock external dependencies using interfaces
- Test files in same package (use `package_test` only when needed)

## gRPC Patterns

### Server Side

**Structure:**
```go
type Server struct {
    proto.UnimplementedNodeCommunicationServer
    config      *config.Config
    logger      *slog.Logger
    nodeRepo    repository.NodeRepository
    connections map[string]*StreamConnection
    mu          sync.RWMutex
}

func NewServer(cfg *config.Config, logger *slog.Logger, nodeRepo repository.NodeRepository) *Server {
    return &Server{
        config:      cfg,
        logger:      logger,
        nodeRepo:    nodeRepo,
        connections: make(map[string]*StreamConnection),
    }
}
```

**Rules:**
- Embed `UnimplementedXServer` from generated proto code
- Use mutexes for concurrent access to shared state
- Handle context cancellation properly
- Use gRPC status codes: `status.Errorf(codes.NotFound, "node not found")`
- Log errors with context before returning

### Client Side

**Structure:**
```go
type PlatformClient struct {
    grpcEndpoint string
    logger       *slog.Logger
    conn         *grpc.ClientConn
    client       proto.NodeCommunicationClient
    stream       proto.NodeCommunication_EstablishStreamClient
    mu           sync.RWMutex
    connected    bool
}

func NewPlatformClient(grpcEndpoint string, logger *slog.Logger, version string) *PlatformClient {
    return &PlatformClient{
        grpcEndpoint: grpcEndpoint,
        logger:       logger,
        version:      version,
        connected:    false,
    }
}
```

**Rules:**
- Use mutexes for thread-safe access
- Implement cleanup methods for connection management
- Handle connection errors gracefully
- Check context cancellation in loops
- Use `errors.Is()` to check for specific gRPC errors

## Command/Query Pattern

### Commands (Write Operations)

**Structure:**
```go
package user_register

type RegisterCommand struct {
    Email    string `json:"email" validate:"required,email"`
    Password string `json:"password" validate:"required,min=8"`
    Name     string `json:"name" validate:"required"`
    InviteCode string `json:"invite_code"`
}

func (c *RegisterCommand) Validate() error {
    if c.Email == "" {
        return app_errors.ErrUserEmailRequired
    }
    // Additional validation
    return nil
}
```

**Rules:**
- Commands in `ports/in/commands/` directory
- Each command in its own subdirectory
- Include `Validate()` method
- Use JSON tags for HTTP binding
- Include `AuthContext` when needed for authorization

### Queries (Read Operations)

**Structure:**
```go
package node_get

type GetNodeQuery struct {
    NodeID      uuid.UUID
    AuthContext commands.AuthContext
}
```

**Rules:**
- Queries in `ports/in/queries/` directory
- Each query in its own subdirectory
- Include `AuthContext` for authorization checks
- Keep queries simple and focused

## API Documentation Maintenance

### Swagger Annotations

**MANDATORY Requirements:**
- All HTTP handler methods MUST have complete Swagger annotations
- When modifying HTTP handlers, you MUST update Swagger annotations to reflect changes
- Swagger annotations must accurately document:
  - Request body structure and validation rules
  - Path parameters
  - Query parameters
  - Response structures for all status codes
  - Authentication requirements
  - Route paths and HTTP methods

**Example:**
```go
// Signup handles user registration
// @Summary Register a new user
// @Description Creates a new user account with email and password
// @Tags auth
// @Accept json
// @Produce json
// @Param user body user_register.RegisterCommand true "User registration data"
// @Success 201 {object} results.AuthResult "User registered successfully"
// @Failure 400 {object} map[string]string "Validation error"
// @Failure 409 {object} map[string]string "User already exists"
// @Failure 500 {object} map[string]string "Internal server error"
// @Router /auth/signup [post]
func (h *AuthHandler) Signup(c echo.Context) error {
    // Implementation
}
```

### Postman Collection Updates

**MANDATORY Requirements:**
- When making ANY changes to the API (routes, request/response structures, status codes, authentication), you MUST update the corresponding Postman collection
- Postman collections are located in `./postman/` directory
- Update collections to reflect:
  - New endpoints or removed endpoints
  - Changed request body structures
  - Changed response structures
  - Changed status codes
  - Changed authentication requirements
  - Updated example requests/responses
- Keep Postman collections in sync with the actual API implementation
- Test Postman collection requests after updating to ensure they work correctly

**Postman Collection Files:**
- `Orchestrator Platform API - Auth, Users & Accounts.postman_collection.json`
- `Orchestrator Platform API - Nodes.postman_collection.json`
- `Orchestrator Platform Environment.postman_environment.json`

## General Best Practices

1. **Always use dependency injection** - Never use global variables or singletons
2. **Keep functions focused** - Single responsibility principle
3. **Use meaningful names** - Code should be self-documenting
4. **Handle errors explicitly** - Never ignore errors
5. **Use context for cancellation** - Especially in long-running operations
6. **Log with context** - Include relevant information in log messages
7. **Verify interface implementations** - Use `var _ Interface = (*Impl)(nil)`
8. **Use constructor functions** - Always provide `NewX()` constructors
9. **Keep domain pure** - No external dependencies in domain layer
10. **Test edge cases** - Especially error conditions and boundary values
11. **Update API documentation** - Always update Swagger annotations and Postman collections when changing APIs

## Project-Specific Patterns

### Backend (`./backend`)

- Package path: `github.com/uptime-lab/orchestrator/backend/pkg/webapp/...`
- Uses Echo framework for HTTP
- Uses GORM for database access
- Uses gRPC for node communication
- **Swagger annotations are MANDATORY for all HTTP handlers** - must be kept up-to-date
- **Postman collections must be updated** when API changes are made (located in `./postman/`)

### Agent (`./agent`)

- Package path: `agent/pkg/...` (relative imports)
- Platform-specific command handlers with build tags
- Hardware fingerprint reading
- gRPC client for platform communication
- Adoption file parsing for configuration

## Backend Architecture - CQRS Pattern

This backend follows the **CQRS (Command Query Responsibility Segregation)** pattern.

### Directory Structure

- **Commands**: `backend/pkg/webapp/application/ports/in/commands/`
  - Commands represent write operations (create, update, delete, actions)
  - Each command is in its own directory with a `*_command.go` file
  - Example: `node_provision_connection/node_provision_connection_command.go`

- **Results**: `backend/pkg/webapp/application/ports/out/results/`
  - Results represent the output/response types for API responses
  - Each result type is in its own file: `*_result.go`
  - Example: `provision_connection_result.go`

- **Queries**: `backend/pkg/webapp/application/ports/in/queries/`
  - Queries represent read operations (get, list, fetch)
  - Each query is in its own directory with a `*_query.go` file
  - Example: `node_get/node_get_query.go`

### Pattern Rules

1. **Commands and Results are separate**: Never put result types in the commands package
2. **Commands go in `/in/commands/`**: Write operations
3. **Results go in `/out/results/`**: Response types for API
4. **Queries go in `/in/queries/`**: Read operations
5. **One command/query per directory**: Each command or query has its own subdirectory
6. **One result per file**: Each result type is in its own file in the results package

### Interface Rules

1. **NO anonymous interfaces**: Always define named interfaces in the appropriate ports package
2. **Interfaces must be complete**: Include all methods that will be called, not just a subset
3. **No nil checks for required dependencies**: Set all required dependencies in the constructor and panic if nil
4. **Use strict interfaces**: Rely on the interface contract, not type assertions or nil checks
