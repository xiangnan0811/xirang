import { useTranslation } from "react-i18next";
import { Input } from "@/components/ui/input";
import type { NodeRecord } from "@/types/domain";
import type { PolicyDraft } from "@/components/policy-editor-dialog";

type PolicySourceTargetProps = {
  draft: PolicyDraft;
  nodes: NodeRecord[];
  saving: boolean;
  onChange: (updater: (prev: PolicyDraft) => PolicyDraft) => void;
};

export function PolicySourceTarget({ draft, nodes, saving: _saving, onChange }: PolicySourceTargetProps) {
  const { t } = useTranslation();

  const handleNodeToggle = (nodeId: number, checked: boolean) => {
    onChange((prev) => ({
      ...prev,
      nodeIds: checked
        ? [...prev.nodeIds, nodeId]
        : prev.nodeIds.filter((id) => id !== nodeId),
    }));
  };

  return (
    <>
      <div>
        <label htmlFor="policy-edit-source" className="mb-1 block text-sm font-medium">
          {t("policyEditor.sourcePath")}
        </label>
        <Input
          id="policy-edit-source"
          placeholder={t("policyEditor.sourcePathPlaceholder")}
          value={draft.sourcePath}
          onChange={(e) => onChange((prev) => ({ ...prev, sourcePath: e.target.value }))}
        />
        <p className="mt-1 text-xs text-muted-foreground">
          {t("policyEditor.backupStorageInfo")}
        </p>
      </div>

      <div>
        <label htmlFor="policy-edit-target" className="mb-1 block text-sm font-medium">
          {t("policyEditor.targetPath")}
        </label>
        <Input
          id="policy-edit-target"
          placeholder={t("policyEditor.targetPathPlaceholder")}
          value={draft.targetPath ?? ""}
          onChange={(e) => onChange((prev) => ({ ...prev, targetPath: e.target.value }))}
        />
      </div>

      {/* Associated nodes */}
      {nodes.length > 0 ? (
        <div>
          <div className="mb-1 text-sm font-medium">
            {t("policyEditor.relatedNodes")}
            {draft.nodeIds.length > 0 ? (
              <span className="ml-1 text-xs text-muted-foreground">
                {t("policyEditor.relatedNodesSelected", { count: draft.nodeIds.length })}
              </span>
            ) : null}
          </div>
          <div className="glass-panel max-h-40 overflow-y-auto rounded-md border border-border/60 p-2">
            <div className="flex flex-col gap-1.5">
              {nodes.map((node) => {
                const checked = draft.nodeIds.includes(node.id);
                return (
                  <label
                    key={node.id}
                    className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors hover:bg-accent/40"
                  >
                    <input
                      type="checkbox"
                      className="size-4 shrink-0"
                      checked={checked}
                      onChange={(e) => handleNodeToggle(node.id, e.target.checked)}
                      aria-label={t("policyEditor.selectNode", { name: node.name })}
                    />
                    <span className="truncate">{node.name}</span>
                    <span className="ml-auto shrink-0 text-xs text-muted-foreground">{node.host}</span>
                  </label>
                );
              })}
            </div>
          </div>
        </div>
      ) : null}

      {/* Per-node path preview */}
      {draft.nodeIds.length > 0 && nodes.length > 0 && (
        <div>
          <div className="mb-1 text-sm font-medium">{t("policyEditor.perNodePathPreview")}</div>
          <div className="glass-panel rounded-md border border-border/60 px-3 py-2 font-mono text-xs text-muted-foreground">
            {draft.nodeIds.map((nodeId, idx) => {
              const node = nodes.find((n) => n.id === nodeId);
              if (!node) return null;
              const dirName = node.backupDir || node.name;
              const isLast = idx === draft.nodeIds.length - 1;
              const prefix = isLast ? "\u2514" : "\u251C";
              return (
                <div key={nodeId}>
                  {prefix} {node.name} → /backup/{dirName}/
                </div>
              );
            })}
          </div>
        </div>
      )}
    </>
  );
}
