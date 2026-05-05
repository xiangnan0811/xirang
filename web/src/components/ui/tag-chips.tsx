import { useState, type KeyboardEvent } from "react";
import { X } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";

export type TagChipsProps = {
  value: string[];
  onChange: (next: string[]) => void;
  placeholder?: string;
  "aria-labelledby"?: string;
};

export function TagChips({ value, onChange, placeholder, ...aria }: TagChipsProps) {
  const [draft, setDraft] = useState("");

  const addTag = () => {
    const v = draft.trim();
    if (!v || value.includes(v)) return;
    onChange([...value, v]);
    setDraft("");
  };

  const removeTag = (tag: string) => {
    onChange(value.filter((t) => t !== tag));
  };

  const handleKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault();
      addTag();
    }
  };

  return (
    <div className="space-y-2">
      {value.length > 0 && (
        <div className="flex flex-wrap gap-1.5 pb-1">
          {value.map((tag) => (
            <span
              key={tag}
              className="inline-flex items-center gap-1 rounded-full border border-border bg-muted px-2.5 py-[3px] text-xs font-medium text-foreground"
            >
              {tag}
              <button
                type="button"
                aria-label={`移除标签 ${tag}`}
                className="ml-0.5 rounded-full text-muted-foreground hover:text-foreground focus-visible:outline-none"
                onClick={() => removeTag(tag)}
              >
                <X className="size-3" aria-hidden="true" />
              </button>
            </span>
          ))}
        </div>
      )}
      <div className="flex gap-2">
        <Input
          type="text"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder={placeholder}
          aria-labelledby={aria["aria-labelledby"]}
        />
        <Button type="button" variant="outline" size="sm" onClick={addTag}>
          添加
        </Button>
      </div>
    </div>
  );
}
