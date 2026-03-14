import { useState } from "react";
import { CheckCircle2, ChevronRight, Rocket, Server, KeyRound, ListChecks, Zap, PartyPopper } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import { useAuth } from "@/context/auth-context";
import { request } from "@/lib/api/core";

type Step = {
  id: string;
  title: string;
  description: string;
  icon: React.ElementType;
  action?: { label: string; path: string };
};

const STEPS: Step[] = [
  {
    id: "node",
    title: "添加第一个节点",
    description: '前往"节点"页面，点击"添加节点"，输入服务器 SSH 信息。',
    icon: Server,
    action: { label: "去添加节点", path: "/app/nodes" },
  },
  {
    id: "ssh_key",
    title: "创建 SSH 密钥",
    description: '前往"SSH Key"页面，导入或粘贴已有私钥，便于后续节点认证。',
    icon: KeyRound,
    action: { label: "去管理密钥", path: "/app/ssh-keys" },
  },
  {
    id: "policy",
    title: "创建备份策略",
    description: '前往"策略"页面，从推荐模板创建第一个备份策略，绑定节点后即可定时执行。',
    icon: ListChecks,
    action: { label: "去创建策略", path: "/app/policies" },
  },
  {
    id: "trigger",
    title: "测试连接并触发首次备份",
    description: '在节点列表点击"测试连接"，确认连通后，在任务列表手动触发一次备份验证配置。',
    icon: Zap,
    action: { label: "去查看任务", path: "/app/tasks" },
  },
  {
    id: "done",
    title: "引导完成",
    description: "您已完成所有设置步骤，平台已准备就绪。可随时返回任意页面继续配置。",
    icon: PartyPopper,
  },
];

type Props = {
  open: boolean;
  onFinish: () => void;
};

export function OnboardingWizard({ open, onFinish }: Props) {
  const { token } = useAuth();
  const [step, setStep] = useState(0);
  const navigate = useNavigate();
  const current = STEPS[step];

  const isLast = step === STEPS.length - 1;

  const handleNext = () => {
    if (isLast) {
      void completeOnboarding();
    } else {
      setStep((s) => s + 1);
    }
  };

  const handleNavigate = (path: string) => {
    void completeOnboarding();
    navigate(path);
  };

  const completeOnboarding = async () => {
    try {
      await request("/me/onboarded", { method: "POST", token: token ?? undefined });
    } catch { /* ignore */ }
    onFinish();
  };

  const Icon = current.icon;

  return (
    <Dialog open={open} onOpenChange={() => { /* controlled */ }}>
      <DialogContent className="max-w-md" aria-describedby={undefined}>
        <DialogHeader>
          <div className="mb-1 flex items-center gap-2">
            <Rocket className="size-5 text-primary" />
            <DialogTitle>快速上手引导</DialogTitle>
          </div>
        </DialogHeader>

        <DialogBody>
          {/* 步骤指示器 */}
          <div className="mb-6 flex items-center gap-1.5">
            {STEPS.map((s, i) => (
              <div
                key={s.id}
                className={cn(
                  "h-1.5 flex-1 rounded-full transition-colors",
                  i < step
                    ? "bg-primary"
                    : i === step
                    ? "bg-primary/60"
                    : "bg-muted"
                )}
              />
            ))}
          </div>

          {/* 当前步骤内容 */}
          <div className="flex flex-col items-center text-center">
            <div className={cn(
              "mb-4 flex size-16 items-center justify-center rounded-2xl",
              isLast ? "bg-success/10" : "bg-primary/10"
            )}>
              <Icon className={cn("size-8", isLast ? "text-success" : "text-primary")} />
            </div>
            <p className="text-sm font-medium text-muted-foreground">
              步骤 {step + 1} / {STEPS.length}
            </p>
            <h3 className="mt-1 text-lg font-semibold">{current.title}</h3>
            <p className="mt-2 text-sm text-muted-foreground">{current.description}</p>
          </div>

          {/* 已完成步骤列表 */}
          {step > 0 && (
            <div className="mt-4 space-y-1">
              {STEPS.slice(0, step).map((s) => (
                <div key={s.id} className="flex items-center gap-2 text-xs text-muted-foreground">
                  <CheckCircle2 className="size-3.5 text-success shrink-0" />
                  <span>{s.title}</span>
                </div>
              ))}
            </div>
          )}
        </DialogBody>

        <div className="flex justify-between gap-2 px-6 pb-6 pt-2">
          <Button variant="ghost" size="sm" onClick={() => void completeOnboarding()}>
            跳过引导
          </Button>
          <div className="flex gap-2">
            {current.action && (
              <Button
                variant="outline"
                size="sm"
                onClick={() => handleNavigate(current.action!.path)}
              >
                {current.action.label}
                <ChevronRight className="ml-1 size-4" />
              </Button>
            )}
            <Button size="sm" onClick={handleNext}>
              {isLast ? "完成" : "下一步"}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
