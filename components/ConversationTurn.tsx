"use client";

import { memo } from "react";
import { CornerDownRight, Wrench } from "lucide-react";
import { ApprovalPanel } from "@/components/ApprovalPanel";
import { AssistantMessage } from "@/components/AssistantMessage";
import { CollapsibleMessage } from "@/components/CollapsibleMessage";
import { MessageActions } from "@/components/MessageActions";
import { MessageView } from "@/components/MessageView";
import { RunStatus, type RunStatusProps } from "@/components/RunStatus";
import { ToolCallView } from "@/components/ToolCallView";
import { useT } from "@/components/LocaleProvider";
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

export const ConversationTurn = memo(function ConversationTurn({ sessionId, turn, active = false, pendingApproval, resolvingApproval, decideApproval, runStatus,
  activeLeafMessageId, branchDisabled, createBranch }: ConversationTurnProps) {
  const approvalBatchID = pendingApproval ? turn.steps.find(step => step.kind === "tool_batch" &&
    pendingApproval.items.some(item => step.activities.some(activity => activity.toolCallId === item.toolCallId)))?.id : undefined;
  const lastAssistantIndex = turn.steps.map(step => step.kind).lastIndexOf("assistant");
  return <article className={`conversation-turn ${active ? "is-active" : ""}`} data-turn-id={turn.id}>
    {turn.user && <div className="user-row" data-message-id={turn.user.sourceMessageId ?? turn.user.id}>
      <div className="user-message"><CollapsibleMessage text={turn.user.text}><MessageView text={turn.user.text} /></CollapsibleMessage></div>
      <MessageActions text={turn.user.text} time={turn.user.createdAt}
        branchMessageId={turn.user.sourceMessageId !== activeLeafMessageId ? turn.user.sourceMessageId : undefined}
        branchDisabled={branchDisabled} onBranch={createBranch} />
    </div>}
    <div className="agent-flow">
      {turn.steps.map((step, index) => {
        if (step.kind === "assistant") return <AssistantMessage step={step} key={step.id}
          metrics={index === lastAssistantIndex ? turn.metrics : undefined}
          activeLeafMessageId={activeLeafMessageId} branchDisabled={branchDisabled} createBranch={createBranch} />;
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
});

const ToolBatch = memo(function ToolBatch({ sessionId, batch, approval, resolving, decide }: {
  sessionId: string;
  batch: ToolBatchStep;
  approval?: ToolApprovalRequest | null;
  resolving?: ApprovalDecision | null;
  decide?: (decision: ApprovalDecision, standingGrantCallIndexes?: number[]) => void;
}) {
  const t = useT();
  const count = batch.activities.length;
  return <section className="tool-batch" aria-label={(count === 1 ? t("conversation.toolBatch.ariaOne") : t("conversation.toolBatch.ariaMany")).replace("{count}", String(count))}>
    <div className="tool-batch-heading"><Wrench size={14} aria-hidden="true" />
      <span>{count === 1 ? t("conversation.toolBatch.titleOne") : t("conversation.toolBatch.titleMany").replace("{count}", String(count))}</span>
    </div>
    <div className="tool-activity-list">{batch.activities.map(activity =>
      <ToolCallView sessionId={sessionId} activity={activity} key={activity.toolCallId} />)}</div>
    {approval && decide && <ApprovalPanel approval={approval} resolving={resolving ?? null} decide={decide} />}
  </section>;
});

export function PendingApprovalBatch({ approval, resolving, decide }: {
  approval: ToolApprovalRequest;
  resolving?: ApprovalDecision | null;
  decide?: (decision: ApprovalDecision, standingGrantCallIndexes?: number[]) => void;
}) {
  const t = useT();
  if (!decide) return null;
  const count = approval.items.length;
  return <section className="tool-batch pending-tool-batch" aria-label={(count === 1 ? t("conversation.toolBatch.ariaOne") : t("conversation.toolBatch.ariaMany")).replace("{count}", String(count))}>
    <div className="tool-batch-heading"><Wrench size={14} aria-hidden="true" />
      <span>{count === 1 ? t("conversation.toolBatch.pendingOne") : t("conversation.toolBatch.pendingMany").replace("{count}", String(count))}</span>
    </div>
    <ApprovalPanel approval={approval} resolving={resolving ?? null} decide={decide} />
  </section>;
}
