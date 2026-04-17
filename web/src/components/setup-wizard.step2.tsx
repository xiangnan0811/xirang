import { useTranslation } from "react-i18next";
import { CheckCircle2, ExternalLink } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

interface WizardStep {
  id: string;
  title: string;
  linkTo: string | null;
  linkLabel: string | null;
}

interface SetupWizardStep2Props {
  /** All steps (including welcome at index 0 and complete at last index) */
  steps: WizardStep[];
  /** Current step index (1-based for middle steps) */
  currentStepIndex: number;
  /** Total steps count */
  totalSteps: number;
  /** Called when user clicks a step dot */
  onJumpToStep: (index: number) => void;
  /** Called when user clicks the navigation link */
  onNavigate: (path: string) => void;
}

export function SetupWizardStep2({
  steps,
  currentStepIndex,
  totalSteps,
  onJumpToStep,
  onNavigate,
}: SetupWizardStep2Props) {
  const { t } = useTranslation();

  // Progress percentage: welcome = 0%, complete = 100%
  const progress = Math.round((currentStepIndex / (totalSteps - 1)) * 100);
  const currentStep = steps[currentStepIndex];

  return (
    <div className="space-y-6">
      {/* Progress bar */}
      <div className="space-y-2">
        <div className="flex items-center justify-between text-sm font-medium">
          <span className="text-muted-foreground">{t("setupWizard.configProgress")}</span>
          <span className="text-primary">{progress}%</span>
        </div>
        <div className="h-2 rounded-full bg-muted/60 overflow-hidden backdrop-blur-sm">
          <div
            className="h-full bg-primary transition-[width] duration-500 ease-in-out"
            style={{ width: `${progress}%` }}
          />
        </div>
      </div>

      {/* Step indicator dots (exclude welcome at 0 and complete at last) */}
      <div className="flex items-center justify-center gap-2">
        {steps.slice(1, -1).map((s, index) => {
          const stepIndex = index + 1;
          const isDone = stepIndex < currentStepIndex;
          const isCurrent = stepIndex === currentStepIndex;

          return (
            <button
              key={s.id}
              type="button"
              onClick={() => onJumpToStep(stepIndex)}
              className={cn(
                "flex items-center justify-center rounded-full transition-[color,background-color,transform] duration-300",
                isCurrent
                  ? "size-8 border-2 border-primary bg-primary/10"
                  : isDone
                    ? "size-8 border-2 border-success/50 bg-success/10"
                    : "size-8 border-2 border-border/50 bg-muted/20 opacity-60 hover:opacity-90",
              )}
              aria-label={t("setupWizard.jumpToStep", { title: s.title })}
              aria-current={isCurrent ? "step" : undefined}
            >
              {isDone ? (
                <CheckCircle2 className="size-4 text-success" />
              ) : (
                <span
                  className={cn(
                    "text-xs font-semibold",
                    isCurrent ? "text-primary" : "text-muted-foreground",
                  )}
                >
                  {index + 1}
                </span>
              )}
            </button>
          );
        })}
      </div>

      {/* Navigation link button */}
      {currentStep?.linkTo && currentStep.linkLabel && (
        <div className="flex justify-center pt-2">
          <Button
            variant="outline"
            className="gap-2"
            onClick={() => onNavigate(currentStep.linkTo!)}
          >
            <ExternalLink className="size-4" />
            {currentStep.linkLabel}
          </Button>
        </div>
      )}
    </div>
  );
}
