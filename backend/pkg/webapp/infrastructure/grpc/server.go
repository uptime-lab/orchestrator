package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	node_connect "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_connect"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/image_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/node_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/node_commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/eeprom_provision"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/crypto"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/websockets"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

// Server represents the gRPC server
type Server struct {
	proto.UnimplementedNodeCommunicationServer
	config *config.Config
	logger *slog.Logger
	// nodeEventLast rate-limits spontaneous NodeEvents per node (a button in a
	// human's hand, fanned out to every browser of the account). Swept lazily
	// on use; bounded by the connected-node count.
	nodeEventMu               sync.Mutex
	nodeEventLast             map[string]time.Time
	nodeService               services.NodeService
	utilitySettler            UtilitySettler
	nodeFileService           services.FileService
	nodeCommandService        services.NodeCommandService
	imageService              services.ImageService
	masterTokenService        services.MasterTokenService
	organizationalUnitService services.OrganizationalUnitService
	accessResolver            services.AccessResolver
	eepromProvision           *eeprom_provision.Service
	grpcServer                *grpc.Server
	connections               map[string]*StreamConnection
	mu                        sync.RWMutex
	monitor                   *Monitor
	websocketClient           websockets.WebsocketClient
	// terminalWarnings throttles the "not your session" warning per node; see
	// warnDroppedTerminalFrame.
	terminalWarnings *logThrottle
	// reportWarnings throttles the "report too large" warning per node; see
	// device_report_limits.go.
	reportWarnings *logThrottle
	// terminals holds active interactive terminal sessions keyed by session id.
	terminals   map[string]*terminalSession
	terminalsMu sync.RWMutex
	// adoptionRejections collapses the log line for a peer that keeps failing
	// the handshake and raises an anomaly when it becomes a loop. See
	// adoption_rejections.go for why a rejected peer is otherwise invisible.
	adoptionRejections *adoptionRejectionTracker
	// deletedRefusalStamps throttles the tombstone "still trying" mark per
	// deleted node id, before the database is asked anything.
	deletedRefusalStamps *stampThrottle
	// preAuth caps how many streams may be waiting to authenticate at once.
	// See preauth_limits.go — the 15-second deadline bounds their lifetime, not
	// their number.
	preAuth *preAuthGate
	// preAuthSource caps pre-auth streams PER CLIENT ADDRESS. nil unless the
	// listener requires PROXY protocol, because only then is the peer address
	// the client's rather than the proxy's (proxy_protocol.go).
	preAuthSource *preAuthSourceGate
	// preAuthRefusals throttles the per-source refusal line by source, so a
	// flood from one address writes one line a minute, not one per stream.
	preAuthRefusals *logThrottle
	// nodeStreams caps how many AUTHENTICATED streams one node may hold at
	// once. See postauth_limits.go — the pre-auth gate stops counting the
	// moment a token validates, and nothing counted after that.
	nodeStreams *nodeStreamGate
	// anomalyRepo is optional: without it rejections are still collapsed in the
	// log, they just do not surface as an anomaly. Set via SetAnomalyRepository.
	anomalyRepo repository.InstanceAnomalyRepository
}

// connectionsSnapshot returns a private copy of the live connection registry,
// taken under the server's own lock.
//
// This is the ONLY way anything outside this type may read the registry. The
// monitor used to be handed the map itself and guard it with its own mutex
// while this type wrote to it under s.mu — an unsynchronised concurrent
// iteration and write, which Go reports as `fatal error: concurrent map
// iteration and map write`. That is not recoverable in-process, so with
// restart:always it presents as a restart loop rather than a crash.
func (s *Server) connectionsSnapshot() map[string]*StreamConnection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]*StreamConnection, len(s.connections))
	for nodeID, conn := range s.connections {
		out[nodeID] = conn
	}
	return out
}

// SetAnomalyRepository wires the anomaly store in after construction, following
// the same setter pattern main.go uses for the services the gRPC layer needs.
func (s *Server) SetAnomalyRepository(repo repository.InstanceAnomalyRepository) {
	s.anomalyRepo = repo
}

// maxGRPCMessageBytes is the explicit per-frame ceiling for the agent stream in
// both directions. See where it is applied in Start() for why it is 8 MiB and
// what it is protecting against.
const maxGRPCMessageBytes = 8 * 1024 * 1024

// StreamConnection represents an active node connection
type StreamConnection struct {
	NodeID    string
	AccountID string
	// OUID is the node's organizational unit, captured at connect time so the
	// high-frequency metrics push can be OU-scoped without a database lookup
	// per 5-second sample. Read by the fan-out under Server.mu.RLock, written
	// under Server.mu.Lock; refreshed to the COMMITTED row value by
	// Server.refreshConnectionScope, never from a caller's argument.
	OUID *uuid.UUID
	// scopeMu serialises "read the committed unit, then publish it" for THIS
	// connection (see refreshConnectionScope). Per connection rather than
	// Server.mu because the read goes to the database, and Server.mu must never
	// wait on it.
	scopeMu sync.Mutex
	// Stream is owned by this connection's writer goroutine. Do NOT call
	// Stream.Send from anywhere else — grpc-go forbids concurrent SendMsg on one
	// stream, and two callers doing it (dispatch and the terminal handlers)
	// killed the process. Use Send, which queues on `out`.
	Stream grpc.BidiStreamingServer[proto.NodeToPlatform, proto.PlatformToNode]
	// lastSeenNanos is written by every inbound frame handler and read
	// concurrently by the connection monitor; a plain time.Time there is a
	// data race — torn reads and a blocker for any future -race run. Access
	// only through Touch/LastSeenAt.
	lastSeenNanos atomic.Int64
	Cancel        context.CancelFunc

	// Outbound plumbing; see stream_writer.go.
	out        chan *proto.PlatformToNode
	quit       chan struct{}
	writerDone chan struct{}
	stopOnce   sync.Once
	sendErrMu  sync.Mutex
	sendErr    error
}

// newStreamConnection builds a connection and starts its writer.
//
// The writer starts BEFORE the connection is published in the registry, because
// the instant it is published the dispatch path and queued-command redelivery may
// both enqueue frames.
func newStreamConnection(
	ctx context.Context,
	logger *slog.Logger,
	nodeID, accountID string,
	ouID *uuid.UUID,
	stream grpc.BidiStreamingServer[proto.NodeToPlatform, proto.PlatformToNode],
	lastSeen time.Time,
	cancel context.CancelFunc,
) *StreamConnection {
	conn := &StreamConnection{
		NodeID:     nodeID,
		AccountID:  accountID,
		OUID:       ouID,
		Stream:     stream,
		Cancel:     cancel,
		out:        make(chan *proto.PlatformToNode, outboundBuffer),
		quit:       make(chan struct{}),
		writerDone: make(chan struct{}),
	}
	conn.Touch(lastSeen)
	conn.startWriter(ctx, logger)
	return conn
}

// Touch records stream activity at the given time.
func (c *StreamConnection) Touch(t time.Time) {
	c.lastSeenNanos.Store(t.UnixNano())
}

// LastSeenAt returns the most recently recorded stream activity.
func (c *StreamConnection) LastSeenAt() time.Time {
	return time.Unix(0, c.lastSeenNanos.Load())
}

// NewServer creates a new gRPC server
func NewServer(cfg *config.Config, logger *slog.Logger, nodeService services.NodeService) *Server {
	server := &Server{
		config:               cfg,
		logger:               logger,
		nodeService:          nodeService,
		connections:          make(map[string]*StreamConnection),
		terminals:            make(map[string]*terminalSession),
		terminalWarnings:     newLogThrottle(),
		reportWarnings:       newLogThrottle(),
		adoptionRejections:   newAdoptionRejectionTracker(),
		deletedRefusalStamps: newStampThrottle(deletedRefusalStampInterval, deletedRefusalStampCapacity),
		preAuth:              newPreAuthGate(cfg.Quota.MaxPreAuthStreams),
		preAuthRefusals:      newLogThrottle(),
		nodeStreams:          newNodeStreamGate(cfg.Quota.MaxAgentStreamsPerNode),
	}
	// Per-source gate ONLY behind PROXY protocol. Constructing it
	// unconditionally would be the trap the whole design avoids: on an install
	// whose proxy sends no header, every agent shares the proxy's address and
	// this gate refuses the fleet at 256.
	if cfg.GRPC.ProxyProtocolRequired() {
		server.preAuthSource = newPreAuthSourceGate(cfg.Quota.MaxPreAuthStreamsPerSource)
	}

	// Create and configure monitor
	server.monitor = NewMonitor(nodeService, logger)
	// The monitor gets a SNAPSHOT function, never the map itself: it has its own
	// mutex, and two mutexes around one map is a data race, not synchronisation.
	server.monitor.SetConnectionsSnapshot(server.connectionsSnapshot)
	// Set cleanup callback to use Server's mutex for connection removal
	server.monitor.SetCleanupCallback(func(nodeID string, conn *StreamConnection) bool {
		// Delete the entry ONLY if it is still the connection the monitor swept.
		//
		// The monitor works from a snapshot and does per-node database work
		// between taking it and acting on it, so a node can reconnect in that
		// window: the new stream registers, correctly displaces the old one, and
		// then this callback — holding the OLD connection — would remove the NEW
		// one from the registry. Nothing re-registers it (registration happens
		// once per stream) and its handler is never cancelled, so it keeps
		// heartbeating: `last_seen_at` stays fresh, the database-side stale
		// sweep never revisits it, and `IsNodeConnected` is false forever while
		// the stream is alive. Every command then fails "not connected" and the
		// terminal refuses, on a node the operator can see is up.
		//
		// The teardown path already makes exactly this comparison, with a
		// comment saying clobbering it "is exactly the bug that left reconnected
		// nodes stuck offline".
		server.mu.Lock()
		current, exists := server.connections[nodeID]
		swept := exists && current == conn
		if swept {
			delete(server.connections, nodeID)
		}
		server.mu.Unlock()
		if !swept {
			// Still stop the connection we were handed — it IS dead — but tell
			// the monitor the registry belongs to someone else now, so its
			// follow-up status write does not mark a live node offline.
			server.logger.Info("stale connection was already displaced by a reconnect; leaving the registry alone",
				"node_id", nodeID)
		}
		// The monitor has decided this stream is dead. Its writer would exit on
		// the cancelled stream context anyway; stopping it explicitly means a
		// frame enqueued in the same instant fails immediately instead of
		// waiting out the enqueue grace.
		if conn != nil {
			conn.Stop()
		}
		return swept
	})

	return server
}

// SetWebsocketClient sets the WebSocket client for sending status updates
func (s *Server) SetWebsocketClient(client websockets.WebsocketClient) {
	s.websocketClient = client
	s.monitor.SetWebsocketClient(client)
}

// SetNodeService sets the node service for handling node operations
func (s *Server) SetNodeService(nodeService services.NodeService) {
	s.nodeService = nodeService
}

// SetNodeFileService sets the node file service for handling file operations
func (s *Server) SetNodeFileService(service services.FileService) {
	s.nodeFileService = service
}

// UtilitySettler is the narrow slice of the utility service the terminal-status
// hook needs (per-utility ladder settle).
type UtilitySettler interface {
	SettleApply(ctx context.Context, cmd *node_command.NodeCommand, status string, result map[string]string)
	// ForgetAppliedAfterReimage drops the node's ladder once a new OS image has
	// replaced the disk the recorded hashes describe.
	ForgetAppliedAfterReimage(ctx context.Context, accountID, nodeID uuid.UUID) error
}

// SetUtilitySettler wires the utility apply settle hook.
func (s *Server) SetUtilitySettler(u UtilitySettler) {
	s.utilitySettler = u
}

// SetNodeCommandService sets the node command service for handling command tracking
func (s *Server) SetNodeCommandService(service services.NodeCommandService) {
	s.nodeCommandService = service
	// The monitor needs it too: it is the only place that can decide a node is
	// never coming back, and a node's commands have to be settled by whoever
	// makes that call.
	if s.monitor != nil {
		s.monitor.SetNodeCommandService(service)
	}
}

// SetImageService sets the image service for handling image lookups in install_os commands
func (s *Server) SetImageService(service services.ImageService) {
	s.imageService = service
}

// SetMasterTokenService sets the master token service for validating master tokens
func (s *Server) SetMasterTokenService(service services.MasterTokenService) {
	s.masterTokenService = service
}

// SetOrganizationalUnitService sets the OU service used to resolve a node's
// effective provisioning inputs when building install_os/provision commands.
func (s *Server) SetOrganizationalUnitService(service services.OrganizationalUnitService) {
	s.organizationalUnitService = service
}

// UpdateNodeOU refreshes a connected node's live organizational-unit scope
// after an assignment has COMMITTED.
//
// The unit is captured at registration (see StreamConnection.OUID) so the
// 5-second metrics push can scope itself without a database read per sample.
// Left without a refresh, a node moved between units while its stream stayed up
// kept fanning its metrics out to the OLD unit's viewers and starved the new one
// until it happened to reconnect. The assignment path calls this
// right after writing the column; leaving it unwired is that bug back.
//
// ouID is what the caller committed, and it is deliberately NOT what gets
// applied: the live value is re-read from the row under the connection's own
// lock (refreshConnectionScope has the ordering argument), so two assignments
// whose in-memory updates land out of order still converge on the row, and an
// assignment that commits during registration is captured by the post-publish
// refresh. The argument is only named in the log when the read fails.
//
// A node without a live stream is a no-op and costs no read: its next
// registration reads the column. The id is canonicalised the way the registry
// keys are.
func (s *Server) UpdateNodeOU(nodeID string, ouID *uuid.UUID) {
	if id, err := uuid.Parse(nodeID); err == nil {
		nodeID = id.String()
	}
	s.mu.RLock()
	conn := s.connections[nodeID]
	s.mu.RUnlock()
	if conn == nil {
		return
	}
	s.refreshConnectionScope(context.Background(), conn, ouID)
}

// SetAccessService wires the access resolver used to fill a node's effective
// access set when building apply_access_config commands.
func (s *Server) SetAccessService(resolver services.AccessResolver) {
	s.accessResolver = resolver
}

// SetEEPROMProvisionService wires the shared network-first EEPROM provisioning
// service used by the first-system-info auto-provision hook.
func (s *Server) SetEEPROMProvisionService(service *eeprom_provision.Service) {
	s.eepromProvision = service
}

// maybeAutoProvisionEEPROM runs the one-shot network-first EEPROM flash the first
// time a freshly-created "single node" reports full system info. It is a no-op
// unless the node carries the AutoProvisionEEPROMPending flag. The flag is
// cleared only once a flash is actually dispatched, so a report that arrives
// before SoC/boot-device is known (or a transient signer failure) simply retries
// on the next report instead of being lost. Uses a detached context so a
// mid-handshake disconnect cannot abort the flash dispatch or the flag write.
func (s *Server) maybeAutoProvisionEEPROM(nodeUUID, accountUUID uuid.UUID) {
	if s.eepromProvision == nil || !s.eepromProvision.Enabled() {
		return
	}
	ctx := context.Background()
	n, err := s.nodeService.GetByIDAndAccount(ctx, nodeUUID, accountUUID)
	if err != nil {
		s.logger.Error("auto-eeprom: failed to load node", "node_id", nodeUUID, "error", err)
		return
	}
	if !n.AutoProvisionEEPROMPending {
		return
	}

	// The adoption token baked into the EEPROM needs the platform's reachable
	// URLs; the gRPC layer has no request to fall back on, so fail closed (keep
	// the flag armed) when the server advertisement URL is not configured.
	platformURL := s.config.Server.AdvertisementURL
	grpcEndpoint := s.config.GRPC.AdvertisementURL
	if grpcEndpoint == "" {
		grpcEndpoint = net.JoinHostPort(s.config.GRPC.Host, fmt.Sprintf("%d", s.config.GRPC.Port))
	}
	if platformURL == "" {
		s.logger.Warn("auto-eeprom skipped: server advertisement URL not configured; cannot bake adoption token", "node_id", nodeUUID)
		return
	}

	triggered, err := s.eepromProvision.MaybeProvisionNetworkFirst(ctx, accountUUID, uuid.Nil, n, platformURL, grpcEndpoint)
	if err != nil {
		s.logger.Error("auto-eeprom provisioning failed; will retry on next report", "node_id", nodeUUID, "error", err)
		return
	}
	if !triggered {
		return
	}
	if cerr := s.nodeService.ClearAutoProvisionEEPROMPending(ctx, nodeUUID, accountUUID); cerr != nil {
		s.logger.Error("auto-eeprom: failed to clear one-shot flag", "node_id", nodeUUID, "error", cerr)
		return
	}
	s.logger.Info("auto-eeprom: flash triggered on first system info; device will reboot into netboot", "node_id", nodeUUID)
}

// serverOptions builds every grpc.ServerOption except transport credentials,
// which Start appends from config. A method rather than inline in Start so a
// test can serve the REAL option set over an in-memory listener: the
// per-connection stream cap is enforced by grpc-go's transport, not by any
// code in this package, and only a real transport can show it is applied.
func (s *Server) serverOptions() []grpc.ServerOption {
	// Configure keepalive to detect dead connections faster
	// This is critical for detecting clients that lose power suddenly
	keepaliveParams := keepalive.ServerParameters{
		// MaxConnectionIdle: If a client is idle for this duration, send a GOAWAY
		// Set to 0 to disable - agent connections should stay alive indefinitely
		MaxConnectionIdle: 0,
		// MaxConnectionAge: Maximum time a connection may exist before being closed
		// Set to 0 to disable - agent connections are long-lived and should not be forced to reconnect
		MaxConnectionAge: 0,
		// MaxConnectionAgeGrace: Allow pending RPCs to complete before forcibly closing
		MaxConnectionAgeGrace: 10 * time.Second,
		// Time: After a duration of this time if the server doesn't see any activity
		// it pings the client to see if the transport is still alive
		Time: 30 * time.Second,
		// Timeout: Wait this duration for the ping ack before assuming connection is dead
		Timeout: 10 * time.Second,
	}

	keepalivePolicy := keepalive.EnforcementPolicy{
		// MinTime: Minimum amount of time a client should wait before sending a keepalive ping
		MinTime: 10 * time.Second,
		// PermitWithoutStream: Allow pings even when there are no active streams
		PermitWithoutStream: true,
	}

	opts := []grpc.ServerOption{
		grpc.KeepaliveParams(keepaliveParams),
		grpc.KeepaliveEnforcementPolicy(keepalivePolicy),
		// Bound HTTP/2 streams per connection. grpc-go's default is effectively
		// unlimited, and this port faces the internet. An agent needs exactly
		// one stream (two, briefly, across a reconnect), and a reverse proxy
		// in front of :50051 forwards TCP, so each agent arrives on its own
		// connection rather than multiplexed onto a shared one. Kept tight on purpose: it
		// is the per-connection half of the pre-auth arithmetic (connections
		// per client IP, bounded at the edge, x streams per connection must
		// stay below the global gate), and with PROXY protocol required the
		// per-source gate bounds the product directly, per client address.
		// See preauth_limits.go, including why this is not a substitute for
		// the grpc-go version bump.
		grpc.MaxConcurrentStreams(maxConcurrentStreamsPerConnection),
		// Make the frame ceiling explicit instead of inheriting grpc-go's 4 MiB
		// default, which nothing in this codebase had ever stated or reasoned
		// about.
		//
		// It mattered because exceeding it does not fail a COMMAND, it kills the
		// STREAM: the node drops off, its in-flight result is lost, the
		// node_command sits in in_progress until the abandonment sweep, and the
		// workflow execution never reaches completed_at, so it permanently
		// consumes one of the per-account active-execution slots. A run_script
		// block doing `journalctl -b` was enough to trigger it.
		//
		// The real fix is the agent bounding what it sends (see
		// handlers.MaxResultOutputBytes in the agent module). This ceiling is
		// the half that protects a fleet still running OLDER agents, which send
		// unbounded output and cannot be fixed without rolling them —
		// deliberately NOT solved by giving run_script a MinAgentVersion floor,
		// which would break every playbook on the fleet as it stands. It is
		// deliberately a compromise: raising it trades stream death for memory,
		// since a frame is buffered whole. 8 MiB covers any realistic script
		// output while keeping a simultaneous fleet-wide burst survivable on a
		// small host. Once the fleet is fully on a bounding agent this can come
		// back down — 1 MiB would be ample, as the largest legitimate frame is a
		// 512 KiB node-file body.
		grpc.MaxRecvMsgSize(maxGRPCMessageBytes),
		grpc.MaxSendMsgSize(maxGRPCMessageBytes),
		// Contain a panic to the stream that caused it.
		//
		// grpc-go deliberately installs no recovery, and this process also
		// serves the whole HTTP API — so any panic in a handler, reachable by
		// anyone who can open a stream, would take down the API, every other
		// agent connection and every in-flight workflow at once. Echo has had
		// middleware.Recover() on the HTTP side since the beginning; this is the
		// same guarantee for the side that is exposed to unauthenticated peers.
		//
		// Deliberately NOT a substitute for validating input: it converts a
		// crash into a 500 for one caller, which is the floor, not the fix.
		grpc.StreamInterceptor(s.recoveryStreamInterceptor),
		grpc.UnaryInterceptor(s.recoveryUnaryInterceptor),
	}
	return opts
}

// Start starts the gRPC server
func (s *Server) Start(ctx context.Context, commandQueue chan node_commands.NodeCommandOperation) error {
	addr := fmt.Sprintf("%s:%d", s.config.GRPC.Host, s.config.GRPC.Port)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	// PROXY protocol, when required, is applied to the LISTENER so that every
	// accepted connection's RemoteAddr — and so peer.FromContext in the
	// handlers — is the client's address rather than the proxy's. An
	// unrecognised mode is a refused boot, not a fallback (proxy_protocol.go).
	lis, err = wrapListenerForProxyProtocol(lis, s.config.GRPC)
	if err != nil {
		return err
	}

	opts := s.serverOptions()

	// Serve TLS when a cert/key pair is configured; otherwise plaintext, which
	// is either terminated upstream, declared acceptable for a trusted LAN
	// (AGENT_GRPC_PLAINTEXT_OK — the Advisories line at config load is the
	// loud one; this is the listener's own record), or undeclared — which
	// production has already refused at Validate, so the last branch is a
	// development install.
	tlsEnabled := s.config.GRPC.TLSDirect()
	switch {
	case tlsEnabled:
		creds, err := credentials.NewServerTLSFromFile(s.config.GRPC.TLSCertFile, s.config.GRPC.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("failed to load gRPC TLS cert/key: %w", err)
		}
		opts = append(opts, grpc.Creds(creds))
	case s.config.GRPC.TLSTerminatedExternally:
		s.logger.Info("gRPC server serving plaintext; TLS is terminated by an upstream proxy (grpc.tls_terminated_externally=true)")
	case s.config.GRPC.PlaintextOK:
		s.logger.Warn("gRPC server serving plaintext by declaration (AGENT_GRPC_PLAINTEXT_OK=true); device credentials cross this listener readable")
	default:
		s.logger.Warn("gRPC server running WITHOUT TLS and without a declaration (plaintext); production refuses this at boot — set GRPC_TLS_CERT_FILE/GRPC_TLS_KEY_FILE, GRPC_TLS_TERMINATED_EXTERNALLY=true, or AGENT_GRPC_PLAINTEXT_OK=true")
	}

	s.grpcServer = grpc.NewServer(opts...)
	proto.RegisterNodeCommunicationServer(s.grpcServer, s)

	s.logger.Info("gRPC server starting", "address", addr, "tls", tlsEnabled,
		"proxy_protocol", s.config.GRPC.ProxyProtocolRequired(),
		"preauth_per_source_gate", s.preAuthSource != nil)

	// Start background monitor with main context
	go s.monitor.Start(ctx)

	// Start command queue processor
	go s.ProcessCommandQueue(ctx, commandQueue)

	go func() {
		if err := s.grpcServer.Serve(lis); err != nil {
			s.logger.Error("gRPC server failed", "error", err)
		}
	}()

	return nil
}

// Stop gracefully stops the gRPC server
func (s *Server) Stop(ctx context.Context) error {
	s.logger.Info("stopping gRPC server...")

	// Stop monitor
	s.monitor.Stop()

	// Cancel all active connections
	s.mu.Lock()
	for _, conn := range s.connections {
		conn.Cancel()
	}
	s.connections = make(map[string]*StreamConnection)
	s.mu.Unlock()

	// Graceful stop with timeout
	done := make(chan struct{})
	go func() {
		s.grpcServer.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
		s.logger.Info("gRPC server stopped gracefully")
		return nil
	case <-ctx.Done():
		s.logger.Warn("gRPC server stop timeout, forcing stop")
		s.grpcServer.Stop()
		return ctx.Err()
	}
}

// EstablishStream implements the bidirectional streaming RPC
func (s *Server) EstablishStream(stream grpc.BidiStreamingServer[proto.NodeToPlatform, proto.PlatformToNode]) error {
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	var nodeID, accountID string
	var connection *StreamConnection
	// releasePreAuth hands back the pre-auth slot. Set below when the gate is
	// wired; a no-op otherwise so the call sites stay unconditional.
	releasePreAuth := func() {}
	// releaseNodeStream hands back this node's authenticated-stream slot. Taken
	// once the token validates; released here, so every exit path — including a
	// panic recovered by the stream interceptor — gives it back. A slot that
	// leaks would eventually refuse a real node, which is worse than the flood
	// the gate exists to bound.
	releaseNodeStream := func() {}
	defer func() { releaseNodeStream() }()
	// pacer bounds how fast ONE authenticated stream may drive the message loop
	// below. nil until the token validates, and nil when pacing is switched off.
	var pacer *rate.Limiter
	// sysInfoPacer paces system-info reports below pacer; nil until the token
	// validates. See device_report_limits.go.
	var sysInfoPacer *rate.Limiter

	// Pre-auth concurrency cap. The deadline below bounds how LONG an
	// unauthenticated stream lives; this bounds how MANY exist at once, which
	// nothing did — and a peer can open them far faster than 15 seconds retires
	// them. Refused immediately rather than queued: waiting here would hold the
	// goroutine this exists to bound.
	if s.preAuth != nil {
		if !s.preAuth.acquire() {
			// source_ip is the client's address only behind PROXY protocol;
			// without it this names the proxy, which is still worth having
			// when a per-source install is later compared against it.
			s.logger.Warn("refusing gRPC stream: too many unauthenticated streams in flight",
				"in_flight", s.preAuth.inFlight(), "cap", s.preAuth.limit(), "source_ip", peerIP(ctx))
			return status.Error(codes.ResourceExhausted, "too many unauthenticated connections; retry shortly")
		}
		// Released once, whichever comes first: authentication (the stream is no
		// longer pre-auth and may live for days) or the stream ending. Holding
		// it for the whole session would make the cap a cap on the FLEET.
		var releaseOnce sync.Once
		releasePreAuth = func() { releaseOnce.Do(s.preAuth.release) }
		defer releasePreAuth()
	}

	// Per-source pre-auth cap, inside the global one. Nil unless the listener
	// requires PROXY protocol (see NewServer): only then does the peer address
	// belong to the client rather than the proxy. Same shape as the global
	// gate — refused immediately with ResourceExhausted, released once on
	// whichever comes first, authentication or the stream ending — and chained
	// into releasePreAuth so the single call site after the token validates
	// hands back BOTH slots. A peer with no address at all (a hand-built stream
	// in a test) is bounded by the global gate alone rather than by a shared
	// bucket every such peer would land in.
	if s.preAuthSource != nil {
		if source, ok := peerSourceKey(ctx); ok {
			if !s.preAuthSource.acquire(source) {
				s.warnPreAuthSourceRefusal(source)
				return status.Error(codes.ResourceExhausted, "too many unauthenticated connections from this address; retry shortly")
			}
			var releaseSourceOnce sync.Once
			releaseSource := func() { releaseSourceOnce.Do(func() { s.preAuthSource.release(source) }) }
			defer releaseSource()
			releaseGlobal := releasePreAuth
			releasePreAuth = func() { releaseGlobal(); releaseSource() }
		}
	}

	// Pre-auth deadline: a peer that opens the stream but never sends a valid
	// ConnectionInit would otherwise hold a goroutine + connection forever
	// (keepalive only reaps transport-dead peers, not a live attacker). Reap it.
	//
	// Cancelling is NOT enough on its own. grpc-go's stream.Recv does not observe the handler's context: a peer
	// that opens a stream and says nothing parks the handler inside Recv, the
	// loop below never reaches its ctx.Done() check, and releasePreAuth — a
	// deferred call in a function that never returns — never runs. Streams
	// that say nothing would then hold pre-auth slots with no credentials until
	// the gate is exhausted, and :50051 is typically internet-facing. (The
	// per-connection cap and the gate's sizing are in preauth_limits.go.)
	//
	// So the receive moved off this goroutine (see recvFrames below) and the
	// handler RETURNS on the deadline, which is the only thing that ends a gRPC
	// stream from the server side.
	var authed atomic.Bool
	var preAuthExpired atomic.Bool
	reaper := time.AfterFunc(preAuthDeadline, func() {
		if !authed.Load() {
			// Debug, not Warn: this fires once for every rejected handshake, so
			// on a looping peer it would double the flood the rejection log
			// already is — the two lines are 1:1. The rejection itself
			// is still reported, throttled and with a count, by
			// noteAdoptionRejection.
			s.logger.Debug("closing gRPC stream: no authenticated ConnectionInit within the pre-auth deadline")
			preAuthExpired.Store(true)
			cancel()
		}
	})
	defer reaper.Stop()

	// One receiver goroutine per stream, so the loop below can select against
	// the context instead of blocking in Recv. It exits on ctx.Done() and,
	// failing that, when the handler returns: returning ends the stream, which
	// is what unblocks its pending Recv. The channel is buffered so a frame that
	// arrives exactly as the handler returns does not pin the goroutine.
	type receivedFrame struct {
		msg *proto.NodeToPlatform
		err error
	}
	frames := make(chan receivedFrame, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			select {
			case frames <- receivedFrame{msg: msg, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	// Cleanup on disconnect — only for a stream that registered a connection.
	// nodeID is peer-supplied and set before the handshake is checked, so a
	// refused handshake carries one too; it never owned a registry entry, a
	// terminal or an online status, so there is nothing of it to tear down.
	defer func() {
		if connection != nil {
			// Use a fresh timeout context to ensure DB updates run even if stream context is canceled
			dcCtx, dcCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer dcCancel()
			s.handleDisconnection(dcCtx, nodeID, connection)
		}
	}()

	// Handle incoming messages from agent
	for {
		var msg *proto.NodeToPlatform
		select {
		case <-ctx.Done():
			// The pre-auth reaper is the one canceller that has to say
			// something specific: the peer is being hung up on, and the code
			// tells it why rather than looking like a server-side abort.
			if preAuthExpired.Load() {
				return status.Error(codes.DeadlineExceeded, "no authenticated connection init within the pre-auth deadline")
			}
			return ctx.Err()
		case frame := <-frames:
			if frame.err != nil {
				err := frame.err
				// Check if it's a context cancellation (normal shutdown)
				if errors.Is(err, context.Canceled) || errors.Is(err, grpc.ErrClientConnClosing) {
					s.logger.Info("connection closed", "node_id", nodeID)
				} else {
					s.logger.Error("failed to receive message", "node_id", nodeID, "error", err)
				}
				return err
			}
			msg = frame.msg
		}

		// Pace what this stream may make the platform do. Almost every branch
		// below writes to Postgres or fans out to every WebSocket client of the
		// account, and nothing bounded how fast an authenticated peer could
		// drive it — a shell on one device could saturate the database shared by
		// every tenant.
		//
		// The frame is DELAYED, never dropped: a dropped CommandStatusUpdate
		// loses a command result and leaves the node_command in_progress until
		// the abandonment sweep, so waiting is the only bound that cannot lose
		// information. Backpressure lands on the flooder alone, since each
		// stream has its own goroutine and its own limiter.
		if pacer != nil && pacedInbound(msg) {
			if err := pacer.Wait(ctx); err != nil {
				s.logger.InfoContext(ctx, "stream ended while pacing inbound frames", "node_id", nodeID, "error", err)
				return err
			}
		}

		// Handle different message types
		switch payload := msg.Payload.(type) {
		case *proto.NodeToPlatform_Connection:
			// ONCE per stream. A second ConnectionInit used to run this whole
			// branch again, which built a SECOND StreamConnection — and
			// therefore a second writer goroutine — over the same gRPC stream,
			// then overwrote the map entry so the first was never retired. Two
			// goroutines calling SendMsg on one stream is exactly the invariant
			// stream_writer.go exists to enforce; grpc-go forbids it and does
			// not detect it, and two callers doing it can kill the process.
			//
			// `connection` is this STREAM's, not this node's, so a node
			// reconnecting on a fresh stream is unaffected — that is a separate
			// EstablishStream call with its own local. Replacing a node's
			// previous connection stays the map's job.
			//
			// Safe because an honest agent cannot reach it: every agent path was
			// read. Connect sends exactly one init on a stream it just
			// opened; reconnect() tears the stream down and calls Connect, so the
			// init lands on a NEW stream; the message queue replays only status,
			// command-status and system-info; and the master-token identity
			// handover deliberately leaves the live stream alone
			// (AdoptReconnectIdentity) and changes only what the NEXT connect
			// presents. So no agent has a reason to send a second one.
			if connection != nil {
				s.logger.Warn("refusing a second ConnectionInit on an established stream",
					"node_id", boundedIdentifier(msg.NodeId))
				return status.Error(codes.FailedPrecondition, "connection already initialised on this stream")
			}

			// Initial connection
			nodeID = msg.NodeId
			accountID = msg.AccountId

			// Parse UUIDs
			//
			// Both ids are peer-supplied and reach a log line before anything
			// has authenticated, so they are bounded first. Unthrottled ERROR
			// lines carrying an unbounded string are how the container's 20 MB x
			// 5 log ring gets flushed of whatever it was actually recording.
			nodeUUID, err := uuid.Parse(nodeID)
			if err != nil {
				s.logger.Error("invalid node ID format", "node_id", boundedIdentifier(nodeID), "error", err)
				return status.Errorf(codes.InvalidArgument, "invalid node ID format")
			}

			accountUUID, err := uuid.Parse(accountID)
			if err != nil {
				s.logger.Error("invalid account ID format", "account_id", boundedIdentifier(accountID), "error", err)
				return status.Errorf(codes.InvalidArgument, "invalid account ID format")
			}

			// CANONICALISE both ids before anything keys on them.
			//
			// `uuid.Parse` accepts five spellings of one UUID (canonical,
			// upper-case, braced, `urn:uuid:`, 32-char hyphenless), so the raw
			// string is not an identity — case alone gives about a million
			// distinct strings for one node. Everything downstream keys on it:
			// the per-node stream cap (`nodeStreams.acquire`), the connection
			// registry and therefore the displacement of a previous stream, and
			// the dispatch lookup, which uses the canonical form. Without this,
			// an adversary holding ONE node's token opens unbounded concurrent
			// streams by varying the case — each with its own receive goroutine,
			// writer goroutine, frame buffer and inbound pacer — while
			// AGENT_MAX_STREAMS_PER_NODE, documented as the bound
			// on exactly this, never binds. The master-token arm already
			// re-canonicalises further down; this makes the node-token arm agree
			// and is a no-op for an honest agent.
			nodeID = nodeUUID.String()
			accountID = accountUUID.String()

			// Convert proto fingerprint to domain fingerprint.
			//
			// Through the GETTERS, and with an explicit nil check, because
			// `fingerprint` is an optional proto3 message: a ConnectionInit that
			// simply omits it unmarshals to nil. This runs BEFORE the token is
			// validated, so a direct dereference here would be an
			// unauthenticated remote panic. A nil fingerprint is legal wire
			// format and must be refused, never dereferenced; the recovery
			// interceptor is the backstop, not the check.
			fp := payload.Connection.GetFingerprint()
			if fp == nil {
				s.noteAdoptionRejection(ctx, "", peerIP(ctx), "connection init carried no hardware fingerprint")
				return status.Error(codes.InvalidArgument, "hardware fingerprint is required")
			}
			// Refuse an over-long serial outright rather than truncating it into
			// the fingerprint. Truncation would be worse than the flood: two
			// devices sharing a 128-byte prefix would collapse onto one node
			// through the serial match, and the serial is what master-token
			// adoption identifies hardware by. An honest agent cannot reach this
			// — its own regex caps the value at 16 characters.
			if len(fp.GetHardwareSerial()) > maxIdentifierBytes {
				s.noteAdoptionRejection(ctx, boundedIdentifier(fp.GetHardwareSerial()), peerIP(ctx),
					"hardware serial exceeds the maximum length")
				return status.Errorf(codes.InvalidArgument, "hardware serial exceeds %d bytes", maxIdentifierBytes)
			}
			domainFingerprint := node.HardwareFingerprint{
				MacAddress:     fp.GetMacAddress(),
				HardwareSerial: fp.GetHardwareSerial(),
			}

			var connectedNode *node.Node
			var isPendingApproval bool
			// Whether this handshake authenticated with the shared enrolment
			// token rather than the node's own. Drives the identity handoff
			// below — see IdentityAssignment in the proto for why that matters.
			var adoptedViaMasterToken bool

			// Check if this is master token mode (node_id is nil UUID)
			if nodeUUID == uuid.Nil {
				// Master token mode - validate master token and create/find node
				if s.masterTokenService == nil {
					s.logger.Error("master token connection attempted but service not configured")
					return status.Errorf(codes.Unauthenticated, "master token authentication not available")
				}

				// Validate master token (sent in unique_token field)
				masterToken, err := s.masterTokenService.ValidateToken(ctx, msg.UniqueToken)
				if err != nil {
					// Throttled, not silenced: an agent whose token expired
					// retries about once a second forever, because the stream is
					// accepted and only then refused, which its backoff does not
					// treat as a refusal. See adoption_rejections.go.
					s.noteAdoptionRejection(ctx, domainFingerprint.HardwareSerial, peerIP(ctx), err.Error())
					return status.Errorf(codes.Unauthenticated, "invalid master token")
				}

				// Use account ID from master token (override any sent value)
				accountUUID = masterToken.AccountID
				accountID = accountUUID.String()

				// Validate hardware serial is present in fingerprint. The agent
				// falls back to the literal "unknown" when /proc/cpuinfo has no
				// Serial line (non-Pi hardware) — accepting it would merge every
				// such device into one node via the serial match.
				if domainFingerprint.HardwareSerial == "" || domainFingerprint.HardwareSerial == "unknown" {
					s.logger.Error("master token connection missing usable hardware serial", "serial", domainFingerprint.HardwareSerial)
					return status.Errorf(codes.InvalidArgument, "a unique hardware serial is required for master token adoption (unsupported device)")
				}

				// Get or create node by hardware serial (from fingerprint)
				defaults := services.NodeDefaults{
					DefaultUsername:           masterToken.DefaultUsername,
					DefaultPassword:           masterToken.DefaultPassword,
					DefaultPasswordUnreadable: masterToken.DefaultPasswordUnreadable,
					SSHKeyID:                  masterToken.SSHKeyID,
					OUID:                      masterToken.OUID,
				}
				connectedNode, err = s.nodeService.ConnectWithMasterToken(ctx, masterToken.ID, accountUUID, domainFingerprint, defaults)
				if err != nil {
					if st, ok := deviceCapStatus(err); ok {
						// Parked like the serial conflict: the device asks
						// again on a refusal it cannot act on, and the park
						// bounds that to one handshake per park per device.
						// No ERROR line here: the service records the refusal
						// once per device per hour.
						s.parkBeforeReject(ctx)
						return st.Err()
					}
					s.logger.Error("master token connection failed", "error", err, "hardware_serial", domainFingerprint.HardwareSerial)
					if errors.Is(err, app_errors.ErrHardwareSerialConflict) {
						s.parkBeforeReject(ctx)
						return status.Error(codes.PermissionDenied, "device cannot be adopted into this account")
					}
					// Fixed string: the internal error text must not reach a
					// cross-tenant caller (it may name another account's state).
					return status.Error(codes.Unauthenticated, "connection failed")
				}

				// Update node ID for this connection
				nodeUUID = connectedNode.ID
				nodeID = nodeUUID.String()
				isPendingApproval = connectedNode.ApprovalStatus == node.ApprovalStatusPendingApproval
				adoptedViaMasterToken = true

				s.logger.Info("master token connection established",
					"node_id", nodeID,
					"account_id", accountID,
					"hardware_serial", domainFingerprint.HardwareSerial,
					"pending_approval", isPendingApproval)
			} else {
				// Standard node token mode - validate using existing flow.
				// An empty token is refused before any lookup: it can never be
				// a node's credential, and the service refuses it too.
				if msg.UniqueToken == "" {
					s.noteAdoptionRejection(ctx, domainFingerprint.HardwareSerial, peerIP(ctx), "connection init carried no node token")
					return status.Errorf(codes.Unauthenticated, "connection failed")
				}
				connectCmd := node_connect.NodeConnectCommand{
					NodeID:      nodeUUID,
					AccountID:   accountUUID,
					UniqueToken: msg.UniqueToken,
					Fingerprint: domainFingerprint,
				}

				connectedNode, err = s.nodeService.Connect(ctx, connectCmd)
				if err != nil {
					// Throttled like the master-token arm, and for a reason that
					// grows: the identity handoff converts every master-token
					// adoptee to node-token mode, so THIS is the branch a
					// retrying device will hit from now on. A node whose own
					// token stops working (node deleted, account purged,
					// fingerprint rebound) reproduces the same ~50/min ERROR
					// storm the throttle was written for, on the path that is
					// becoming the normal one.
					s.noteAdoptionRejection(ctx, domainFingerprint.HardwareSerial, peerIP(ctx), err.Error())
					if errors.Is(err, app_errors.ErrHardwareSerialConflict) {
						s.parkBeforeReject(ctx)
						return status.Error(codes.PermissionDenied, "device cannot be adopted into this account")
					}
					// The first connect of a row that is not yet admitted is
					// its admission; at the cap the row is left as it was and
					// the device is told so, parked like the master-token arm.
					if st, ok := deviceCapStatus(err); ok {
						s.parkBeforeReject(ctx)
						return st.Err()
					}
					// A DELETED node's device lands here forever. Leave a
					// "still trying" mark on its record so the operator can
					// see it — after an in-memory throttle, because the
					// service's check costs a row read and a decrypt and the
					// device redials about once a second. Nothing about the
					// refusal changes: same error, no stream, no row.
					if errors.Is(err, app_errors.ErrNodeNotFound) &&
						s.deletedRefusalStamps.allow(nodeUUID, time.Now()) {
						s.nodeService.NoteDeletedNodeRefused(ctx, accountUUID, nodeUUID,
							msg.UniqueToken, domainFingerprint.HardwareSerial)
					}
					return status.Errorf(codes.Unauthenticated, "connection failed")
				}
			}

			// Connection is authenticated (token/master-token validated above) —
			// disarm the pre-auth reaper and hand back the pre-auth slot. An
			// agent stream then lives for days, so holding the slot would turn a
			// cap on unauthenticated peers into a cap on the fleet.
			authed.Store(true)
			releasePreAuth()

			// Take this node's authenticated-stream slot. Nothing counted past
			// the pre-auth gate, so one valid node credential — a shell on one
			// device is enough — could open streams without limit, and each one
			// costs a goroutine, a writer and a stream that no reaper here can
			// reach once a newer stream displaces it in the registry.
			//
			// Refused rather than queued, and the NEW stream is what is
			// refused: nothing here holds another stream's cancel func, so a
			// peer holding one open and saying nothing cannot be made to go
			// away from this call site. (Its OWN handler can now end it — the
			// message loop selects against its context rather than blocking in
			// Recv — but that is a different goroutine's decision.) The refusal
			// is self-healing: the agent's own backoff retries, and the
			// transport keepalive reaps whatever was wedged in the meantime.
			if release, inFlight, ok := s.nodeStreams.acquire(nodeID); ok {
				releaseNodeStream = release
			} else {
				s.logger.WarnContext(ctx, "refusing gRPC stream: this node already holds the maximum number of authenticated streams",
					"node_id", nodeID, "account_id", accountID, "in_flight", inFlight)
				return status.Error(codes.ResourceExhausted, "too many concurrent streams for this node; retry shortly")
			}
			pacer = s.newInboundPacer()
			sysInfoPacer = newSystemInfoPacer()

			// Update agent info and set status to online
			now := time.Now()
			if err := s.nodeService.UpdateAgentInfo(ctx, nodeUUID, payload.Connection.AgentVersion, normalizeAgentKind(payload.Connection.AgentKind), payload.Connection.LedSources, now); err != nil {
				s.logger.Error("failed to update agent info", "node_id", nodeID, "error", err)
			}

			// Compare the reported agent process instance id against the
			// previous session's BEFORE sweeping stale commands: the verdict is
			// what lets the sweep tell a surviving process (leave its
			// in_progress work alone) from a restarted one (that work is gone).
			// An error degrades to Unknown — the grace heuristic — never to
			// Changed, which would fail live work over a read hiccup.
			continuity := node.AgentInstanceUnknown
			if verdict, err := s.nodeService.RotateAgentInstance(ctx, nodeUUID, payload.Connection.AgentInstanceId); err != nil {
				s.logger.Warn("failed to rotate agent instance id", "node_id", nodeID, "error", err)
			} else {
				continuity = verdict
			}

			// The node is back — whatever we were waiting on it to return
			// from has returned.
			if err := s.nodeService.ClearAwaitingReturn(ctx, nodeUUID); err != nil {
				s.logger.Warn("failed to clear awaiting-return", "node_id", nodeID, "error", err)
			}

			// Always set status to online when node reconnects, regardless of previous status
			if err := s.updateNodeStatus(ctx, nodeID, accountID, string(node.StatusOnline)); err != nil {
				s.logger.Error("failed to update node status to online", "node_id", nodeID, "error", err)
			}

			// A fresh connection after a reboot means the reboot is done. Complete
			// any still-pending reboot commands regardless of the previous status:
			// a rebooting node is often marked offline by the heartbeat monitor
			// before it reconnects, so it never passes through "restarting" here
			// and the command would otherwise hang in_progress forever.
			// CompleteRebootCommandsForNode is a no-op when nothing is pending.
			if s.nodeCommandService != nil {
				if err := s.nodeCommandService.CompleteRebootCommandsForNode(ctx, nodeUUID); err != nil {
					s.logger.Warn("failed to complete reboot commands for node", "node_id", nodeID, "error", err)
				}
				// Clear non-reboot commands abandoned by the previous session so a
				// command the old agent never acknowledged (e.g. one it couldn't
				// decode) doesn't hang and keep the node pinned in "maintenance".
				if err := s.nodeCommandService.FailStalePendingCommandsForNode(ctx, nodeUUID, continuity); err != nil {
					s.logger.Warn("failed to clear stale pending commands for node", "node_id", nodeID, "error", err)
				}
			}

			// Hand a master-token-adopted node its OWN credentials, once, before
			// anything else can write to this stream.
			//
			// Sent here rather than after registration on purpose: from the
			// moment the connection is in s.connections, queued-command
			// redelivery and the dispatch path may Send concurrently, and
			// grpc-go forbids concurrent SendMsg on one stream. Nothing else
			// holds this stream yet at this point.
			//
			// This is the ONE place that may write to the stream directly, and
			// only because it runs before the connection — and therefore its
			// writer goroutine — exists. Everything after registration goes
			// through StreamConnection.Send. Moving this below registration
			// without switching it to Send reintroduces the concurrent-SendMsg crash.
			//
			// Without this the shared enrolment token becomes the permanent
			// credential of every device adopted with it: when one expires the
			// node goes offline for good and needs physical access to recover,
			// because the platform cannot command a
			// node it cannot authenticate. An agent too old to understand the
			// message ignores an unknown oneof arm and keeps working exactly as
			// before, so this is safe to ship ahead of the agent.
			// NOT sent to a node awaiting approval. The identity is a permanent
			// credential, and node-token `Connect` checks only the token and the
			// fingerprint — it has no approval check at all. So handing this to
			// a pending device would survive the operator pressing Reject: the
			// status flips but the row and its token remain, and the device
			// would reconnect in node-token mode forever. Master-token connect
			// is the only path that enforces the decision, which is exactly why
			// a rejected device must stay on it.
			if adoptedViaMasterToken && !isPendingApproval && connectedNode != nil && connectedNode.UniqueToken != "" {
				if err := stream.Send(&proto.PlatformToNode{
					Payload: &proto.PlatformToNode_IdentityAssignment{
						IdentityAssignment: &proto.IdentityAssignment{
							NodeId:      nodeID,
							AccountId:   accountID,
							UniqueToken: connectedNode.UniqueToken,
						},
					},
				}); err != nil {
					// Not fatal: the node is connected and working. It simply
					// stays on the master token until a later connect succeeds
					// in handing it over.
					s.logger.Warn("failed to send identity assignment", "node_id", nodeID, "error", err)
				} else {
					s.logger.Info("node identity handed to master-token adoptee", "node_id", nodeID, "account_id", accountID)
				}
			}

			// Register connection. The node's OU is captured here so pushed
			// telemetry can be OU-scoped without a lookup per sample. This
			// pre-publish read fills the FIRST value only; an assignment that
			// commits between it and the publish below calls UpdateNodeOU
			// against a registry that does not hold this connection yet, so
			// the scope is re-read once more right after the publish.
			var connOU *uuid.UUID
			if accountUUID, perr := uuid.Parse(accountID); perr == nil {
				if n, nerr := s.nodeService.GetByIDAndAccount(ctx, nodeUUID, accountUUID); nerr == nil && n != nil {
					connOU = n.OUID
				}
			}
			connection = newStreamConnection(ctx, s.logger, nodeID, accountID, connOU, stream, now, cancel)

			s.mu.Lock()
			displaced := s.connections[nodeID]
			s.connections[nodeID] = connection
			s.mu.Unlock()

			// Post-publish refresh: any assignment committed before this read
			// is captured by it, and any committed after finds the published
			// connection. See refreshConnectionScope for the invariant.
			s.refreshConnectionScope(ctx, connection, connOU)

			// A node that reconnects before its previous stream has torn down
			// leaves that StreamConnection in nobody's hands: the assignment
			// above is the only reference the process had, and EVERY reaper is
			// registry-scoped — the monitor sweeps the snapshot, Stop() ranges
			// the map, and handleDisconnection deliberately does nothing on a
			// stale teardown. So the displaced connection's writer goroutine ran
			// on with a buffer nothing would ever drain, until the transport
			// died.
			//
			// Stop retires that writer now. Cancel additionally ends the old
			// handler at its next inbound frame; it cannot do better than that,
			// because cancelling a context does not unblock stream.Recv. The old
			// handler's deferred handleDisconnection then takes the stale-teardown
			// branch and leaves this newer connection alone, which is what makes
			// this safe.
			if displaced != nil && displaced != connection {
				s.logger.WarnContext(ctx, "node reconnected before its previous stream ended; retiring the displaced connection",
					"node_id", nodeID, "account_id", accountID)
				displaced.Stop()
				if displaced.Cancel != nil {
					displaced.Cancel()
				}
			}

			s.logger.Info("node connected", "node_id", nodeID, "account_id", accountID, "agent_version", payload.Connection.AgentVersion)

			// Deliver whatever was queued while the node was away. This MUST run
			// after the connection is registered above: the dispatch path checks
			// IsNodeConnected, so redelivering any earlier would fail every
			// command instantly and silently defeat the queue.
			//
			// Not to a watcher: the binary executes nothing, so a redelivered
			// command would sit "sent" until the abandonment sweep. Anything
			// queued from the node's full-agent days waits — the next FULL
			// connect (recovery, or a reinstalled full agent) delivers it, or
			// the queue TTL expires it honestly.
			if s.nodeCommandService != nil && normalizeAgentKind(payload.Connection.AgentKind) != node.AgentKindWatcher {
				if err := s.nodeCommandService.RedeliverQueuedCommandsForNode(ctx, nodeUUID); err != nil {
					s.logger.Warn("failed to redeliver queued commands for node", "node_id", nodeID, "error", err)
				}
			}

		case *proto.NodeToPlatform_Heartbeat:
			// Update heartbeat
			if connection != nil {
				now := time.Now()
				connection.Touch(now)

				// Update database
				// TODO: move to redis in the future (starting from 100 nodes)
				nodeUUID, _ := uuid.Parse(nodeID)
				if err := s.nodeService.UpdateHeartbeat(ctx, nodeUUID, now); err != nil {
					// A heartbeat for a node that no longer has a row ends the
					// stream. This is the general cure for a gap in deletion: nothing
					// in the delete path tears a device's stream down, so a deleted
					// node kept heartbeating into a platform that had no row for it,
					// and — because a live stream never re-sends ConnectionInit —
					// a RESTORED node stayed `offline` with an empty agent_version
					// until something else broke the connection — in practice a
					// backend restart.
					//
					// Done HERE rather than in node.Service.Delete on purpose. The
					// heartbeat is the one moment the platform re-asks "does this
					// node still exist?" for free — the repository already answers
					// ErrNodeNotFound — so this covers every way a row can go
					// (delete, account purge, an out-of-band purge) instead of the
					// one path someone remembered to wire. The cost is that it is
					// bounded by the heartbeat interval (30s), not immediate; the
					// release_identity settle below is the prompt path for the case
					// that needs one.
					//
					// Returning is what actually ends the RPC: cancelling the
					// derived context does NOT unblock stream.Recv, so a handler
					// that merely cancels sits here until the next frame anyway.
					if errors.Is(err, app_errors.ErrNodeNotFound) {
						s.logger.InfoContext(ctx, "ending the stream of a node that no longer exists; the device will retry and be refused until it is restored or re-provisioned",
							"node_id", nodeID)
						return status.Error(codes.NotFound, "node no longer exists")
					}
					s.logger.Error("failed to update heartbeat", "node_id", nodeID, "error", err)
				}

				s.logger.Debug("heartbeat received", "node_id", nodeID, "sequence", payload.Heartbeat.SequenceNumber)
			}

		case *proto.NodeToPlatform_MetricsUpdate:
			// Live utilization sample: count as activity, store in the ring,
			// and push to the UI. Never persisted to the database.
			if connection != nil {
				connection.Touch(time.Now())
				s.recordMetrics(nodeID, accountID, payload.MetricsUpdate)
			}

		case *proto.NodeToPlatform_NodeEvent:
			// Spontaneous device fact (a human pressed the physical button).
			// Rate-limited HERE as well as on the agent: the agent's debounce
			// is a courtesy, this bound is the guarantee — the event fans out
			// to every connected browser of the account. Never persisted.
			if connection != nil {
				connection.Touch(time.Now())
				s.handleNodeEvent(ctx, nodeID, accountID, payload.NodeEvent)
			}

		case *proto.NodeToPlatform_TerminalOutput:
			// PTY output from the device → route to the browser bridge, but only if
			// the session belongs to THIS authenticated node (reject frames naming
			// another node's session).
			out := payload.TerminalOutput
			if s.terminalOwnedBy(out.GetSessionId(), nodeID) {
				s.deliverTerminalOutput(out.GetSessionId(), out.GetData())
			} else {
				s.warnDroppedTerminalFrame("output", nodeID, out.GetSessionId())
			}

		case *proto.NodeToPlatform_TerminalClosed:
			tc := payload.TerminalClosed
			if s.terminalOwnedBy(tc.GetSessionId(), nodeID) {
				s.endTerminal(tc.GetSessionId(), services.TerminalExit{
					Code:  tc.GetExitCode(),
					Error: tc.GetError(),
				})
			} else {
				s.warnDroppedTerminalFrame("close", nodeID, tc.GetSessionId())
			}

		case *proto.NodeToPlatform_StatusUpdate:
			// Handle status update
			if connection != nil {
				now := time.Now()
				connection.Touch(now)
				s.handleAgentStatusUpdate(ctx, nodeID, accountID, payload.StatusUpdate.Status, payload.StatusUpdate.Message, now)
			}

		case *proto.NodeToPlatform_SystemInfoUpdate:
			// Handle system info update (storage, network, hardware, OS)
			// Note: We can handle this even if connection is nil, as long as we have nodeID and accountID from the message
			if nodeID != "" && accountID != "" && s.nodeService != nil {
				now := time.Now()
				if connection != nil {
					connection.Touch(now)
				}

				update := payload.SystemInfoUpdate
				if size, tooLarge := systemInfoReportTooLarge(update); tooLarge {
					s.warnOversizedReport("system_info", nodeID, size)
					break
				}
				if sysInfoPacer != nil {
					if err := sysInfoPacer.Wait(ctx); err != nil {
						s.logger.InfoContext(ctx, "stream ended while pacing system-info reports", "node_id", nodeID, "error", err)
						return err
					}
				}
				deviceCount := 0
				if update.StorageDevices != nil {
					deviceCount = len(update.StorageDevices)
				}

				s.logger.Info("system info update received",
					"node_id", nodeID,
					"device_count", deviceCount,
					"has_network", update.Network != nil,
					"has_hardware", update.Hardware != nil,
					"has_os", update.Os != nil)

				// Convert proto StorageDevice map to domain StorageDevice map
				devices := make(map[string]*node.StorageDevice)
				if update.StorageDevices != nil {
					for k, v := range update.StorageDevices {
						if v != nil {
							// Convert partitions
							partitions := make([]node.StoragePartition, 0, len(v.Partitions))
							for _, p := range v.Partitions {
								if p != nil {
									partitions = append(partitions, node.StoragePartition{
										PartitionID:    p.PartitionId,
										MountPoint:     p.MountPoint,
										Filesystem:     p.Filesystem,
										UsedSpace:      p.UsedSpace,
										AvailableSpace: p.AvailableSpace,
									})
								}
							}

							devices[k] = &node.StorageDevice{
								Path:          v.Path,
								Label:         v.Label,
								Model:         v.Model,
								Vendor:        v.Vendor,
								Serial:        v.Serial,
								DeviceType:    v.DeviceType,
								TotalCapacity: v.TotalCapacity,
								IsRemovable:   v.IsRemovable,
								IsReadOnly:    v.IsReadOnly,
								Partitions:    partitions,
							}
						}
					}
				}

				// Convert proto NetworkInfo to domain NetworkInfo
				var networkInfo *node.NetworkInfo
				if update.Network != nil {
					networkInfo = &node.NetworkInfo{
						IP:       update.Network.Ip,
						Hostname: update.Network.Hostname,
					}
				}

				// Convert proto HardwareInfo to domain HardwareInfo
				var hardwareInfo *node.HardwareInfo
				if update.Hardware != nil {
					hardwareInfo = &node.HardwareInfo{}

					// Convert CPU info
					if update.Hardware.Cpu != nil {
						hardwareInfo.CPU = &node.CPUInfo{
							Model:  update.Hardware.Cpu.Model,
							Vendor: update.Hardware.Cpu.Vendor,
							Cores:  update.Hardware.Cpu.Cores,
							Info:   update.Hardware.Cpu.Info,
							// This converter is hand-written: `make proto/generate`
							// does NOT touch it, so a new proto field silently
							// arrives as a zero value until it is added here.
							BoardRevision: update.Hardware.Cpu.BoardRevision,
						}
					}

					// Convert RAM info
					if update.Hardware.Ram != nil {
						hardwareInfo.RAM = &node.RAMInfo{
							Size: update.Hardware.Ram.Size,
						}
					}

					// Convert TPM info
					if update.Hardware.Tpm != nil {
						hardwareInfo.TPM = &node.TPMInfo{
							Present:     update.Hardware.Tpm.Present,
							Version:     update.Hardware.Tpm.Version,
							Model:       update.Hardware.Tpm.Model,
							Description: update.Hardware.Tpm.Description,
						}
					}

					// HAT ID EEPROM (nil = no HAT). Sanitized here — the values
					// come from an EEPROM any vendor programs (board-revision
					// canon: raw on the wire, interpreted on the platform).
					if update.Hardware.Hat != nil {
						hardwareInfo.HAT = &node.HATInfo{
							Vendor:     node.SanitizeHATField(update.Hardware.Hat.Vendor),
							Product:    node.SanitizeHATField(update.Hardware.Hat.Product),
							ProductID:  node.SanitizeHATField(update.Hardware.Hat.ProductId),
							ProductVer: node.SanitizeHATField(update.Hardware.Hat.ProductVer),
							UUID:       node.SanitizeHATField(update.Hardware.Hat.Uuid),
						}
					}

					// Convert Boot info
					if update.Hardware.Boot != nil {
						bootInfo := &node.BootInfo{
							CurrentBootDevice:   update.Hardware.Boot.CurrentBootDevice,
							BootDevicesOrder:    update.Hardware.Boot.BootDevicesOrder,
							BootHTTPHost:        update.Hardware.Boot.BootHttpHost,
							BootHTTPPath:        update.Hardware.Boot.BootHttpPath,
							BootloaderVersion:   update.Hardware.Boot.BootloaderVersion,
							BootloaderTimestamp: update.Hardware.Boot.BootloaderTimestamp,
							RecoveryBootstrap:   update.Hardware.Boot.RecoveryBootstrap,
							RootDevice:          update.Hardware.Boot.RootDevice,
							RootPersistence:     update.Hardware.Boot.RootPersistence,
						}
						// Resolve booted_at. Prefer computing it from the
						// device-reported uptime_seconds anchored to the SERVER
						// clock at receipt time: the server clock is authoritative
						// and this yields a stale-proof absolute timestamp even on
						// RTC-less/netbooted devices whose own wall clock is unsynced
						// (where the device's booted_at collapses to ~0). Fall back
						// to the device's booted_at for older agents that don't
						// report uptime_seconds.
						if update.Hardware.Boot.UptimeSeconds > 0 {
							uptimeSeconds := update.Hardware.Boot.UptimeSeconds
							bootInfo.UptimeSeconds = &uptimeSeconds
							bootedAt := time.Now().Unix() - uptimeSeconds
							bootInfo.BootedAt = &bootedAt
						} else if update.Hardware.Boot.BootedAt != 0 {
							bootedAt := update.Hardware.Boot.BootedAt
							bootInfo.BootedAt = &bootedAt
						}
						hardwareInfo.Boot = bootInfo
					}

					// Convert EEPROM info
					if update.Hardware.Eeprom != nil {
						hardwareInfo.EEPROM = &node.EEPROMInfo{
							Enabled:     update.Hardware.Eeprom.Enabled,
							Description: update.Hardware.Eeprom.Description,
						}
					}

					// Convert power/throttling info. The nil check is the feature,
					// not boilerplate: a device that cannot read `vcgencmd
					// get_throttled` sends no PowerInfo, and that must stay
					// distinguishable from a device reporting a clean 0. Decoding a
					// zero mask into "no problems" for an agent below 0.5.4 would
					// claim the supply is fine on every node in the fleet.
					if update.Hardware.Power != nil {
						hardwareInfo.Power = node.NewPowerInfo(update.Hardware.Power.ThrottledFlags)
					}

					// Attached non-storage USB devices. Nil stays nil: an agent
					// older than 0.5.5 sends none, and an empty list would claim
					// "nothing is plugged in" about a device that never looked.
					for _, u := range update.Hardware.Usb {
						if u == nil {
							continue
						}
						hardwareInfo.USB = append(hardwareInfo.USB, node.USBDevice{
							VendorID:     u.VendorId,
							ProductID:    u.ProductId,
							Manufacturer: u.Manufacturer,
							Product:      u.Product,
							ClassCode:    u.ClassCode,
							Speed:        u.Speed,
							Port:         u.Port,
						})
					}
				}

				// Convert proto OSInfo to domain OSInfo
				var osInfo *node.OSInfo
				if update.Os != nil {
					osInfo = &node.OSInfo{
						Version: update.Os.Version,
						Details: update.Os.Details,
					}
				}

				// Build domain SystemInfo
				systemInfo := &node.SystemInfo{
					Network:        networkInfo,
					Hardware:       hardwareInfo,
					OS:             osInfo,
					StorageDevices: devices,
				}

				// Parse UUIDs
				nodeUUID, err := uuid.Parse(nodeID)
				if err != nil {
					s.logger.Error("failed to parse node ID", "node_id", nodeID, "error", err)
				} else {
					accountUUID, err := uuid.Parse(accountID)
					if err != nil {
						s.logger.Error("failed to parse account ID", "account_id", accountID, "error", err)
					} else {
						// Update system info in database
						if err := s.nodeService.UpdateSystemInfo(ctx, nodeUUID, accountUUID, systemInfo); err != nil {
							s.logger.Error("failed to update system info", "node_id", nodeID, "error", err)
						} else {
							s.logger.Info("system info updated successfully", "node_id", nodeID, "device_count", deviceCount)
							// This is the moment the node's boot stamp is FRESH,
							// and therefore the only moment a pending reboot can
							// be settled honestly. At ConnectionInit the stamp is
							// still the previous boot, so the completion there
							// finds nothing and this one does the work — the same
							// reason the managed-OS hold is released here.
							if s.nodeCommandService != nil {
								if err := s.nodeCommandService.CompleteRebootCommandsForNode(ctx, nodeUUID); err != nil {
									s.logger.Warn("failed to complete reboot commands after a system info report",
										"node_id", nodeID, "error", err)
								}
							}
						}

						// Update boot order in dedicated field if available (store as array directly)
						if update.Hardware != nil {
							if update.Hardware.Boot != nil && len(update.Hardware.Boot.BootDevicesOrder) > 0 {
								if err := s.nodeService.UpdateBootOrderFromAgent(ctx, nodeUUID, accountUUID, update.Hardware.Boot.BootDevicesOrder); err != nil {
									s.logger.Warn("failed to update boot order", "node_id", nodeID, "error", err)
								} else {
									s.logger.Info("boot order updated successfully", "node_id", nodeID, "boot_order", update.Hardware.Boot.BootDevicesOrder, "current_boot_device", update.Hardware.Boot.CurrentBootDevice)
								}
							} else {
								s.logger.Debug("boot order is empty, skipping update", "node_id", nodeID)
							}
						} else {
							s.logger.Debug("hardware info or boot info is nil, skipping boot order update", "node_id", nodeID)
						}

						// Update last seen timestamp
						if err := s.nodeService.UpdateAgentInfo(ctx, nodeUUID, "", "", nil, now); err != nil {
							s.logger.Error("failed to update last seen", "node_id", nodeID, "error", err)
						}

						// One-shot network-first EEPROM provisioning: SoC + boot device
						// become known only now (SystemInfo is nil at connect), so this
						// is the earliest safe trigger for single-node onboarding.
						s.maybeAutoProvisionEEPROM(nodeUUID, accountUUID)
					}
				}
			} else {
				s.logger.Warn("system info update ignored - missing nodeID, accountID, or nodeService",
					"node_id", nodeID, "account_id", accountID, "has_service", s.nodeService != nil)
			}

		case *proto.NodeToPlatform_FileChangedEvent:
			// Handle file changed event from agent
			if nodeID != "" && accountID != "" {
				now := time.Now()
				if connection != nil {
					connection.Touch(now)
				}

				event := payload.FileChangedEvent
				s.logger.Info("file changed event received",
					"node_id", nodeID,
					"path", event.Path,
					"version", event.Version,
					"status", event.Status)

				nodeUUID, err := uuid.Parse(nodeID)
				if err != nil {
					s.logger.Error("failed to parse node ID", "node_id", nodeID, "error", err)
				} else {
					// Process file changed event
					if s.nodeFileService != nil {
						var errorMsg *string
						if event.ErrorMessage != "" {
							errorMsg = &event.ErrorMessage
						}

						_, err := s.nodeFileService.CreateOrUpdateFromAgent(
							ctx,
							nodeUUID,
							event.Path,
							event.Content,
							event.Hash,
							int(event.Version),
							event.Status,
							errorMsg,
						)
						if err != nil {
							s.logger.Error("failed to process file changed event", "node_id", nodeID, "error", err)
						} else {
							s.logger.Info("file changed event processed successfully", "node_id", nodeID, "path", event.Path, "version", event.Version)
						}
					} else {
						s.logger.Warn("file service not available", "node_id", nodeID)
					}
				}
			}

		case *proto.NodeToPlatform_CommandStatusUpdate:
			// Handle command status update
			if connection != nil {
				now := time.Now()
				connection.Touch(now)

				update := payload.CommandStatusUpdate
				commandID, err := uuid.Parse(update.CommandId)
				if err != nil {
					s.logger.Error("invalid command_id in status update", "command_id", update.CommandId, "error", err)
					break
				}

				// Bind the frame to THIS stream's authenticated node: a command
				// belongs to exactly one node, so an agent must not drive the
				// status/result of another node's command (which would flip that
				// node's state and feed the workflow engine).
				streamNodeUUID, nerr := uuid.Parse(nodeID)
				streamAccUUID, aerr := uuid.Parse(accountID)
				if nerr != nil || aerr != nil {
					s.logger.Error("status update before authenticated connection", "command_id", update.CommandId)
					break
				}
				owned, oerr := s.nodeCommandService.GetNodeCommandByID(ctx, streamAccUUID, commandID)
				if oerr != nil || owned.NodeID != streamNodeUUID {
					s.logger.Warn("rejecting command status update for a command not owned by the connected node",
						"command_id", update.CommandId, "stream_node_id", nodeID)
					break
				}

				// Update node command status via node command service (direct call, no queue)
				progress := int(update.Progress)
				if progress < 0 {
					progress = 0 // Handle -1 as "not applicable"
				}
				applied, err := s.nodeCommandService.UpdateNodeCommandStatusWithResult(ctx, commandID, update.Status, progress, update.Details, commandResultToStore(update.Status, update.Result))
				if err != nil {
					s.logger.Error("failed to update command status", "command_id", update.CommandId, "error", err)
				} else if applied {
					// A report the service ignored (the command is already
					// settled) changed nothing and is logged there as ignored.
					s.logger.Info("command status updated",
						"command_id", update.CommandId,
						"status", update.Status,
						"progress", update.Progress)
				}

				// A tracked-file write reports through the command, not through a
				// FileChangedEvent (only the legacy update_file emits one), so the
				// version would sit at "applying" forever without this. The same
				// hook drops a tracked file once its delete actually lands.
				//
				// All four settle a TRANSITION, so they run only when the report
				// actually moved the command. Without that, a node replaying a
				// terminal frame for one of its own long-finished commands
				// re-drives them — and settleWatchdogApply re-stamps the applied
				// hash, which clears a recorded breaker disarm: the device would
				// be erasing the one signal that says it disarmed itself.
				if applied {
					s.settleTrackedFileWrite(ctx, owned, update.Status, update.Details)
					s.settleTrackedFileDelete(ctx, owned, update.Status)
					s.settleTrackedFileRead(ctx, owned, update.Status, update.Result)
					s.settleWatchdogApply(ctx, owned, update.Status, update.Details, update.Result)
					s.settleAccessApply(ctx, owned, update.Status, update.Result)
					s.settleDeviceProfileApply(ctx, owned, update.Status)
					s.settleBootloaderFlash(ctx, owned, update.Status)
					s.settleLEDApply(ctx, owned, update.Status)
					// A failed identify must clear the registry's claimed blink,
					// or every freshly opened browser hydrates a phantom lamp.
					if owned.Slug == node_command.SlugIdentify && update.Status == "failed" && s.nodeService != nil {
						s.nodeService.RecordIdentifyFailed(owned.AccountID, owned.NodeID)
					}
					if s.utilitySettler != nil && owned.Slug == node_command.SlugApplyUtilityConfig {
						s.utilitySettler.SettleApply(ctx, owned, update.Status, update.Result)
					}
					s.settleReimage(ctx, owned, update.Status)
				}

				// A device that has just deleted its own adoption file must not
				// keep the stream it opened with that identity.
				//
				// The agent deliberately does NOT disconnect itself: a
				// self-inflicted disconnect would race the very status report that
				// tells us the release happened, and we would never learn whether
				// it did. So the platform closes the stream, and only once the
				// report is in and says success — on a failure the device still
				// holds its identity, and the heartbeat rule above will end the
				// stream anyway if the node row is gone.
				//
				// Checked before UpdateAgentInfo below, which would otherwise be a
				// write against a node row that a forced delete has already
				// removed.
				if owned.Slug == node_command.SlugReleaseIdentity &&
					node_command.NodeCommandStatus(update.Status) == node_command.StatusSuccess {
					// The node row is gone by now; what remains is the tombstone,
					// and "requested" becomes "the device reported it did" there.
					if s.nodeService != nil {
						if err := s.nodeService.RecordIdentityReleased(ctx, owned.AccountID, owned.NodeID); err != nil {
							s.logger.WarnContext(ctx, "failed to record the identity release on the deleted node's record",
								"error", err, "node_id", nodeID, "command_id", update.CommandId)
						}
					}
					s.logger.InfoContext(ctx, "node released its adoption identity; closing its stream",
						"node_id", nodeID, "command_id", update.CommandId)
					return status.Error(codes.Unauthenticated, "adoption identity released")
				}

				// Update last seen timestamp
				nodeUUID, _ := uuid.Parse(nodeID)
				if err := s.nodeService.UpdateAgentInfo(ctx, nodeUUID, "", "", nil, now); err != nil {
					s.logger.Error("failed to update last seen", "node_id", nodeID, "error", err)
				}
				break
			}
		}
	}
}

// settleTrackedFileWrite moves a tracked file's version out of "applying" once
// the write_file command that carried it reaches a terminal state.
func (s *Server) settleTrackedFileWrite(ctx context.Context, cmd *node_command.NodeCommand, status, details string) {
	if cmd == nil || cmd.Slug != node_command.SlugWriteFile || s.nodeFileService == nil {
		return
	}
	path, _ := cmd.Payload["path"].(string)
	versionF, ok := cmd.Payload["version"].(float64)
	if path == "" || !ok {
		return
	}
	var fileStatus string
	var errMsg *string
	switch status {
	case "success":
		fileStatus = node_file.StatusApplied
	case "failed":
		fileStatus = node_file.StatusFailed
		if details != "" {
			d := details
			errMsg = &d
		}
	default:
		return
	}
	if err := s.nodeFileService.UpdateStatus(ctx, cmd.NodeID, path, int(versionF), fileStatus, errMsg); err != nil {
		s.logger.Warn("failed to settle tracked file status",
			"node_id", cmd.NodeID, "path", path, "version", int(versionF), "error", err)
	}
	// "Last applied" is a fact about the file, not about a version row, and this
	// is the only moment the platform knows it: the device has confirmed the
	// write. Nothing wrote it before, so the column and the field the API exposes
	// from it were null for every user-tracked file that ever existed — the one
	// writer was the watchdog's rendered-config path. Bookkeeping: a failure here
	// must not turn a successful write into a failed command.
	if fileStatus == node_file.StatusApplied && s.nodeService != nil {
		if err := s.nodeService.MarkTrackedFileApplied(ctx, cmd.AccountID, cmd.NodeID, path); err != nil {
			s.logger.Warn("failed to record the tracked file's applied time",
				"node_id", cmd.NodeID, "path", path, "error", err)
		}
	}
}

// settleTrackedFileRead records a file the operator asked to track (or
// refresh) as soon as the device's read arrives. Tracking used to complete
// only when a client polled for the result, so a browser closed in between
// left a successful read and no tracked file. Bookkeeping: a failure here is
// logged and never changes the command's own outcome.
func (s *Server) settleTrackedFileRead(ctx context.Context, cmd *node_command.NodeCommand, status string, result map[string]string) {
	if cmd == nil || cmd.Slug != node_command.SlugReadFile || status != "success" || s.nodeService == nil {
		return
	}
	if err := s.nodeService.CompleteTrackedFileRead(ctx, cmd, result); err != nil {
		s.logger.Warn("failed to record a tracked file from its read",
			"node_id", cmd.NodeID, "command_id", cmd.ID, "error", err)
	}
}

// settleTrackedFileDelete stops tracking a file once the device confirms it is
// gone. Untracking at dispatch time would lose the record even when the delete
// failed, leaving a file on the device that the platform no longer shows.
func (s *Server) settleTrackedFileDelete(ctx context.Context, cmd *node_command.NodeCommand, status string) {
	if cmd == nil || cmd.Slug != node_command.SlugDeleteFile || status != "success" || s.nodeService == nil {
		return
	}
	path, _ := cmd.Payload["path"].(string)
	if path == "" {
		return
	}
	if err := s.nodeService.UntrackNodeFile(ctx, cmd.AccountID, cmd.NodeID, path); err != nil {
		s.logger.Warn("failed to untrack a file deleted on the device",
			"node_id", cmd.NodeID, "path", path, "error", err)
	}
}

// settleWatchdogApply settles a terminal apply_watchdog_config command. On
// success it records the confirmed config hash (so the UI can tell "asked for"
// from "running") and stores the rendered /etc/watchdog.conf the agent returned
// as a platform tracked-file version. On failure with a machine refusal code it
// records the refusal as the node's own fact; any other failure already
// surfaces through the generic command surface.
// settleDeviceProfileApply records the applied hash when an
// apply_device_profile reaches terminal success. The hash comes from the
// command's OWN payload — what was dispatched — never from a fresh render:
// the equipage may have been edited while the command sat queued, and marking
// the newer desired state applied on the strength of the older dispatch is
// exactly the confusion the ladder exists to expose. A failed apply records
// nothing: the ladder stays at declared/update_pending, which is the truth.
func (s *Server) settleDeviceProfileApply(ctx context.Context, cmd *node_command.NodeCommand, status string) {
	if cmd == nil || cmd.Slug != node_command.SlugApplyDeviceProfile || s.nodeService == nil {
		return
	}
	if status != "success" {
		return
	}
	hash, _ := cmd.Payload["content_hash"].(string)
	if hash == "" {
		return
	}
	if err := s.nodeService.RecordDeviceProfileApplied(ctx, cmd.AccountID, cmd.NodeID, hash); err != nil {
		s.logger.WarnContext(ctx, "failed to record device profile applied hash",
			"error", err, "node_id", cmd.NodeID, "command_id", cmd.ID)
	}
}

// settleBootloaderFlash stamps the node when an update_boot_order reaches
// terminal success. The EEPROM is written at command time and read at boot
// time; recording the moment durably is what lets every later view answer
// "flashed, reboot still owed" without re-deriving it from the command log —
// which loses the answer whenever the log has not loaded yet. A failed flash records nothing.
func (s *Server) settleBootloaderFlash(ctx context.Context, cmd *node_command.NodeCommand, status string) {
	if cmd == nil || cmd.Slug != node_command.SlugUpdateBootOrder || s.nodeService == nil {
		return
	}
	if status != "success" {
		return
	}
	if err := s.nodeService.RecordBootloaderFlashed(ctx, cmd.AccountID, cmd.NodeID); err != nil {
		s.logger.WarnContext(ctx, "failed to record the EEPROM flash time",
			"error", err, "node_id", cmd.NodeID, "command_id", cmd.ID)
	}
}

// settleLEDApply records the applied LED hash from the DISPATCH payload on the
// command's terminal success (the device-profile pattern: the settle records
// what was sent, never a later render). A failed apply records nothing.
// settleReimage forgets what the platform believed the device had applied, once
// a new OS image has replaced the disk it applied it to.
//
// install_os SUCCESS only. erase_drive is deliberately not here: it names a
// target device and may be pointed at a data disk while the OS keeps running,
// so clearing on it would report a converged node as unconverged. install_os
// exists to replace the running system.
//
// Access is not among the markers cleared — it already self-heals through
// accessReapplyNeeded, which compares the last successful apply against the
// last successful install_os. See InvalidateAppliedStateAfterReimage.
func (s *Server) settleReimage(ctx context.Context, cmd *node_command.NodeCommand, status string) {
	if cmd == nil || cmd.Slug != node_command.SlugInstallOS || status != "success" {
		return
	}
	if s.nodeService != nil {
		if err := s.nodeService.InvalidateAppliedStateAfterReimage(ctx, cmd.AccountID, cmd.NodeID); err != nil {
			s.logger.Warn("failed to clear applied markers after a reimage",
				"node_id", cmd.NodeID, "error", err)
		}
		// A successful install settles into a self-reboot into the new OS, so
		// from this moment the platform EXPECTS the node back. If it never
		// returns, the stamp is what lets its offline read as "never came back
		// after install_os" instead of a bare offline. Cleared on reconnect.
		if err := s.nodeService.MarkAwaitingReturn(ctx, cmd.NodeID, cmd.Slug); err != nil {
			s.logger.Warn("failed to stamp awaiting-return after install",
				"node_id", cmd.NodeID, "error", err)
		}
	}
	if s.utilitySettler != nil {
		if err := s.utilitySettler.ForgetAppliedAfterReimage(ctx, cmd.AccountID, cmd.NodeID); err != nil {
			s.logger.Warn("failed to clear the utility ladder after a reimage",
				"node_id", cmd.NodeID, "error", err)
		}
	}
}

func (s *Server) settleLEDApply(ctx context.Context, cmd *node_command.NodeCommand, status string) {
	if cmd == nil || cmd.Slug != node_command.SlugApplyLEDConfig || s.nodeService == nil {
		return
	}
	if status != "success" {
		return
	}
	hash, _ := cmd.Payload["config_hash"].(string)
	if hash == "" {
		return
	}
	if err := s.nodeService.RecordLEDApplied(ctx, cmd.AccountID, cmd.NodeID, hash); err != nil {
		s.logger.WarnContext(ctx, "failed to record the applied led hash",
			"error", err, "node_id", cmd.NodeID, "command_id", cmd.ID)
	}
}

func (s *Server) settleWatchdogApply(ctx context.Context, cmd *node_command.NodeCommand, status, details string, result map[string]string) {
	if cmd == nil || cmd.Slug != node_command.SlugApplyWatchdogConfig || s.nodeService == nil {
		return
	}
	switch status {
	case "success":
		applied := result["applied_hash"]
		if applied == "" {
			applied, _ = cmd.Payload["config_hash"].(string)
		}
		if err := s.nodeService.RecordWatchdogApplied(ctx, cmd.AccountID, cmd.NodeID, applied); err != nil {
			s.logger.Warn("failed to record the applied watchdog hash", "node_id", cmd.NodeID, "error", err)
		}
		// The agent returns the exact watchdog.conf it wrote (absent/empty on
		// the disable path — nothing was written, so nothing is recorded).
		// Recording is bookkeeping: a failure is logged, never fails the settle.
		if conf := result["conf"]; conf != "" {
			if err := s.nodeService.RecordPlatformRenderedFile(ctx, cmd.AccountID, cmd.NodeID, node_file.WatchdogConfPath, conf); err != nil {
				s.logger.Warn("failed to record the rendered watchdog.conf", "node_id", cmd.NodeID, "error", err)
			}
		}
	case "failed":
		// A refusal is the agent's pre-arm verification declining a config that
		// would reboot-loop the device. The details string is operator-worded;
		// the code is the machine fact.
		if refusal := result["refusal"]; refusal != "" {
			if err := s.nodeService.RecordWatchdogRefused(ctx, cmd.AccountID, cmd.NodeID, refusal, details); err != nil {
				s.logger.Warn("failed to record the watchdog refusal", "node_id", cmd.NodeID, "error", err)
			}
		}
	}
}

// settleAccessApply records the access generation the device confirmed, which is
// what makes the applied-vs-desired self-heal possible at all: the hash has
// travelled to the agent and back since the feature shipped, and nothing read it.
//
// Two cases are deliberately NOT recorded as applied:
//
//   - A failed apply. The agent still echoes the DESIRED generation in its
//     failure report (it is the hash it was asked for, not the one it reached),
//     so trusting it would record convergence for a reconcile that did not
//     happen.
//   - A success that skipped an unmanaged user. The agent marks its own state
//     partial in that case; recording the generation would make the next push of
//     the same config a no-op, so an operator who fixes the cause and re-applies
//     would be told "already current" while nothing changed. The recorded
//     generation is cleared instead, which puts the node back to "we make no
//     claim" rather than leaving a stale one behind.
func (s *Server) settleAccessApply(ctx context.Context, cmd *node_command.NodeCommand, status string, result map[string]string) {
	// The offline (recovery-mode) apply settles through the same rules: it
	// writes the same resolved config — onto the target disk instead of the
	// live system — and reports the same generation claim, which is checked
	// against the platform's own recompute either way.
	if cmd == nil || s.nodeService == nil ||
		(cmd.Slug != node_command.SlugApplyAccessConfig && cmd.Slug != node_command.SlugApplyAccessConfigOffline) {
		return
	}
	if status != "success" {
		return
	}
	if accessApplySkippedUsers(result["skipped_unmanaged"]) {
		if err := s.nodeService.ClearAccessApplied(ctx, cmd.AccountID, cmd.NodeID); err != nil {
			s.logger.Warn("failed to clear the access generation after a partial apply",
				"node_id", cmd.NodeID, "error", err)
		}
		return
	}
	generation := result["generation"]
	if generation == "" {
		// A non-Linux agent reports no result map at all. Nothing to record, and
		// nothing to claim.
		return
	}
	// Passed on as a CLAIM, not as a value to store. RecordAccessApplied accepts
	// it only if it matches a hash the platform resolves for itself, because this
	// string decides whether the drift self-heal ever pushes this node again —
	// see the reasoning on that method.
	if err := s.nodeService.RecordAccessApplied(ctx, cmd.AccountID, cmd.NodeID, generation); err != nil {
		s.logger.Warn("failed to record the applied access generation",
			"node_id", cmd.NodeID, "error", err)
	}
}

// accessApplySkippedUsers reports whether an apply skipped any pre-existing
// unmanaged user.
//
// The agent encodes the list as JSON, and an EMPTY list is the two-character
// string "[]", not "". Testing the raw string for emptiness would read every
// clean apply as partial, and the generation would never be recorded on any
// node. Parsing the list is also
// robust to the encoder changing shape again.
func accessApplySkippedUsers(encoded string) bool {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" || encoded == "[]" {
		return false
	}
	var skipped []string
	if err := json.Unmarshal([]byte(encoded), &skipped); err != nil {
		// Unparseable is not proof of a clean apply, and claiming convergence is
		// the outcome that hides a problem. Treat it as skipped.
		return true
	}
	return len(skipped) > 0
}

// recordWatchdogDisarm routes the agent breaker's "watchdog_disarmed" status
// report to the node service. The frame arrives on an authenticated stream, so
// the ids are this stream's own; an unparseable id means the report raced the
// connection handshake and there is nothing safe to attach it to.
func (s *Server) recordWatchdogDisarm(ctx context.Context, nodeID, accountID, reason string) {
	nodeUUID, nerr := uuid.Parse(nodeID)
	accountUUID, aerr := uuid.Parse(accountID)
	if nerr != nil || aerr != nil {
		s.logger.Warn("watchdog disarm report before an authenticated connection", "node_id", nodeID)
		return
	}
	// A disarm the PLATFORM asked for is not a fault, and must not be recorded
	// as one. Agents up to 0.4.0 write the same on-disk flag for both events and
	// re-report it on every start, so a node the operator deliberately disarmed
	// came back claiming its own breaker had tripped "after repeated reboots" —
	// red banner, red fleet pill, and the applied hash cleared so a converged
	// node read as out of sync. Newer agents carry the source in the reason
	// prefix; this string match covers the ones already in the field.
	if node.IsPlatformDisarmReason(reason) {
		s.logger.Debug("ignoring the agent's echo of a platform-requested disarm",
			"node_id", nodeID, "reason", reason)
		return
	}
	s.logger.Info("watchdog disarmed by the agent's on-device breaker",
		"node_id", nodeID, "reason", reason)
	if err := s.nodeService.RecordWatchdogDisarmed(ctx, accountUUID, nodeUUID, reason); err != nil {
		s.logger.Error("failed to record the watchdog disarm", "node_id", nodeID, "error", err)
	}
}

// stringParam / boolParam / int32Param read a command payload's typed fields.
// jsonb round-trips numbers as float64, which is why the int helper exists.
func stringParam(payload map[string]interface{}, key string) string {
	v, _ := payload[key].(string)
	return v
}

func boolParam(payload map[string]interface{}, key string) bool {
	v, _ := payload[key].(bool)
	return v
}

func int32Param(payload map[string]interface{}, key string) int32 {
	if v, ok := payload[key].(float64); ok {
		return int32(v)
	}
	return 0
}

// parkBeforeReject delays a terminal ConnectionInit rejection. Fielded agents
// treat PermissionDenied as non-recoverable but keep the stream loop spinning
// until the next heartbeat tick, then reconnect immediately — so an instant
// rejection yields a busy retry cycle on the device. Holding the stream open
// before failing it throttles a permanently-rejected agent to roughly one
// attempt per park interval with no agent-side change.
func (s *Server) parkBeforeReject(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
	}
}

// handleAgentStatusUpdate applies one StatusUpdate frame from an
// authenticated stream.
func (s *Server) handleAgentStatusUpdate(ctx context.Context, nodeID, accountID, status, message string, now time.Time) {
	// The agent's on-device watchdog breaker reports through the
	// generic status channel because it trips with no command in
	// flight. It is a watchdog fact, not a connection state — route
	// it out BEFORE the node-status whitelist so it can never be
	// written into nodes.status.
	if status == node.WatchdogDisarmedStatus {
		s.recordWatchdogDisarm(ctx, nodeID, accountID, message)
		return
	}

	// A device reports only statuses INSIDE the admitted set: online and
	// offline (maintenance, restarting and shutdown are imposed by the
	// command service). The pre-connect statuses are platform-set and
	// refused here with no write: a device that could put ready_for_adoption
	// on its own row would leave the device cap's count while its stream
	// stays up, free a slot for another device and step back in. No stock
	// agent sends one; the refusal is for a modified agent.
	if node.IsPreConnectStatus(node.NodeStatus(status)) {
		s.logger.Warn("refusing a pre-connect status reported by a device: it is platform-set and decides device-cap admission",
			"node_id", nodeID,
			"status", status,
			"message", message)
		return
	}
	validStatuses := map[string]bool{
		string(node.StatusOnline):  true,
		string(node.StatusOffline): true,
	}

	if !validStatuses[status] {
		s.logger.Warn("ignoring invalid status update from agent",
			"node_id", nodeID,
			"status", status,
			"message", message)
		return
	}

	s.logger.Info("status update received",
		"node_id", nodeID,
		"status", status,
		"message", message)

	// Parse node UUID for status check
	nodeUUID, _ := uuid.Parse(nodeID)
	accountUUID, _ := uuid.Parse(accountID)

	// Check previous node status before updating (if status is changing to "online")
	wasRestarting := false
	if status == string(node.StatusOnline) {
		previousNode, err := s.nodeService.GetByIDAndAccount(ctx, nodeUUID, accountUUID)
		if err == nil && previousNode != nil {
			wasRestarting = previousNode.Status == node.StatusRestarting
		}
	}

	// Update node status in database
	if err := s.updateNodeStatus(ctx, nodeID, accountID, status); err != nil {
		s.logger.Error("failed to update node status", "error", err)
	}

	// Update last seen timestamp
	if err := s.nodeService.UpdateAgentInfo(ctx, nodeUUID, "", "", nil, now); err != nil {
		s.logger.Error("failed to update last seen", "node_id", nodeID, "error", err)
	}

	// If status changed from "restarting" to "online", complete any pending reboot commands
	if wasRestarting && status == string(node.StatusOnline) && s.nodeCommandService != nil {
		if err := s.nodeCommandService.CompleteRebootCommandsForNode(ctx, nodeUUID); err != nil {
			s.logger.Warn("failed to complete reboot commands for node", "node_id", nodeID, "error", err)
		}
	}
}

// updateNodeStatus updates the node status in the database and sends WebSocket notification
func (s *Server) updateNodeStatus(ctx context.Context, nodeID, accountID, status string) error {
	nodeUUID, err := uuid.Parse(nodeID)
	if err != nil {
		return fmt.Errorf("invalid node ID format: %w", err)
	}

	// Convert string to NodeStatus type (for type safety, but still pass as string to service)
	nodeStatus := node.NodeStatus(status)
	err = s.nodeService.UpdateStatus(ctx, nodeUUID, string(nodeStatus))
	if err != nil {
		return fmt.Errorf("failed to update node status: %w", err)
	}

	s.logger.Debug("node status updated", "node_id", nodeID, "status", status)

	// Send WebSocket notification
	if s.websocketClient != nil {
		accountUUID, err := uuid.Parse(accountID)
		if err == nil {
			// Fetch the updated node to send complete data
			node, err := s.nodeService.GetByID(ctx, node_get.NodeGetQuery{
				AuthContext: commands.NewAuthContext(accountUUID, uuid.Nil), // Use account ID only
				ID:          nodeUUID,
			})
			if err == nil && node != nil {
				nodeResult := results.NewNodeResult(node)
				// OU-scoped like every other node event: this carries the
				// node's hostname, IP and state.
				err := s.websocketClient.Send(websockets3.NewAccountMessageForOU(accountUUID, node.OUID, websockets3.MessageWrapper{
					Type:    "node_updated",
					Payload: nodeResult,
				}))
				if err != nil {
					s.logger.Warn("failed to send node status update websocket event", "node_id", nodeID, "error", err)
				}
			}
		}
	}

	return nil
}

// recordMetrics pushes a live sample to the UI over WebSocket. Nothing is
// retained here and nothing is written to the database: a sample is only ever
// worth the moment it arrives, and holding a history of device-reported arrays
// let one node's stream pin backend memory for nothing.
// handleNodeEvent validates, rate-limits and fans out a spontaneous device
// event. Only button_press exists; unknown types are dropped silently so a
// NEWER agent talking to this platform degrades to nothing instead of noise.
func (s *Server) handleNodeEvent(ctx context.Context, nodeID, accountID string, ev *proto.NodeEvent) {
	if ev == nil || ev.EventType != "button_press" || s.nodeService == nil {
		return
	}
	nodeUUID, err := uuid.Parse(nodeID)
	if err != nil {
		return
	}
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return
	}
	// One event per node per second, whatever the agent claims.
	now := time.Now()
	s.nodeEventMu.Lock()
	if s.nodeEventLast == nil {
		s.nodeEventLast = make(map[string]time.Time)
	}
	if last, ok := s.nodeEventLast[nodeID]; ok && now.Sub(last) < time.Second {
		s.nodeEventMu.Unlock()
		return
	}
	s.nodeEventLast[nodeID] = now
	// Lazy sweep: drop entries older than a minute so the map tracks only
	// recently-active nodes.
	for k, v := range s.nodeEventLast {
		if now.Sub(v) > time.Minute {
			delete(s.nodeEventLast, k)
		}
	}
	s.nodeEventMu.Unlock()

	pressKind := ev.PressKind
	if pressKind == "" {
		pressKind = "short"
	}
	if err := s.nodeService.NotifyButtonPressed(ctx, accountUUID, nodeUUID, pressKind); err != nil {
		s.logger.WarnContext(ctx, "failed to fan out node button event", "node_id", nodeID, "error", err)
	}
}

func (s *Server) recordMetrics(nodeID, accountID string, m *proto.MetricsUpdate) {
	if m == nil {
		return
	}
	snapshot := results.NewNodeMetricsResult(m)

	if len(m.CpuPercentPerCore) > results.MaxMetricsCPUCores || len(m.Thermal) > results.MaxMetricsThermalZones {
		// Debug, not Warn: metrics frames carry no rate limit, so a per-sample
		// warning is an amplification vector for the very peer this truncation
		// bounds.
		s.logger.Debug("truncated an oversized node metrics sample",
			"node_id", nodeID,
			"cores", len(m.CpuPercentPerCore),
			"thermal_zones", len(m.Thermal))
	}

	// Push over a dedicated event (not node_updated, which replaces the whole
	// node and would thrash the list on every 5s sample).
	if s.websocketClient != nil {
		if accountUUID, err := uuid.Parse(accountID); err == nil {
			// OU-scoped from the connection's captured unit: this frame is
			// keyed by node id and fires every few seconds, so it is the
			// highest-volume cross-OU leak of the lot.
			s.mu.RLock()
			var ouID *uuid.UUID
			if conn, ok := s.connections[nodeID]; ok {
				ouID = conn.OUID
			}
			s.mu.RUnlock()
			if err := s.websocketClient.Send(websockets3.NewAccountMessageForOU(accountUUID, ouID, websockets3.MessageWrapper{
				Type:    "node_metrics",
				Payload: results.NodeMetricsEvent{NodeID: nodeID, Metrics: snapshot},
			})); err != nil {
				s.logger.Debug("failed to send node_metrics websocket event", "node_id", nodeID, "error", err)
			}
		}
	}
}

// SendCommand sends a command to a specific node
func (s *Server) SendCommand(nodeID string, command *proto.Command) error {
	s.mu.RLock()
	connection, exists := s.connections[nodeID]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("node %s not connected", nodeID)
	}

	platformMsg := &proto.PlatformToNode{
		Payload: &proto.PlatformToNode_Command{
			Command: command,
		},
	}

	// Queued for this connection's writer, never sent inline: this call runs on
	// the single fleet-wide dispatch goroutine, and a blocking Send here is what
	// let one wedged agent stall commands for every other node.
	if err := connection.Send(platformMsg); err != nil {
		s.logger.Error("failed to send command", "node_id", nodeID, "error", err)
		return err
	}

	s.logger.Info("command sent", "node_id", nodeID, "command_id", command.CommandId)
	return nil
}

// GetConnectedNodes returns a list of currently connected node IDs
func (s *Server) GetConnectedNodes() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	nodes := make([]string, 0, len(s.connections))
	for nodeID := range s.connections {
		nodes = append(nodes, nodeID)
	}
	return nodes
}

// IsNodeConnected checks if a node has an active gRPC connection
func (s *Server) IsNodeConnected(nodeID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, exists := s.connections[nodeID]
	return exists
}

// handleDisconnection processes node disconnection. closing is the specific
// StreamConnection whose stream is ending; it is used to detect a stale
// teardown: if the node has already reconnected on a NEWER stream (common right
// after a backend restart), the registry no longer points at `closing`, and we
// must NOT delete that newer entry or clobber its online status — the new
// stream owns the node now.
func (s *Server) handleDisconnection(ctx context.Context, nodeID string, closing *StreamConnection) {
	// Stop this stream's writer first, and unconditionally: the stream is
	// finished either way, and a later enqueue must fail with a reason rather
	// than park a frame in a buffer nobody will drain. Safe on a stale teardown
	// too — `closing` is the OLD connection there, and the new one has its own
	// writer.
	if closing != nil {
		closing.Stop()
	}

	nodeUUID, err := uuid.Parse(nodeID)
	if err != nil {
		s.logger.Error("invalid node ID format during disconnection", "node_id", nodeID, "error", err)
		return
	}

	// Remove connection from registry and get account ID — but only if the
	// registered connection is still THIS stream. Otherwise a newer reconnect
	// has taken over and this is a stale teardown we should ignore.
	s.mu.Lock()
	current, exists := s.connections[nodeID]
	isCurrent := exists && current == closing
	var accountID string
	if closing != nil {
		accountID = closing.AccountID
	} else if current != nil {
		accountID = current.AccountID
	}
	if isCurrent {
		delete(s.connections, nodeID)
		s.logger.Info("connection removed from registry", "node_id", nodeID)
	}
	s.mu.Unlock()

	if !isCurrent {
		// A newer stream already replaced this one (reconnect). Leave the
		// registry, terminals and online status to the new connection —
		// clobbering them here is exactly the bug that left reconnected nodes
		// stuck "offline".
		s.logger.Info("ignoring stale stream teardown; node already reconnected", "node_id", nodeID)
		return
	}

	// End any open terminal sessions for the node.
	s.closeNodeTerminals(nodeID)

	// Without an accountID (conn should always carry one) the WebSocket
	// notification below is skipped.
	if accountID == "" {
		s.logger.Warn("no account ID available for disconnection notification", "node_id", nodeID)
	}

	// Mark as offline in database and send WebSocket notification (but skip if node is restarting or shutdown)
	if accountID != "" {
		accountUUID, err := uuid.Parse(accountID)
		if err == nil {
			// Check current node status before marking as offline
			currentNode, err := s.nodeService.GetByIDAndAccount(ctx, nodeUUID, accountUUID)
			if err == nil {
				// Don't mark as offline if node is restarting or shutdown
				if currentNode.Status == node.StatusRestarting || currentNode.Status == node.StatusShutdown {
					s.logger.Debug("skipping offline status update for node in restarting/shutdown state",
						"node_id", nodeID,
						"current_status", currentNode.Status)
					return
				}
			}
		}

		if err := s.updateNodeStatus(ctx, nodeID, accountID, string(node.StatusOffline)); err != nil {
			// A node with no row is the ORDINARY teardown now that a delete ends
			// the device's stream, so it must not paint an ERROR line: an error
			// that fires by design is how people learn to scroll past ERROR. Same
			// reasoning as the force-delete 409, which was doing exactly that.
			if errors.Is(err, app_errors.ErrNodeNotFound) {
				s.logger.InfoContext(ctx, "node was deleted while its device was connected; nothing to mark offline",
					"node_id", nodeID)
			} else {
				s.logger.Error("failed to mark node as offline", "node_id", nodeID, "error", err)
			}
		} else {
			logFields := []interface{}{
				"node_id", nodeID,
				"was_connected", exists,
			}
			if closing != nil {
				logFields = append(logFields, "last_seen", closing.LastSeenAt())
			}
			s.logger.Info("node disconnected", logFields...)
		}
	} else {
		// For nodes without account ID, we can't check status, so skip offline update
		// This is a fallback case that shouldn't happen in practice
		s.logger.Warn("skipping offline status update - no account ID available", "node_id", nodeID)
	}
}

// ProcessCommandQueue consumes SendCommand operations from queue
func (s *Server) ProcessCommandQueue(ctx context.Context, commandQueue chan node_commands.NodeCommandOperation) {
	for {
		select {
		case <-ctx.Done():
			return
		case operation, ok := <-commandQueue:
			if !ok {
				return
			}

			if operation.Type != node_commands.OperationTypeSendCommand {
				s.logger.Warn("unknown operation type in command queue", "type", operation.Type)
				continue
			}

			sendOp := operation.SendCommand
			if sendOp == nil {
				continue
			}

			// Get node command from database
			nodeCmd, err := s.nodeCommandService.GetNodeCommandByID(ctx, sendOp.AccountID, sendOp.NodeCommandID)
			if err != nil {
				s.logger.Error("failed to get node command", "node_command_id", sendOp.NodeCommandID, "error", err)
				// Try to mark as failed if we can't even get it
				if markErr := s.nodeCommandService.MarkNodeCommandFailed(ctx, sendOp.NodeCommandID, fmt.Sprintf("Failed to retrieve command from database: %v", err)); markErr != nil {
					s.logger.Error("failed to mark command as failed after get error", "node_command_id", sendOp.NodeCommandID, "error", markErr)
				}
				continue
			}

			// Check if node is connected
			nodeIDStr := sendOp.NodeID.String()
			if !s.IsNodeConnected(nodeIDStr) {
				// A queueable command must survive the node dropping between
				// registration and this dispatch: leave it "registered" so
				// reconnect redelivery picks it up, instead of failing it (which
				// would silently defeat the deliver-on-reconnect guarantee, since
				// redelivery only looks at registered/sent).
				if nodeCmd.Queueable {
					s.logger.Info("node not connected; leaving queueable command for reconnect", "node_id", nodeIDStr, "node_command_id", sendOp.NodeCommandID)
					continue
				}
				s.logger.Warn("node not connected, marking command as failed", "node_id", nodeIDStr, "node_command_id", sendOp.NodeCommandID)
				if err := s.nodeCommandService.MarkNodeCommandFailed(ctx, nodeCmd.ID, fmt.Sprintf("Node %s is not connected", nodeIDStr)); err != nil {
					s.logger.Error("failed to mark command as failed", "node_command_id", nodeCmd.ID, "error", err)
				}
				continue
			}

			// A command that only makes sense on a managed OS is held while the
			// node reports being netbooted into the recovery bootstrap — applying
			// e.g. access config to the ephemeral Buildroot initramfs can only
			// produce a spurious terminal FAILED (no groupadd, no persistent
			// disk). It stays "registered"; a fresh system-info report showing a
			// non-recovery boot triggers RedeliverManagedOSQueuedCommandsForNode.
			if nodeCmd.Queueable && node_command.RequiresManagedOS(nodeCmd.Slug) {
				if n, nerr := s.nodeService.GetByIDAndAccount(ctx, sendOp.NodeID, nodeCmd.AccountID); nerr == nil && n != nil && n.IsRecoveryBootstrap() {
					s.logger.Info("node is in recovery; holding managed-OS command for a managed-OS boot",
						"node_id", nodeIDStr, "node_command_id", sendOp.NodeCommandID, "slug", nodeCmd.Slug)
					continue
				}
			}

			// Build proto command
			protoCommand, err := s.buildProtoCommandFromNodeCommand(ctx, nodeCmd)
			if err != nil {
				s.logger.Error("failed to build proto command", "node_command_id", sendOp.NodeCommandID, "error", err)
				// The message lands verbatim in the command history, so no
				// "failed to build proto command" prefix: the refusal itself is
				// the operator-facing text (docs/COPY-STYLE.md).
				if markErr := s.nodeCommandService.MarkNodeCommandFailed(ctx, nodeCmd.ID, err.Error()); markErr != nil {
					s.logger.Error("failed to mark command as failed", "node_command_id", nodeCmd.ID, "error", markErr)
				}
				continue
			}

			// Set command_id to node command ID
			protoCommand.CommandId = nodeCmd.ID.String()
			protoCommand.IssuedAt = time.Now().Unix()

			// Send command to node
			if err := s.SendCommand(nodeIDStr, protoCommand); err != nil {
				s.logger.Error("failed to send command", "node_command_id", sendOp.NodeCommandID, "error", err)
				// Same as the not-connected case: a queueable command stays
				// registered for redelivery (bounded by the attempts cap) rather
				// than failing on a transient stream error.
				if nodeCmd.Queueable {
					continue
				}
				if markErr := s.nodeCommandService.MarkNodeCommandFailed(ctx, nodeCmd.ID, fmt.Sprintf("Failed to send command to node: %v", err)); markErr != nil {
					s.logger.Error("failed to mark command as failed", "node_command_id", nodeCmd.ID, "error", markErr)
				}
				continue
			}

			// Mark as sent
			if err := s.nodeCommandService.MarkNodeCommandSent(ctx, nodeCmd.ID); err != nil {
				s.logger.Error("failed to mark command as sent", "node_command_id", nodeCmd.ID, "error", err)
			}
		}
	}
}

// resolveProvisioningForDispatch resolves the inputs (image, credentials, ssh
// key, env) for a command that hands a credential to a device.
//
// It replaced a helper that turned every failure into an empty struct, which
// for these two commands meant "provision with no password" — a locked account
// on the device, reported as a success. Access config already resolved directly
// for the same reason; this is the rest of that lesson.
//
// requirePassword separates the two callers. provision_connection is the only
// command that actually creates the OS user, so an undecryptable password there
// is fatal. install_os passes the field to an agent that ignores it — no agent
// version this platform still serves reads it — so failing every
// OS install in an account over a credential the command does not use would be
// pure collateral — it warns instead.
func (s *Server) resolveProvisioningForDispatch(ctx context.Context, nodeCmd *node_command.NodeCommand, requirePassword bool) (services.EffectiveProvisioning, error) {
	if s.organizationalUnitService == nil {
		// No OU service wired: payload-only, exactly as before. Nothing was
		// resolved, so there is nothing that could be silently wrong.
		return services.EffectiveProvisioning{}, nil
	}
	eff, err := s.organizationalUnitService.ResolveEffectiveProvisioning(ctx, nodeCmd.AccountID, nodeCmd.NodeID)
	if err != nil {
		return services.EffectiveProvisioning{}, fmt.Errorf("failed to resolve effective provisioning: %w", err)
	}
	if eff.PasswordUnreadable {
		if requirePassword {
			return services.EffectiveProvisioning{}, fmt.Errorf(
				"the provisioning password stored for this node cannot be decrypted with the configured SECRETS_ENCRYPTION_KEY; " +
					"set it again (or restore the key) rather than provisioning the device with no password")
		}
		s.logger.Warn("provisioning password is undecryptable; sending none (this command does not apply it)",
			"node_id", nodeCmd.NodeID, "slug", nodeCmd.Slug)
		eff.Password = ""
	}
	// Never hand a device the at-rest envelope. This is the guard node files
	// already carry: a backend built without the decryption code, deployed over
	// encrypted rows, would otherwise set the device's password to "v1:…".
	if node_file.LooksLikeStoredCiphertext(eff.Password) {
		return services.EffectiveProvisioning{}, fmt.Errorf(
			"the resolved provisioning password reads as stored ciphertext rather than a password; refusing to set it on the device")
	}
	return eff, nil
}

// provisioningUsernameOrigin says, in a short parenthetical, where the resolved
// username came from. The commonest dead end is a username the operator did not
// type and cannot see at the node — a unit's provisioning default — so the
// refusal points at the level to go and fix, quoting the unit by name. Empty
// when there is nothing useful to add.
func provisioningUsernameOrigin(payload map[string]interface{}, eff services.EffectiveProvisioning) string {
	if u, _ := payload["username"].(string); u != "" {
		return " (named in the command)"
	}
	switch eff.UsernameSource {
	case organizational_unit.SourceNode:
		return " (set on the node)"
	case organizational_unit.SourceOU:
		if ou := strings.TrimSpace(eff.OUName); ou != "" {
			return fmt.Sprintf(" (set by its organizational unit %q)", ou)
		}
		return " (set by its organizational unit)"
	case organizational_unit.SourceRootOU:
		if root := strings.TrimSpace(eff.RootOUName); root != "" {
			return fmt.Sprintf(" (set by its parent unit %q)", root)
		}
		return " (set by its parent unit)"
	case organizational_unit.SourceAccount:
		return " (set by the account policy)"
	default:
		return ""
	}
}

// floatPayload reads a numeric payload field (JSON numbers decode as float64).
func floatPayload(payload map[string]interface{}, key string) float64 {
	if v, ok := payload[key].(float64); ok {
		return v
	}
	return 0
}

// assertImageURLIsProtected refuses a plaintext OS-image download in the
// hardened mode.
//
// OS images carry no independent signature, and that is a DECISION, not a gap
// waiting on a release: the signed-manifest design was considered and rejected
// (docs/ARTIFACT-SIGNING.md — its anti-rollback semantics conflict with
// legitimately restoring an older capture). The protections that exist are the
// gRPC-delivered digest — ENFORCED, the agent holds the image head back and
// never commits it unless the digest matches, so a bad download is never
// bootable — and the channel that carries the digest. The digest is only as
// trustworthy as that channel, which is why production requires TLS here: an
// on-path attacker who can rewrite both the URL and the digest substitutes the
// image outright.
func (s *Server) assertImageURLIsProtected(imageURL string) error {
	if s.plaintextImageURLAllowed(imageURL) {
		return nil
	}
	return fmt.Errorf(
		"refusing to install an OS image over a plaintext URL (%s): the image is root-equivalent on the device, "+
			"and the digest that guards it travels over the same connection — https is what makes that digest trustworthy. "+
			"Serve images over https (set server.advertisement_url with an https scheme), or set "+
			"IMAGE_ALLOW_PLAINTEXT_URL=true to accept the risk on a trusted network",
		imageURL)
}

// assertCaptureURLIsProtected is the same transport policy pointed the other
// way: a capture streams the node's WHOLE DISK to this URL, so plaintext leaks
// the account's data rather than corrupting the device. One knob
// governs both directions on purpose — an install that trusts the network has
// already accepted this risk, and two flags would drift.
func (s *Server) assertCaptureURLIsProtected(uploadURL string) error {
	if s.plaintextImageURLAllowed(uploadURL) {
		return nil
	}
	return fmt.Errorf(
		"refusing to capture a disk image to a plaintext upload URL (%s): the capture is the node's whole disk "+
			"and would travel unencrypted. Serve the platform over https (set server.advertisement_url with an "+
			"https scheme), or set IMAGE_ALLOW_PLAINTEXT_URL=true to accept the risk on a trusted network",
		uploadURL)
}

// plaintextImageURLAllowed is the shared policy: production refuses a
// non-https image transfer URL unless IMAGE_ALLOW_PLAINTEXT_URL opts in.
func (s *Server) plaintextImageURLAllowed(rawURL string) bool {
	if s.config == nil || !s.config.IsProduction() || s.config.Image.AllowPlaintextURL {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(rawURL)), "https://")
}

func (s *Server) buildProtoCommandFromNodeCommand(ctx context.Context, nodeCmd *node_command.NodeCommand) (*proto.Command, error) {
	// Refuse a command whose stored payload could not be read back in full.
	//
	// The read path DELETES fields it cannot decrypt, which is right for display
	// and catastrophic here: a queued apply_access_config waiting for an offline
	// node, dispatched after SECRETS_ENCRYPTION_KEY_OLD was dropped, would go out
	// missing users or keys and settle SUCCESS — a revocation turned into a
	// grant, with the device convinced it is converged. Failing here is the only
	// honest outcome, and the message names the remedy, because the operator's
	// next step is to re-issue the command, not to retry this one.
	//
	// This sits in the single function that turns a stored command into a wire
	// command, so no per-slug arm has to remember it.
	if !nodeCmd.PayloadIsComplete() {
		return nil, fmt.Errorf(
			"this command's stored parameters (%s) can no longer be decrypted, most likely because the previous SECRETS_ENCRYPTION_KEY was removed; re-issue the command",
			strings.Join(nodeCmd.UndecryptablePayloadKeys, ", "))
	}

	switch nodeCmd.Slug {
	case node_command.SlugReboot:
		delaySeconds := uint32(0)
		if delay, ok := nodeCmd.Payload["delay_seconds"].(float64); ok {
			// Backstop for rows written before the dispatch clamp existed (and
			// for any writer that skips it): the wire field is a uint32, and a
			// negative float64 converted straight into it is a sleep of years
			// on the agent's single command goroutine.
			delay, _ = node_command.ClampDelaySeconds(delay)
			delaySeconds = uint32(delay)
		}
		return &proto.Command{
			CommandType: &proto.Command_Restart{
				Restart: &proto.RestartCommand{
					DelaySeconds: delaySeconds,
				},
			},
		}, nil

	case node_command.SlugShutdown:
		delaySeconds := uint32(0)
		if delay, ok := nodeCmd.Payload["delay_seconds"].(float64); ok {
			// Same backstop as reboot above.
			delay, _ = node_command.ClampDelaySeconds(delay)
			delaySeconds = uint32(delay)
		}
		return &proto.Command{
			CommandType: &proto.Command_Shutdown{
				Shutdown: &proto.ShutdownCommand{
					DelaySeconds: delaySeconds,
				},
			},
		}, nil

	case node_command.SlugStatusCheck:
		return &proto.Command{
			CommandType: &proto.Command_StatusCheck{
				StatusCheck: &proto.StatusCheckCommand{},
			},
		}, nil

	case node_command.SlugUpdateBootOrder:
		bootOrder, ok := nodeCmd.Payload["boot_order"].([]interface{})
		if !ok {
			return nil, fmt.Errorf("boot_order is required for update_boot_order command")
		}
		bootOrderStr := make([]string, 0, len(bootOrder))
		for _, v := range bootOrder {
			if str, ok := v.(string); ok {
				bootOrderStr = append(bootOrderStr, str)
			}
		}
		pkgExp, _ := nodeCmd.Payload["pkg_exp"].(string)
		pkgSig, _ := nodeCmd.Payload["pkg_sig"].(string)
		return &proto.Command{
			CommandType: &proto.Command_UpdateBootOrder{
				UpdateBootOrder: &proto.UpdateBootOrderCommand{
					BootOrder: bootOrderStr,
					PkgExp:    pkgExp,
					PkgSig:    pkgSig,
				},
			},
		}, nil

	case node_command.SlugUpdateFile:
		path, ok := nodeCmd.Payload["path"].(string)
		if !ok || path == "" {
			return nil, fmt.Errorf("path is required for update_file command")
		}
		content, ok := nodeCmd.Payload["content"].(string)
		if !ok {
			return nil, fmt.Errorf("content is required for update_file command")
		}
		hash, ok := nodeCmd.Payload["hash"].(string)
		if !ok {
			return nil, fmt.Errorf("hash is required for update_file command")
		}
		version := int32(0)
		if v, ok := nodeCmd.Payload["version"].(float64); ok {
			version = int32(v)
		}
		return &proto.Command{
			CommandType: &proto.Command_UpdateFile{
				UpdateFile: &proto.UpdateFileCommand{
					Path:    path,
					Content: content,
					Hash:    hash,
					Version: version,
				},
			},
		}, nil

	case node_command.SlugInstallOS:
		// Fill blanks (image, credentials, ssh key) from the node's effective
		// OU policy so a node in an OU inherits the group's provisioning defaults.
		eff, err := s.resolveProvisioningForDispatch(ctx, nodeCmd, false)
		if err != nil {
			return nil, err
		}

		// Extract image_id from payload, falling back to the effective OS image.
		imageIDStr, _ := nodeCmd.Payload["image_id"].(string)
		if imageIDStr == "" && eff.OSImageID != nil {
			imageIDStr = eff.OSImageID.String()
		}
		if imageIDStr == "" {
			return nil, fmt.Errorf("image_id is required for install_os command (not in payload or effective policy)")
		}

		imageID, err := uuid.Parse(imageIDStr)
		if err != nil {
			return nil, fmt.Errorf("invalid image_id format: %w", err)
		}

		// Look up image and generate signed URL
		// Create minimal auth context (UserID not needed for image lookup)
		authCtx := commands.NewAuthContext(nodeCmd.AccountID, uuid.Nil)
		img, err := s.imageService.GetByID(ctx, image_get.ImageGetQuery{
			ID:          imageID,
			AuthContext: authCtx,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to get image: %w", err)
		}

		// Check if image has a file uploaded
		if img.FileStorageInfo == nil || img.FileStorageInfo.FilePath == "" {
			return nil, fmt.Errorf("image file not uploaded")
		}

		// Check if image has checksum
		if img.Checksum == "" {
			return nil, fmt.Errorf("image checksum not available")
		}

		// A record a compression or an upload holds is mid-replacement: its
		// file and checksum describe different objects until the job ends, and
		// the device wipes its disk before the download can fail. Checked here
		// as well as at dispatch, because the job can start in between.
		if image.IsBusy(img.CompressionStatus) {
			return nil, fmt.Errorf("image %q is still being processed (%s); install it once it is ready", img.Title, img.CompressionStatus)
		}

		// Generate signed URL for image download (valid for 1 hour)
		signedURL, err := s.imageService.GetSignedURL(ctx, nodeCmd.AccountID, imageID, time.Hour)
		if err != nil {
			return nil, fmt.Errorf("failed to generate signed URL: %w", err)
		}

		// An OS image is root-equivalent on the target device, and the only
		// thing vouching for it is a checksum that travels in THIS command —
		// so it authenticates nothing against an attacker who can rewrite the
		// download. Refuse to hand a node a plaintext URL unless the operator
		// has explicitly accepted that (or is running in development).
		if err := s.assertImageURLIsProtected(signedURL); err != nil {
			return nil, err
		}

		// A backup image carries its SOURCE node's machine identity (machine-id,
		// SSH host keys, adoption file). Deploying it onto a different node must
		// strip that identity during install, or the restored device reconnects
		// as the source node (an identity clone). Fail closed: an agent too old
		// to strip refuses the command instead of cloning silently. Same-node
		// restores and golden/os images are unaffected.
		stripIdentity := img.NeedsIdentityStripFor(nodeCmd.NodeID)
		if stripIdentity {
			targetNode, nodeErr := s.nodeService.GetByIDAndAccount(ctx, nodeCmd.NodeID, nodeCmd.AccountID)
			if nodeErr != nil {
				return nil, fmt.Errorf("failed to resolve target node for backup restore: %w", nodeErr)
			}
			if !node_command.MeetsMinAgentVersion(targetNode.AgentVersion, node_command.MinAgentVersionStripIdentity) {
				return nil, fmt.Errorf(
					"image '%s' is a backup of another node and must be generalized during install; that requires agent >= %s on the target node (running '%s')",
					img.Title, node_command.MinAgentVersionStripIdentity, targetNode.AgentVersion)
			}
		}

		// Extract other fields from payload
		targetDevice, ok := nodeCmd.Payload["target_device"].(string)
		if !ok || targetDevice == "" {
			return nil, fmt.Errorf("target_device is required for install_os command")
		}

		reboot := false
		if rebootVal, ok := nodeCmd.Payload["reboot"].(bool); ok {
			reboot = rebootVal
		}

		wipe := false
		if wipeVal, ok := nodeCmd.Payload["wipe"].(bool); ok {
			wipe = wipeVal
		}

		// username / password / ssh_key are deliberately NOT sent.
		//
		// The agent ignores all three — nothing in handleInstallOS reads them —
		// so populating them would only expose them: whoever holds one node's
		// adoption file could ask for an install and receive the account's
		// provisioning credentials, which are usually an OU or account DEFAULT
		// and therefore shared across the fleet. Leaving them empty needs no
		// proto change and no version floor precisely BECAUSE no agent reads
		// them.
		// The user is created by provision_connection, which does.

		return &proto.Command{
			CommandType: &proto.Command_InstallOs{
				InstallOs: &proto.InstallOSCommand{
					ImageId:      imageID.String(),
					ImageUrl:     signedURL,
					Checksum:     img.Checksum,
					TargetDevice: targetDevice,
					Reboot:       reboot,
					// Env is NOT populated. handleInstallOS reads image id, url,
					// checksum, target device, reboot, wipe and strip_identity —
					// and nothing else — so populating it would carry every env
					// secret's plaintext to the device for no functional reason.
					// Same shape as the username/password/ssh_key fields above,
					// and left empty the same way: no proto change and no version floor are needed
					// precisely BECAUSE no agent reads it. Env secrets reach the
					// installed OS through provision_connection, which does read
					// them.
					Wipe:          wipe,
					StripIdentity: stripIdentity,
				},
			},
		}, nil

	case node_command.SlugInstallAgent:
		targetDevice, ok := nodeCmd.Payload["target_device"].(string)
		if !ok || targetDevice == "" {
			return nil, fmt.Errorf("target_device is required for install_agent command")
		}
		rootPartition := uint32(0)
		if rp, ok := nodeCmd.Payload["root_partition"].(float64); ok {
			rootPartition = uint32(rp)
		}
		agentVersion, ok := nodeCmd.Payload["agent_version"].(string)
		if !ok || agentVersion == "" {
			return nil, fmt.Errorf("agent_version is required for install_agent command")
		}
		variant, _ := nodeCmd.Payload["variant"].(string)
		return &proto.Command{
			CommandType: &proto.Command_InstallAgentOnVolume{
				InstallAgentOnVolume: &proto.InstallAgentOnVolumeCommand{
					TargetDevice:  targetDevice,
					RootPartition: rootPartition,
					AgentVersion:  agentVersion,
					Variant:       variant,
				},
			},
		}, nil

	case node_command.SlugUpdateAgent:
		agentVersion, _ := nodeCmd.Payload["agent_version"].(string)
		if agentVersion == "" {
			agentVersion = "latest"
		}
		allowDowngrade, _ := nodeCmd.Payload["allow_downgrade"].(bool)
		// The agent treats its running version as a no-op unless the version
		// check is relaxed, and that flag is the only one the wire carries —
		// it also lifts the anti-rollback check. A reinstall therefore relaxes
		// it only when the named version IS the one the node runs now, read at
		// send time: a queued reinstall delivered after the node moved to a
		// newer agent must not become a silent downgrade. Otherwise the flag
		// is dropped and the agent upgrades, or refuses an older package, as
		// it would for any update.
		if reinstall, _ := nodeCmd.Payload["reinstall"].(bool); reinstall && !allowDowngrade && s.nodeService != nil {
			if target, err := s.nodeService.GetByIDAndAccount(ctx, nodeCmd.NodeID, nodeCmd.AccountID); err == nil && target != nil &&
				node_command.SameAgentVersion(agentVersion, target.AgentVersion) {
				allowDowngrade = true
			}
		}
		return &proto.Command{
			CommandType: &proto.Command_UpdateAgent{
				UpdateAgent: &proto.UpdateAgentCommand{
					AgentVersion:   agentVersion,
					AllowDowngrade: allowDowngrade,
				},
			},
		}, nil

	case node_command.SlugSetHostname:
		hostname, _ := nodeCmd.Payload["hostname"].(string)
		if hostname == "" {
			return nil, fmt.Errorf("set_hostname command missing hostname")
		}
		// Re-checked here and not only at registration: this arm also serves
		// commands queued before the check existed, and a value the device
		// rejects comes back quoted inside its failure report — which is
		// plaintext details every node.read caller can list.
		normalized, err := node_command.NormalizeHostname(hostname)
		if err != nil {
			return nil, fmt.Errorf("set_hostname: %w", err)
		}
		return &proto.Command{
			CommandType: &proto.Command_SetHostname{
				SetHostname: &proto.SetHostnameCommand{Hostname: normalized},
			},
		}, nil

	case node_command.SlugReleaseIdentity:
		reason, _ := nodeCmd.Payload["reason"].(string)
		return &proto.Command{
			CommandType: &proto.Command_ReleaseIdentity{
				ReleaseIdentity: &proto.ReleaseIdentityCommand{Reason: reason},
			},
		}, nil

	case node_command.SlugProvisionConnection:
		// Fill blanks from the node's effective OU policy. This is the ONLY
		// command that creates the OS user, so it resolves strictly.
		eff, err := s.resolveProvisioningForDispatch(ctx, nodeCmd, true)
		if err != nil {
			return nil, err
		}
		username, _ := nodeCmd.Payload["username"].(string)
		if username == "" {
			username = eff.Username
		}
		if username == "" {
			return nil, fmt.Errorf(
				"nothing is assigned to provision: this node has no OS username. " +
					"Set a username and a password or an SSH key on the node or its organizational unit, then run it again")
		}
		password, _ := nodeCmd.Payload["password"].(string)
		if password == "" {
			password = eff.Password
		}
		sshKey, _ := nodeCmd.Payload["ssh_key"].(string) // Optional
		if sshKey == "" {
			sshKey = eff.SSHKey
		}
		sshKeyType, _ := nodeCmd.Payload["ssh_key_type"].(string) // Optional, defaults to "ssh"
		if sshKeyType == "" && eff.SSHKeyType != "" {
			sshKeyType = eff.SSHKeyType
		}
		if sshKeyType == "" {
			sshKeyType = "ssh"
		}
		// A user with neither a password nor a key is an account nobody can log
		// into, created and reported as a success. Only the username was ever
		// required here, which was survivable while a password was mandatory
		// everywhere upstream; once it is optional this is the check that stops
		// the platform provisioning a dead end.
		if password == "" && sshKey == "" {
			return nil, fmt.Errorf(
				"the user %q%s has no password and no SSH key, so it would be created with no way to log in. "+
					"Set a password or an SSH key, then run it again",
				username, provisioningUsernameOrigin(nodeCmd.Payload, eff))
		}
		targetDevice, _ := nodeCmd.Payload["target_device"].(string)
		rootPartition := uint32(0)
		if rp, ok := nodeCmd.Payload["root_partition"].(float64); ok {
			rootPartition = uint32(rp)
		}
		// Hashed here for the same reason as the access config, and it matters
		// as much: this password is usually the OU or ACCOUNT default, so it is
		// one credential shared across the whole fleet. The dead-end check above
		// deliberately runs on the cleartext, before the hash exists — an empty
		// password and a hash of an empty password are not the same question.
		passwordHash := ""
		if password != "" {
			h, err := crypto.HashPasswordForShadow(password)
			if err != nil {
				return nil, fmt.Errorf("hashing the provisioning password for %q: %w", username, err)
			}
			passwordHash = h
		}
		return &proto.Command{
			CommandType: &proto.Command_ProvisionConnection{
				ProvisionConnection: &proto.ProvisionConnectionCommand{
					Username:      username,
					PasswordHash:  passwordHash,
					SshKey:        sshKey,
					SshKeyType:    sshKeyType,
					TargetDevice:  targetDevice,
					RootPartition: rootPartition,
					Env:           eff.EnvVars,
				},
			},
		}, nil

	case node_command.SlugPatchFile:
		path, _ := nodeCmd.Payload["path"].(string)
		content, ok := nodeCmd.Payload["content"].(string)
		if !ok || content == "" {
			return nil, fmt.Errorf("content is required for patch_file command")
		}
		marker, _ := nodeCmd.Payload["marker"].(string)
		return &proto.Command{
			CommandType: &proto.Command_PatchFile{
				PatchFile: &proto.PatchFileCommand{
					Path:    path,
					Content: content,
					Marker:  marker,
				},
			},
		}, nil

	case node_command.SlugApplyDeviceProfile:
		path, _ := nodeCmd.Payload["path"].(string)
		marker, _ := nodeCmd.Payload["marker"].(string)
		content, _ := nodeCmd.Payload["content"].(string)
		contentHash, _ := nodeCmd.Payload["content_hash"].(string)
		if path == "" || marker == "" || contentHash == "" {
			return nil, fmt.Errorf("path, marker and content_hash are required for apply_device_profile")
		}
		var kinds []string
		if raw, ok := nodeCmd.Payload["setup_kinds"].([]interface{}); ok {
			for _, k := range raw {
				if s, ok := k.(string); ok && s != "" {
					kinds = append(kinds, s)
				}
			}
		}
		buttonGPIO, _ := nodeCmd.Payload["button_gpio"].(float64)
		identifyBackend, _ := nodeCmd.Payload["identify_backend"].(string)
		identifyGPIO, _ := nodeCmd.Payload["identify_gpio"].(float64)
		identifyColor, _ := nodeCmd.Payload["identify_color"].(string)
		identifyDuration, _ := nodeCmd.Payload["identify_duration_sec"].(float64)
		return &proto.Command{
			CommandType: &proto.Command_ApplyDeviceProfile{
				ApplyDeviceProfile: &proto.ApplyDeviceProfileCommand{
					Path:                path,
					Marker:              marker,
					Content:             content,
					ContentHash:         contentHash,
					SetupKinds:          kinds,
					ButtonGpio:          int32(buttonGPIO),
					IdentifyBackend:     identifyBackend,
					IdentifyGpio:        int32(identifyGPIO),
					IdentifyColor:       identifyColor,
					IdentifyDurationSec: int32(identifyDuration),
				},
			},
		}, nil

	case node_command.SlugApplyUtilityConfig:
		unionHash, _ := nodeCmd.Payload["union_hash"].(string)
		rawList, _ := nodeCmd.Payload["utilities"].([]interface{})
		if unionHash == "" || len(rawList) == 0 {
			return nil, fmt.Errorf("union_hash and utilities are required for apply_utility_config")
		}
		desired := make([]*proto.UtilityDesired, 0, len(rawList))
		for _, raw := range rawList {
			entry, _ := raw.(map[string]interface{})
			id, _ := entry["utility_id"].(string)
			hash, _ := entry["config_hash"].(string)
			paramsJSON, _ := entry["params_json"].(string)
			if id == "" || hash == "" {
				return nil, fmt.Errorf("every utility of apply_utility_config needs utility_id and config_hash")
			}
			desired = append(desired, &proto.UtilityDesired{
				UtilityId:  id,
				ConfigHash: hash,
				ParamsJson: paramsJSON,
			})
		}
		return &proto.Command{
			CommandType: &proto.Command_ApplyUtilityConfig{
				ApplyUtilityConfig: &proto.ApplyUtilityConfigCommand{
					UnionHash: unionHash,
					Utilities: desired,
				},
			},
		}, nil

	case node_command.SlugApplyLEDConfig:
		configHash, _ := nodeCmd.Payload["config_hash"].(string)
		if configHash == "" {
			return nil, fmt.Errorf("config_hash is required for apply_led_config")
		}
		stealth, _ := nodeCmd.Payload["stealth_mode"].(bool)
		actOff, _ := nodeCmd.Payload["act_led_off"].(bool)
		pwrOff, _ := nodeCmd.Payload["pwr_led_off"].(bool)
		var sources []string
		if raw, ok := nodeCmd.Payload["digital_sources"].([]interface{}); ok {
			for _, v := range raw {
				if str, ok := v.(string); ok {
					sources = append(sources, str)
				}
			}
		}
		digitalGPIO, _ := nodeCmd.Payload["digital_gpio"].(float64)
		stealthGPIO, _ := nodeCmd.Payload["stealth_gpio"].(float64)
		var ledSpecs []*proto.DigitalLEDSpec
		if raw, ok := nodeCmd.Payload["digital_leds"].([]interface{}); ok {
			for _, v := range raw {
				entry, _ := v.(map[string]interface{})
				if entry == nil {
					continue
				}
				source, _ := entry["source"].(string)
				low, _ := entry["low"].(float64)
				high, _ := entry["high"].(float64)
				brightness, _ := entry["brightness"].(float64)
				ledSpecs = append(ledSpecs, &proto.DigitalLEDSpec{
					Source:     source,
					Low:        low,
					High:       high,
					Brightness: uint32(brightness),
				})
			}
		}
		return &proto.Command{
			CommandType: &proto.Command_ApplyLedConfig{
				ApplyLedConfig: &proto.ApplyLEDConfigCommand{
					ConfigHash:     configHash,
					StealthMode:    stealth,
					ActLedOff:      actOff,
					PwrLedOff:      pwrOff,
					DigitalSources: sources,
					DigitalGpio:    int32(digitalGPIO),
					StealthGpio:    int32(stealthGPIO),
					DigitalLeds:    ledSpecs,
				},
			},
		}, nil

	case node_command.SlugIdentify:
		duration, _ := nodeCmd.Payload["duration_sec"].(float64)
		backend, _ := nodeCmd.Payload["backend"].(string)
		gpio, _ := nodeCmd.Payload["gpio"].(float64)
		color, _ := nodeCmd.Payload["color"].(string)
		stop, _ := nodeCmd.Payload["stop"].(bool)
		return &proto.Command{
			CommandType: &proto.Command_Identify{
				Identify: &proto.IdentifyCommand{
					DurationSec: int32(duration),
					Backend:     backend,
					Gpio:        int32(gpio),
					Color:       color,
					Stop:        stop,
				},
			},
		}, nil

	case node_command.SlugReadFile:
		path, _ := nodeCmd.Payload["path"].(string)
		if path == "" {
			return nil, fmt.Errorf("path is required for read_file command")
		}
		pattern, _ := nodeCmd.Payload["pattern"].(string)
		maxBytes := uint32(0)
		if v, ok := nodeCmd.Payload["max_bytes"].(float64); ok && v > 0 {
			maxBytes = uint32(v)
		}
		return &proto.Command{
			CommandType: &proto.Command_ReadFile{
				ReadFile: &proto.ReadFileCommand{Path: path, Pattern: pattern, MaxBytes: maxBytes},
			},
		}, nil

	case node_command.SlugWaitForFile:
		path, _ := nodeCmd.Payload["path"].(string)
		pattern, _ := nodeCmd.Payload["pattern"].(string)
		if path == "" || pattern == "" {
			return nil, fmt.Errorf("path and pattern are required for wait_for_file command")
		}
		// Clamped BEFORE the int32 conversion, which is architecture-dependent
		// for an out-of-range value (arm64 saturates to a 68-year sleep, amd64
		// wraps negative) — see node_command.MaxBlockSeconds.
		waitInterval, intervalBound := node_command.ClampBlockSeconds(floatPayload(nodeCmd.Payload, "interval_seconds"))
		waitTimeout, timeoutBound := node_command.ClampBlockSeconds(floatPayload(nodeCmd.Payload, "timeout_seconds"))
		if intervalBound || timeoutBound {
			s.logger.Warn("wait_for_file wait bounded to the block ceiling",
				"command_id", nodeCmd.ID, "node_id", nodeCmd.NodeID,
				"interval_seconds", waitInterval, "timeout_seconds", waitTimeout,
				"max_seconds", node_command.MaxBlockSeconds)
		}
		return &proto.Command{
			CommandType: &proto.Command_WaitForFile{
				WaitForFile: &proto.WaitForFileCommand{
					Path:            path,
					Pattern:         pattern,
					IntervalSeconds: int32(waitInterval),
					TimeoutSeconds:  int32(waitTimeout),
				},
			},
		}, nil

	case node_command.SlugRunScript:
		script, _ := nodeCmd.Payload["script"].(string)
		if script == "" {
			return nil, fmt.Errorf("script is required for run_script command")
		}
		scriptTimeout, scriptBound := node_command.ClampBlockSeconds(floatPayload(nodeCmd.Payload, "timeout_seconds"))
		if scriptBound {
			s.logger.Warn("run_script timeout bounded to the block ceiling",
				"command_id", nodeCmd.ID, "node_id", nodeCmd.NodeID,
				"timeout_seconds", scriptTimeout, "max_seconds", node_command.MaxBlockSeconds)
		}
		return &proto.Command{
			CommandType: &proto.Command_RunScript{
				RunScript: &proto.RunScriptCommand{
					Script:         script,
					TimeoutSeconds: int32(scriptTimeout),
				},
			},
		}, nil

	case node_command.SlugEnableEEPROM:
		return &proto.Command{
			CommandType: &proto.Command_EnableEeprom{
				EnableEeprom: &proto.EnableEEPROMCommand{},
			},
		}, nil

	case node_command.SlugEraseDrive:
		targetDevice, ok := nodeCmd.Payload["target_device"].(string)
		if !ok || targetDevice == "" {
			return nil, fmt.Errorf("target_device is required for erase_drive command")
		}
		return &proto.Command{
			CommandType: &proto.Command_EraseDrive{
				EraseDrive: &proto.EraseDriveCommand{
					TargetDevice: targetDevice,
				},
			},
		}, nil

	case node_command.SlugApplyAccessConfig:
		payload, err := s.buildAccessConfigPayload(ctx, nodeCmd)
		if err != nil {
			return nil, err
		}
		return &proto.Command{
			CommandType: &proto.Command_ApplyAccessConfig{ApplyAccessConfig: payload},
		}, nil

	case node_command.SlugApplyAccessConfigOffline:
		// Same resolved config, same lockout gate — only the delivery differs:
		// the agent writes it into a mounted target rootfs from recovery.
		payload, err := s.buildAccessConfigPayload(ctx, nodeCmd)
		if err != nil {
			return nil, err
		}
		targetDevice, _ := nodeCmd.Payload["target_device"].(string)
		if targetDevice == "" {
			return nil, fmt.Errorf("target_device is required for apply_access_config_offline")
		}
		return &proto.Command{
			CommandType: &proto.Command_ApplyAccessConfigOffline{
				ApplyAccessConfigOffline: &proto.ApplyAccessConfigOfflineCommand{
					Config:        payload,
					TargetDevice:  targetDevice,
					RootPartition: uint32(floatPayload(nodeCmd.Payload, "root_partition")),
				},
			},
		}, nil
	case node_command.SlugPatchFileOffline:
		targetDevice, _ := nodeCmd.Payload["target_device"].(string)
		if targetDevice == "" {
			return nil, fmt.Errorf("target_device is required for patch_file_offline")
		}
		path, _ := nodeCmd.Payload["path"].(string)
		content, _ := nodeCmd.Payload["content"].(string)
		marker, _ := nodeCmd.Payload["marker"].(string)
		return &proto.Command{
			CommandType: &proto.Command_PatchFileOffline{
				PatchFileOffline: &proto.PatchFileOfflineCommand{
					Path:          path,
					Content:       content,
					Marker:        marker,
					TargetDevice:  targetDevice,
					RootPartition: uint32(floatPayload(nodeCmd.Payload, "root_partition")),
				},
			},
		}, nil
	case node_command.SlugCaptureImage:
		imageIDStr, _ := nodeCmd.Payload["image_id"].(string)
		if imageIDStr == "" {
			return nil, fmt.Errorf("image_id is required for capture_image command")
		}
		imageID, err := uuid.Parse(imageIDStr)
		if err != nil {
			return nil, fmt.Errorf("invalid image_id format: %w", err)
		}
		// Mint the signed upload URL fresh (short-lived): a capture of a whole
		// disk can take a while, so allow it a generous window.
		uploadURL, err := s.imageService.CaptureUploadURL(ctx, nodeCmd.AccountID, imageID, 6*time.Hour)
		if err != nil {
			return nil, fmt.Errorf("failed to mint capture upload URL: %w", err)
		}
		if err := s.assertCaptureURLIsProtected(uploadURL); err != nil {
			return nil, err
		}
		generalize := false
		if v, ok := nodeCmd.Payload["generalize"].(bool); ok {
			generalize = v
		}
		targetDevice, _ := nodeCmd.Payload["target_device"].(string)
		return &proto.Command{
			CommandType: &proto.Command_CaptureImage{
				CaptureImage: &proto.CaptureImageCommand{
					ImageId:      imageID.String(),
					UploadUrl:    uploadURL,
					TargetDevice: targetDevice,
					Generalize:   generalize,
				},
			},
		}, nil

	case node_command.SlugInstallPackages:
		packages := stringSliceFromPayload(nodeCmd.Payload["packages"])
		if len(packages) == 0 {
			return nil, fmt.Errorf("install_packages command requires at least one package")
		}
		update := true
		if v, ok := nodeCmd.Payload["update"].(bool); ok {
			update = v
		}
		return &proto.Command{
			CommandType: &proto.Command_InstallPackages{
				InstallPackages: &proto.InstallPackagesCommand{
					Packages: packages,
					Update:   update,
				},
			},
		}, nil

	case node_command.SlugManageService:
		name, _ := nodeCmd.Payload["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("manage_service command requires a unit name")
		}
		action, _ := nodeCmd.Payload["action"].(string)
		unitContent, _ := nodeCmd.Payload["unit_content"].(string)
		return &proto.Command{
			CommandType: &proto.Command_ManageService{
				ManageService: &proto.ManageServiceCommand{
					Name:        name,
					Action:      action,
					UnitContent: unitContent,
				},
			},
		}, nil

	case node_command.SlugDownloadFile:
		url, _ := nodeCmd.Payload["url"].(string)
		dest, _ := nodeCmd.Payload["dest_path"].(string)
		if url == "" || dest == "" {
			return nil, fmt.Errorf("download_file command requires url and dest_path")
		}
		checksum, _ := nodeCmd.Payload["checksum"].(string)
		mode, _ := nodeCmd.Payload["mode"].(string)
		owner, _ := nodeCmd.Payload["owner"].(string)
		extract := false
		if v, ok := nodeCmd.Payload["extract"].(bool); ok {
			extract = v
		}
		return &proto.Command{
			CommandType: &proto.Command_DownloadFile{
				DownloadFile: &proto.DownloadFileCommand{
					Url:      url,
					DestPath: dest,
					Checksum: checksum,
					Extract:  extract,
					Mode:     mode,
					Headers:  parseHeaderMap(nodeCmd.Payload["headers"]),
					Owner:    owner,
				},
			},
		}, nil

	case node_command.SlugWriteFile:
		path, _ := nodeCmd.Payload["path"].(string)
		if path == "" {
			return nil, fmt.Errorf("write_file command requires a path")
		}
		content, _ := nodeCmd.Payload["content"].(string)
		mode, _ := nodeCmd.Payload["mode"].(string)
		owner, _ := nodeCmd.Payload["owner"].(string)
		backup := false
		if v, ok := nodeCmd.Payload["backup"].(bool); ok {
			backup = v
		}
		return &proto.Command{
			CommandType: &proto.Command_WriteFile{
				WriteFile: &proto.WriteFileCommand{
					Path:    path,
					Content: content,
					Mode:    mode,
					Owner:   owner,
					Backup:  backup,
				},
			},
		}, nil

	case node_command.SlugApplyWatchdogConfig:
		wd := &proto.ApplyWatchdogConfigCommand{
			ConfigHash:  stringParam(nodeCmd.Payload, "config_hash"),
			Enabled:     boolParam(nodeCmd.Payload, "enabled"),
			OsHang:      boolParam(nodeCmd.Payload, "os_hang"),
			Network:     boolParam(nodeCmd.Payload, "network"),
			Interface:   stringParam(nodeCmd.Payload, "interface"),
			AgentHealth: boolParam(nodeCmd.Payload, "agent_health"),
			// Forced true regardless of the payload: the on-device pre-arm
			// checks are what turn a typo'd ping target into an error instead
			// of a device that reboots every minute until someone drives to it.
			// A payload that could switch them off (a replayed command, a
			// future caller that forgets the flag) must not be able to.
			VerifyBeforeArm: true,
		}
		wd.WatchdogTimeoutSec = int32Param(nodeCmd.Payload, "watchdog_timeout_sec")
		wd.PingCount = int32Param(nodeCmd.Payload, "ping_count")
		wd.AgentChangeSec = int32Param(nodeCmd.Payload, "agent_change_sec")
		wd.MaxLoad_1 = int32Param(nodeCmd.Payload, "max_load_1")
		wd.MaxLoad_5 = int32Param(nodeCmd.Payload, "max_load_5")
		wd.MaxLoad_15 = int32Param(nodeCmd.Payload, "max_load_15")
		wd.MinMemoryPages = int32Param(nodeCmd.Payload, "min_memory_pages")
		wd.MaxTemperatureC = int32Param(nodeCmd.Payload, "max_temperature_c")
		wd.IntervalSec = int32Param(nodeCmd.Payload, "interval_sec")
		wd.RetryTimeoutSec = int32Param(nodeCmd.Payload, "retry_timeout_sec")
		if raw, ok := nodeCmd.Payload["ping_targets"].([]interface{}); ok {
			for _, t := range raw {
				if str, ok := t.(string); ok && str != "" {
					wd.PingTargets = append(wd.PingTargets, str)
				}
			}
		}
		return &proto.Command{
			CommandType: &proto.Command_ApplyWatchdogConfig{ApplyWatchdogConfig: wd},
		}, nil

	case node_command.SlugDeleteFile:
		path, _ := nodeCmd.Payload["path"].(string)
		if path == "" {
			return nil, fmt.Errorf("delete_file command requires a path")
		}
		backup := false
		if v, ok := nodeCmd.Payload["backup"].(bool); ok {
			backup = v
		}
		return &proto.Command{
			CommandType: &proto.Command_DeleteFile{
				DeleteFile: &proto.DeleteFileCommand{Path: path, Backup: backup},
			},
		}, nil

	default:
		return nil, fmt.Errorf("unknown command slug: %s", nodeCmd.Slug)
	}
}

// stringSliceFromPayload coerces a persisted JSON array (which round-trips as
// []interface{}) into a []string, dropping non-string entries.
// normalizeAgentKind sanitizes the agent's self-declared kind from
// ConnectionInit. Empty means the agent predates the field — preserve whatever
// is stored. Unknown values are recorded as "full": dispatching to an
// over-claiming agent fails cleanly as "unknown command type", while wrongly
// recording "watcher" would make the node unmanageable.
func normalizeAgentKind(kind string) string {
	switch kind {
	case "":
		return ""
	case node.AgentKindWatcher:
		return node.AgentKindWatcher
	case "observer":
		// The Watcher's pre-rename wire value (0.6.x pre-release builds). Mapping it here is the suspenders:
		// falling through to "full" would invite command dispatch to a binary
		// that carries no execution code.
		return node.AgentKindWatcher
	default:
		return node.AgentKindFull
	}
}

// buildAccessConfigPayload resolves the node's effective access config into
// the wire payload shared by apply_access_config and its recovery-mode
// offline variant.
//
// Deliberately NOT built through resolveEffectiveProvisioning: that helper
// swallows errors into a zero value, and a zero access config means "lock
// every managed user" while a zero policy flag means "refuse password
// logins". Silently shipping either on a transient database error would
// lock the operator out of their own hardware, so this path resolves
// directly and fails loudly.
func (s *Server) buildAccessConfigPayload(ctx context.Context, nodeCmd *node_command.NodeCommand) (*proto.ApplyAccessConfigCommand, error) {
	if s.accessResolver == nil {
		return nil, fmt.Errorf("access resolver not available; cannot build apply_access_config")
	}
	if s.organizationalUnitService == nil {
		return nil, fmt.Errorf("organizational unit service not available; cannot resolve password policy")
	}
	eff, err := s.organizationalUnitService.ResolveEffectiveProvisioning(ctx, nodeCmd.AccountID, nodeCmd.NodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve password-auth policy: %w", err)
	}
	// The policy is resolved BEFORE the config so it can be folded into the
	// config hash. Attaching it afterwards left it outside the hash, so
	// flipping only this flag produced an identical hash and the agent
	// short-circuited its no-op check — reporting success while sshd was
	// never rewritten.
	cfg, err := s.accessResolver.ResolveAccessConfig(
		ctx, nodeCmd.AccountID, nodeCmd.NodeID, eff.SSHPasswordAuthManaged, eff.SSHPasswordAuth)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve access config: %w", err)
	}
	// Do not send a configuration that leaves no way into the node. The agent
	// refuses it on arrival, so this changes no outcome on the device — it
	// moves the refusal to the side that can name the cause, and keeps the
	// platform from asking a remote machine to lock itself. Same predicate as
	// the agent's gate, so the two cannot disagree about what a login is.
	if cfg.WouldLockOutAllAccess() {
		return nil, fmt.Errorf(
			"this access configuration would leave no working login on the node: password authentication is turned off " +
				"and no managed user has an SSH key (or a certificate principal with a trusted CA). " +
				"Add a key to a user that applies here, or re-enable password authentication, before applying")
	}

	users := make([]*proto.AccessUser, 0, len(cfg.Users))
	for _, u := range cfg.Users {
		// A locked user is expressed as an empty key set — the agent derives
		// the lock from that rather than from a separate flag, so the two can
		// never disagree.
		keys := u.AuthorizedKeys
		if u.Locked {
			keys = nil
		}
		// CLEAR is emitted, not just SET: without it, removing a password from
		// an entry dropped it from the database and reported has_password
		// false while the node kept accepting that password forever.
		action := proto.PasswordAction_PASSWORD_ACTION_CLEAR
		passwordHash := ""
		if u.HasPassword {
			action = proto.PasswordAction_PASSWORD_ACTION_SET
			// The cleartext password is hashed HERE and the `password` field
			// is left empty: a node's token must never yield cleartext
			// passwords. An unprivileged shell on one node can read that
			// node's adoption token and open a stream as it, and the
			// product's own advertised use case is a contractor with exactly
			// such a login.
			//
			// A failure fails the whole command rather than falling back to
			// cleartext or to no password: silently sending nothing turns
			// "sudo prompts" into NOPASSWD on arrival, which is the opposite
			// of the intent.
			h, err := crypto.HashPasswordForShadow(u.Password)
			if err != nil {
				return nil, fmt.Errorf("hashing the password for %q: %w", u.Username, err)
			}
			passwordHash = h
		}
		users = append(users, &proto.AccessUser{
			Username:       u.Username,
			AuthorizedKeys: keys,
			Sudo:           u.Sudo,
			SudoNopasswd:   u.SudoNoPasswd,
			PasswordAction: action,
			PasswordHash:   passwordHash,
			AdoptUnmanaged: u.AdoptUnmanaged,
			Principals:     u.Principals,
		})
	}

	cas := make([]*proto.AccessCA, 0, len(cfg.CAs))
	for _, c := range cfg.CAs {
		cas = append(cas, &proto.AccessCA{
			CaPublicKey: c.PublicKey,
			Principals:  c.Principals,
		})
	}

	return &proto.ApplyAccessConfigCommand{
		ConfigHash: cfg.Hash,
		// Only claim the setting when a level set it explicitly. Sending
		// the resolved default would force password logins ON for a host
		// whose image already refuses them.
		ManageSshPasswordAuth: cfg.ManagePasswordAuth,
		SshPasswordAuth:       cfg.PasswordAuthEnabled,
		Users:                 users,
		Cas:                   cas,
	}, nil
}

func stringSliceFromPayload(v interface{}) []string {
	switch t := v.(type) {
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	default:
		return nil
	}
}

// parseHeaderMap turns the "Name: value" lines a download_file block carries
// into a header map. {{...}} references were already resolved by the engine.
func parseHeaderMap(v interface{}) map[string]string {
	raw, ok := v.(string)
	if !ok || raw == "" {
		return nil
	}
	headers := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, val, found := strings.Cut(line, ":")
		if found {
			headers[strings.TrimSpace(k)] = strings.TrimSpace(val)
		}
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

// warnDroppedTerminalFrame reports a terminal frame naming a session this node
// does not own, at most once per node per window.
//
// Both bounds matter and neither is cosmetic. The session id is PEER-SUPPLIED
// and is truncated before it is formatted, for the same reason every other
// peer-supplied identifier here is. And the line is throttled per node, because
// terminal frames are deliberately exempt from the inbound pacer — they carry
// PTY bytes to a browser and touch no database, so pacing them would make an
// operator's shell stutter for no gain. Without the throttle an authenticated
// node can drive line-rate writes into a container log ring capped at 20 MB x 5
// and flush the evidence of everything else, including the adoption-rejection
// and pre-auth records these limits exist to protect.
func (s *Server) warnDroppedTerminalFrame(kind, nodeID, sessionID string) {
	if s.terminalWarnings == nil {
		return
	}
	logNow, suppressed := s.terminalWarnings.allow(nodeID, time.Now())
	if !logNow {
		return
	}
	s.logger.Warn("dropped a terminal frame for a session not owned by this node",
		"kind", kind,
		"node_id", nodeID,
		"session", boundedIdentifier(sessionID),
		"suppressed_since_last", suppressed,
		"window_seconds", int(logThrottleWindow.Seconds()))
}
