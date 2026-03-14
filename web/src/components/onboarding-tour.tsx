import { useEffect, useState } from "react";
import { useAuth } from "@/context/auth-context";
import { request } from "@/lib/api/core";
import { CheckCircle2, ChevronRight, ChevronLeft, Rocket } from "lucide-react";
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
import { Badge } from "@/components/ui/badge";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { cn } from "@/lib/utils";

interface OnboardingStep {
  id: string;
  title: string;
  description: string;
}

export function OnboardingTour() {
  const { token } = useAuth();
  const steps: OnboardingStep[] = [
    {
      id: "welcome",
      title: "欢迎使用息壤",
      description: "息壤是一个服务器运维备份管理平台，让我们快速了解使用流程。",
    },
    {
      id: "ssh-key",
      title: "第 1 步：配置 SSH Key",
      description: "先配置可用密钥，后续节点可直接复用，避免重复输入密码。",
    },
    {
      id: "node",
      title: "第 2 步：添加节点",
      description: "添加至少一个服务器节点并完成连接探测，确保可以正常通信。",
    },
    {
      id: "policy",
      title: "第 3 步：创建策略",
      description: "定义 Rsync 源/目标路径与执行周期，设置备份规则。",
    },
    {
      id: "task",
      title: "第 4 步：创建任务",
      description: "绑定节点与策略，尝试手动触发，验证整个流程是否正常。",
    },
  ];

  const [tourState, setTourState] = usePersistentState<{
    completed: boolean;
    currentStepIndex: number;
    dismissed: boolean;
  }>("xirang.onboarding.tour", {
    completed: false,
    currentStepIndex: 0,
    dismissed: false,
  });

  const [showDialog, setShowDialog] = useState(false);

  // 首次登录自动显示欢迎对话框
  useEffect(() => {
    if (!tourState.completed && !tourState.dismissed) {
      const timer = setTimeout(() => setShowDialog(true), 500);
      return () => clearTimeout(timer);
    }
  }, [tourState.completed, tourState.dismissed]);

  const currentStep = steps[tourState.currentStepIndex];
  // 欢迎页面不算作正式步骤
  const isWelcome = tourState.currentStepIndex === 0;
  // 已完成步骤：即看过的步骤
  const completedSteps = Math.max(0, tourState.currentStepIndex);
  const totalRealSteps = steps.length - 1;
  const progress = isWelcome ? 0 : Math.round((completedSteps / totalRealSteps) * 100);
  const isLastStep = tourState.currentStepIndex === steps.length - 1;

  // 键盘导航支持
  useEffect(() => {
    if (!showDialog) return;

    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "ArrowLeft" && !isWelcome) {
        e.preventDefault();
        handlePrevious();
      } else if (e.key === "ArrowRight") {
        e.preventDefault();
        if (isLastStep) {
          handleFinish();
        } else {
          handleNext();
        }
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [showDialog, isWelcome, isLastStep, tourState.currentStepIndex]);

  const markOnboardedInBackend = () => {
    void request("/me/onboarded", { method: "POST", token: token ?? undefined }).catch(() => { /* best-effort */ });
  };

  const handleFinish = () => {
    setTourState((prev) => ({ ...prev, completed: true, dismissed: true }));
    setShowDialog(false);
    markOnboardedInBackend();
  };

  const handleNext = () => {
    if (!isLastStep) {
      setTourState((prev) => ({ ...prev, currentStepIndex: prev.currentStepIndex + 1 }));
    } else {
      handleFinish();
    }
  };

  const handlePrevious = () => {
    setTourState((prev) => ({
      ...prev,
      currentStepIndex: Math.max(0, prev.currentStepIndex - 1),
    }));
  };

  const handleNeverShowAgain = () => {
    setTourState((prev) => ({ ...prev, completed: true, dismissed: true }));
    setShowDialog(false);
    markOnboardedInBackend();
  };

  const handleDialogClose = (open: boolean) => {
    if (!open) {
      // 用户主动关闭视作跳过
      setTourState((prev) => ({ ...prev, dismissed: true }));
      markOnboardedInBackend();
    }
    setShowDialog(open);
  };

  const handleStepClick = (stepIndex: number) => {
    if (stepIndex > 0 && stepIndex <= steps.length - 1) {
      setTourState((prev) => ({ ...prev, currentStepIndex: stepIndex }));
    }
  };

  // 如果已完成或已关闭，直接不渲染
  if (tourState.completed || tourState.dismissed) {
    return null;
  }

  return (
    <Dialog open={showDialog} onOpenChange={handleDialogClose}>
      <DialogContent size="md" className="sm:max-w-xl glass-panel border-border/70 overflow-hidden">
        {isWelcome && (
          <div className="absolute inset-0 z-0 bg-gradient-to-br from-primary/10 via-background to-secondary/5 pointer-events-none" />
        )}
        <DialogCloseButton className="z-10 bg-background/50 backdrop-blur-sm" />

        <DialogHeader className="relative z-10 pt-4 pb-2">
          <DialogTitle className="flex items-center gap-2.5 text-xl">
            {isWelcome ? (
              <Rocket className="size-6 text-primary" />
            ) : (
              <Badge variant="outline" className="h-7 text-xs sm:text-sm font-medium border-primary/30 text-primary">
                {completedSteps} / {totalRealSteps}
              </Badge>
            )}
            {currentStep?.title}
          </DialogTitle>
          <DialogDescription className="text-base mt-2">
            {currentStep?.description}
          </DialogDescription>
        </DialogHeader>

        <DialogBody className="relative z-10 py-6">
          {!isWelcome && (
            <div className="space-y-6">
              <div className="space-y-2">
                <div className="flex items-center justify-between text-sm font-medium">
                  <span className="text-muted-foreground">引导进度</span>
                  <span className={cn(progress === 100 ? "text-success" : "text-primary")}>
                    {progress}%
                  </span>
                </div>
                <div className="h-2 rounded-full bg-muted/60 overflow-hidden backdrop-blur-sm">
                  <div
                    className={cn(
                      "h-full transition-all duration-700 ease-in-out",
                      progress === 100 ? "bg-success" : "bg-primary"
                    )}
                    style={{ width: `${progress}%` }}
                  />
                </div>
              </div>

              <div className="grid grid-cols-4 gap-3">
                {steps.slice(1).map((step, index) => {
                  const stepNumber = index + 1;
                  const isCompleted = stepNumber < tourState.currentStepIndex;
                  const isCurrent = stepNumber === tourState.currentStepIndex;

                  return (
                    <button
                      key={step.id}
                      onClick={() => handleStepClick(stepNumber)}
                      className={cn(
                        "flex flex-col items-center justify-center p-3 rounded-xl border transition-all duration-300 cursor-pointer hover:scale-105 active:scale-95",
                        isCompleted
                          ? "border-success/30 bg-success/10 text-success hover:border-success/50 hover:bg-success/15"
                          : isCurrent
                            ? "border-primary/50 bg-primary/10 shadow-sm shadow-primary/10 hover:border-primary/70 hover:bg-primary/15"
                            : "border-border/40 bg-muted/20 opacity-70 hover:opacity-90 hover:border-border/60"
                      )}
                      aria-label={`跳转到${step.title}`}
                    >
                      <span className={cn(
                        "text-xs font-semibold mb-2",
                        isCurrent ? "text-primary" : isCompleted ? "text-success" : "text-muted-foreground"
                      )}>
                        步骤 {stepNumber}
                      </span>
                      {isCompleted ? (
                        <CheckCircle2 className="size-5" />
                      ) : isCurrent ? (
                        <div className="size-5 rounded-full border-[3px] border-primary/80 animate-pulse border-t-transparent" />
                      ) : (
                        <div className="size-5 rounded-full border-2 border-muted-foreground/30" />
                      )}
                    </button>
                  );
                })}
              </div>
            </div>
          )}

          {isWelcome && (
            <div className="py-8 flex justify-center">
              <div className="grid grid-cols-2 gap-4 w-full">
                <div className="glass-panel p-4 flex flex-col items-center text-center space-y-2 border-border/40">
                  <div className="p-3 bg-primary/10 rounded-full text-primary">
                    <CheckCircle2 className="size-6" />
                  </div>
                  <h3 className="font-medium text-sm">简单 4 步</h3>
                  <p className="text-xs text-muted-foreground">跟随指引快速熟悉核心概念</p>
                </div>
                <div className="glass-panel p-4 flex flex-col items-center text-center space-y-2 border-border/40">
                  <div className="p-3 bg-success/10 rounded-full text-success">
                    <Rocket className="size-6" />
                  </div>
                  <h3 className="font-medium text-sm">即刻保护</h3>
                  <p className="text-xs text-muted-foreground">十分钟内完成首个备份任务</p>
                </div>
              </div>
            </div>
          )}
        </DialogBody>

        <DialogFooter className="relative z-10 pt-2 pb-4 flex items-center sm:justify-between">
          <div className="flex items-center gap-2">
            {!isWelcome && (
              <>
                <Button variant="ghost" size="sm" onClick={handleFinish} className="text-muted-foreground hover:text-foreground">
                  跳过引导
                </Button>
                <Button variant="ghost" size="sm" onClick={handleNeverShowAgain} className="text-muted-foreground/70 hover:text-muted-foreground text-xs">
                  不再显示
                </Button>
              </>
            )}
          </div>
          <div className="flex items-center gap-2">
            {!isWelcome && (
              <Button variant="outline" onClick={handlePrevious}>
                <ChevronLeft className="mr-2 size-4" />
                上一步
              </Button>
            )}

            {isWelcome ? (
              <Button onClick={handleNext} className="w-full sm:w-auto px-8 gap-2">
                开始引导
                <ChevronRight className="size-4" />
              </Button>
            ) : isLastStep ? (
              <Button onClick={handleFinish} variant="default" className="bg-success hover:bg-success/90 text-success-foreground px-8 gap-2">
                <CheckCircle2 className="size-4" />
                完成
              </Button>
            ) : (
              <Button onClick={handleNext} className="px-6 gap-2">
                下一步
                <ChevronRight className="size-4" />
              </Button>
            )}
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
