"use client";

import { CornerDownRight, GitFork, Wrench } from "lucide-react";
import { ApprovalPanel } from "@/components/ApprovalPanel";
import { AssistantMessage } from "@/components/AssistantMessage";
import { MessageView } from "@/components/MessageView";
import { RunStatus, type RunStatusProps } from "@/components/RunStatus";
import { ToolCallView } from "@/components/ToolCallView";
import type { ApprovalDecision, ToolApprovalRequest } from "@/lib/approval";
import type { ConversationTurn as ConversationTurnModel, ToolBatchStep } from "@/lib/chat-messages";

interface ConversationTurnProps {
  sessionId: string;
  turn: ConversationTurnModel;
  active?: boolean;
  pendingApproval?: ToolApprovalRequest | null;
  resolvingApproval?: ApprovalDecision | null;
  decideApproval?: (decision: ApprovalDecision) => void;
  runStatus?: RunStatusProps;
  activeLeafMessageId?: string;
  branchDisabled?: boolean;
  createBranch?: (messageId: string) => void;
}

export function ConversationTurn({ sessionId, turn, active = false, pendingApproval, resolvingApproval, decideApproval, runStatus,
  activeLeafMessageId, branchDisabled, createBranch }: ConversationTurnProps) {
  const approvalBatchID = pendingApproval ? turn.steps.find(step => step.kind === "tool_batch" &&
    pendingApproval.items.some(item => step.activities.some(activity => activity.toolCallId === item.toolCallId)))?.id : undefined;
  return <article className={`conversation-turn ${active ? "is-active" : ""}`} data-turn-id={turn.id}>
    {turn.user && <div className="user-row" data-message-id={turn.user.sourceMessageId ?? turn.user.id}>
      <div className="user-message"><MessageView text={turn.user.text} /></div>
      {turn.user.sourceMessageId && turn.user.sourceMessageId !== activeLeafMessageId && createBranch &&
        <button type="button" className="branch-from-message" aria-label="Branch from this message"
          title="Branch from this message" disabled={branchDisabled}
          onClick={() => createBranch(turn.user!.sourceMessageId!)}>
          <GitFork size={14} aria-hidden="true" />
        </button>}
    </div>}
    <div className="agent-flow">
      {turn.steps.map(step => {
        if (step.kind === "assistant") return <AssistantMessage step={step} key={step.id} />;
        if (step.kind === "steer") return <div className="steer-message" key={step.id}>
          <CornerDownRight size={14} aria-hidden="true" /><span>{step.text}</span>
        </div>;
        const matchesApproval = step.id === approvalBatchID;
        return <ToolBatch sessionId={sessionId} batch={step} key={step.id}
          approval={matchesApproval ? pendingApproval : null}
          resolving={resolvingApproval} decide={decideApproval} />;
      })}
      {pendingApproval && !approvalBatchID && <PendingApprovalBatch approval={pendingApproval}
        resolving={resolvingApproval} decide={decideApproval} />}
      {runStatus && <RunStatus {...runStatus} />}
    </div>
  </article>;
}

function ToolBatch({ sessionId, batch, approval, resolving, decide }: {
  sessionId: string;
  batch: ToolBatchStep;
  approval?: ToolApprovalRequest | null;
  resolving?: ApprovalDecision | null;
  decide?: (decision: ApprovalDecision) => void;
}) {
  return <section className="tool-batch" aria-label={`Tool batch with ${batch.activities.length} actions`}>
    <div className="tool-batch-heading"><Wrench size={14} aria-hidden="true" />
      <span>{batch.activities.length === 1 ? "Tool activity" : `${batch.activities.length} tool activities`}</span>
    </div>
    <div className="tool-activity-list">{batch.activities.map(activity =>
      <ToolCallView sessionId={sessionId} activity={activity} key={activity.toolCallId} />)}</div>
    {approval && decide && <ApprovalPanel approval={approval} resolving={resolving ?? null} decide={decide} />}
  </section>;
}

export function PendingApprovalBatch({ approval, resolving, decide }: {
  approval: ToolApprovalRequest;
  resolving?: ApprovalDecision | null;
  decide?: (decision: ApprovalDecision) => void;
}) {
  if (!decide) return null;
  return <section className="tool-batch pending-tool-batch" aria-label={`Pending tool batch with ${approval.items.length} actions`}>
    <div className="tool-batch-heading"><Wrench size={14} aria-hidden="true" />
      <span>{approval.items.length === 1 ? "Pending tool activity" : `${approval.items.length} pending tool activities`}</span>
    </div>
    <ApprovalPanel approval={approval} resolving={resolving ?? null} decide={decide} />
  </section>;
}
