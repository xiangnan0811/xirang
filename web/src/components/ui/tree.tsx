import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { ChevronRight, File, Folder, FolderOpen } from "lucide-react";
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
  onLoadChildren?: (item: TreeItemData) => Promise<TreeItemData[]>;
  loadingIds?: Set<string>;
  childrenMap?: Map<string, TreeItemData[]>;
  focusedId?: string;
  buttonRefs?: Map<string, HTMLButtonElement>;
  onItemFocus?: (id: string) => void;
  onItemKeyDown?: (event: KeyboardEvent<HTMLButtonElement>, item: TreeItemData) => void;
};

function TreeItem({
  item,
  depth = 0,
  selected,
  expanded,
  onSelect,
  onToggle,
  loadingIds,
  childrenMap,
  focusedId,
  buttonRefs,
  onItemFocus,
  onItemKeyDown,
}: TreeItemProps) {
  const { t } = useTranslation();
  const isExpanded = expanded?.has(item.id) ?? false;
  const isSelected = selected === item.id;
  const isLoading = loadingIds?.has(item.id) ?? false;
  const resolvedChildren = childrenMap?.get(item.id) ?? item.children;
  const hasChildren = Boolean(item.isDir || (resolvedChildren && resolvedChildren.length > 0));

  const handleClick = () => {
    onItemFocus?.(item.id);
    if (hasChildren) onToggle?.(item);
    onSelect?.(item);
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (onItemKeyDown) {
      onItemKeyDown(event, item);
      return;
    }
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      handleClick();
    } else if (event.key === "ArrowRight" && hasChildren && !isExpanded) {
      event.preventDefault();
      onToggle?.(item);
    } else if (event.key === "ArrowLeft" && hasChildren && isExpanded) {
      event.preventDefault();
      onToggle?.(item);
    }
  };

  const defaultIcon = hasChildren ? (
    isExpanded ? (
      <FolderOpen className="size-4 shrink-0 text-warning" aria-hidden />
    ) : (
      <Folder className="size-4 shrink-0 text-warning" aria-hidden />
    )
  ) : (
    <File className="size-4 shrink-0 text-muted-foreground" aria-hidden />
  );

  return (
    <div
      role="treeitem"
      aria-expanded={hasChildren ? isExpanded : undefined}
      aria-selected={isSelected}
      aria-level={depth + 1}
    >
      <button
        ref={(node) => {
          if (!buttonRefs) return;
          if (node) buttonRefs.set(item.id, node);
          else buttonRefs.delete(item.id);
        }}
        type="button"
        tabIndex={focusedId === undefined || focusedId === item.id ? 0 : -1}
        className={cn(
          "flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-sm transition-colors",
          "hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          isSelected && "bg-accent text-accent-foreground"
        )}
        style={{ paddingLeft: `${depth * 16 + 8}px` }}
        onClick={handleClick}
        onFocus={() => onItemFocus?.(item.id)}
        onKeyDown={handleKeyDown}
        aria-label={`${hasChildren ? (isExpanded ? t("tree.collapse") : t("tree.expand")) : t("tree.select")} ${item.label}`}
      >
        {hasChildren ? (
          <ChevronRight
            className={cn(
              "size-3.5 shrink-0 text-muted-foreground transition-transform duration-200",
              isExpanded && "rotate-90",
              isLoading && "animate-spin"
            )}
            aria-hidden
          />
        ) : (
          <span className="w-3.5 shrink-0" />
        )}
        {item.icon ?? defaultIcon}
        <span className="truncate">{item.label}</span>
      </button>

      {hasChildren && isExpanded && resolvedChildren && resolvedChildren.length > 0 ? (
        <div role="group">
          {resolvedChildren.map((child) => (
            <TreeItem
              key={child.id}
              item={child}
              depth={depth + 1}
              selected={selected}
              expanded={expanded}
              onSelect={onSelect}
              onToggle={onToggle}
              loadingIds={loadingIds}
              childrenMap={childrenMap}
              focusedId={focusedId}
              buttonRefs={buttonRefs}
              onItemFocus={onItemFocus}
              onItemKeyDown={onItemKeyDown}
            />
          ))}
        </div>
      ) : null}
    </div>
  );
}

type TreeProps = {
  items: TreeItemData[];
  className?: string;
  selected?: string;
  expanded?: Set<string>;
  onSelect?: (item: TreeItemData) => void;
  onToggle?: (item: TreeItemData) => void;
  onLoadChildren?: (item: TreeItemData) => Promise<TreeItemData[]>;
};

interface VisibleTreeNode {
  item: TreeItemData;
  depth: number;
  parentId: string | null;
}

function Tree({ items, className, selected, expanded, onSelect, onToggle, onLoadChildren }: TreeProps) {
  const { t } = useTranslation();
  const [internalSelected, setInternalSelected] = useState<string | undefined>(selected);
  const [internalExpanded, setInternalExpanded] = useState<Set<string>>(expanded ?? new Set());
  const [loadingIds, setLoadingIds] = useState<Set<string>>(new Set());
  const [childrenMap, setChildrenMap] = useState<Map<string, TreeItemData[]>>(new Map());
  const currentSelected = selected !== undefined ? selected : internalSelected;
  const currentExpanded = expanded !== undefined ? expanded : internalExpanded;
  const visibleNodes = useMemo(
    () => flattenVisibleNodes(items, currentExpanded, childrenMap),
    [childrenMap, currentExpanded, items]
  );
  const parentMap = useMemo(() => buildParentMap(items, childrenMap), [childrenMap, items]);
  const previousParentMapRef = useRef(parentMap);
  const buttonRefs = useRef(new Map<string, HTMLButtonElement>());
  const [focusedId, setFocusedId] = useState<string | undefined>(() => {
    if (selected && visibleNodes.some((node) => node.item.id === selected)) return selected;
    return visibleNodes[0]?.item.id;
  });

  const focusNode = useCallback((id: string | undefined) => {
    if (id === undefined) return;
    setFocusedId(id);
    buttonRefs.current.get(id)?.focus();
  }, []);

  useEffect(() => {
    const visibleIds = new Set(visibleNodes.map((node) => node.item.id));
    if (focusedId !== undefined && !visibleIds.has(focusedId)) {
      let fallback = previousParentMapRef.current.get(focusedId) ?? null;
      while (fallback !== null && !visibleIds.has(fallback)) {
        fallback = previousParentMapRef.current.get(fallback) ?? null;
      }
      const target = fallback ?? visibleNodes[0]?.item.id;
      setFocusedId(target);
      queueMicrotask(() => {
        if (target !== undefined) buttonRefs.current.get(target)?.focus();
      });
    } else if (focusedId === undefined && visibleNodes.length > 0) {
      setFocusedId(visibleNodes[0]?.item.id);
    }
    previousParentMapRef.current = parentMap;
  }, [focusedId, parentMap, visibleNodes]);

  const handleSelect = useCallback(
    (item: TreeItemData) => {
      if (selected === undefined) setInternalSelected(item.id);
      onSelect?.(item);
    },
    [onSelect, selected]
  );

  const handleToggle = useCallback(
    async (item: TreeItemData) => {
      const willExpand = !currentExpanded.has(item.id);
      const cached = childrenMap.get(item.id);
      const hasInlineChildren = Boolean(item.children && item.children.length > 0);
      const needsLoad = willExpand && Boolean(onLoadChildren) && !hasInlineChildren && !cached;

      if (needsLoad && onLoadChildren) {
        setLoadingIds((previous) => new Set(previous).add(item.id));
        try {
          const children = await onLoadChildren(item);
          setChildrenMap((previous) => {
            const next = new Map(previous);
            next.set(item.id, children);
            return next;
          });
        } finally {
          setLoadingIds((previous) => {
            const next = new Set(previous);
            next.delete(item.id);
            return next;
          });
        }
      }

      if (expanded === undefined) {
        setInternalExpanded((previous) => {
          const next = new Set(previous);
          if (willExpand) next.add(item.id);
          else next.delete(item.id);
          return next;
        });
      }
      onToggle?.(item);
    },
    [childrenMap, currentExpanded, expanded, onLoadChildren, onToggle]
  );

  const handleItemKeyDown = useCallback(
    (event: KeyboardEvent<HTMLButtonElement>, item: TreeItemData) => {
      const index = visibleNodes.findIndex((node) => node.item.id === item.id);
      if (index === -1) return;
      const current = visibleNodes[index];
      const resolvedChildren = childrenMap.get(item.id) ?? item.children;
      const hasChildren = Boolean(item.isDir || (resolvedChildren && resolvedChildren.length > 0));

      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        if (hasChildren) void handleToggle(item);
        handleSelect(item);
        return;
      }
      if (event.key === "ArrowDown") {
        event.preventDefault();
        focusNode(visibleNodes[Math.min(index + 1, visibleNodes.length - 1)]?.item.id);
      } else if (event.key === "ArrowUp") {
        event.preventDefault();
        focusNode(visibleNodes[Math.max(index - 1, 0)]?.item.id);
      } else if (event.key === "Home") {
        event.preventDefault();
        focusNode(visibleNodes[0]?.item.id);
      } else if (event.key === "End") {
        event.preventDefault();
        focusNode(visibleNodes[visibleNodes.length - 1]?.item.id);
      } else if (event.key === "ArrowRight" && hasChildren) {
        event.preventDefault();
        if (!currentExpanded.has(item.id)) {
          void handleToggle(item);
        } else {
          focusNode(visibleNodes.find((node) => node.parentId === item.id)?.item.id);
        }
      } else if (event.key === "ArrowLeft") {
        if (hasChildren && currentExpanded.has(item.id)) {
          event.preventDefault();
          void handleToggle(item);
        } else if (current.parentId !== null) {
          event.preventDefault();
          focusNode(current.parentId);
        }
      }
    },
    [childrenMap, currentExpanded, focusNode, handleSelect, handleToggle, visibleNodes]
  );

  return (
    <div role="tree" className={cn("space-y-0.5", className)} aria-label={t("tree.treeViewLabel")}>
      {items.map((item) => (
        <TreeItem
          key={item.id}
          item={item}
          selected={currentSelected}
          expanded={currentExpanded}
          onSelect={handleSelect}
          onToggle={(target) => void handleToggle(target)}
          loadingIds={loadingIds}
          childrenMap={childrenMap}
          focusedId={focusedId}
          buttonRefs={buttonRefs.current}
          onItemFocus={setFocusedId}
          onItemKeyDown={handleItemKeyDown}
        />
      ))}
    </div>
  );
}

function flattenVisibleNodes(
  items: TreeItemData[],
  expanded: Set<string>,
  childrenMap: Map<string, TreeItemData[]>
): VisibleTreeNode[] {
  const result: VisibleTreeNode[] = [];
  const visit = (nodes: TreeItemData[], depth: number, parentId: string | null) => {
    for (const item of nodes) {
      result.push({ item, depth, parentId });
      const children = childrenMap.get(item.id) ?? item.children;
      if (expanded.has(item.id) && children && children.length > 0) visit(children, depth + 1, item.id);
    }
  };
  visit(items, 0, null);
  return result;
}

function buildParentMap(
  items: TreeItemData[],
  childrenMap: Map<string, TreeItemData[]>
): Map<string, string | null> {
  const result = new Map<string, string | null>();
  const visit = (nodes: TreeItemData[], parentId: string | null) => {
    for (const item of nodes) {
      result.set(item.id, parentId);
      const children = childrenMap.get(item.id) ?? item.children;
      if (children) visit(children, item.id);
    }
  };
  visit(items, null);
  return result;
}

export { Tree, TreeItem };
export type { TreeItemData, TreeItemProps, TreeProps };
