import { useEffect, useState } from "react";
import { FileText, AlertCircle } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogBody,
  DialogCloseButton,
} from "@/components/ui/dialog";
import type { FileContentResult } from "@/lib/api/files-api";

type FilePreviewDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  filePath: string;
  fetchContent: () => Promise<FileContentResult>;
};

export function FilePreviewDialog({
  open,
  onOpenChange,
  filePath,
  fetchContent,
}: FilePreviewDialogProps) {
  const [content, setContent] = useState<string>("");
  const [size, setSize] = useState<number>(0);
  const [truncated, setTruncated] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setContent("");
    setError(null);
    setLoading(true);
    fetchContent()
      .then((result) => {
        setContent(result.content);
        setSize(result.size);
        setTruncated(result.truncated);
      })
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : "加载文件失败");
      })
      .finally(() => setLoading(false));
  }, [open, fetchContent]);

  const fileName = filePath.split("/").pop() ?? filePath;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="lg" className="md:max-w-[800px]">
        <DialogHeader>
          <div className="flex items-center gap-2 pr-8">
            <FileText className="size-4 shrink-0 text-muted-foreground" />
            <DialogTitle className="truncate text-base font-medium">{fileName}</DialogTitle>
          </div>
          <DialogDescription className="text-xs text-muted-foreground truncate">{filePath}</DialogDescription>
          <DialogCloseButton />
        </DialogHeader>
        <DialogBody className="p-0">
          {loading && (
            <div className="flex items-center justify-center p-12 text-sm text-muted-foreground">
              加载中...
            </div>
          )}
          {error && !loading && (
            <div className="flex items-center gap-2 p-6 text-sm text-destructive">
              <AlertCircle className="size-4 shrink-0" />
              {error}
            </div>
          )}
          {!loading && !error && (
            <>
              {truncated && (
                <div className="border-b border-border/40 bg-amber-500/10 px-4 py-2 text-xs text-amber-600 dark:text-amber-400">
                  文件较大（{formatBytes(size)}），仅展示前 1MB 内容
                </div>
              )}
              <pre className="overflow-auto p-4 text-xs leading-relaxed font-mono whitespace-pre-wrap break-all thin-scrollbar max-h-[60vh]">
                {content || <span className="text-muted-foreground">（文件内容为空）</span>}
              </pre>
            </>
          )}
        </DialogBody>
      </DialogContent>
    </Dialog>
  );
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}
