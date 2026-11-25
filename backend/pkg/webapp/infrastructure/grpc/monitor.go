package grpc

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/websockets"
)

// Monitor handles background monitoring of node connections
type Monitor struct {
	nodeService services.NodeService
	logger      *slog.Logger
	// connectionsSnapshot returns a private copy of the server's live
	// connection registry, taken under the SERVER's lock. The monitor never
	// holds the server's map — see SetConnectionsSnapshot.
	connectionsSnapshot func() map[string]*StreamConnection
	mu                  sync.RWMutex
	stopChan            chan struct{}
	checkInterval       time.Duration
	offlineThreshold    time.Duration
	// transientGrace bounds how long a node may sit in a status a command put
	// it in ('restarting'/'maintenance') before the monitor concludes the device
	// is not coming back. Ten times the offline threshold: a Pi reboots in well
	// under a minute and even a slow one is back inside two, so fifteen minutes
	// is generous enough that a healthy reboot can never trip it, while still
	// bounded — which is the entire point, because nothing else bounds it.
	transientGrace time.Duration
	// cleanupCallback removes a stale connection from the Server's registry and
	// reports whether it was still the REGISTERED one. A false means the node
	// reconnected while this sweep was working, so the monitor must not go on to
	// mark it offline — the entry it swept has already been replaced by a live
	// stream.
	cleanupCallback    func(nodeID string, conn *StreamConnection) bool
	nodeCommandService services.NodeCommandService
	websocketClient    websockets.WebsocketClient
	// abandonedSweepInterval paces the backstop that settles commands which
	// stopped reporting on a node that is still connected.
	abandonedSweepInterval time.Duration
}

// NewMonitor creates a new node monitor
func NewMonitor(nodeService services.NodeService, logger *slog.Logger) *Monitor {
	return &Monitor{
		nodeService:      nodeService,
		logger:           logger,
		stopChan:         make(chan struct{}),
		checkInterval:    30 * time.Second, // Check every 30 seconds
		offlineThreshold: 90 * time.Second, // Mark offline if no heartbeat for 90 seconds (3 missed heartbeats)
		transientGrace:   15 * time.Minute, // 10x the offline threshold; see the field comment
		// Its own cadence, not the health check's: the grace behind it is half
		// an hour, so asking every 30 seconds would be a fleet-wide query for
		// nothing 59 times out of 60.
		abandonedSweepInterval: 5 * time.Minute,
	}
}

// SetNodeCommandService lets the monitor settle the commands of a node it has
// concluded is not coming back. Wired by a setter like the rest of the gRPC
// server's late dependencies; a monitor without it still frees the node's
// status and only leaves the commands, which is the lesser half of the bug.
func (m *Monitor) SetNodeCommandService(service services.NodeCommandService) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodeCommandService = service
}

// SetConnectionsSnapshot gives the monitor a way to READ the server's live
// connection registry without sharing the map itself.
//
// It used to be handed the server's map directly and guard it with the
// monitor's own mutex — while the server wrote to that same map under a
// DIFFERENT mutex. Two locks around one map is not synchronisation: the
// monitor's minute tick ranging the registry while a node connects or
// disconnects is an unsynchronised concurrent iteration and write, which Go
// reports as `fatal error: concurrent map iteration and map write`. That is not
// a panic — it is unrecoverable, it takes the whole control plane down, and
// with restart:always it becomes a restart loop rather than an incident.
// Invisible at three nodes, likely at fifty.
//
// The snapshot is taken by the server under the server's lock, so there is
// exactly one mutex for the map and the monitor holds a private copy.
func (m *Monitor) SetConnectionsSnapshot(snapshot func() map[string]*StreamConnection) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connectionsSnapshot = snapshot
}

// connections returns a private copy of the live registry, or an empty map when
// no provider is wired (a monitor with no server is a no-op, not a crash).
func (m *Monitor) connectionsCopy() map[string]*StreamConnection {
	m.mu.RLock()
	snapshot := m.connectionsSnapshot
	m.mu.RUnlock()
	if snapshot == nil {
		return map[string]*StreamConnection{}
	}
	return snapshot()
}

// SetCleanupCallback sets the callback function to remove stale connections
func (m *Monitor) SetCleanupCallback(callback func(nodeID string, conn *StreamConnection) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupCallback = callback
}

// SetWebsocketClient sets the WebSocket client for sending status updates
func (m *Monitor) SetWebsocketClient(client websockets.WebsocketClient) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.websocketClient = client
}

// Start begins the background monitoring
func (m *Monitor) Start(ctx context.Context) {
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()

	m.logger.Info("node monitor started",
		"check_interval", m.checkInterval,
		"offline_threshold", m.offlineThreshold)

	// A second, slower ticker rather than a counter on the first: the two ask
	// different questions on different timescales, and a shared tick would tie
	// the sweep's cadence to the heartbeat threshold.
	sweep := time.NewTicker(m.abandonedSweepInterval)
	defer sweep.Stop()

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("node monitor stopped")
			return
		case <-m.stopChan:
			m.logger.Info("node monitor stopped")
			return
		case <-ticker.C:
			m.checkNodeHealth(ctx)
		case <-sweep.C:
			m.sweepAbandonedCommands(ctx)
		}
	}
}

// Stop stops the monitor
func (m *Monitor) Stop() {
	close(m.stopChan)
}

// checkNodeHealth checks the health of all connected nodes and actively removes stale connections
func (m *Monitor) checkNodeHealth(ctx context.Context) {
	connections := m.connectionsCopy()

	now := time.Now()
	staleNodes := make([]string, 0)

	for nodeID, conn := range connections {
		// Check if connection is stale
		if now.Sub(conn.LastSeenAt()) > m.offlineThreshold {
			staleNodes = append(staleNodes, nodeID)

			// Parse node ID
			nodeUUID, err := uuid.Parse(nodeID)
			if err != nil {
				m.logger.Error("invalid node ID format", "node_id", nodeID, "error", err)
				continue
			}

			// Cancel the connection context if available
			if conn.Cancel != nil {
				conn.Cancel()
			}

			// Remove connection from registry using callback (which uses Server's mutex)
			m.mu.RLock()
			callback := m.cleanupCallback
			m.mu.RUnlock()

			if callback != nil {
				if !callback(nodeID, conn) {
					// The node reconnected while this sweep was working: the
					// registry now holds a LIVE stream, and marking it offline
					// would contradict a connection that is answering. Leave the
					// status alone and let the next sweep judge the new one.
					m.logger.Info("stale connection was already replaced by a reconnect; not marking the node offline",
						"node_id", nodeID,
						"last_seen", conn.LastSeenAt())
					continue
				}
				m.logger.Info("removed stale connection from registry",
					"node_id", nodeID,
					"last_seen", conn.LastSeenAt(),
					"time_since_last_seen", now.Sub(conn.LastSeenAt()))
			}

			// Mark as offline in database (but skip if node is restarting or shutdown)
			if m.nodeService == nil {
				m.logger.Warn("node service not available, skipping status update", "node_id", nodeID)
				continue
			}

			// Check current node status before marking as offline
			accountUUID, err := uuid.Parse(conn.AccountID)
			if err != nil || accountUUID == uuid.Nil {
				m.logger.Warn("invalid account ID, skipping status update", "node_id", nodeID, "account_id", conn.AccountID)
				continue
			}

			currentNode, err := m.nodeService.GetByIDAndAccount(ctx, nodeUUID, accountUUID)
			if err != nil {
				m.logger.Warn("failed to get current node status, skipping offline update", "node_id", nodeID, "error", err)
				continue
			}

			// Don't mark as offline if node is restarting or shutdown.
			// Bounded, not unconditional: settleUnreachableTransientNodes below
			// takes over once the grace period expires. Without that bound a
			// node that never returns from a reboot reads 'restarting' forever.
			if currentNode.Status == node.StatusRestarting || currentNode.Status == node.StatusShutdown {
				m.logger.Debug("skipping offline status update for node in restarting/shutdown state",
					"node_id", nodeID,
					"current_status", currentNode.Status)
				continue
			}

			if err := m.nodeService.UpdateStatus(ctx, nodeUUID, string(node.StatusOffline)); err != nil {
				m.logger.Error("failed to mark node as offline", "node_id", nodeID, "error", err)
			} else {
				m.logger.Info("node marked as offline due to stale connection",
					"node_id", nodeID,
					"last_seen", conn.LastSeenAt(),
					"time_since_last_seen", now.Sub(conn.LastSeenAt()))

				// Send WebSocket notification
				m.mu.RLock()
				wsClient := m.websocketClient
				m.mu.RUnlock()

				if wsClient != nil && conn.AccountID != "" {
					accountUUID, err := uuid.Parse(conn.AccountID)
					if err == nil {
						// Fetch the updated node to send complete data
						node, err := m.nodeService.GetByIDAndAccount(ctx, nodeUUID, accountUUID)
						if err == nil && node != nil {
							nodeResult := results.NewNodeResult(node)
							err := wsClient.Send(websockets3.NewAccountMessageForOU(accountUUID, node.OUID, websockets3.MessageWrapper{
								Type:    "node_updated",
								Payload: nodeResult,
							}))
							if err != nil {
								m.logger.Warn("failed to send node status update websocket event", "node_id", nodeID, "error", err)
							}
						}
					}
				}
			}
		}
	}

	// Also check database for nodes marked as online but without active connections
	// This handles cases where connections were lost (e.g., server restart) but nodes are still marked online
	if m.nodeService != nil {
		threshold := now.Add(-m.offlineThreshold)
		staleOnlineNodes, err := m.nodeService.GetStaleOnlineNodes(ctx, threshold)
		if err != nil {
			m.logger.Warn("failed to get stale online nodes from database", "error", err)
		} else {
			for _, staleNode := range staleOnlineNodes {
				// Skip if node is already in connections map (handled above)
				nodeIDStr := staleNode.ID.String()
				if _, exists := connections[nodeIDStr]; exists {
					continue
				}

				// Skip if node is restarting or shutdown. (Unreachable in
				// practice — GetStaleOnlineNodes matches 'online' only — which
				// is precisely why the transient states needed a pass of their
				// own; see settleUnreachableTransientNodes.)
				if staleNode.Status == node.StatusRestarting || staleNode.Status == node.StatusShutdown {
					m.logger.Debug("skipping offline status update for node in restarting/shutdown state",
						"node_id", nodeIDStr,
						"current_status", staleNode.Status)
					continue
				}

				// Mark as offline
				if err := m.nodeService.UpdateStatus(ctx, staleNode.ID, string(node.StatusOffline)); err != nil {
					m.logger.Error("failed to mark stale online node as offline", "node_id", nodeIDStr, "error", err)
				} else {
					lastSeenStr := "never"
					timeSinceLastSeen := "unknown"
					if staleNode.LastSeenAt != nil {
						lastSeenStr = staleNode.LastSeenAt.Format(time.RFC3339)
						timeSinceLastSeen = now.Sub(*staleNode.LastSeenAt).String()
					}
					m.logger.Info("node marked as offline due to stale last_seen_at",
						"node_id", nodeIDStr,
						"last_seen_at", lastSeenStr,
						"time_since_last_seen", timeSinceLastSeen)

					// Send WebSocket notification
					m.mu.RLock()
					wsClient := m.websocketClient
					m.mu.RUnlock()

					if wsClient != nil {
						nodeResult := results.NewNodeResult(&staleNode)
						err := wsClient.Send(websockets3.NewAccountMessageForOU(staleNode.AccountID, staleNode.OUID, websockets3.MessageWrapper{
							Type:    "node_updated",
							Payload: nodeResult,
						}))
						if err != nil {
							m.logger.Warn("failed to send node status update websocket event", "node_id", nodeIDStr, "error", err)
						}
					}
				}
			}
		}
	}

	m.settleUnreachableTransientNodes(ctx, now, connections)

	// Log health metrics
	activeCount := len(connections) - len(staleNodes)
	m.logger.Debug("node health check completed",
		"total_connections", len(connections),
		"active_nodes", activeCount,
		"stale_nodes_removed", len(staleNodes))
}

// settleUnreachableTransientNodes bounds the two statuses a command puts a node
// into and nothing else clears.
//
// 'restarting' is set at reboot dispatch on purpose — the agent's own success
// report is unreliable, since it may go down before the report leaves — and the
// two passes above deliberately preserve it through the downtime. Nothing,
// however, ever gave up: the only settler ran on the gRPC reconnect arm, so a
// device that did not come back stayed 'restarting' indefinitely. It never read
// 'offline', so no pill and no alert saw it; its reboot command stayed
// non-terminal, so any workflow waiting on it never finished and it counted
// against the per-account cap of active executions; and because the dispatch
// path refuses non-queueable commands for a node that is neither online nor in
// maintenance, the operator could not even send it a status_check to find out
// what happened. The documented escape was to delete the node — which strands
// the workflow execution instead.
//
// "Did not come back" is the failure mode of the single most-used command in
// the product, so it must have an outcome.
func (m *Monitor) settleUnreachableTransientNodes(ctx context.Context, now time.Time, connections map[string]*StreamConnection) {
	if m.nodeService == nil {
		return
	}
	m.mu.RLock()
	grace := m.transientGrace
	commandService := m.nodeCommandService
	wsClient := m.websocketClient
	m.mu.RUnlock()

	if grace <= 0 {
		return
	}

	nodes, err := m.nodeService.GetStaleTransientNodes(ctx, now.Add(-grace))
	if err != nil {
		m.logger.Warn("failed to list nodes stuck in a transient status", "error", err)
		return
	}

	for _, staleNode := range nodes {
		// A live stream means the device is answering, whatever last_seen_at
		// says. Belt and braces: a heartbeat writes last_seen_at, so a healthy
		// node should not be in this list at all.
		if conn, live := connections[staleNode.ID.String()]; live && now.Sub(conn.LastSeenAt()) <= m.offlineThreshold {
			continue
		}

		previousStatus := staleNode.Status

		// Commands first, node status second. If the status update fails, the
		// commands are still terminal and the next tick retries the status; the
		// reverse order would report a node offline while its reboot still
		// claims to be in progress.
		if commandService != nil {
			if failedCount, err := commandService.FailCommandsForUnreachableNode(ctx, staleNode.ID); err != nil {
				m.logger.Warn("failed to settle commands for an unreachable node", "node_id", staleNode.ID, "error", err)
			} else if failedCount > 0 {
				m.logger.Info("settled commands for a node that did not come back",
					"node_id", staleNode.ID, "previous_status", previousStatus, "commands_failed", failedCount)
			}
		}

		if err := m.nodeService.UpdateStatus(ctx, staleNode.ID, string(node.StatusOffline)); err != nil {
			m.logger.Error("failed to mark an unreachable node offline", "node_id", staleNode.ID, "error", err)
			continue
		}
		m.logger.Warn("node did not return; marked offline",
			"node_id", staleNode.ID,
			"previous_status", previousStatus,
			"grace", grace,
			"last_seen_at", lastSeenLabel(staleNode.LastSeenAt))

		if wsClient == nil {
			continue
		}
		updated, err := m.nodeService.GetByIDAndAccount(ctx, staleNode.ID, staleNode.AccountID)
		if err != nil || updated == nil {
			continue
		}
		if err := wsClient.Send(websockets3.NewAccountMessageForOU(staleNode.AccountID, updated.OUID, websockets3.MessageWrapper{
			Type:    "node_updated",
			Payload: results.NewNodeResult(updated),
		})); err != nil {
			m.logger.Warn("failed to send node status update websocket event", "node_id", staleNode.ID, "error", err)
		}
	}
}

// lastSeenLabel keeps "never" out of the log as a zero timestamp.
func lastSeenLabel(lastSeen *time.Time) string {
	if lastSeen == nil {
		return "never"
	}
	return lastSeen.Format(time.RFC3339)
}

// GetHealthStats returns current health statistics
func (m *Monitor) GetHealthStats() map[string]interface{} {
	connections := m.connectionsCopy()

	m.mu.RLock()
	offlineThreshold := m.offlineThreshold
	checkInterval := m.checkInterval
	m.mu.RUnlock()

	now := time.Now()
	activeCount := 0
	staleCount := 0

	for _, conn := range connections {
		if now.Sub(conn.LastSeenAt()) <= offlineThreshold {
			activeCount++
		} else {
			staleCount++
		}
	}

	return map[string]interface{}{
		"total_connections": len(connections),
		"active_nodes":      activeCount,
		"stale_nodes":       staleCount,
		"check_interval":    checkInterval.String(),
		"offline_threshold": m.offlineThreshold.String(),
	}
}

// sweepAbandonedCommands settles commands that stopped reporting on nodes the
// platform still hears from.
//
// Best-effort and never fatal to the monitor: a failed sweep costs one pass,
// and the next tick tries again. A monitor without the command service simply
// does not sweep, matching how the rest of its late dependencies behave.
func (m *Monitor) sweepAbandonedCommands(ctx context.Context) {
	m.mu.RLock()
	svc := m.nodeCommandService
	m.mu.RUnlock()
	if svc == nil {
		return
	}
	if _, err := svc.FailAbandonedInProgressCommands(ctx); err != nil {
		m.logger.Warn("abandoned-command sweep failed", "error", err)
	}
}
