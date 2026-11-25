package workflow_template

import (
	"fmt"
	"sort"
	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/cronexpr"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
)

// GraphSchemaVersion is the current workflow graph schema version.
const GraphSchemaVersion = 1

// GraphPosition is the editor-only canvas position of a node; the execution
// engine ignores it.
type GraphPosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// BlockPolicy is the per-block execution policy.
type BlockPolicy struct {
	// Retries re-dispatches a failed block this many times before giving up.
	Retries int `json:"retries,omitempty"`
	// TimeoutSeconds fails a block whose work has not completed in time
	// (0 = no timeout). The underlying agent command is not cancelled.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// OnError: "fail" (default) stops the node's run, "continue" follows the
	// normal output as if the block succeeded.
	OnError string `json:"on_error,omitempty"`
}

const (
	OnErrorFail     = "fail"
	OnErrorContinue = "continue"
)

// GraphNode is one block instance placed on the workflow canvas.
type GraphNode struct {
	ID          string                 `json:"id"`
	Type        string                 `json:"type"` // block type from the registry, e.g. "command.reboot"
	Name        string                 `json:"name,omitempty"`
	Description string                 `json:"description,omitempty"`
	Params      map[string]interface{} `json:"params,omitempty"`
	Policy      *BlockPolicy           `json:"policy,omitempty"`
	Position    GraphPosition          `json:"position"`
}

// GraphEdge connects an output port of one node to another node.
type GraphEdge struct {
	ID       string `json:"id,omitempty"`
	From     string `json:"from"`
	To       string `json:"to"`
	FromPort string `json:"from_port,omitempty"` // defaults to "out"
}

// Port returns the edge's source port with the default applied.
func (e GraphEdge) Port() string {
	if e.FromPort == "" {
		return workflow_block.DefaultOutputPort
	}
	return e.FromPort
}

// WorkflowGraph is the node-graph definition of a workflow template.
type WorkflowGraph struct {
	SchemaVersion int         `json:"schema_version"`
	Nodes         []GraphNode `json:"nodes"`
	Edges         []GraphEdge `json:"edges"`
}

// NodeByID returns the graph node with the given id.
func (g *WorkflowGraph) NodeByID(id string) (GraphNode, bool) {
	for _, node := range g.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return GraphNode{}, false
}

// TriggerNode returns the graph's entry node (the single trigger.* block).
func (g *WorkflowGraph) TriggerNode() (GraphNode, bool) {
	for _, node := range g.Nodes {
		if strings.HasPrefix(node.Type, "trigger.") {
			return node, true
		}
	}
	return GraphNode{}, false
}

// EdgesFrom returns the outgoing edges of a node, optionally filtered by port.
func (g *WorkflowGraph) EdgesFrom(nodeID, port string) []GraphEdge {
	var out []GraphEdge
	for _, edge := range g.Edges {
		if edge.From == nodeID && (port == "" || edge.Port() == port) {
			out = append(out, edge)
		}
	}
	return out
}

// EdgesTo returns the incoming edges of a node.
func (g *WorkflowGraph) EdgesTo(nodeID string) []GraphEdge {
	var out []GraphEdge
	for _, edge := range g.Edges {
		if edge.To == nodeID {
			out = append(out, edge)
		}
	}
	return out
}

// ScheduleCron returns the trigger's cron expression ("" = manual only).
func (g *WorkflowGraph) ScheduleCron() string {
	trigger, ok := g.TriggerNode()
	if !ok {
		return ""
	}
	cron, _ := trigger.Params["cron"].(string)
	return strings.TrimSpace(cron)
}

// Validate checks graph structure and per-node params against the block
// registry. The graph must be a DAG with a single trigger entry; branching
// rules: condition ports carry at most one edge each, the default "out" port
// may fan out to several parallel edges, and only control.join blocks accept
// more than one incoming edge.
// MaxGraphNodes and MaxGraphEdges bound one playbook document. See validate.
const (
	MaxGraphNodes = 500
	MaxGraphEdges = 2000
)

func (g *WorkflowGraph) Validate() error {
	return g.validate(true)
}

// ValidateDraft applies the same structural rules but tolerates missing
// required parameters (present values are still type-checked): a draft is
// work in progress, hidden from run pickers and the cron scheduler, so an
// unfilled Target Device must not block saving. Activation runs Validate.
func (g *WorkflowGraph) ValidateDraft() error {
	return g.validate(false)
}

func (g *WorkflowGraph) validate(requireParams bool) error {
	if g.SchemaVersion != GraphSchemaVersion {
		return fmt.Errorf("unsupported graph schema_version %d (expected %d)", g.SchemaVersion, GraphSchemaVersion)
	}
	if len(g.Nodes) == 0 {
		return fmt.Errorf("graph must have at least one node")
	}
	// A ceiling on the document itself. Nothing else bounds it: a graph is
	// validated, linearized, snapshotted into every execution and walked once
	// per pass, so cost is superlinear in node count while the row that carries
	// it is free to write. Set two orders of magnitude above anything a human
	// authors in the canvas editor — this is a runaway guard, not a product
	// limit, and it should never be the thing an operator meets.
	if len(g.Nodes) > MaxGraphNodes {
		return fmt.Errorf("graph has %d nodes, which is over the %d-node limit", len(g.Nodes), MaxGraphNodes)
	}
	if len(g.Edges) > MaxGraphEdges {
		return fmt.Errorf("graph has %d edges, which is over the %d-edge limit", len(g.Edges), MaxGraphEdges)
	}

	nodesByID := make(map[string]GraphNode, len(g.Nodes))
	triggerCount := 0
	executable := 0
	for i, node := range g.Nodes {
		if node.ID == "" {
			return fmt.Errorf("node %d: id is required", i)
		}
		if _, dup := nodesByID[node.ID]; dup {
			return fmt.Errorf("duplicate node id '%s'", node.ID)
		}
		nodesByID[node.ID] = node

		block, known := workflow_block.FindByType(node.Type)
		if !known {
			return fmt.Errorf("node '%s': unknown block type '%s'", node.ID, node.Type)
		}
		if strings.HasPrefix(node.Type, "trigger.") {
			triggerCount++
		} else {
			executable++
		}
		paramErr := block.ValidateParamTypes(node.Params)
		if requireParams {
			paramErr = block.ValidateParams(node.Params)
		}
		if paramErr != nil {
			return fmt.Errorf("node '%s' (%s): %w", node.ID, node.Type, paramErr)
		}
		// A schedule the platform cannot fire is refused HERE, where the author
		// can still fix it. Nothing parsed the expression at save time, and the
		// matcher resolved an out-of-range field to "never matches" — so
		// `99 * * * *` saved cleanly, was examined every minute forever, and the
		// run simply never happened. Skipped when blank: a draft is allowed an
		// unfilled required param, and ValidateDraft still type-checks what is
		// present.
		if node.Type == workflow_block.TypeSchedule {
			cron, _ := node.Params["cron"].(string)
			if cron = strings.TrimSpace(cron); cron != "" {
				if err := cronexpr.Validate(cron); err != nil {
					return fmt.Errorf("node '%s' (%s): %w", node.ID, node.Type, err)
				}
			}
		}
		if err := validatePolicy(node.Policy); err != nil {
			return fmt.Errorf("node '%s': %w", node.ID, err)
		}
	}
	if triggerCount != 1 {
		return fmt.Errorf("graph must have exactly one trigger block (found %d)", triggerCount)
	}
	if executable == 0 {
		return fmt.Errorf("graph must have at least one executable block")
	}

	trigger, _ := g.TriggerNode()

	edgesPerPort := make(map[string]int, len(g.Edges))
	incoming := make(map[string]int, len(g.Edges))
	adjacency := make(map[string][]string, len(g.Edges))
	for i, edge := range g.Edges {
		fromNode, ok := nodesByID[edge.From]
		if !ok {
			return fmt.Errorf("edge %d: unknown source node '%s'", i, edge.From)
		}
		toNode, ok := nodesByID[edge.To]
		if !ok {
			return fmt.Errorf("edge %d: unknown target node '%s'", i, edge.To)
		}
		if edge.From == edge.To {
			return fmt.Errorf("edge %d: self-loop on node '%s'", i, edge.From)
		}
		if strings.HasPrefix(toNode.Type, "trigger.") {
			return fmt.Errorf("edge %d: the trigger block must not have incoming edges", i)
		}

		port := edge.Port()
		block, _ := workflow_block.FindByType(fromNode.Type)
		if !block.HasOutputPort(port) {
			return fmt.Errorf("edge %d: node '%s' has no output port '%s'", i, edge.From, port)
		}
		// Condition ports (anything but the default) route a decision and
		// carry at most one edge; the default port may fan out in parallel.
		portKey := edge.From + "\x00" + port
		edgesPerPort[portKey]++
		if port != workflow_block.DefaultOutputPort && edgesPerPort[portKey] > 1 {
			return fmt.Errorf("node '%s': port '%s' has more than one outgoing edge", edge.From, port)
		}

		incoming[edge.To]++
		if incoming[edge.To] > 1 && toNode.Type != workflow_block.TypeJoin {
			return fmt.Errorf("node '%s' has more than one incoming edge (use a Join block to merge paths)", edge.To)
		}
		adjacency[edge.From] = append(adjacency[edge.From], edge.To)
	}

	// Reachability from the trigger (BFS over all ports).
	visited := map[string]bool{trigger.ID: true}
	queue := []string{trigger.ID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range adjacency[current] {
			if !visited[next] {
				visited[next] = true
				queue = append(queue, next)
			}
		}
	}
	if len(visited) != len(g.Nodes) {
		return fmt.Errorf("all nodes must be connected to the trigger (%d of %d reachable)", len(visited), len(g.Nodes))
	}

	// Acyclicity (Kahn's algorithm).
	degree := make(map[string]int, len(g.Nodes))
	for _, node := range g.Nodes {
		degree[node.ID] = 0
	}
	for _, edge := range g.Edges {
		degree[edge.To]++
	}
	queue = queue[:0]
	for id, d := range degree {
		if d == 0 {
			queue = append(queue, id)
		}
	}
	processed := 0
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		processed++
		for _, next := range adjacency[current] {
			degree[next]--
			if degree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if processed != len(g.Nodes) {
		return fmt.Errorf("graph contains a cycle")
	}

	return nil
}

func validatePolicy(policy *BlockPolicy) error {
	if policy == nil {
		return nil
	}
	if policy.Retries < 0 || policy.Retries > 10 {
		return fmt.Errorf("policy retries must be between 0 and 10")
	}
	if policy.TimeoutSeconds < 0 || policy.TimeoutSeconds > 86400 {
		return fmt.Errorf("policy timeout_seconds must be between 0 and 86400")
	}
	if policy.OnError != "" && policy.OnError != OnErrorFail && policy.OnError != OnErrorContinue {
		return fmt.Errorf("policy on_error must be 'fail' or 'continue'")
	}
	return nil
}

// CommandNodes returns the command.* nodes in a stable topological order
// (execution order for chains; a deterministic linearization for branching
// graphs, used for the legacy step list). The graph must be valid.
func (g *WorkflowGraph) CommandNodes() []GraphNode {
	order := g.topologicalOrder()
	out := make([]GraphNode, 0, len(order))
	for _, id := range order {
		node, _ := g.NodeByID(id)
		if strings.HasPrefix(node.Type, "command.") {
			out = append(out, node)
		}
	}
	return out
}

// topologicalOrder returns node ids in a deterministic topological order.
func (g *WorkflowGraph) topologicalOrder() []string {
	degree := make(map[string]int, len(g.Nodes))
	adjacency := make(map[string][]string, len(g.Edges))
	for _, node := range g.Nodes {
		degree[node.ID] = 0
	}
	for _, edge := range g.Edges {
		if _, ok := degree[edge.From]; !ok {
			continue
		}
		if _, ok := degree[edge.To]; !ok {
			continue
		}
		degree[edge.To]++
		adjacency[edge.From] = append(adjacency[edge.From], edge.To)
	}
	var frontier []string
	for _, node := range g.Nodes { // insertion order keeps determinism
		if degree[node.ID] == 0 {
			frontier = append(frontier, node.ID)
		}
	}
	var order []string
	for len(frontier) > 0 {
		sort.Strings(frontier)
		current := frontier[0]
		frontier = frontier[1:]
		order = append(order, current)
		for _, next := range adjacency[current] {
			degree[next]--
			if degree[next] == 0 {
				frontier = append(frontier, next)
			}
		}
	}
	return order
}

// CommandSteps converts the graph's command blocks into the ordered step list
// kept alongside the graph (legacy display/back-compat; the engine executes
// the graph itself). Server-executed blocks have no step representation.
func (g *WorkflowGraph) CommandSteps() ([]WorkflowStep, error) {
	commandNodes := g.CommandNodes()
	steps := make([]WorkflowStep, 0, len(commandNodes))
	for i, node := range commandNodes {
		block, known := workflow_block.FindByType(node.Type)
		if !known || block.Slug == "" {
			return nil, fmt.Errorf("node '%s': block type '%s' has no node-command mapping", node.ID, node.Type)
		}
		payload := node.Params
		if payload == nil {
			payload = map[string]interface{}{}
		}
		steps = append(steps, WorkflowStep{
			Order:       i,
			Command:     CommandSpec{Slug: block.Slug, Payload: payload},
			Name:        node.Name,
			Description: node.Description,
		})
	}
	return steps, nil
}

// GraphFromSteps synthesizes a chain graph from a legacy step list so older
// templates open in the canvas editor. Positions form a simple vertical
// layout; the editor is free to re-layout.
func GraphFromSteps(steps []WorkflowStep) *WorkflowGraph {
	sorted := make([]WorkflowStep, len(steps))
	copy(sorted, steps)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Order < sorted[j].Order })

	const (
		columnX  = 120.0
		startY   = 60.0
		rowSpace = 140.0
	)

	graph := &WorkflowGraph{
		SchemaVersion: GraphSchemaVersion,
		Nodes: []GraphNode{{
			ID:       "start",
			Type:     workflow_block.TypeStart,
			Position: GraphPosition{X: columnX, Y: startY},
		}},
		Edges: []GraphEdge{},
	}

	previousID := "start"
	for i, step := range sorted {
		nodeID := fmt.Sprintf("step-%d", i)
		graph.Nodes = append(graph.Nodes, GraphNode{
			ID:          nodeID,
			Type:        "command." + step.Command.Slug,
			Name:        step.Name,
			Description: step.Description,
			Params:      step.Command.Payload,
			Position:    GraphPosition{X: columnX, Y: startY + rowSpace*float64(i+1)},
		})
		graph.Edges = append(graph.Edges, GraphEdge{
			ID:       fmt.Sprintf("edge-%d", i),
			From:     previousID,
			To:       nodeID,
			FromPort: workflow_block.DefaultOutputPort,
		})
		previousID = nodeID
	}

	return graph
}
