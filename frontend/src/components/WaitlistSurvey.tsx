import { useState, useEffect, useCallback } from "react";
import { Check, ArrowLeft } from "lucide-react";
import { updateWaitlistSurvey } from "../lib/api";

const QUESTIONS = [
  {
    key: "use_case",
    label: "What are you planning to run?",
    multi: true,
    options: [
      "Claude / Anthropic agents",
      "OpenAI / GPT agents",
      "Browser automation (Playwright, Puppeteer)",
      "Custom AI workflows",
      "Development environment",
    ],
  },
  {
    key: "current_setup",
    label: "What are you using today?",
    multi: false,
    options: [
      "VPS (DigitalOcean, Linode, Hetzner)",
      "Cloud IDE (Gitpod, Codespaces)",
      "Local machine",
      "Nothing yet",
    ],
  },
  {
    key: "pain_point",
    label: "What's your biggest frustration?",
    multi: false,
    options: [
      "Setup & configuration",
      "Security & isolation",
      "Maintenance & updates",
      "Cost unpredictability",
      "Scaling multiple agents",
    ],
  },
  {
    key: "integrations",
    label: "Which integrations do you want on day one?",
    multi: true,
    options: [
      "Slack",
      "Discord",
      "Telegram",
      "GitHub",
      "Linear / Jira",
      "Email",
    ],
  },
] as const;

type Answers = Record<string, string | string[]>;

export function WaitlistSurvey({
  email,
  open,
  onClose,
}: {
  email: string;
  open: boolean;
  onClose: () => void;
}) {
  const [step, setStep] = useState(0);
  const [answers, setAnswers] = useState<Answers>({});
  const [done, setDone] = useState(false);

  // Lock body scroll when modal is open
  useEffect(() => {
    if (open) {
      document.body.style.overflow = "hidden";
      return () => {
        document.body.style.overflow = "";
      };
    }
  }, [open]);

  // Escape key closes modal
  useEffect(() => {
    if (!open) return;
    const handler = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [open, onClose]);

  const question = QUESTIONS[step];
  const progress = ((step + 1) / QUESTIONS.length) * 100;

  const submit = useCallback(async (finalAnswers: Answers) => {
    try {
      await updateWaitlistSurvey(email, finalAnswers);
    } catch {
      // best-effort
    }
    setDone(true);
  }, [email]);

  const advance = useCallback(
    (updatedAnswers: Answers) => {
      if (step < QUESTIONS.length - 1) {
        setStep((s) => s + 1);
      } else {
        submit(updatedAnswers);
      }
    },
    [step, submit],
  );

  const selectSingle = (key: string, value: string) => {
    const updated = { ...answers, [key]: value };
    setAnswers(updated);
    // Auto-advance after brief visual feedback
    setTimeout(() => advance(updated), 200);
  };

  const toggleMulti = (key: string, value: string) => {
    setAnswers((prev) => {
      const arr = (prev[key] as string[] | undefined) ?? [];
      return {
        ...prev,
        [key]: arr.includes(value)
          ? arr.filter((v) => v !== value)
          : [...arr, value],
      };
    });
  };

  const isSelected = (key: string, value: string) => {
    const a = answers[key];
    if (Array.isArray(a)) return a.includes(value);
    return a === value;
  };

  const goBack = () => {
    if (step > 0) setStep((s) => s - 1);
    else onClose();
  };

  const multiHasSelection =
    question?.multi &&
    Array.isArray(answers[question.key]) &&
    (answers[question.key] as string[]).length > 0;

  if (!open) return null;

  return (
    <div className="fixed inset-0 z-[100] flex items-start justify-center pt-[15vh] px-4">
      {/* Backdrop */}
      <div
        className="absolute inset-0 bg-gray-950/80 backdrop-blur-sm"
        onClick={onClose}
      />

      {/* Card */}
      <div className="relative w-full max-w-[520px]">
        {done ? (
          /* ── Thank You ── */
          <div className="bg-gray-900 border border-gray-800 rounded-2xl p-8 text-center">
            <div className="w-16 h-16 rounded-full bg-teal-400/15 flex items-center justify-center mx-auto mb-6">
              <Check className="w-8 h-8 text-teal-400" />
            </div>
            <h3 className="text-2xl font-bold text-white mb-2">
              Thanks for the feedback!
            </h3>
            <p className="text-gray-400 text-sm mb-8">
              We'll use this to build what you need.
            </p>
            <button
              onClick={onClose}
              className="rounded-lg bg-gray-800 px-6 py-2.5 text-sm font-medium text-gray-300 hover:bg-gray-700 transition-colors"
            >
              Close
            </button>
          </div>
        ) : (
          /* ── Question Card ── */
          <div className="bg-gray-900 border border-gray-800 rounded-2xl overflow-hidden">
            {/* Progress bar */}
            <div className="h-1 bg-gray-800">
              <div
                className="h-full bg-orange-500 rounded-r-full transition-all duration-300 ease-out"
                style={{ width: `${progress}%` }}
              />
            </div>

            <div className="p-8">
              {/* Back + step counter */}
              <div className="flex items-center justify-between mb-8">
                <button
                  onClick={goBack}
                  className="text-gray-500 hover:text-white transition-colors flex items-center gap-1.5 text-sm"
                >
                  <ArrowLeft className="w-4 h-4" />
                  Back
                </button>
                <span className="text-gray-600 text-xs">
                  {step + 1} / {QUESTIONS.length}
                </span>
              </div>

              {/* Question */}
              <h3 className="text-xl font-semibold text-white mb-2 leading-snug">
                {question.label}
              </h3>
              {question.multi && (
                <p className="text-gray-500 text-sm mb-6">
                  Select all that apply
                </p>
              )}
              {!question.multi && (
                <div className="mb-6" />
              )}

              {/* Options */}
              <div className="space-y-2.5">
                {question.options.map((opt) => {
                  const selected = isSelected(question.key, opt);
                  return (
                    <button
                      key={opt}
                      type="button"
                      onClick={() =>
                        question.multi
                          ? toggleMulti(question.key, opt)
                          : selectSingle(question.key, opt)
                      }
                      className={`w-full text-left rounded-xl px-5 py-4 text-[15px] border-2 transition-all duration-150 ${
                        selected
                          ? "bg-orange-500/10 border-orange-500 text-white"
                          : "bg-gray-800/50 border-gray-800 text-gray-300 hover:border-gray-600"
                      }`}
                    >
                      <div className="flex items-center gap-3">
                        {/* Radio / Checkbox indicator */}
                        <div
                          className={`w-5 h-5 flex-shrink-0 flex items-center justify-center rounded-${question.multi ? "md" : "full"} border-2 transition-colors ${
                            selected
                              ? "bg-orange-500 border-orange-500"
                              : "border-gray-600 bg-transparent"
                          }`}
                        >
                          {selected && (
                            <Check className="w-3 h-3 text-white" />
                          )}
                        </div>
                        {opt}
                      </div>
                    </button>
                  );
                })}
              </div>

              {/* Continue button for multi-select questions */}
              {question.multi && (
                <div className="flex items-center justify-between mt-8">
                  <button
                    onClick={onClose}
                    className="text-sm text-gray-600 hover:text-gray-400 transition-colors"
                  >
                    Skip survey
                  </button>
                  <button
                    onClick={() => advance(answers)}
                    disabled={!multiHasSelection}
                    className={`rounded-lg px-6 py-2.5 text-sm font-semibold transition-colors ${
                      multiHasSelection
                        ? "bg-orange-500 text-white hover:bg-orange-600"
                        : "bg-gray-800 text-gray-600 cursor-not-allowed"
                    }`}
                  >
                    {step === QUESTIONS.length - 1 ? "Submit" : "Continue"}
                  </button>
                </div>
              )}

              {/* Skip for single-select (auto-advances) */}
              {!question.multi && (
                <div className="mt-8">
                  <button
                    onClick={onClose}
                    className="text-sm text-gray-600 hover:text-gray-400 transition-colors"
                  >
                    Skip survey
                  </button>
                </div>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
