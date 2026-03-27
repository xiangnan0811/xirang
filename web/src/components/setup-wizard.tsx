import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { useAuth } from "@/context/auth-context";
import { request } from "@/lib/api/core";
import {
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  ExternalLink,
  Key,
  MonitorCheck,
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
import { usePersistentState } from "@/hooks/use-persistent-state";
import { cn } from "@/lib/utils";

/* -------------------------------------------------------------------------- */
/*  类型                                                                       */
/* -------------------------------------------------------------------------- */

interface WizardStep {
  id: string;
  title: string;
  description: string;
  icon: React.ReactNode;
  /** 跳转路径，null 表示无链接（欢迎页 / 完成页） */
  linkTo: string | null;
  linkLabel: string | null;
}

/* -------------------------------------------------------------------------- */
/*  组件                                                                       */
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

  // 首次登录延迟弹出
  useEffect(() => {
    if (!wizardState.completed && !wizardState.dismissed) {
      const timer = setTimeout(() => setShowDialog(true), 600);
      return () => clearTimeout(timer);
    }
  }, [wizardState.completed, wizardState.dismissed]);

  /* ---- 派生状态 ---- */
  const step = wizardState.currentStep;
  const currentStep = STEPS[step];
  const isWelcome = step === 0;
  const isComplete = step === TOTAL_STEPS - 1;
  // 进度百分比：欢迎页 0%，完成页 100%
  const progress = Math.round((step / (TOTAL_STEPS - 1)) * 100);

  /* ---- 后端标记 ---- */
  const markOnboarded = useCallback(() => {
    void request("/me/onboarded", {
      method: "POST",
      token: token ?? undefined,
    }).catch(() => {
      /* best-effort */
    });
  }, [token]);

  /* ---- 操作 ---- */
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
    // 短暂延迟让用户看到完成状态
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
        // 用户主动关闭视作跳过
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

  // 键盘导航
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

  // 已完成或已跳过则不渲染
  if (wizardState.completed || wizardState.dismissed) {
    return null;
  }

  return (
    <Dialog open={showDialog} onOpenChange={handleDialogClose}>
      <DialogContent size="md" className="sm:max-w-xl glass-panel border-border/70 overflow-hidden">
        {/* 欢迎页 / 完成页渐变背景 */}
        {(isWelcome || isComplete) && (
          <div className="absolute inset-0 z-0 bg-gradient-to-br from-primary/10 via-background to-secondary/5 pointer-events-none" />
        )}
        <DialogCloseButton className="z-10 bg-background/50 backdrop-blur-sm" />

        {/* ---------- 头部 ---------- */}
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

        {/* ---------- 主体 ---------- */}
        <DialogBody className="relative z-10 py-6">
          {/* 欢迎页：能力卡片 */}
          {isWelcome && (
            <div className="grid grid-cols-3 gap-3">
              {[
                { icon: <ShieldCheck className="size-5" />, label: t("setupWizard.capabilities.backupMgmt"), desc: t("setupWizard.capabilities.backupMgmtDesc") },
                { icon: <MonitorCheck className="size-5" />, label: t("setupWizard.capabilities.nodeMonitor"), desc: t("setupWizard.capabilities.nodeMonitorDesc") },
                { icon: <ClipboardList className="size-5" />, label: t("setupWizard.capabilities.policySchedule"), desc: t("setupWizard.capabilities.policyScheduleDesc") },
              ].map((item) => (
                <div
                  key={item.label}
                  className="glass-panel p-4 flex flex-col items-center text-center space-y-2 border-border/40"
                >
                  <div className="p-2.5 bg-primary/10 rounded-full text-primary">
                    {item.icon}
                  </div>
                  <h3 className="font-medium text-sm">{item.label}</h3>
                  <p className="text-xs text-muted-foreground">{item.desc}</p>
                </div>
              ))}
            </div>
          )}

          {/* 中间步骤：进度条 + 步骤指示器 + 操作链接 */}
          {!isWelcome && !isComplete && (
            <div className="space-y-6">
              {/* 进度条 */}
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

              {/* 步骤指示器（点状） */}
              <div className="flex items-center justify-center gap-2">
                {STEPS.slice(1, -1).map((s, index) => {
                  const stepIndex = index + 1;
                  const isDone = stepIndex < step;
                  const isCurrent = stepIndex === step;

                  return (
                    <button
                      key={s.id}
                      type="button"
                      onClick={() =>
                        setWizardState((prev) => ({ ...prev, currentStep: stepIndex }))
                      }
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

              {/* 操作链接按钮 */}
              {currentStep.linkTo && currentStep.linkLabel && (
                <div className="flex justify-center pt-2">
                  <Button
                    variant="outline"
                    className="gap-2"
                    onClick={() => handleNavigate(currentStep.linkTo!)}
                  >
                    <ExternalLink className="size-4" />
                    {currentStep.linkLabel}
                  </Button>
                </div>
              )}
            </div>
          )}

          {/* 完成页 */}
          {isComplete && (
            <div className="flex flex-col items-center text-center space-y-4 py-4">
              <div className="p-4 bg-success/10 rounded-full text-success">
                <CheckCircle2 className="size-10" />
              </div>
              <div className="space-y-1">
                <p className="text-sm text-muted-foreground">
                  {t("setupWizard.completeHint1")}
                </p>
                <p className="text-sm text-muted-foreground">
                  {t("setupWizard.completeHint2")}
                </p>
              </div>
            </div>
          )}
        </DialogBody>

        {/* ---------- 底部按钮 ---------- */}
        <DialogFooter className="relative z-10 pt-2 pb-4 flex items-center sm:justify-between">
          {/* 左侧：跳过 */}
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

          {/* 右侧：导航按钮 */}
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
