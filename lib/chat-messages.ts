import type { ArtifactReference } from "./artifacts";
import type { components } from "./worker-api.gen";
import { classifyDisplayRisk, type DisplayRiskClass, type ToolActivityState } from "./tool-presentation";

export type CanonicalMessage = components["schemas"]["Message"];
export type CanonicalMessagePage = components["schemas"]["SessionMessagePage"];
export type TurnMetric = components["schemas"]["TurnMetric"];
type CanonicalPart = CanonicalMessage["parts"][number];
type GeneratedCheckpoint = components["schemas"]["ContextCompaction"];

export type ContextCheckpoint = Pick<GeneratedCheckpoint,
  "id" | "status" | "reason" | "summary" | "reclaimedTokens" | "firstKeptMessageId" |
  "sourceThroughMessageId" | "baseLeafMessageId" | "createdAt">;

export interface TurnMessage {
  id: string;
  role: string;
  text: string;
  thinking?: string;
  kind?: string;
  toolName?: string;
  toolCallId?: string;
  arguments?: Record<string, unknown>;
  argumentsFragment?: string;
  isError?: boolean;
  toolState?: ToolActivityState;
  sourceMessageId?: string;
  createdAt?: string;
  runId?: string;
}

export interface UserStep {
  id: string;
  text: string;
  sourceMessageId?: string;
  createdAt?: string;
}

export type AssistantBlock =
  | { kind: "text"; text: string }
  | { kind: "thinking"; text: string }
  | { kind: "image"; artifactId: string; mimeType: string; width: number; height: number }
  | { kind: "image_description"; artifactId: string; text: string };

export interface SpeakerLabel {
  kind?: string;
  objectId?: string;
  versionId?: string;
  handle?: string;
  displayName?: string;
  icon?: string;
  color?: string;
  /** Denormalized from the run's frozen effective config (see MessageRepo.Page). */
  modelProfileId?: string;
  apiModel?: string;
  /** Resolved display name (set by useChatController from the model catalog). */
  modelName?: string;
}

export interface AssistantStep {
  kind: "assistant";
  id: string;
  blocks: AssistantBlock[];
  speaker?: SpeakerLabel;
  sourceMessageId?: string;
  createdAt?: string;
}

export interface ToolActivity {
  id: string;
  toolCallId: string;
  toolName: string;
  arguments?: Record<string, unknown>;
  argumentsFragment?: string;
  result?: { content: string; isError: boolean; artifacts: ArtifactReference[] };
  state: ToolActivityState;
  riskClass: DisplayRiskClass;
  sourceMessageIds: string[];
  runId?: string;
}

export interface ToolBatchStep {
  kind: "tool_batch";
  id: string;
  activities: ToolActivity[];
}

export interface SteerStep {
  kind: "steer";
  id: string;
  text: string;
  createdAt?: string;
}

export type ConversationStep = AssistantStep | ToolBatchStep | SteerStep;

export interface ConversationTurn {
  kind: "turn";
  id: string;
  user?: UserStep;
  steps: ConversationStep[];
  messageIds: string[];
  runId?: string;
  metrics?: TurnMetric;
}

export interface CheckpointNode {
  kind: "checkpoint";
  id: string;
  reason: string;
  summary: string;
  reclaimedTokens: number;
  createdAt: string;
}

export type ConversationNode = ConversationTurn | CheckpointNode;

type ProjectionItem = { kind: "message"; value: CanonicalMessage } | { kind: "checkpoint"; value: ContextCheckpoint };

export function prependCanonicalMessages(current: CanonicalMessage[], older: CanonicalMessage[]): CanonicalMessage[] {
  const seen = new Set<string>();
  return [...older, ...current].filter(message => {
    if (seen.has(message.id)) return false;
    seen.add(message.id);
    return true;
  });
}

export function reconcileLatestMessages(current: CanonicalMessage[], latest: CanonicalMessage[]): CanonicalMessage[] {
  if (current.length === 0) return latest;
  if (latest.length === 0) return current;
  const latestIDs = new Set(latest.map(message => message.id));
  const overlap = current.findIndex(message => latestIDs.has(message.id));
  if (overlap < 0) return latest;
  return [...current.slice(0, overlap), ...latest];
}

/**
 * The stable prefix of the timeline projection: canonical messages + completed
 * checkpoints. It is deliberately NOT flushed at the end — the open tail turn
 * (plus its tool registry) is returned so `applyTransient` can continue it
 * without re-projecting the whole history.
 */
export interface TimelineBase {
  /** Flushed turns + checkpoint nodes (reference-stable across applies). */
  nodes: ConversationNode[];
  /** Unflushed tail turn left open by the canonical projection. */
  openTurn: ConversationTurn | null;
  /** Tool-call registry for `openTurn` (resume/retry merge target). */
  calls: Map<string, ToolActivity>;
  /** Orphan-turn id counter, carried so transient ids never collide. */
  syntheticTurn: number;
}

export function projectBase(
  messages: CanonicalMessage[],
  checkpoints: ContextCheckpoint[],
  turnMetrics?: Map<string, TurnMetric>,
): TimelineBase {
  const nodes: ConversationNode[] = [];
  let turn: ConversationTurn | null = null;
  let syntheticTurn = 0;
  const calls = new Map<string, ToolActivity>();

  const flushTurn = () => {
    if (!turn) return;
    if (turn.runId) turn.metrics = turnMetrics?.get(turn.runId);
    if (turn.user || turn.steps.length > 0) nodes.push(turn);
    turn = null;
    calls.clear();
  };
  const ensureTurn = () => {
    if (!turn) {
      syntheticTurn += 1;
      turn = { kind: "turn", id: `turn-orphan-${syntheticTurn}`, steps: [], messageIds: [] };
    }
    return turn;
  };

  for (const item of projectionItems(messages, checkpoints)) {
    if (item.kind === "checkpoint") {
      flushTurn();
      nodes.push(checkpointNode(item.value));
      continue;
    }
    projectCanonicalIntoTurn(item.value);
  }
  return { nodes, openTurn: turn, calls, syntheticTurn };

  function projectCanonicalIntoTurn(message: CanonicalMessage) {
    if (message.role === "user") {
      flushTurn();
      turn = {
        kind: "turn",
        id: `turn-${message.id}`,
        user: { id: message.id, sourceMessageId: message.id, text: userText(message.parts), createdAt: message.createdAt },
        steps: [],
        messageIds: [message.id],
      };
      return;
    }
    const active = ensureTurn();
    active.messageIds.push(message.id);
    if (message.runId) active.runId = active.runId ?? message.runId;
    if (message.role === "assistant") projectAssistantParts(active, message);
    else if (message.role === "tool") projectToolParts(active, message);
  }

  function projectAssistantParts(active: ConversationTurn, message: CanonicalMessage) {
    let assistantBlocks: AssistantBlock[] = [];
    let batch: ToolActivity[] = [];
    const flushAssistant = () => {
      if (assistantBlocks.length === 0) return;
      active.steps.push({ kind: "assistant", id: `${message.id}-assistant-${active.steps.length}`,
        sourceMessageId: message.id, blocks: assistantBlocks,
        speaker: { ...message.speakerSnapshot, modelProfileId: message.modelProfileId, apiModel: message.apiModel },
        createdAt: message.createdAt });
      assistantBlocks = [];
    };
    const flushBatch = () => {
      if (batch.length === 0) return;
      addToolBatch(active, batch, calls);
      batch = [];
    };
    for (const part of message.parts) {
      if (part.type === "tool_call") {
        flushAssistant();
        const activity: ToolActivity = {
          id: `${message.id}-${part.toolCall.id}`,
          toolCallId: part.toolCall.id,
          toolName: part.toolCall.name,
          arguments: part.toolCall.arguments,
          argumentsFragment: part.toolCall.argumentsFragment,
          state: part.toolCall.partial ? "interrupted" : "pending",
          riskClass: classifyDisplayRisk(part.toolCall.name),
          sourceMessageIds: [message.id],
          runId: message.runId,
        };
        batch.push(activity);
        calls.set(activity.toolCallId, activity);
      } else {
        flushBatch();
        const block = assistantBlock(part);
        if (block) assistantBlocks.push(block);
      }
    }
    flushAssistant();
    flushBatch();
  }

  function projectToolParts(active: ConversationTurn, message: CanonicalMessage) {
    const orphaned: ToolActivity[] = [];
    for (const part of message.parts) {
      if (part.type !== "tool_result") continue;
      const existing = calls.get(part.toolResult.toolCallId);
      if (existing) {
        existing.result = { content: part.toolResult.content, isError: part.toolResult.isError,
          artifacts: part.toolResult.artifacts ?? [] };
        existing.state = resultState(part.toolResult.content, part.toolResult.isError);
        existing.sourceMessageIds.push(message.id);
      } else {
        const activity: ToolActivity = {
          id: `${message.id}-${part.toolResult.toolCallId}`,
          toolCallId: part.toolResult.toolCallId,
          toolName: part.toolResult.toolName,
          result: { content: part.toolResult.content, isError: part.toolResult.isError,
            artifacts: part.toolResult.artifacts ?? [] },
          state: resultState(part.toolResult.content, part.toolResult.isError),
          riskClass: classifyDisplayRisk(part.toolResult.toolName),
          sourceMessageIds: [message.id],
          runId: message.runId,
        };
        calls.set(activity.toolCallId, activity);
        orphaned.push(activity);
      }
    }
    if (orphaned.length > 0) addToolBatch(active, orphaned, calls);
  }
}

/**
 * Merge streaming deltas into the base's tail without mutating `base`. Only the
 * tail (openTurn + new transient turns) is constructed; every other node keeps
 * its reference so memoized consumers skip re-render.
 */
export function applyTransient(
  base: TimelineBase,
  transient: TurnMessage[],
  turnMetrics?: Map<string, TurnMetric>,
): ConversationNode[] {
  if (transient.length === 0) {
    const tail = finalize(base.openTurn, turnMetrics);
    return tail ? [...base.nodes, tail] : base.nodes;
  }

  const nodes = [...base.nodes];
  const calls = new Map(base.calls);
  let syntheticTurn = base.syntheticTurn;
  let turn: ConversationTurn | null = base.openTurn;
  let turnIsBase = turn !== null;

  const ensureMutable = (): ConversationTurn => {
    if (turn === null) {
      syntheticTurn += 1;
      turn = { kind: "turn", id: `turn-orphan-${syntheticTurn}`, steps: [], messageIds: [] };
      turnIsBase = false;
      return turn;
    }
    if (turnIsBase) {
      // clone-on-write: the first mutation of base.openTurn copies it (deep
      // enough to detach tool activities) and rebinds the registry.
      turn = cloneTurn(turn);
      calls.clear();
      for (const step of turn.steps) {
        if (step.kind === "tool_batch") for (const activity of step.activities) calls.set(activity.toolCallId, activity);
      }
      turnIsBase = false;
    }
    return turn;
  };
  const flushTurn = () => {
    if (!turn) return;
    const outgoing = turnIsBase ? { ...turn } : turn;
    if (outgoing.runId) outgoing.metrics = turnMetrics?.get(outgoing.runId);
    if (outgoing.user || outgoing.steps.length > 0) nodes.push(outgoing);
    turn = null;
    turnIsBase = false;
    calls.clear();
  };

  for (const message of transient) projectTransientIntoTurn(message);
  flushTurn();
  return nodes;

  function projectTransientIntoTurn(message: TurnMessage) {
    if (message.role === "user" && message.kind !== "steer") {
      flushTurn();
      turn = {
        kind: "turn",
        id: `turn-${message.id}`,
        user: { id: message.id, text: message.text, sourceMessageId: message.sourceMessageId, createdAt: message.createdAt },
        steps: [],
        messageIds: [message.sourceMessageId ?? message.id],
      };
      turnIsBase = false;
      return;
    }
    const active = ensureMutable();
    active.messageIds.push(message.sourceMessageId ?? message.id);
    if (message.runId) active.runId = active.runId ?? message.runId;
    if (message.role === "user" && message.kind === "steer") {
      active.steps.push({ kind: "steer", id: message.id, text: message.text.replace(/^↪\s*/, ""), createdAt: message.createdAt });
      return;
    }
    if (message.role === "assistant") {
      const blocks: AssistantBlock[] = [];
      if (message.thinking) blocks.push({ kind: "thinking", text: message.thinking });
      if (message.text) blocks.push({ kind: "text", text: message.text });
      if (blocks.length > 0) active.steps.push({ kind: "assistant", id: message.id, sourceMessageId: message.sourceMessageId,
        blocks, createdAt: message.createdAt });
      return;
    }
    if (message.role === "tool") {
      const callID = message.toolCallId ?? message.id;
      const existing = calls.get(callID);
      if (existing) {
        if (message.arguments) existing.arguments = message.arguments;
        if (message.runId) existing.runId = message.runId;
        existing.result = message.toolState === "running" ? existing.result : {
          content: message.text, isError: Boolean(message.isError), artifacts: [],
        };
        existing.state = message.toolState ?? resultState(message.text, Boolean(message.isError));
        existing.sourceMessageIds.push(message.sourceMessageId ?? message.id);
        return;
      }
      addToolBatch(active, [{
        id: message.id,
        toolCallId: callID,
        toolName: message.toolName ?? "tool",
        arguments: message.arguments,
        argumentsFragment: message.argumentsFragment,
        result: message.toolState === "running" ? undefined : {
          content: message.text, isError: Boolean(message.isError), artifacts: [],
        },
        state: message.toolState ?? resultState(message.text, Boolean(message.isError)),
        riskClass: classifyDisplayRisk(message.toolName ?? "tool"),
        sourceMessageIds: [message.sourceMessageId ?? message.id],
        runId: message.runId,
      }], calls);
    }
  }
}

/**
 * Backward-compatible projection: the composition of the split. Existing
 * callers and unit tests keep working unchanged.
 */
export function mergeTimeline(
  messages: CanonicalMessage[],
  checkpoints: ContextCheckpoint[],
  transient: TurnMessage[],
  turnMetrics?: Map<string, TurnMetric>,
): ConversationNode[] {
  return applyTransient(projectBase(messages, checkpoints, turnMetrics), transient, turnMetrics);
}

function addToolBatch(active: ConversationTurn, activities: ToolActivity[], calls: Map<string, ToolActivity>) {
  const previous = active.steps[active.steps.length - 1];
  if (previous?.kind === "tool_batch") previous.activities.push(...activities);
  else active.steps.push({ kind: "tool_batch", id: `batch-${activities[0].id}`, activities });
  for (const activity of activities) calls.set(activity.toolCallId, activity);
}

function finalize(turn: ConversationTurn | null, turnMetrics?: Map<string, TurnMetric>): ConversationTurn | null {
  if (!turn) return null;
  const outgoing = { ...turn };
  if (outgoing.runId) outgoing.metrics = turnMetrics?.get(outgoing.runId);
  if (!outgoing.user && outgoing.steps.length === 0) return null;
  return outgoing;
}

/** Deep enough to detach mutable tool activities; assistant/steer steps are append-only. */
function cloneTurn(turn: ConversationTurn): ConversationTurn {
  return {
    ...turn,
    messageIds: [...turn.messageIds],
    steps: turn.steps.map((step) => step.kind === "tool_batch"
      ? { ...step, activities: step.activities.map((activity) => ({
          ...activity,
          sourceMessageIds: [...activity.sourceMessageIds],
          result: activity.result ? { ...activity.result } : undefined,
        })) }
      : step),
  };
}

function projectionItems(messages: CanonicalMessage[], checkpoints: ContextCheckpoint[]): ProjectionItem[] {
  const canonical = messages.filter(message => message.visibility !== "private" && message.visibility !== "room_control");
  const visible = new Set(canonical.map(message => message.id));
  const unique = [...new Map(checkpoints.filter(value => value.status === "completed").map(value => [value.id, value])).values()]
    .sort((left, right) => left.createdAt.localeCompare(right.createdAt));
  const before = new Map<string, ContextCheckpoint[]>();
  const after = new Map<string, ContextCheckpoint[]>();
  for (const checkpoint of unique) {
    if (checkpoint.firstKeptMessageId && visible.has(checkpoint.firstKeptMessageId)) append(before, checkpoint.firstKeptMessageId, checkpoint);
    else if (checkpoint.sourceThroughMessageId && visible.has(checkpoint.sourceThroughMessageId)) append(after, checkpoint.sourceThroughMessageId, checkpoint);
  }
  return canonical.flatMap(message => [
    ...(before.get(message.id) ?? []).map(value => ({ kind: "checkpoint" as const, value })),
    { kind: "message" as const, value: message },
    ...(after.get(message.id) ?? []).map(value => ({ kind: "checkpoint" as const, value })),
  ]);
}

function append(target: Map<string, ContextCheckpoint[]>, id: string, value: ContextCheckpoint) {
  target.set(id, [...(target.get(id) ?? []), value]);
}

function checkpointNode(checkpoint: ContextCheckpoint): CheckpointNode {
  return { kind: "checkpoint", id: `compaction-${checkpoint.id}`, reason: checkpoint.reason,
    summary: checkpoint.summary, reclaimedTokens: checkpoint.reclaimedTokens, createdAt: checkpoint.createdAt };
}

function userText(parts: CanonicalPart[]): string {
  return parts.map(part => {
    if (part.type === "text") return part.text;
    if (part.type === "image") return `[Image · ${part.image.width}×${part.image.height}]`;
    if (part.type === "image_description") return part.imageDescription.text;
    return "";
  }).filter(Boolean).join("\n\n");
}

function assistantBlock(part: CanonicalPart): AssistantBlock | null {
  if (part.type === "text") return { kind: "text", text: part.text };
  if (part.type === "thinking") return { kind: "thinking", text: part.text };
  if (part.type === "image") return { kind: "image", artifactId: part.image.artifactId, mimeType: part.image.mimeType,
    width: part.image.width, height: part.image.height };
  if (part.type === "image_description") return { kind: "image_description", artifactId: part.imageDescription.artifactId,
    text: part.imageDescription.text };
  return null;
}

function resultState(content: string, isError: boolean): ToolActivityState {
  if (/approval[_ ]rejected/i.test(content)) return "rejected";
  return isError ? "failed" : "completed";
}
