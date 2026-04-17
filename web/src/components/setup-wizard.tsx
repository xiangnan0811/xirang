import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { useAuth } from "@/context/auth-context";
import { request } from "@/lib/api/core";
import {
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  Key,
  PartyPopper,
  Rocket,
  Server,
  ShieldCheck,
  ClipboardList,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Stepper } from "@/components/ui/stepper";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { cn } from "@/lib/utils";
import { SetupWizardStep1 } from "./setup-wizard.step1";
import { SetupWizardStep2 } from "./setup-wizard.step2";
import { SetupWizardStep3 } from "./setup-wizard.step3";

/* -------------------------------------------------------------------------- */
/*  Types                                                                      */
/* -------------------------------------------------------------------------- */

interface WizardStep {
  id: string;
  title: string;
  description: string;
  icon: React.ReactNode;
  /** Navigation path, null means no link (welcome / complete pages) */
  linkTo: string | null;
  linkLabel: string | null;
}

/* -------------------------------------------------------------------------- */
/*  Component                                                                  */
/* -------------------------------------------------------------------------- */

const ICON_CLASS = "size-6";

export function SetupWizard() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const navigate = useNavigate();

  const STEPS: WizardStep[] = [
    {
      id: "welcome",
      title: t("setupWizard.steps.welcome.title"),
      description: t("setupWizard.steps.welcome.description"),
      icon: <Rocket className={ICON_CLASS} />,
      linkTo: null,
      linkLabel: null,
    },
    {
      id: "ssh-key",
      title: t("setupWizard.steps.sshKey.title"),
      description: t("setupWizard.steps.sshKey.description"),
      icon: <Key className={ICON_CLASS} />,
      linkTo: "/app/ssh-keys",
      linkLabel: t("setupWizard.steps.sshKey.linkLabel"),
    },
    {
      id: "node",
      title: t("setupWizard.steps.addNode.title"),
      description: t("setupWizard.steps.addNode.description"),
      icon: <Server className={ICON_CLASS} />,
      linkTo: "/app/nodes",
      linkLabel: t("setupWizard.steps.addNode.linkLabel"),
    },
    {
      id: "policy",
      title: t("setupWizard.steps.createPolicy.title"),
      description: t("setupWizard.steps.createPolicy.description"),
      icon: <ShieldCheck className={ICON_CLASS} />,
      linkTo: "/app/policies",
      linkLabel: t("setupWizard.steps.createPolicy.linkLabel"),
    },
    {
      id: "task",
      title: t("setupWizard.steps.testBackup.title"),
      description: t("setupWizard.steps.testBackup.description"),
      icon: <ClipboardList className={ICON_CLASS} />,
      linkTo: "/app/tasks",
      linkLabel: t("setupWizard.steps.testBackup.linkLabel"),
    },
    {
      id: "complete",
      title: t("setupWizard.steps.complete.title"),
      description: t("setupWizard.steps.complete.description"),
      icon: <PartyPopper className={ICON_CLASS} />,
      linkTo: null,
      linkLabel: null,
    },
  ];

  const TOTAL_STEPS = STEPS.length;

  const [wizardState, setWizardState] = usePersistentState<{
    completed: boolean;
    dismissed: boolean;
    currentStep: number;
  }>("xirang.setup-wizard", {
    completed: false,
    dismissed: false,
    currentStep: 0,
  });

  const [showDialog, setShowDialog] = useState(false);
  const [completing, setCompleting] = useState(false);

  // Delay popup on first login
  useEffect(() => {
    if (!wizardState.completed && !wizardState.dismissed) {
      const timer = setTimeout(() => setShowDialog(true), 600);
      return () => clearTimeout(timer);
    }
  }, [wizardState.completed, wizardState.dismissed]);

  /* ---- Derived state ---- */
  const step = wizardState.currentStep;
  const currentStep = STEPS[step];
  const isWelcome = step === 0;
  const isComplete = step === TOTAL_STEPS - 1;

  /* ---- Stepper labels (welcome + middle steps + complete) ---- */
  const stepperLabels = [
    t("setupWizard.steps.welcome.title"),
    t("setupWizard.steps.sshKey.title"),
    t("setupWizard.steps.addNode.title"),
    t("setupWizard.steps.createPolicy.title"),
    t("setupWizard.steps.testBackup.title"),
    t("setupWizard.steps.complete.title"),
  ];

  /* ---- Backend mark ---- */
  const markOnboarded = useCallback(() => {
    void request("/me/onboarded", {
      method: "POST",
      token: token ?? undefined,
    }).catch(() => {
      /* best-effort */
    });
  }, [token]);

  /* ---- Actions ---- */
  const handleNext = useCallback(() => {
    if (step < TOTAL_STEPS - 1) {
      setWizardState((prev) => ({ ...prev, currentStep: prev.currentStep + 1 }));
    }
  }, [step, TOTAL_STEPS, setWizardState]);

  const handlePrevious = useCallback(() => {
    if (step > 0) {
      setWizardState((prev) => ({ ...prev, currentStep: prev.currentStep - 1 }));
    }
  }, [step, setWizardState]);

  const handleFinish = useCallback(() => {
    setCompleting(true);
    markOnboarded();
    setTimeout(() => {
      setWizardState((prev) => ({ ...prev, completed: true, dismissed: true }));
      setShowDialog(false);
      setCompleting(false);
    }, 300);
  }, [markOnboarded, setWizardState]);

  const handleSkip = useCallback(() => {
    setWizardState((prev) => ({ ...prev, completed: true, dismissed: true }));
    setShowDialog(false);
    markOnboarded();
  }, [markOnboarded, setWizardState]);

  const handleDialogClose = useCallback(
    (open: boolean) => {
      if (!open) {
        setWizardState((prev) => ({ ...prev, dismissed: true }));
        markOnboarded();
      }
      setShowDialog(open);
    },
    [markOnboarded, setWizardState],
  );

  const handleNavigate = useCallback(
    (path: string) => {
      setWizardState((prev) => ({ ...prev, dismissed: true }));
      setShowDialog(false);
      markOnboarded();
      navigate(path);
    },
    [markOnboarded, navigate, setWizardState],
  );

  const handleJumpToStep = useCallback(
    (index: number) => {
      setWizardState((prev) => ({ ...prev, currentStep: index }));
    },
    [setWizardState],
  );

  // Keyboard navigation
  useEffect(() => {
    if (!showDialog) return;

    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "ArrowLeft" && !isWelcome) {
        e.preventDefault();
        handlePrevious();
      } else if (e.key === "ArrowRight") {
        e.preventDefault();
        if (isComplete) {
          handleFinish();
        } else {
          handleNext();
        }
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [showDialog, isWelcome, isComplete, handlePrevious, handleNext, handleFinish]);

  // Do not render if completed or dismissed
  if (wizardState.completed || wizardState.dismissed) {
    return null;
  }

  return (
    <Dialog open={showDialog} onOpenChange={handleDialogClose}>
      <DialogContent size="md" className="sm:max-w-xl glass-panel border-border/70 overflow-hidden">
        {/* Welcome / complete gradient background */}
        {(isWelcome || isComplete) && (
          <div className="absolute inset-0 z-0 bg-gradient-to-br from-primary/10 via-background to-secondary/5 pointer-events-none" />
        )}
        <DialogCloseButton className="z-10 bg-background/50 backdrop-blur-sm" />

        {/* ---------- Header ---------- */}
        <DialogHeader className="relative z-10 pt-4 pb-2">
          <DialogTitle className="flex items-center gap-2.5 text-xl">
            <span
              className={cn(
                "flex items-center justify-center rounded-lg p-1.5",
                isComplete
                  ? "bg-success/15 text-success"
                  : "bg-primary/15 text-primary",
              )}
            >
              {currentStep.icon}
            </span>
            {currentStep.title}
          </DialogTitle>
          <DialogDescription className="text-base mt-2">
            {currentStep.description}
          </DialogDescription>
        </DialogHeader>

        {/* ---------- Stepper progress bar ---------- */}
        <div className="relative z-10 px-6 pt-1 pb-2">
          <Stepper steps={stepperLabels} current={step} />
        </div>

        {/* ---------- Body ---------- */}
        <DialogBody className="relative z-10 py-6">
          {/* Step 1: Welcome capability cards */}
          {isWelcome && <SetupWizardStep1 />}

          {/* Steps 2–5: Progress + dot nav + link button */}
          {!isWelcome && !isComplete && (
            <SetupWizardStep2
              steps={STEPS}
              currentStepIndex={step}
              totalSteps={TOTAL_STEPS}
              onJumpToStep={handleJumpToStep}
              onNavigate={handleNavigate}
            />
          )}

          {/* Step 6: Complete */}
          {isComplete && <SetupWizardStep3 />}
        </DialogBody>

        {/* ---------- Footer buttons ---------- */}
        <DialogFooter className="relative z-10 pt-2 pb-4 flex items-center sm:justify-between">
          {/* Left: skip */}
          <div className="flex items-center">
            {!isWelcome && !isComplete && (
              <Button
                variant="ghost"
                size="sm"
                onClick={handleSkip}
                className="text-muted-foreground hover:text-foreground"
              >
                {t("setupWizard.skipGuide")}
              </Button>
            )}
          </div>

          {/* Right: navigation buttons */}
          <div className="flex items-center gap-2">
            {!isWelcome && (
              <Button variant="outline" onClick={handlePrevious}>
                <ChevronLeft className="mr-1 size-4" />
                {t("common.prev")}
              </Button>
            )}

            {isWelcome ? (
              <Button onClick={handleNext} className="w-full sm:w-auto px-8 gap-2">
                {t("setupWizard.startSetup")}
                <ChevronRight className="size-4" />
              </Button>
            ) : isComplete ? (
              <Button
                onClick={handleFinish}
                loading={completing}
                className="bg-success hover:bg-success/90 text-success-foreground px-8 gap-2"
              >
                <CheckCircle2 className="size-4" />
                {t("common.finish")}
              </Button>
            ) : (
              <Button onClick={handleNext} className="px-6 gap-2">
                {t("common.next")}
                <ChevronRight className="size-4" />
              </Button>
            )}
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
