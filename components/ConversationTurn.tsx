"use client";

import { memo, useId, useState } from "react";
import { ChevronRight, CornerDownRight, Wrench } from "lucide-react";
import { ApprovalPanel } from "@/components/ApprovalPanel";
import { AssistantMessage } from "@/components/AssistantMessage";
import { CollapsibleMessage } from "@/components/CollapsibleMessage";
import { MessageActions } from "@/components/MessageActions";
import { MessageView } from "@/components/MessageView";
import { QuestionCard } from "@/components/QuestionCard";
import { RunStatus, type RunStatusProps } from "@/components/RunStatus";
import { ToolCallView } from "@/components/ToolCallView";
import { useT } from "@/components/LocaleProvider";
import type { ApprovalDecision, QuestionAnswer, ToolApprovalRequest } from "@/lib/approval";
import type { ConversationTurn as ConversationTurnModel, ToolBatchStep } from "@/lib/chat-messages";
import { shouldExpandBatch, toolBatchKey } from "@/lib/tool-batch-state";

interface ConversationTurnProps {
  sessionId: string;
  turn: ConversationTurnModel;
  active?: boolean;
  pendingApproval?: ToolApprovalRequest | null;
  resolvingApproval?: ApprovalDecision | null;
  decideApproval?: (decision: ApprovalDecision) => void;
  answeringQuestion?: boolean;
  answerQuestion?: (answers: QuestionAnswer[]) => void;
  runStatus?: RunStatusProps;
  activeLeafMessageId?: string;
  branchDisabled?: boolean;
  createBranch?: (messageId: string) => void;
}

export const ConversationTurn = memo(function ConversationTurn({ sessionId, turn, active = false, pendingApproval, resolvingApproval, decideApproval,
  answeringQuestion, answerQuestion, runStatus, activeLeafMessageId, branchDisabled, createBranch }: ConversationTurnProps) {
  const approvalBatchID = pendingApproval ? turn.steps.find(step => step.kind === "tool_batch" &&
    pendingApproval.items.some(item => step.activities.some(activity => activity.toolCallId === item.toolCallId)))?.id : undefined;
  const lastAssistantIndex = turn.steps.map(step => step.kind).lastIndexOf("assistant");
  const firstAssistantIndex = turn.steps.map(step => step.kind).indexOf("assistant");
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
          isFirst={index === firstAssistantIndex} isFinal={index === lastAssistantIndex}
          metrics={index === lastAssistantIndex ? turn.metrics : undefined}
          activeLeafMessageId={activeLeafMessageId} branchDisabled={branchDisabled} createBranch={createBranch} />;
        if (step.kind === "steer") return <div className="steer-message" key={step.id}>
          <CornerDownRight size={14} aria-hidden="true" /><span>{step.text}</span>
        </div>;
        const matchesApproval = step.id === approvalBatchID;
        return <ToolBatch sessionId={sessionId} batch={step} key={toolBatchKey(step)}
          isLast={index === turn.steps.length - 1}
          approval={matchesApproval ? pendingApproval : null}
          resolving={resolvingApproval} decide={decideApproval} />;
      })}
      {pendingApproval && !approvalBatchID && <PendingApprovalBatch approval={pendingApproval}
        resolving={resolvingApproval} decide={decideApproval}
        answering={answeringQuestion} answer={answerQuestion} />}
      {runStatus && <RunStatus {...runStatus} />}
    </div>
  </article>;
});

const ToolBatch = memo(function ToolBatch({ sessionId, batch, isLast, approval, resolving, decide }: {
  sessionId: string;
  batch: ToolBatchStep;
  isLast: boolean;
  approval?: ToolApprovalRequest | null;
  resolving?: ApprovalDecision | null;
  decide?: (decision: ApprovalDecision, standingGrantCallIndexes?: number[]) => void;
}) {
  const t = useT();
  const bodyId = useId();
  const [override, setOverride] = useState<boolean | null>(null);
  const count = batch.activities.length;
  const hasActive = batch.activities.some((activity) => activity.state === "running" || activity.state === "pending");
  const failed = batch.activities.filter((activity) => activity.state === "failed" || activity.state === "rejected").length;
  const hasOutcome = failed > 0 || batch.activities.some((activity) =>
    (activity.result?.artifacts.length ?? 0) > 0 ||
    activity.toolName === "delegate_tasks" || activity.toolName === "delegate_roles");
  const expanded = override ?? shouldExpandBatch({ isLast, hasActive, hasApproval: Boolean(approval), hasOutcome });
  const current = [...batch.activities].reverse().find((activity) => activity.state === "running" || activity.state === "pending")
    ?? batch.activities[batch.activities.length - 1];
  const now = current
    ? (current.state === "running" || current.state === "pending"
      ? t("conversation.toolBatch.running").replace("{tool}", current.toolName)
      : current.toolName)
    : "";

  return <section className="tool-batch" data-open={expanded || undefined}
    aria-label={(count === 1 ? t("conversation.toolBatch.ariaOne") : t("conversation.toolBatch.ariaMany")).replace("{count}", String(count))}>
    <button type="button" className="tool-batch-summary" aria-expanded={expanded} aria-controls={bodyId}
      onClick={() => setOverride(!expanded)}>
      <ChevronRight size={14} className="tool-batch-chevron" aria-hidden="true" />
      <span className="tool-batch-title">{count === 1 ? t("conversation.toolBatch.titleOne") : t("conversation.toolBatch.titleMany").replace("{count}", String(count))}</span>
      {now && <span className="tool-batch-now">{now}</span>}
      {failed > 0 && <span className="tool-batch-failed">{t("conversation.toolBatch.failed").replace("{count}", String(failed))}</span>}
    </button>
    <div id={bodyId} className="tool-batch-body" hidden={!expanded}>
      <div className="tool-activity-list">{batch.activities.map(activity =>
        <ToolCallView sessionId={sessionId} activity={activity} key={activity.toolCallId} />)}</div>
    </div>
    {approval && decide && <ApprovalPanel approval={approval} resolving={resolving ?? null} decide={decide} />}
  </section>;
});

export function PendingApprovalBatch({ approval, resolving, decide, answering, answer }: {
  approval: ToolApprovalRequest;
  resolving?: ApprovalDecision | null;
  decide?: (decision: ApprovalDecision, standingGrantCallIndexes?: number[]) => void;
  answering?: boolean;
  answer?: (answers: QuestionAnswer[]) => void;
}) {
  const t = useT();
  if (approval.kind === "question") {
    return <QuestionCard approval={approval} submitting={Boolean(answering)}
      answer={answer ?? (() => undefined)} />;
  }
  if (!decide) return null;
  const count = approval.items.length;
  return <section className="tool-batch pending-tool-batch" aria-label={(count === 1 ? t("conversation.toolBatch.ariaOne") : t("conversation.toolBatch.ariaMany")).replace("{count}", String(count))}>
    <div className="tool-batch-heading"><Wrench size={14} aria-hidden="true" />
      <span>{count === 1 ? t("conversation.toolBatch.pendingOne") : t("conversation.toolBatch.pendingMany").replace("{count}", String(count))}</span>
    </div>
    <ApprovalPanel approval={approval} resolving={resolving ?? null} decide={decide} />
  </section>;
}
