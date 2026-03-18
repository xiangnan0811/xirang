import { useEffect, useState } from "react";
import { FileText, AlertCircle } from "lucide-react";
import { useTranslation } from "react-i18next";
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
import { formatBytes } from "@/lib/utils";

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
  const { t } = useTranslation();
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
        setError(err instanceof Error ? err.message : t('fileBrowser.loadFileFailed'));
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
              {t('common.loading')}
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
                <div className="border-b border-border/40 bg-warning/10 px-4 py-2 text-xs text-warning">
                  {t('fileBrowser.fileTruncated', { size: formatBytes(size) })}
                </div>
              )}
              <pre className="overflow-auto p-4 text-xs leading-relaxed font-mono whitespace-pre-wrap break-all thin-scrollbar max-h-[60vh]">
                {content || <span className="text-muted-foreground">{t('fileBrowser.emptyContent')}</span>}
              </pre>
            </>
          )}
        </DialogBody>
      </DialogContent>
    </Dialog>
  );
}

