"use client";

import { CircleHelp } from "lucide-react";
import { useMemo, useState } from "react";
import { useT } from "@/components/LocaleProvider";
import type { QuestionAnswer, ToolApprovalRequest } from "@/lib/approval";
import styles from "./QuestionCard.module.css";

interface Props {
  approval: ToolApprovalRequest;
  submitting: boolean;
  answer: (answers: QuestionAnswer[]) => void;
}

function optionID(option: { id: string; label: string }): string {
  return option.id || option.label;
}

export function QuestionCard({ approval, submitting, answer }: Props) {
  const t = useT();
  const questions = useMemo(() => approval.questions ?? [], [approval.questions]);
  const [selected, setSelected] = useState<Record<string, string[]>>({});
  const [other, setOther] = useState<Record<string, boolean>>({});
  const [otherText, setOtherText] = useState<Record<string, string>>({});
  // The server may take a beat to clear the pending snapshot; keep a local
  // receipt so the submitted answers stay visible instead of the card
  // disappearing (or reverting to an empty form) as soon as the POST returns.
  const [submittedAnswers, setSubmittedAnswers] = useState<QuestionAnswer[] | null>(null);
  const readOnly = approval.status !== "pending" || submittedAnswers !== null;

  const answerFor = (questionID: string): QuestionAnswer | null => {
    if (other[questionID] === true) {
      const text = (otherText[questionID] ?? "").trim();
      if (!text) return null;
      const question = questions.find((entry) => entry.id === questionID);
      return { questionId: questionID, values: question?.multiSelect ? selected[questionID] ?? [] : [], otherText: text };
    }
    const values = selected[questionID] ?? [];
    return values.length > 0 ? { questionId: questionID, values } : null;
  };

  const complete = questions.length > 0 && questions.every((question) => answerFor(question.id) !== null);

  const toggle = (questionID: string, value: string, multi: boolean) => {
    setSelected((current) => {
      const existing = current[questionID] ?? [];
      if (!multi) return { ...current, [questionID]: [value] };
      return {
        ...current,
        [questionID]: existing.includes(value)
          ? existing.filter((entry) => entry !== value)
          : [...existing, value],
      };
    });
    if (!multi) setOther((current) => ({ ...current, [questionID]: false }));
  };

  const answeredText = (questionID: string): string => {
    const source = approval.answers ?? submittedAnswers ?? [];
    const stored = source.find((entry) => entry.questionId === questionID);
    if (!stored) return "";
    const labels = (stored.values ?? []).map((value) => {
      const option = questions.find((question) => question.id === questionID)?.options.find(
        (candidate) => optionID(candidate) === value,
      );
      return option?.label ?? value;
    });
    if (stored.otherText) labels.push(stored.otherText);
    return labels.join(", ");
  };

  return <section className={styles.card} aria-label={t("conversation.question.title")}>
    <header className={styles.header}>
      <span className={styles.icon}><CircleHelp size={16} aria-hidden="true" /></span>
      <div>
        <strong>{t("conversation.question.title")}</strong>
        <span>{readOnly ? t("conversation.question.submitted") : t("conversation.question.waiting")}</span>
      </div>
    </header>
    <div className={styles.questions}>
      {questions.map((question) => {
        const multi = question.multiSelect === true;
        const values = selected[question.id] ?? [];
        const otherSelected = other[question.id] === true;
        return <fieldset className={styles.question} key={question.id}>
          <legend>{question.prompt}</legend>
          {multi && <small className={styles.hint}>{t("conversation.question.multiHint")}</small>}
          <div className={styles.options}>
            {question.options.map((option) => {
              const id = optionID(option);
              const checked = values.includes(id);
              return <label className={styles.option} key={id} data-selected={checked || undefined}>
                <input
                  type={multi ? "checkbox" : "radio"}
                  name={`question-${approval.id}-${question.id}`}
                  checked={checked}
                  disabled={readOnly || submitting}
                  onChange={() => toggle(question.id, id, multi)}
                />
                <span>
                  <strong>{option.label}</strong>
                  {option.description && <small>{option.description}</small>}
                </span>
              </label>;
            })}
            {question.allowOther && !readOnly && (
              <label className={styles.option} data-selected={otherSelected || undefined}>
                <input
                  type={multi ? "checkbox" : "radio"}
                  name={`question-${approval.id}-${question.id}`}
                  checked={otherSelected}
                  disabled={submitting}
                  onChange={() => {
                    setOther((current) => ({ ...current, [question.id]: !otherSelected }));
                    if (!otherSelected && !multi) {
                      setSelected((current) => ({ ...current, [question.id]: [] }));
                    }
                  }}
                />
                <span>
                  <strong>{t("conversation.question.other")}</strong>
                  {otherSelected && <input
                    className={styles.otherInput}
                    value={otherText[question.id] ?? ""}
                    placeholder={t("conversation.question.otherPlaceholder")}
                    disabled={submitting}
                    onChange={(event) => setOtherText((current) => ({ ...current, [question.id]: event.target.value }))}
                  />}
                </span>
              </label>
            )}
          </div>
          {readOnly && <p className={styles.answered}>{t("conversation.question.answered")}: {answeredText(question.id) || "—"}</p>}
        </fieldset>;
      })}
    </div>
    {!readOnly && <div className={styles.actions}>
      <button type="button" className={styles.submit} disabled={!complete || submitting}
        onClick={() => {
          const answers = questions.map((question) => answerFor(question.id))
            .filter((entry): entry is QuestionAnswer => entry !== null);
          setSubmittedAnswers(answers);
          answer(answers);
        }}>
        {submitting ? t("conversation.question.submitting") : t("conversation.question.submit")}
      </button>
    </div>}
  </section>;
}
