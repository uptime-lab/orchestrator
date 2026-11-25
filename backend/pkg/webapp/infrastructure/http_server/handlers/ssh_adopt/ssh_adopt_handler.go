package ssh_adopt

import (
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_ssh_adopt"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_ssh_preflight"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	node_ssh_adopt_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_ssh_adopt"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// Handler serves the SSH-adopt endpoints.
type Handler struct {
	service *node_ssh_adopt_service.Service
	logger  *slog.Logger
}

// NewHandler constructs the SSH-adopt handler.
func NewHandler(service *node_ssh_adopt_service.Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger.With("handler", "ssh_adopt")}
}

// RegisterRoutes registers SSH-adopt routes. Adopting a device is account-scoped
// (no node exists yet), so it is gated by the account-level ActionNodeSSHAdopt.
func (h *Handler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	g := e.Group("/api/v1/nodes")
	g.Use(authMiddleware)
	adopt := middleware.RequireAction(enforcer, authz.ActionNodeSSHAdopt)
	g.POST("/ssh-preflight", h.Preflight, adopt)
	g.POST("/ssh-adopt", h.Adopt, adopt)

	jobs := e.Group("/api/v1/node-adoption-jobs")
	jobs.Use(authMiddleware)
	jobs.GET("/:id", h.GetJob, adopt)
}

// Adopt starts an asynchronous SSH device adoption and returns the job.
// @Summary Adopt a device over SSH
// @Description Installs the agent on a reachable device over SSH, registers a node, waits for it to connect and (optionally) flashes a network-first EEPROM. Returns a job immediately; poll GET /node-adoption-jobs/:id or subscribe to WebSocket "node_adoption_job_updated" for progress. Installing the agent and flashing the EEPROM reboot the device and can be destructive. For a device that is not registered yet, only node_name is required: default_username/default_password are that node's OPTIONAL console login, not the credentials this adoption connects with, and managed access comes from access entries once the agent reports in. The adoption never accepts a host key on first use: send exactly one of host_key_ticket (the value a reachable preflight returned within the last 15 minutes, bound to your account, user, host, port and the key the preflight saw) or host_key_fingerprint (the device's SHA256 host key, known out of band). The connection is then strict against that key and a different key fails before any credential is offered. Neither, an expired ticket, or a ticket issued for another host, port, account or user is refused with 400 (code host_key_binding_required) before any connection; run the preflight again and adopt with its ticket. Adoptions are bounded to four per server and two per account; one more is refused with 429 (code adoption_capacity) and creates no job.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param adopt body node_ssh_adopt.SSHAdoptCommand true "SSH adoption parameters"
// @Success 202 {object} results.APIResponse{data=results.NodeAdoptionJobResult} "Adoption job started"
// @Failure 400 {object} results.APIResponse "Validation error, or host_key_binding_required (no ticket or fingerprint, or a ticket that does not verify for this request)"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 409 {object} results.APIResponse "Refused before any job starts: the install is at its device cap (code device_cap_reached), or the account at its own device limit (code account_device_limit_reached; data: limit, account_devices, plan, until for a dated subscription, contact)"
// @Failure 429 {object} results.APIResponse "No adoption slot free for this server or this account (adoption_capacity); wait and retry"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Failure 503 {object} results.APIResponse "The device cap could not be checked (code device_cap_unverified): nothing was written; try again"
// @Router /api/v1/nodes/ssh-adopt [post]
func (h *Handler) Adopt(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	var cmd node_ssh_adopt.SSHAdoptCommand
	if err := c.Bind(&cmd); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)
	if err := cmd.Validate(); err != nil {
		return results.SendValidationError(c, err.Error())
	}

	job, err := h.service.StartAdoption(c.Request().Context(), cmd)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to start ssh adoption", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to start ssh adoption", err)
	}
	return results.SendSuccess(c, 202, results.NewNodeAdoptionJobResult(job), "adoption job started")
}

// GetJob returns an adoption job's current state.
// @Summary Get an SSH adoption job
// @Description Returns the current state + per-step progress of an SSH adoption run.
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Job ID"
// @Success 200 {object} results.APIResponse{data=results.NodeAdoptionJobResult} "Adoption job"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Job not found"
// @Router /api/v1/node-adoption-jobs/{id} [get]
func (h *Handler) GetJob(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	jobID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid job ID")
	}
	job, err := h.service.GetJob(c.Request().Context(), accountID, jobID)
	if err != nil {
		if errors.Is(err, app_errors.ErrNodeAdoptionJobNotFound) {
			return results.SendNotFoundError(c, "adoption job not found")
		}
		return results.SendInternalServerError(c, "failed to load job", err)
	}
	return results.SendOKResponse(c, results.NewNodeAdoptionJobResult(job), "adoption job")
}

// Preflight runs a non-destructive SSH connectivity/capability probe.
// @Summary Preflight an SSH device adoption
// @Description Non-destructively probes a reachable device over SSH: verifies connectivity + auth, captures the host-key fingerprint (SHA256), and reports architecture, sudo mode and whether the device can reach the agent CDN. Writes nothing and does not reboot. Use before committing to adoption. A reachable probe also returns host_key_ticket (valid 15 minutes, until host_key_ticket_expires_at; a backend restart invalidates it too), the proof the adoption must present as host_key_ticket. A probe with no expected_host_key_fingerprint takes the key on first use and answers first_contact=true. Send expected_host_key_fingerprint (a value an earlier preflight reported) to check the device still presents that key: a different key answers 200 with host_key_changed=true, both fingerprints and no ticket, and the handshake stops before any credential is offered. To accept a device's new key after a reinstall, repeat the probe with the same expectation and accept_new_host_key=true: the result then carries host_key_reset=true, a ticket for the new key, and the audit trail records both fingerprints. A server with host_key_policy=strict refuses a probe without an expectation (400, code host_key_fingerprint_required). Probes are bounded to eight per server and four per account; one more is refused with 429 (code preflight_capacity) before any lookup or connection.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param preflight body node_ssh_preflight.SSHPreflightCommand true "SSH connection parameters"
// @Success 200 {object} results.APIResponse{data=results.SSHPreflightResult} "Preflight results"
// @Failure 400 {object} results.APIResponse "Validation error, policy refusal, or host_key_fingerprint_required on a strict server"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 429 {object} results.APIResponse "No connection-test slot free for this server or this account (preflight_capacity); wait and retry"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/ssh-preflight [post]
func (h *Handler) Preflight(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	var cmd node_ssh_preflight.SSHPreflightCommand
	if err := c.Bind(&cmd); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	if err := cmd.Validate(); err != nil {
		return results.SendValidationError(c, err.Error())
	}

	result, err := h.service.Preflight(c.Request().Context(), cmd)
	if err != nil {
		h.logger.Warn("ssh preflight rejected", "error", err, "account_id", accountID)
		// A typed refusal declares its own status and code (a 429 capacity
		// refusal, the strict-policy 400); flattening it into the catch-all
		// below would lose the code the UI keys on.
		var facing results.OperatorFacingError
		if errors.As(err, &facing) {
			return results.SendInternalServerError(c, "ssh preflight rejected", err)
		}
		// The remaining preflight errors are policy/config rejections with
		// actionable text (feature disabled, host outside allowed subnets,
		// agent auth off) — surface the message as a 400 instead of a
		// generic 500.
		return results.SendValidationError(c, err.Error())
	}
	return results.SendOKResponse(c, result, "preflight complete")
}
