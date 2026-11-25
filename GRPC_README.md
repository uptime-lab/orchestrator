# Bidirectional gRPC Implementation

This document describes the bidirectional gRPC implementation for agent-platform communication.

## Overview

> **Note:** the message/command inventory below reflects the original
> shutdown/restart/status-check set. The protocol has grown a lot since — the
> authoritative, current source of truth is **`proto/orchestrator.proto`**.
> An enumeration kept here goes stale (one did), so do not re-enumerate: count the
> `Command.command_type` oneof in the proto, because that is the list path policy and
> agent version floors hang off. Among the additions are every file-touching command
> (`patch_file`, `read_file`, `wait_for_file`, `write_file`, `delete_file`,
> `download_file`, `update_file`), `run_script`, `apply_access_config`,
> `apply_watchdog_config`, `capture_image`, `install_packages` and `manage_service`.
> Node→platform also sends
> `MetricsUpdate` (+ThermalZone), `BootInfo.uptime_seconds` and Terminal frames
> (open/input/close/output/closed) multiplexed over the same stream.
> Agent ≥0.2.3 reports `failed` for unknown command types.

The implementation provides a clean, robust bidirectional streaming gRPC solution that enables:
- **Heartbeats** from nodes to platform (every 30 seconds)
- **Commands** from platform to nodes (see the note above; originally shutdown/restart/status-check)
- **Status updates** from nodes to platform (restarting, shutting_down, online, etc.)
- **Live metrics** and a **PTY terminal**, both multiplexed over the same stream

## Architecture

### Proto Definition

The protocol is defined in `./proto/orchestrator.proto` with two main message types:

**NodeToPlatform** (agent → backend):
- `ConnectionInit`: Initial connection — see "Connection identity" below
- `Heartbeat`: Regular heartbeat with sequence number
- `StatusUpdate`: Status changes (restarting, shutting_down, etc.)

**PlatformToNode** (backend → agent):
- `Command`: Commands with unique ID for tracking. The three below are the original set;
  the oneof carries many more today — read the proto for the current list.
  - `ShutdownCommand`: Shutdown with delay
  - `RestartCommand`: Restart with delay  
  - `StatusCheckCommand`: Request current status
- `IdentityAssignment`: hands a master-token adoptee its own permanent node credential

### Connection identity

`ConnectionInit` is more than "fingerprint + version". It carries:

- `fingerprint` — the hardware fingerprint (serial, MACs, board facts) the
  platform binds the node to;
- `agent_version` — checked against the per-command version floors;
- `agent_kind` — what the binary IS: empty or `full` for the managing agent
  (`agent/cmd/agent`), `watcher` for the telemetry-only binary
  (`agent/cmd/watcher`, shipped in the same `.deb`) that carries no
  command-execution code; the platform refuses to dispatch commands to a
  watcher;
- `agent_instance_id` — a fresh random id per agent PROCESS, stable across that
  process's reconnects, so the platform can tell a stream blip (same id) from
  an agent restart (new id);
- `led_sources` — the LED indication sources this agent can render.

Fields an older agent does not send fall back to the documented defaults in
`proto/orchestrator.proto`.

### Service Definition

```protobuf
service NodeCommunication {
  rpc EstablishStream(stream NodeToPlatform) returns (stream PlatformToNode);
}
```

## Implementation

### Backend (Platform)

**Files:**
- `backend/pkg/webapp/infrastructure/grpc/server.go` - gRPC server implementation
- `backend/pkg/webapp/application/ports/service/node_communication_service.go` - Service interface
- `backend/pkg/webapp/application/config/config.go` - gRPC configuration

**Features:**
- Validates node credentials on connection
- Maintains active connections map
- Handles heartbeats and status updates
- Provides methods to send commands to specific nodes
- Graceful shutdown handling

### Agent (Node)

**Files:**
- `agent/pkg/infrastructure/grpc/platform_client.go` - gRPC client implementation
- `agent/pkg/application/handlers/command_handler.go` - Command execution logic
- `agent/pkg/application/service/agent_service.go` - Main orchestration

**Features:**
- Establishes bidirectional stream on startup
- Sends heartbeats every 30 seconds
- Listens for and executes commands
- Sends status updates immediately when commands are received
- Handles reconnection with exponential backoff — **and so does the FIRST connect.**
  If `AgentService.Run` returned on the initial dial failure, `main` would exit and
  systemd would restart the whole process every 10 s. It retries in process instead,
  bounded to 30 minutes, after which it still exits — that exit re-reads the adoption
  file and lets systemd recycle wedged internal state, so it is kept on purpose. Note
  the process does **not** exit once
  connected: `StartReconnectionLoop` retries forever in a goroutine without `Run` returning,
  which is why the master-token identity handover has to be applied in place rather than
  waiting for a restart to pick up the node's own credential.

## Configuration

### Backend Configuration

Add to `config_local/config.yaml`:

```yaml
grpc:
  port: 50051
  host: "0.0.0.0"
```

### Agent Adoption File

The file on disk is ONE line of `base64(JSON)` — never raw JSON. The agent
reads a single line and base64-decodes it unconditionally before unmarshalling
(`agent/pkg/application/config`), so a hand-written raw-JSON file fails with
`failed to base64 decode adoption file`. The DECODED payload looks like this:

```json
{
  "node_id": "123e4567-e89b-12d3-a456-426614174000",
  "account_id": "987fcdeb-51a2-43d1-b456-426614174111",
  "unique_token": "sample-unique-token-12345",
  "platform_url": "http://localhost:8080",
  "grpc_endpoint": "localhost:50051",
  "created_at": 1234567890
}
```

Identity is `node_id`+`unique_token` XOR `master_token` (the provisioning-card
flow carries the latter and no node identity). The file the agent reads is
the one its `--adoption-file` flag names: the packaged
`orchestrator-agent.service` passes `/etc/orchestrator/node-adoption.txt`; the
binary's own default, used when the flag is absent (the recovery image), is
`/mnt/sdcard/node-adoption.txt`; the watcher's default is
`/etc/orchestrator-watcher/node-adoption.txt`. To write a file by hand for
troubleshooting a packaged agent:

```bash
printf '%s' '{"node_id":"...","account_id":"...","unique_token":"...","platform_url":"...","grpc_endpoint":"..."}' \
  | base64 -w0 | sudo tee /etc/orchestrator/node-adoption.txt >/dev/null
sudo systemctl restart orchestrator-agent
```

## Command Execution

Commands are dispatched over the gRPC stream and executed by the agent's command
handler (`agent/pkg/application/handlers/`) — there is no Makefile involved on
either side, and no `make status` / `make shutdown` / `make restart` targets exist in
the root, agent or backend Makefiles. To exercise a command by hand, call the HTTP
route that registers it and let the platform dispatch it, or use the node's Actions
panel in the UI.

## Sending frames: invariants

These two rules are not style preferences. Violating either takes the backend process
down, and both are invisible to the compiler and to grpc-go.

**1. Never call `Stream.Send` on a registered connection — use `StreamConnection.Send`**
(`backend/pkg/webapp/infrastructure/grpc/stream_writer.go`). grpc-go forbids
concurrent `SendMsg` on one stream and does *not* detect it. Two independent writers —
the command dispatch loop and the WebSocket terminal handlers — meet whenever a web
terminal is opened on a node while a command is dispatched to it, an ordinary
debugging action, which is exactly when you least want the control plane to die.
Each connection therefore owns one writer goroutine fed by a 64-frame buffered
channel. Two consequences follow: an enqueue to a node whose buffer is full fails
after a 500 ms grace rather than blocking (a queueable command stays registered and is
redelivered; anything else is failed with a reason), and one agent that has stopped
reading can no longer stall dispatch for the whole fleet, because dispatch is a single
goroutine. There is exactly **one** legitimate direct `stream.Send`: the master-token
identity handover in `server.go`, which runs before the connection and its writer
exist. Moving that below registration without switching it to `Send` reintroduces the
bug.

**2. An oversized frame kills the STREAM, not the command — so bound every payload at
its source.** The per-frame ceiling is explicit and symmetric on both sides
(`maxGRPCMessageBytes`, 8 MiB, in `infrastructure/grpc/server.go` and the agent's
`platform_client.go`) instead of grpc-go's silent defaults, which were asymmetric:
4 MiB receive, effectively unlimited send. An uncapped `run_script` output (a
playbook step doing `journalctl -b`) is enough to tear down the node's connection,
lose the result, leave the `node_command` in `in_progress` until the abandonment
sweep, and leave the execution short of `completed_at`, permanently consuming one of
the per-account active-execution slots. The ceiling is only the
backstop for older agents; the actual fix is the agent bounding what it sends
(`handlers.MaxResultOutputBytes`, 128 KiB per output value, with the truncation stated
inside the value). The largest *legitimate* frame is a 512 KiB node-file body, so
8 MiB is headroom for the rollout, not a target.

## Usage

### 1. Generate Proto Code

```bash
make proto/tools      # once: installs protoc-gen-go + protoc-gen-go-grpc
make proto/generate   # regenerates AND copies into agent/ and backend/
```

There is no `proto/generate.sh`. The copy step matters: `agent/` and `backend/` each
keep their own copy of the generated Go under `.../grpc/proto/`, and hand-editing one
because the documented step failed silently desynchronises the contract.

### 2. Start Backend

```bash
cd backend
go run ./cmd/server
```

The backend will start:
- HTTP server on port 8080
- gRPC server on port 50051

### 3. Start Agent

```bash
cd agent
# an adoption file in the format above, for a node created on the local server
go run ./cmd/agent -adoption-file=/path/to/node-adoption.txt
```

### 4. Test the Implementation

There is no `./test-grpc.sh`. Run `make test` (the backend, agent and
entitlement-signer unit suites) and exercise the
stream against a real node — full-stack verification happens against real hardware,
not locally.

## Example Flow

1. **Agent connects:**
   ```
   Agent → Backend: NodeToPlatform{ connection: {fingerprint, agent_version, agent_kind, agent_instance_id, led_sources} }
   ```

2. **Regular heartbeats:**
   ```
   Agent → Backend: NodeToPlatform{ heartbeat: {seq: 1, timestamp} }
   ```

3. **Platform sends restart command:**
   ```
   Backend → Agent: PlatformToNode{ command: {id: "cmd-123", restart: {delay: 60}} }
   Agent → Backend: NodeToPlatform{ status_update: {status: "restarting", message: "Restart in 60s"} }
   ```

4. **Platform requests status:**
   ```
   Backend → Agent: PlatformToNode{ command: {id: "cmd-456", status_check: {}} }
   Agent → Backend: NodeToPlatform{ status_update: {status: "online", message: "All systems OK"} }
   ```

## Key Design Decisions

1. **No Acknowledgments**: Status updates are sent immediately, no need for command response acks
2. **Separate Message Types**: Clean separation between request/response directions
3. **Stream-per-node**: Each agent maintains one long-lived bidirectional stream
4. **Writer-per-connection**: every outbound frame goes through that connection's writer
   goroutine — see "Sending frames: invariants" above
5. **Command Tracking**: UUID-based command IDs for logging and correlation
6. **Status Management**: Real-time status updates based on command execution

## Testing

`make test` runs the Go unit suites. Note two limits before trusting a green run: on
macOS `go test ./...` does not compile the agent's `//go:build linux` files at all, so
none of the device-side command handlers are built let alone exercised, and there is
no end-to-end harness for the stream itself. Protocol changes are verified against
real hardware.
