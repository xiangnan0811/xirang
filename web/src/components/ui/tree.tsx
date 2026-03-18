import { useState, useCallback, type ReactNode, type KeyboardEvent } from "react";
import { ChevronRight, Folder, FolderOpen, File } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";

type TreeItemData = {
  id: string;
  label: string;
  isDir?: boolean;
  icon?: ReactNode;
  children?: TreeItemData[];
};

type TreeItemProps = {
  item: TreeItemData;
  depth?: number;
  selected?: string;
  expanded?: Set<string>;
  onSelect?: (item: TreeItemData) => void;
  onToggle?: (item: TreeItemData) => void;
  /** 懒加载：展开时获取子节点 */
  onLoadChildren?: (item: TreeItemData) => Promise<TreeItemData[]>;
  loadingIds?: Set<string>;
};

function TreeItem({
  item,
  depth = 0,
  selected,
  expanded,
  onSelect,
  onToggle,
  onLoadChildren,
  loadingIds,
}: TreeItemProps) {
  const { t } = useTranslation();
  const isExpanded = expanded?.has(item.id) ?? false;
  const isSelected = selected === item.id;
  const isLoading = loadingIds?.has(item.id) ?? false;
  const hasChildren = item.isDir || (item.children && item.children.length > 0);

  const handleClick = () => {
    if (hasChildren) {
      onToggle?.(item);
    }
    onSelect?.(item);
  };

  const handleKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      handleClick();
    }
    if (e.key === "ArrowRight" && hasChildren && !isExpanded) {
      e.preventDefault();
      onToggle?.(item);
    }
    if (e.key === "ArrowLeft" && hasChildren && isExpanded) {
      e.preventDefault();
      onToggle?.(item);
    }
  };

  const defaultIcon = hasChildren
    ? isExpanded
      ? <FolderOpen className="size-4 shrink-0 text-warning" />
      : <Folder className="size-4 shrink-0 text-warning" />
    : <File className="size-4 shrink-0 text-muted-foreground" />;

  return (
    <div role="treeitem" aria-expanded={hasChildren ? isExpanded : undefined} aria-selected={isSelected}>
      <button
        type="button"
        className={cn(
          "flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-sm transition-colors",
          "hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          isSelected && "bg-accent text-accent-foreground"
        )}
        style={{ paddingLeft: `${depth * 16 + 8}px` }}
        onClick={handleClick}
        onKeyDown={handleKeyDown}
        aria-label={`${hasChildren ? (isExpanded ? t('tree.collapse') : t('tree.expand')) : t('tree.select')} ${item.label}`}
      >
        {hasChildren ? (
          <ChevronRight
            className={cn(
              "size-3.5 shrink-0 text-muted-foreground transition-transform duration-200",
              isExpanded && "rotate-90",
              isLoading && "animate-spin"
            )}
          />
        ) : (
          <span className="w-3.5 shrink-0" />
        )}
        {item.icon ?? defaultIcon}
        <span className="truncate">{item.label}</span>
      </button>

      {hasChildren && isExpanded && item.children && item.children.length > 0 && (
        <div role="group">
          {item.children.map((child) => (
            <TreeItem
              key={child.id}
              item={child}
              depth={depth + 1}
              selected={selected}
              expanded={expanded}
              onSelect={onSelect}
              onToggle={onToggle}
              onLoadChildren={onLoadChildren}
              loadingIds={loadingIds}
            />
          ))}
        </div>
      )}
    </div>
  );
}

type TreeProps = {
  items: TreeItemData[];
  className?: string;
  /** 受控：当前选中的节点 ID */
  selected?: string;
  /** 受控：当前展开的节点 ID 集合 */
  expanded?: Set<string>;
  onSelect?: (item: TreeItemData) => void;
  onToggle?: (item: TreeItemData) => void;
  /** 懒加载回调：展开目录时调用，返回子节点列表 */
  onLoadChildren?: (item: TreeItemData) => Promise<TreeItemData[]>;
};

function Tree({ items, className, selected, expanded, onSelect, onToggle, onLoadChildren }: TreeProps) {
  const { t } = useTranslation();
  const [internalSelected, setInternalSelected] = useState<string | undefined>(selected);
  const [internalExpanded, setInternalExpanded] = useState<Set<string>>(expanded ?? new Set());
  const [loadingIds, setLoadingIds] = useState<Set<string>>(new Set());

  const isControlled = selected !== undefined || expanded !== undefined;

  const currentSelected = isControlled ? selected : internalSelected;
  const currentExpanded = isControlled ? (expanded ?? new Set()) : internalExpanded;

  const handleSelect = useCallback(
    (item: TreeItemData) => {
      if (!isControlled) {
        setInternalSelected(item.id);
      }
      onSelect?.(item);
    },
    [isControlled, onSelect]
  );

  const handleToggle = useCallback(
    async (item: TreeItemData) => {
      const willExpand = !currentExpanded.has(item.id);

      if (willExpand && onLoadChildren && (!item.children || item.children.length === 0)) {
        setLoadingIds((prev) => new Set(prev).add(item.id));
        try {
          const children = await onLoadChildren(item);
          item.children = children;
        } finally {
          setLoadingIds((prev) => {
            const next = new Set(prev);
            next.delete(item.id);
            return next;
          });
        }
      }

      if (!isControlled) {
        setInternalExpanded((prev) => {
          const next = new Set(prev);
          if (willExpand) {
            next.add(item.id);
          } else {
            next.delete(item.id);
          }
          return next;
        });
      }
      onToggle?.(item);
    },
    [isControlled, currentExpanded, onLoadChildren, onToggle]
  );

  return (
    <div role="tree" className={cn("space-y-0.5", className)} aria-label={t('tree.treeViewLabel')}>
      {items.map((item) => (
        <TreeItem
          key={item.id}
          item={item}
          selected={currentSelected}
          expanded={currentExpanded}
          onSelect={handleSelect}
          onToggle={handleToggle}
          onLoadChildren={onLoadChildren}
          loadingIds={loadingIds}
        />
      ))}
    </div>
  );
}

export { Tree, TreeItem };
export type { TreeItemData, TreeProps, TreeItemProps };
