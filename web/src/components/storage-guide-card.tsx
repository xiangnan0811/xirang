import { useState } from "react";
import { HardDrive } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useAuth } from "@/context/auth-context";
import { NasMountWizard } from "@/components/nas-mount-wizard";

export function StorageGuideCard() {
  const { token, role } = useAuth();
  const [wizardOpen, setWizardOpen] = useState(false);

  if (!token || role !== "admin") return null;

  return (
    <>
      <Card className="glass-panel border-border/70">
        <CardHeader className="pb-3">
          <CardTitle className="text-base">存储引导</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-xs text-muted-foreground mb-3">
            配置 NFS/SMB 网络存储或 USB 外置磁盘作为备份目标，生成挂载命令并验证连通性。
          </p>
          <Button size="sm" variant="outline" onClick={() => setWizardOpen(true)}>
            <HardDrive className="mr-1 size-3.5" />
            配置外部存储
          </Button>
        </CardContent>
      </Card>
      <NasMountWizard open={wizardOpen} onOpenChange={setWizardOpen} />
    </>
  );
}
