import { useRef, useState } from "react";
import { Download, Upload, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import { toast } from "sonner";

export function ConfigExportImport() {
  const { token, role } = useAuth();
  const [exporting, setExporting] = useState(false);
  const [importing, setImporting] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  if (!token || role !== "admin") return null;

  const handleExport = async () => {
    setExporting(true);
    try {
      const data = await apiClient.exportConfig(token);
      const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `xirang-config-${new Date().toISOString().slice(0, 10)}.json`;
      a.click();
      URL.revokeObjectURL(url);
      toast.success("配置已导出");
    } catch (err) {
      toast.error(getErrorMessage(err, "导出失败"));
    } finally {
      setExporting(false);
    }
  };

  const handleImportClick = () => {
    fileInputRef.current?.click();
  };

  const handleFileChange = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    setImporting(true);
    try {
      const text = await file.text();
      const data = JSON.parse(text);
      const result = await apiClient.importConfig(token, data, "skip");
      toast.success(`导入完成：已导入 ${result.imported} 项，跳过 ${result.skipped} 项`);
    } catch (err) {
      toast.error(getErrorMessage(err, "导入失败"));
    } finally {
      setImporting(false);
      if (fileInputRef.current) fileInputRef.current.value = "";
    }
  };

  return (
    <Card className="glass-panel border-border/70">
      <CardHeader className="pb-3">
        <CardTitle className="text-base">系统配置</CardTitle>
      </CardHeader>
      <CardContent>
        <p className="text-xs text-muted-foreground mb-3">
          导出或导入节点、SSH 密钥、策略等配置数据（仅管理员可用）。
        </p>
        <div className="flex gap-2">
          <Button size="sm" variant="outline" onClick={handleExport} disabled={exporting}>
            {exporting ? <Loader2 className="mr-1 size-3.5 animate-spin" /> : <Download className="mr-1 size-3.5" />}
            导出配置
          </Button>
          <Button size="sm" variant="outline" onClick={handleImportClick} disabled={importing}>
            {importing ? <Loader2 className="mr-1 size-3.5 animate-spin" /> : <Upload className="mr-1 size-3.5" />}
            导入配置
          </Button>
          <input ref={fileInputRef} type="file" accept=".json" className="hidden" onChange={handleFileChange} />
        </div>
      </CardContent>
    </Card>
  );
}
