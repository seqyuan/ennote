package agentflow

// The ready set and the lane that dispatches it: which nodes are dispatchable,
// which concurrency class runs first, and how a batch is chosen from it. Split
// out of orchestrator.go so the run loop stays readable next to the scheduling
// rules it applies.

import (
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// readyHeroTasks returns every dispatchable hero/fan_out/check node: pending
// or interrupted, not on an inactive check-gate branch, and with every
// depends task satisfied (the ready set). A failed-but-optional dependency
// counts as satisfied (depSatisfied), which is what lets the rest of a flow run
// after an optional task gives up. Nodes are already ordered by task
// index (ListNodes), which keeps dispatch deterministic and recovery
// reproducible. Terminal tasks are excluded: they complete the flow instead
// of dispatching a child.
func readyHeroTasks(nodes []*domain.RunAgentFlowNode, byHandle map[string]*domain.RunAgentFlowNode,
	def *domain.FlowDefinition) []*domain.RunAgentFlowNode {
	var ready []*domain.RunAgentFlowNode
	for i := range nodes {
		node := nodes[i]
		if node.TerminalState != domain.FlowNodePending && node.TerminalState != domain.FlowNodeInterrupted {
			continue
		}
		if inactiveBranch(node.Handle, def, byHandle, map[string]bool{}) {
			continue
		}
		if task, ok := def.Tasks[node.Handle]; ok {
			if task.Terminal != nil {
				continue // terminal gates complete the flow; never dispatched
			}
			if uncompletedDep(task, byHandle) != "" {
				continue // depends not yet satisfied; not ready
			}
		}
		ready = append(ready, node)
	}
	return ready
}

// firstSpecialTask returns the first ready check task (or nil). Check tasks
// are deterministic gates that run alone, once the parallel lane has drained.
func firstSpecialTask(ready []*domain.RunAgentFlowNode, def *domain.FlowDefinition, taskType string) *domain.RunAgentFlowNode {
	for _, node := range ready {
		if task, ok := def.Tasks[node.Handle]; ok && task.Type == taskType {
			return node
		}
	}
	return nil
}

// firstFanOutTask returns the first ready fan_out task (or nil). Fan_out
// tasks are read-only (validated) and run alone like checks.
func firstFanOutTask(ready []*domain.RunAgentFlowNode, def *domain.FlowDefinition) *domain.RunAgentFlowNode {
	for _, node := range ready {
		if task, ok := def.Tasks[node.Handle]; ok && task.FanOut != nil {
			return node
		}
	}
	return nil
}

// firstReadyClass picks the lane for the next dispatch window. Reader-first:
// when both classes are ready, read-only tasks go first so the mutation lane
// drains before any writer touches the workspace.
func firstReadyClass(ready []*domain.RunAgentFlowNode) int {
	for _, node := range ready {
		if node.ReadOnly {
			return laneReader
		}
	}
	return laneWriter
}

// selectLaneBatch picks the dispatch batch for the active lane: up to
// parallelism.max same-class hero tasks. Writers stay exclusive unless the
// flow opts into disjoint writes scopes; in that case the batch greedily adds
// writers whose frozen Writes are pairwise disjoint with the batch and with
// the in-flight writers. Returns the batch and whether any ready task belongs
// to the lane.
func selectLaneBatch(ready []*domain.RunAgentFlowNode, lane int, def *domain.FlowDefinition,
	maxParallel int, inFlight []inflightTask) ([]*domain.RunAgentFlowNode, bool) {
	var batch []*domain.RunAgentFlowNode
	found := false
	if lane == laneReader {
		for _, node := range ready {
			if !node.ReadOnly {
				continue
			}
			found = true
			if len(batch) < maxParallel {
				batch = append(batch, node)
			}
		}
		return batch, found
	}
	// Writer lane.
	disjoint := def.DisjointWritersEnabled()
	inFlightScopes := make([][]string, 0, len(inFlight))
	for _, f := range inFlight {
		inFlightScopes = append(inFlightScopes, f.node.Writes)
	}
	for _, node := range ready {
		if node.ReadOnly {
			continue
		}
		found = true
		if len(batch) >= maxParallel {
			continue
		}
		if !disjoint {
			if len(batch) == 0 {
				batch = append(batch, node)
			}
			continue
		}
		compatible := true
		for _, other := range inFlightScopes {
			if !writesDisjoint(node.Writes, other) {
				compatible = false
				break
			}
		}
		if compatible {
			for _, selected := range batch {
				if !writesDisjoint(node.Writes, selected.Writes) {
					compatible = false
					break
				}
			}
		}
		if compatible {
			batch = append(batch, node)
		}
	}
	return batch, found
}
