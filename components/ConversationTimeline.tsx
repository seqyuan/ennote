"use client";

import { ArchiveRestore } from "lucide-react";
import { ConversationTurn, PendingApprovalBatch } from "@/components/ConversationTurn";
import { RunStatus, type RunStatusProps } from "@/components/RunStatus";
import type { ApprovalDecision, ToolApprovalRequest } from "@/lib/approval";
import type { ConversationNode } from "@/lib/chat-messages";

interface ConversationTimelineProps {
  sessionId: string;
  nodes: ConversationNode[];
  pendingApproval: ToolApprovalRequest | null;
  resolvingApproval: ApprovalDecision | null;
  decideApproval: (decision: ApprovalDecision) => void;
  runStatus?: RunStatusProps;
  activeLeafMessageId?: string;
  branchDisabled?: boolean;
  createBranch?: (messageId: string) => void;
}

export function ConversationTimeline({ sessionId, nodes, pendingApproval, resolvingApproval, decideApproval, runStatus,
  activeLeafMessageId, branchDisabled, createBranch }: ConversationTimelineProps) {
  let lastTurnIndex = -1;
  for (let index = nodes.length - 1; index >= 0; index -= 1) {
    if (nodes[index].kind === "turn") { lastTurnIndex = index; break; }
  }
  return <div className="conversation-list">
    {nodes.map((node, index) => node.kind === "checkpoint"
      ? <CheckpointSeparator node={node} key={node.id} />
      : <ConversationTurn sessionId={sessionId} turn={node} key={node.id} active={index === lastTurnIndex && Boolean(runStatus?.active)}
          pendingApproval={index === lastTurnIndex ? pendingApproval : null}
          resolvingApproval={resolvingApproval} decideApproval={decideApproval}
          branchDisabled={branchDisabled} activeLeafMessageId={activeLeafMessageId} createBranch={createBranch}
          runStatus={index === lastTurnIndex ? runStatus : undefined} />)}
    {lastTurnIndex < 0 && pendingApproval && <div className="orphan-active-flow">
      <PendingApprovalBatch approval={pendingApproval} resolving={resolvingApproval} decide={decideApproval} />
      {runStatus && <RunStatus {...runStatus} />}
    </div>}
  </div>;
}

function CheckpointSeparator({ node }: { node: Extract<ConversationNode, { kind: "checkpoint" }> }) {
  return <div className="checkpoint-separator" data-checkpoint-id={node.id}>
    <span className="checkpoint-line" />
    <details>
      <summary><ArchiveRestore size={14} aria-hidden="true" /> Context checkpoint
        {node.reclaimedTokens > 0 && <span>{node.reclaimedTokens.toLocaleString()} tokens reclaimed</span>}
      </summary>
      <div>{node.summary}</div>
    </details>
    <span className="checkpoint-line" />
  </div>;
}
