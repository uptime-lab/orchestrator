package http_server

import (
	"log/slog"

	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/service"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	access_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/access"
	account_export_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/account_export"
	audit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/eeprom_provision"
	email_verification_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/email_verification"
	entitlement_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/entitlement"
	master_token_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/master_token"
	member_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/member"
	node_activity_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_activity"
	node_attention_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_attention"
	node_favorite_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_favorite"
	node_replace_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_replace"
	nodeSSHAdoptService "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_ssh_adopt"
	organizational_unit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/organizational_unit"
	passkey_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/passkey"
	password_reset_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/password_reset"
	reauth_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/reauth"
	secret_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/secret"
	secret_tag_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/secret_tag"
	ssh_key_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/ssh_key"
	two_factor_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/two_factor"
	utility_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/utility"
	workflow_execution_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/workflow_execution"
	workflow_template_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/workflow_template"
	oidcapi "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/oidc"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/eepromsigner"
	accessEntryHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/access_entry"
	accountHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/account"
	adoptionHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/adoption"
	auditHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/audit"
	authzHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/authz"
	bootOptionsHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/boot_options"
	cdnHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/cdn"
	emailVerificationHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/email_verification"
	entitlementHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/entitlement"
	fleetHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/fleet"
	httpBootHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/httpboot"
	imageHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/image"
	masterTokenHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/master_token"
	memberHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/member"
	nodeHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/node"
	nodeFavoriteHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/node_favorite"
	nodeReplaceHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/node_replace"
	oidcHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/oidc"
	organizationalUnitHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/organizational_unit"
	passkeyHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/passkey"
	passwordResetHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/password_reset"
	provisioningHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/provisioning"
	reauthHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/reauth"
	secretHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/secret"
	serverInfoHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/server_info"
	sourceOfferHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/source_offer"
	sshAdoptHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/ssh_adopt"
	sshKeyHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/ssh_key"
	swaggerHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/swagger"
	terminalHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/terminal"
	twoFactorHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/two_factor"
	userHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/user"
	utilityHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/utility"
	websocketHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/websockets"
	workflowHandler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/workflow"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/websockets"
)

// SetupRoutes configures all application routes
func SetupRoutes(
	e *echo.Echo,
	cfg *config.Config,
	authService services.AuthService,
	userSvc services.UserService,
	accountSvc services.AccountService,
	nodeSvc services.NodeService,
	imageSvc services.ImageService,
	nodeFileSvc services.FileService,
	nodeCommandSvc services.NodeCommandService,
	sshKeySvc *ssh_key_service.Service,
	masterTokenSvc *master_token_service.Service,
	organizationalUnitSvc *organizational_unit_service.Service,
	accessSvc *access_service.Service,
	utilitySvc *utility_service.Service,
	nodeFavoriteSvc *node_favorite_service.Service,
	secretSvc *secret_service.Service,
	secretTagSvc *secret_tag_service.Service,
	nodeReplaceSvc *node_replace_service.Service,
	memberSvc *member_service.Service,
	authzEnforcer *authz_service.Enforcer,
	oidcClient *oidcapi.Client,
	storageClient storage.StorageClient,
	communicationService service.NodeCommunicationService,
	wsManager *websockets.WebSocketManager,
	workflowTemplateSvc *workflow_template_service.Service,
	workflowExecutionSvc *workflow_execution_service.Service,
	sshAdoptSvc *nodeSSHAdoptService.Service,
	emailVerificationSvc *email_verification_service.Service,
	passwordResetSvc *password_reset_service.Service,
	twoFactorSvc *two_factor_service.Service,
	twoFactorPolicy *two_factor_service.Policy,
	passkeySvc *passkey_service.Service,
	reauthSvc *reauth_service.Service,
	auditRecorder *audit_service.Recorder,
	auditReader *audit_service.Reader,
	accountDeletionRequestRepo repository.AccountDeletionRequestRepository,
	eepromSignerClient *eepromsigner.Client,
	eepromBaseCache *eepromsigner.BaseCache,
	nodeAttentionSvc *node_attention_service.Service,
	nodeActivityReader *node_activity_service.Reader,
	entitlementSvc *entitlement_service.Service,
	logger *slog.Logger,
) {
	// Auth routes (no middleware)
	authHandler := userHandler.NewAuthHandler(logger, authService, cfg)
	authHandler.Register(e, middleware.JWTAuth(cfg.JWT))

	// Step-up confirmation. Guards the routes that change how an account is
	// signed into, so a hijacked session cannot bind its own authenticator —
	// which a later password reset would NOT remove.
	//
	// Constructed HERE, above the OIDC registration, because attaching a
	// federated identity is one of those routes and used to be registered before
	// this line existed.
	recentAuth := middleware.RequireRecentAuth(reauthSvc, logger)

	// JWT middleware for protected routes, with the 2FA enrollment gate composed
	// INSIDE it: on an account that mandates a second factor, a member without
	// one reaches only the enrollment endpoints.
	//
	// The composition is deliberate and load-bearing. Registering the gate with
	// e.Use() does NOT work: Echo applies e.Use middleware to the
	// router-resolved handler, i.e. BEFORE any group/route middleware, so the
	// gate would run ahead of JWTAuth, find no account in the context, and take
	// its fail-open path on every single request — enforcing nothing at all.
	//
	// The per-account API rate limit is composed here for exactly the same
	// reason, and this is the ONE place it can go: every authenticated route in
	// the application uses jwtMiddleware, so wrapping it here covers the whole
	// authenticated surface at a single point, and it sits INSIDE rawJWT so the
	// account id is on the context by the time it runs. Registered with e.Use it
	// would find no account and degrade to one global bucket shared by every
	// tenant — which looks like it works and is worse than nothing.
	//
	// Order within the chain: rate limit BEFORE the 2FA gate, because the gate
	// reads the database and a limiter that only engages after a query has
	// already run does not bound the work it was meant to bound.
	//
	// The open-signup gate is composed here for the same reason, and ahead of
	// the 2FA gate: an account created through open registration is usable
	// for nothing until its owner confirms their address, on every
	// authenticated route at once. It sits after the rate limit, because it
	// may read the database.
	rawJWT := middleware.JWTAuth(cfg.JWT)
	twoFactorGate := middleware.RequireTwoFactorEnrollment(twoFactorPolicy, logger)
	openSignupGate := middleware.RequireSettledOpenSignup(authService, logger)
	apiRateLimit := middleware.NewAccountRateLimiter(
		cfg.Quota.APIRequestsPerSecond, cfg.Quota.APIBurst, logger).Middleware()
	jwtMiddleware := func(next echo.HandlerFunc) echo.HandlerFunc {
		return rawJWT(apiRateLimit(openSignupGate(twoFactorGate(next))))
	}

	// OIDC login routes (only when a provider was discovered at startup).
	if oidcClient != nil {
		oidcHdlr := oidcHandler.NewHandler(oidcClient, authService, cfg.OIDC, cfg.Server.InviteCode, cfg.Server.OpenRegistration, logger)
		// Step-up through the IdP: the only proof a federated user with no local
		// credential can make, and without it they were exempt from the
		// credential-change guard entirely.
		oidcHdlr.SetStepUp(reauthSvc)
		// Attaching and removing a federated identity both change who can sign
		// in, so both belong in the trail.
		oidcHdlr.SetAuditRecorder(auditRecorder)
		// Through the COMPOSED middleware, not a raw JWTAuth. This group was the
		// one authenticated surface the 2FA mandate did not reach, so a member
		// who refused to enrol could still attach a federated identity — a way
		// to sign in that skips the TOTP gate by design, i.e. exactly the
		// requirement they were declining.
		//
		// It only works because the exempt list gained `/auth/oidc/reauth`
		// FIRST; that entry and this line must ship together, in this order.
		// Registered after jwtMiddleware is built for the same reason.
		oidcHdlr.Register(e, jwtMiddleware, recentAuth)
	}

	// Deployment capabilities + one-command installer (the per-install capability split).
	// The installer downloads the agent from the public agent CDN, so this must
	// track exactly which backends that CDN serves — see storage.ServedThroughCDN.
	agentCDNAvailable := storageClient != nil && storage.ServedThroughCDN(storageClient.GetStorageType())
	serverInfoHdlr := serverInfoHandler.NewHandler(cfg, agentCDNAvailable, logger)
	// The sign-in page's registration line asks the same policy the
	// registration enforces; the erasure wording follows the install kind
	// the entitlement in force names.
	serverInfoHdlr.SetRegistration(authService)
	serverInfoHdlr.SetInstallKind(entitlementSvc)
	// The same condition the OIDC routes above are registered under, so the
	// sign-in page asks for the provider only where it is served.
	serverInfoHdlr.SetSingleSignOn(oidcClient != nil)
	serverInfoHdlr.RegisterRoutes(e, jwtMiddleware)

	// The corresponding-source offer for the recovery image (GPLv2 section 3):
	// GET /source/ and GET /source/:name, tokenless and with NO authorization
	// action, because the route reads no tenant data — it serves hand-placed
	// static files from SOURCE_OFFER_DIR and nothing else. Registered
	// unconditionally: with no directory configured it answers a plain-text
	// 404 saying so, which is a truthful answer at a URL the image's own
	// notice points to, where an unregistered route would be the SPA's HTML.
	sourceOfferHdlr := sourceOfferHandler.NewHandler(cfg, logger)
	sourceOfferHdlr.RegisterRoutes(e)

	// The device entitlement: the card and the paste. Applying a file changes
	// what the whole install admits, so the PUT sits behind the step-up.
	entitlementHdlr := entitlementHandler.NewHandler(entitlementSvc, logger)
	entitlementHdlr.RegisterRoutes(e, jwtMiddleware, recentAuth, authzEnforcer)

	// The endpoints that mint the step-up ticket recentAuth (built above) demands.
	reauthHdlr := reauthHandler.NewHandler(reauthSvc, passkeySvc, authService, logger)
	reauthHdlr.RegisterRoutes(e, jwtMiddleware)

	// Protected user routes
	userHdlr := userHandler.NewUserHandler(userSvc, logger)
	userHdlr.RegisterRoutes(e, jwtMiddleware, recentAuth, authzEnforcer)

	// Email verification: public confirm (the link) + authenticated resend.
	emailVerificationHdlr := emailVerificationHandler.NewHandler(emailVerificationSvc, logger)
	emailVerificationHdlr.RegisterRoutes(e, jwtMiddleware)

	// Password reset: both endpoints are public — the token is the credential.
	passwordResetHdlr := passwordResetHandler.NewHandler(passwordResetSvc, logger)
	passwordResetHdlr.RegisterRoutes(e)

	// Second-factor enrollment and management for the signed-in user.
	// The WS and terminal handshakes authenticate with a first-frame token, so
	// they never pass through the REST enrollment gate and must check for
	// themselves.
	wsManager.SetTwoFactorPolicy(twoFactorPolicy)

	twoFactorHdlr := twoFactorHandler.NewHandler(twoFactorSvc, logger)
	twoFactorHdlr.RegisterRoutes(e, jwtMiddleware, recentAuth)

	// Passkeys: management for the signed-in user + the public, usernameless
	// login ceremony. It reuses the auth handler's renderer so a passkey login
	// answers exactly like a password login.
	passkeyHdlr := passkeyHandler.NewHandler(passkeySvc, authService, authHandler, authService, logger)
	passkeyHdlr.RegisterRoutes(e, jwtMiddleware, recentAuth)

	// Protected account routes
	accountHdlr := accountHandler.NewAccountHandler(accountSvc, cfg.Retention.AuditEventDays, logger)
	accountHdlr.RegisterRoutes(e, jwtMiddleware, authzEnforcer)

	// Whole-account data export. Composed from the services already wired above
	// rather than from repositories of its own — every source is a read the same
	// caller can already perform, so the export inherits their account scoping
	// instead of introducing a second way to read a tenant's rows.
	exportSvc := account_export_service.NewService(logger).WithSources(
		accountSvc, memberSvc, organizationalUnitSvc, nodeSvc, accessSvc,
		sshKeySvc, secretSvc, workflowTemplateSvc, imageSvc,
	)
	exportSvc.SetUtilitySource(utilitySvc)
	accountExportHdlr := accountHandler.NewExportHandler(exportSvc, auditRecorder, logger)
	accountExportHdlr.RegisterRoutes(e, jwtMiddleware, authzEnforcer)

	// Audit trail read route. Gated on audit.read (super_admin only, like the
	// export above); the reader is a separate type from the Recorder every
	// writer holds, so no writing service gains a read capability.
	// The Status section: the fleet attention list and the fleet activity log.
	// Neither takes a route-level RequireAction — node.read is not an
	// any-scope read, so a nil-target check there would refuse every OU-scoped
	// member; each handler resolves AllowedOUs itself, as the node list does.
	fleetHdlr := fleetHandler.NewHandler(nodeAttentionSvc, nodeActivityReader, nodeSvc, authzEnforcer, logger)
	fleetHdlr.RegisterRoutes(e, jwtMiddleware)

	auditHdlr := auditHandler.NewHandler(auditReader, logger)
	auditHdlr.RegisterRoutes(e, jwtMiddleware, authzEnforcer)

	// Self-service deletion REQUEST. It files a request; the erasure itself stays
	// with an operator console outside this tree.
	accountDeletionHdlr := accountHandler.NewDeletionRequestHandler(accountDeletionRequestRepo, accountSvc, auditRecorder, logger)
	// Only the hosted install has that console; the notice says which.
	accountDeletionHdlr.SetInstallKind(entitlementSvc)
	accountDeletionHdlr.RegisterRoutes(e, jwtMiddleware, authzEnforcer)

	// Protected node routes
	nodeHdlr := nodeHandler.NewNodeHandler(nodeSvc, nodeFileSvc, nodeCommandSvc, organizationalUnitSvc, authzEnforcer, auditRecorder, eepromSignerClient, eepromBaseCache, logger, cfg)
	if workflowExecutionSvc != nil {
		nodeHdlr.SetRunCommandFilter(workflowExecutionSvc)
	}
	nodeHdlr.RegisterRoutes(e, jwtMiddleware)

	// Public one-time adoption-code exchange (rate-limited): the one-command
	// install link carries an opaque code instead of the adoption blob.
	adoptionHdlr := adoptionHandler.NewHandler(nodeSvc, cfg, logger)
	adoptionHdlr.RegisterRoutes(e)

	// Share the handler's network-first EEPROM provisioning service with the gRPC
	// server so the first-system-info auto-provision hook uses the same instance.
	if setter, ok := communicationService.(interface {
		SetEEPROMProvisionService(*eeprom_provision.Service)
	}); ok {
		setter.SetEEPROMProvisionService(nodeHdlr.EEPROMProvisionService())
	}

	// Node command routes (separate from node routes for cleaner API)
	e.GET("/api/v1/node-commands/:id", nodeHdlr.GetNodeCommand, jwtMiddleware)

	// Protected image routes
	imageHdlr := imageHandler.NewImageHandler(imageSvc, auditRecorder, cfg, logger)
	imageHdlr.RegisterRoutes(e, jwtMiddleware, authzEnforcer)
	// Public, signature-authenticated capture upload (agent streams a captured
	// disk image here; capability is the HMAC in the query string, no JWT).
	imageHdlr.RegisterCaptureRoutes(e)

	// Protected SSH key routes
	sshKeyHdlr := sshKeyHandler.NewSSHKeyHandler(sshKeySvc, cfg, logger)
	sshKeyHdlr.RegisterRoutes(e, jwtMiddleware, authzEnforcer)

	// SSH device adoption (outbound SSH). The runner is built in the composition
	// root (needs node/eeprom/comms/ws/repo deps) and passed in. The endpoints
	// always register; the service returns "not enabled" unless ssh_adopt.enabled.
	sshAdoptHdlr := sshAdoptHandler.NewHandler(sshAdoptSvc, logger)
	sshAdoptHdlr.RegisterRoutes(e, jwtMiddleware, authzEnforcer)

	// Protected master token routes
	masterTokenHdlr := masterTokenHandler.NewMasterTokenHandler(masterTokenSvc, cfg, logger)
	masterTokenHdlr.RegisterRoutes(e, jwtMiddleware, authzEnforcer)

	// Protected provisioning routes (SD/USB bundle download)
	provisioningHdlr := provisioningHandler.NewProvisioningHandler(masterTokenSvc, cfg, logger)
	provisioningHdlr.RegisterRoutes(e, jwtMiddleware, authzEnforcer)

	// Protected organizational unit routes (+ node membership/policy)
	organizationalUnitHdlr := organizationalUnitHandler.NewOrganizationalUnitHandler(organizationalUnitSvc, auditRecorder, logger, nodeSvc)
	organizationalUnitHdlr.RegisterRoutes(e, jwtMiddleware, authzEnforcer)

	// Access entries (declarative shell access) under access.manage.
	accessHdlr := accessEntryHandler.NewHandler(accessSvc, logger)
	accessHdlr.RegisterRoutes(e, jwtMiddleware, authzEnforcer)

	// Utilities (OU-assignable software roles) under utility.read/manage.
	utilityHdlr := utilityHandler.NewHandler(utilitySvc, logger)
	utilityHdlr.RegisterRoutes(e, jwtMiddleware, authzEnforcer)

	// Protected per-user node favorites
	nodeFavoriteHdlr := nodeFavoriteHandler.NewNodeFavoriteHandler(nodeFavoriteSvc, logger)
	nodeFavoriteHdlr.RegisterRoutes(e, jwtMiddleware, authzEnforcer)

	// Protected secret routes
	secretHdlr := secretHandler.NewSecretHandler(secretSvc, secretTagSvc, nodeSvc, auditRecorder, logger)
	secretHdlr.RegisterRoutes(e, jwtMiddleware, authzEnforcer)

	// Protected node-replace flow (transfer identity to a spare node)
	nodeReplaceHdlr := nodeReplaceHandler.NewNodeReplaceHandler(nodeReplaceSvc, logger)
	nodeReplaceHdlr.RegisterRoutes(e, jwtMiddleware, authzEnforcer)

	// Member / invite management (super_admin-gated in the service)
	memberHdlr := memberHandler.NewMemberHandler(memberSvc, logger)
	memberHdlr.RegisterRoutes(e, jwtMiddleware, recentAuth)

	// Permission map (for the frontend) + role-permission settings
	authzHdlr := authzHandler.NewAuthzHandler(authzEnforcer, logger)
	authzHdlr.RegisterRoutes(e, jwtMiddleware, recentAuth)

	// CDN routes (public, no auth required). Registered for every backend the
	// CDN can proxy — NOT just local. Getting this wrong unregisters
	// /cdn/agent/... and takes install.sh and update_agent down fleet-wide.
	if storageClient != nil && storage.ServedThroughCDN(storageClient.GetStorageType()) {
		cdnHdlr := cdnHandler.NewCDNHandler(storageClient, cfg, logger)
		// Agent CDN route with version/arch/pkgType, public and unauthenticated
		e.GET("/cdn/agent-versions", cdnHdlr.ListAgentVersions)
		e.GET("/cdn/agent/:version/:arch/:pkgType", cdnHdlr.ServeAgentPackage)
		e.GET("/cdn/watcher/:version/:arch/:pkgType", cdnHdlr.ServeWatcherPackage)
		e.GET("/cdn/*", cdnHdlr.ServeFile)
	}

	// Network-first HTTP-boot route (public, no auth): the RPi ROM fetches its
	// per-serial bootstrap here and the platform serves or withholds it per the
	// effective boot_to_os policy. Disabled unless a boot dir is configured.
	if cfg.Provisioning.HTTPBootDir != "" {
		httpBootHdlr := httpBootHandler.NewHandler(nodeSvc, organizationalUnitSvc, nodeSvc, cfg, logger)
		// Recognise a DELETED node's serial. Without this a stranded device — one
		// whose node was deleted while it kept running — asks for its own boot.sig
		// on every boot, gets a 404, and is counted as serial enumeration against
		// the IP of the site it sits in.
		httpBootHdlr.SetDeletedNodeLookup(nodeSvc)
		// Serve recovery while an OS install to the node is unfinished, so a
		// reset mid-write does not boot the partial disk.
		httpBootHdlr.SetOSInstallInFlightLookup(nodeSvc)
		httpBootHdlr.RegisterRoutes(e)
	}

	// WebSocket handler (no JWT middleware - handles auth internally)
	wsHdlr := websocketHandler.NewWebSocketHandler(wsManager, logger, cfg)
	wsHdlr.Register(e)

	// Terminal WebSocket (auth handled internally via first message; gated by
	// TERMINAL_ENABLED + account ownership). The gRPC server implements the
	// terminal bridge.
	if ts, ok := communicationService.(services.TerminalService); ok {
		terminalHdlr := terminalHandler.NewHandler(ts, nodeSvc, authService, authzEnforcer, auditRecorder, cfg, logger)
		terminalHdlr.SetTwoFactorPolicy(twoFactorPolicy)
		terminalHdlr.Register(e)
	}

	// Swagger documentation routes
	swaggerHdlr := swaggerHandler.NewSwaggerHandler(logger, cfg)
	swaggerHdlr.Register(e)

	// Boot options routes (public, no auth required)
	bootOptionsHdlr := bootOptionsHandler.NewBootOptionsHandler(logger)
	bootOptionsHdlr.RegisterRoutes(e)

	// Protected workflow routes
	workflowHdlr := workflowHandler.NewWorkflowHandler(workflowTemplateSvc, workflowExecutionSvc, authzEnforcer, auditRecorder, logger)
	workflowHdlr.RegisterRoutes(e, jwtMiddleware)
}
