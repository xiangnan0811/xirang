import { useCallback, useEffect, useRef, useState } from "react";
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
  fetchContent: (signal?: AbortSignal) => Promise<FileContentResult>;
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
  const [loadedPath, setLoadedPath] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const requestSeqRef = useRef(0);

  const clearPreviewState = useCallback(() => {
    setContent("");
    setSize(0);
    setTruncated(false);
    setError(null);
    setLoadedPath(null);
  }, []);

  const abortCurrentRequest = useCallback(() => {
    abortRef.current?.abort();
    abortRef.current = null;
    requestSeqRef.current += 1;
  }, []);

  useEffect(() => {
    if (!open) {
      abortCurrentRequest();
      clearPreviewState();
      setLoading(false);
      return;
    }

    abortCurrentRequest();
    clearPreviewState();

    const ctrl = new AbortController();
    abortRef.current = ctrl;
    const requestSeq = requestSeqRef.current;

    setLoading(true);
    fetchContent(ctrl.signal)
      .then((result) => {
        if (ctrl.signal.aborted || requestSeq !== requestSeqRef.current) return;
        setContent(result.content);
        setSize(result.size);
        setTruncated(result.truncated);
        setLoadedPath(filePath);
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted || requestSeq !== requestSeqRef.current) return;
        setError(err instanceof Error ? err.message : t('fileBrowser.loadFileFailed'));
      })
      .finally(() => {
        if (ctrl.signal.aborted || requestSeq !== requestSeqRef.current) return;
        setLoading(false);
      });

    return () => {
      ctrl.abort();
      if (abortRef.current === ctrl) {
        abortRef.current = null;
      }
      requestSeqRef.current += 1;
    };
  // eslint-disable-next-line react-hooks/exhaustive-deps -- t is stable from react-i18next
  }, [open, filePath, fetchContent, abortCurrentRequest, clearPreviewState]);

  const handleOpenChange = useCallback((nextOpen: boolean) => {
    if (!nextOpen) {
      abortCurrentRequest();
      clearPreviewState();
      setLoading(false);
    }
    onOpenChange(nextOpen);
  }, [abortCurrentRequest, clearPreviewState, onOpenChange]);

  const fileName = filePath.split("/").pop() ?? filePath;
  const visibleContent = loadedPath === filePath ? content : "";

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
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
                {visibleContent || <span className="text-muted-foreground">{t('fileBrowser.emptyContent')}</span>}
              </pre>
            </>
          )}
        </DialogBody>
      </DialogContent>
    </Dialog>
  );
}

