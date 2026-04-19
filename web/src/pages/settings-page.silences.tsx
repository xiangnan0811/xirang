import { useCallback, useEffect, useRef, useState } from "react"
import { Plus, Trash2, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select } from "@/components/ui/select"
import { toast } from "@/components/ui/toast"
import { useAuth } from "@/context/auth-context"
import { apiClient } from "@/lib/api/client"
import {
  createSilence,
  deleteSilence,
  listSilences,
  parseSilenceTags,
  type Silence,
  type SilenceInput,
} from "@/lib/api/silences"
import { getErrorMessage } from "@/lib/utils"
import type { NodeRecord } from "@/types/domain"

// ---------- helpers ----------

function describeMatch(s: Silence): string {
  const tags = parseSilenceTags(s)
  const parts: string[] = []
  if (s.match_node_id) parts.push(`节点 ${s.match_node_id}`)
  else parts.push("全部节点")
  if (s.match_category) parts.push(s.match_category)
  if (tags.length) parts.push(`标签 ${tags.join(",")}`)
  return parts.join(" · ")
}

function formatWindow(start: string, end: string): string {
  const fmt = (iso: string) => {
    const d = new Date(iso)
    if (Number.isNaN(d.getTime())) return iso
    const pad = (n: number) => n.toString().padStart(2, "0")
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
  }
  return `${fmt(start)} → ${fmt(end)}`
}

function remaining(endAt: string): string {
  const end = new Date(endAt)
  if (Number.isNaN(end.getTime())) return "—"
  const diffMs = end.getTime() - Date.now()
  if (diffMs <= 0) return "已过期"
  const hours = Math.floor(diffMs / 3_600_000)
  if (hours < 1) return "剩余 < 1 小时"
  return `剩余 ${hours} 小时`
}

// Static alert error-code prefixes as datalist hints
const ALERT_CODE_HINTS = [
  "XR-EXEC-",
  "XR-VRFY-",
  "XR-NODE-",
  "XR-NODE-EXPIRY-",
  "XR-RETN-",
  "XR-INTG-",
  "XR-REPORT-",
]

// ---------- CreateSilenceDialog ----------

type CreateSilenceDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: () => void
  token: string
}

function nowPlusHours(h: number): string {
  return new Date(Date.now() + h * 3_600_000).toISOString().slice(0, 16)
}

function CreateSilenceDialog({ open, onOpenChange, onCreated, token }: CreateSilenceDialogProps) {
  const [name, setName] = useState("")
  const [matchNodeId, setMatchNodeId] = useState("")
  const [matchCategory, setMatchCategory] = useState("")
  const [tags, setTags] = useState<string[]>([])
  const [tagInput, setTagInput] = useState("")
  const [startsAt, setStartsAt] = useState(() => nowPlusHours(0))
  const [endsAt, setEndsAt] = useState(() => nowPlusHours(1))
  const [note, setNote] = useState("")
  const [submitting, setSubmitting] = useState(false)

  const [nodes, setNodes] = useState<NodeRecord[]>([])
  const [recentCodes, setRecentCodes] = useState<string[]>([])
  const tagInputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (open) {
      setName("")
      setMatchNodeId("")
      setMatchCategory("")
      setTags([])
      setTagInput("")
      setStartsAt(nowPlusHours(0))
      setEndsAt(nowPlusHours(1))
      setNote("")

      // Fetch nodes for dropdown
      apiClient.getNodes(token).then(setNodes).catch(() => { /* silently ignore */ })

      // Fetch recent alert error codes for datalist
      apiClient.getAlerts(token).then((alerts) => {
        const codes = Array.from(new Set(alerts.map((a) => a.errorCode).filter(Boolean)))
        setRecentCodes(codes.slice(0, 20))
      }).catch(() => { /* silently ignore; fall back to static hints */ })
    }
  }, [open, token])

  const applyPreset = (hours: number) => {
    setStartsAt(nowPlusHours(0))
    setEndsAt(nowPlusHours(hours))
  }

  const addTag = () => {
    const v = tagInput.trim()
    if (!v || tags.includes(v)) {
      setTagInput("")
      return
    }
    setTags([...tags, v])
    setTagInput("")
  }

  const removeTag = (t: string) => {
    setTags(tags.filter((x) => x !== t))
  }

  const handleTagKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault()
      addTag()
    }
  }

  const handleSubmit = async () => {
    if (!name.trim()) {
      toast.error("请填写名称")
      return
    }
    if (new Date(endsAt) <= new Date(startsAt)) {
      toast.error("结束时间必须晚于开始时间")
      return
    }
    const input: SilenceInput = {
      name: name.trim(),
      match_node_id: matchNodeId ? Number(matchNodeId) : null,
      match_category: matchCategory.trim(),
      match_tags: tags,
      starts_at: new Date(startsAt).toISOString(),
      ends_at: new Date(endsAt).toISOString(),
      note: note.trim() || undefined,
    }
    setSubmitting(true)
    try {
      await createSilence(token, input)
      toast.success("静默规则已创建")
      onOpenChange(false)
      onCreated()
    } catch (err) {
      toast.error(getErrorMessage(err))
    } finally {
      setSubmitting(false)
    }
  }

  // Datalist options: prefer fetched recent codes, fall back to static prefixes
  const datalistOptions = recentCodes.length > 0 ? recentCodes : ALERT_CODE_HINTS

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="md">
        <DialogHeader>
          <DialogTitle>新建静默规则</DialogTitle>
        </DialogHeader>
        <div className="space-y-3 py-2">
          {/* 名称 */}
          <div className="space-y-1">
            <label htmlFor="silence-name" className="text-sm font-medium">
              名称
            </label>
            <Input
              id="silence-name"
              aria-label="名称"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="维护窗口-A"
            />
          </div>

          {/* 节点 dropdown */}
          <div className="space-y-1">
            <label htmlFor="silence-node" className="text-sm font-medium">
              节点（留空表示全部节点）
            </label>
            <Select
              id="silence-node"
              value={matchNodeId}
              onChange={(e) => setMatchNodeId(e.target.value)}
            >
              <option value="">全部节点</option>
              {nodes.map((n) => (
                <option key={n.id} value={String(n.id)}>
                  {n.name}
                </option>
              ))}
            </Select>
          </div>

          {/* 告警 ErrorCode */}
          <div className="space-y-1">
            <label htmlFor="silence-category" className="text-sm font-medium">
              告警 ErrorCode（留空匹配全部）
            </label>
            <input
              id="silence-category"
              list="alert-categories"
              value={matchCategory}
              onChange={(e) => setMatchCategory(e.target.value)}
              placeholder="如 XR-NODE-5 或留空匹配全部"
              className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
            />
            <datalist id="alert-categories">
              {datalistOptions.map((code) => (
                <option key={code} value={code} />
              ))}
            </datalist>
          </div>

          {/* 标签 chip picker */}
          <div className="space-y-1">
            <label className="text-sm font-medium">标签</label>
            {tags.length > 0 && (
              <div className="flex flex-wrap gap-1.5 pb-1">
                {tags.map((t) => (
                  <span
                    key={t}
                    className="inline-flex items-center gap-1 rounded-full border border-border bg-muted px-2.5 py-[3px] text-xs font-medium text-foreground"
                  >
                    {t}
                    <button
                      type="button"
                      aria-label={`移除标签 ${t}`}
                      onClick={() => removeTag(t)}
                      className="ml-0.5 rounded-full text-muted-foreground hover:text-foreground focus-visible:outline-none"
                    >
                      <X className="size-3" />
                    </button>
                  </span>
                ))}
              </div>
            )}
            <div className="flex gap-2">
              <Input
                ref={tagInputRef}
                value={tagInput}
                onChange={(e) => setTagInput(e.target.value)}
                onKeyDown={handleTagKeyDown}
                placeholder="输入标签后按 Enter 或点击添加"
              />
              <Button type="button" variant="outline" size="sm" onClick={addTag}>
                添加
              </Button>
            </div>
          </div>

          {/* 静默窗口 */}
          <div className="space-y-1">
            <div className="flex items-center gap-2">
              <label className="text-sm font-medium">静默窗口</label>
              {[{ label: "1 小时", h: 1 }, { label: "4 小时", h: 4 }, { label: "1 天", h: 24 }].map((p) => (
                <Button key={p.h} size="sm" variant="outline" type="button" onClick={() => applyPreset(p.h)}>
                  {p.label}
                </Button>
              ))}
            </div>
            <div className="flex gap-2">
              <div className="flex-1 space-y-1">
                <label htmlFor="silence-starts" className="text-xs text-muted-foreground">开始</label>
                <Input
                  id="silence-starts"
                  aria-label="开始"
                  type="datetime-local"
                  value={startsAt}
                  onChange={(e) => setStartsAt(e.target.value)}
                />
              </div>
              <div className="flex-1 space-y-1">
                <label htmlFor="silence-ends" className="text-xs text-muted-foreground">结束</label>
                <Input
                  id="silence-ends"
                  aria-label="结束"
                  type="datetime-local"
                  value={endsAt}
                  onChange={(e) => setEndsAt(e.target.value)}
                />
              </div>
            </div>
          </div>

          {/* 备注 */}
          <div className="space-y-1">
            <label htmlFor="silence-note" className="text-sm font-medium">
              备注
            </label>
            <Input
              id="silence-note"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder="可选备注"
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={submitting}>
            取消
          </Button>
          <Button onClick={() => void handleSubmit()} disabled={submitting}>
            {submitting ? "创建中…" : "创建"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ---------- SilencesPanel ----------

export function SilencesPanel() {
  const { token } = useAuth()
  const [silences, setSilences] = useState<Silence[]>([])
  const [loading, setLoading] = useState(true)
  const [createOpen, setCreateOpen] = useState(false)
  const [revoking, setRevoking] = useState<number | null>(null)

  const refresh = useCallback(() => {
    if (!token) return
    setLoading(true)
    listSilences(token)
      .then(setSilences)
      .catch((err) => toast.error(getErrorMessage(err)))
      .finally(() => setLoading(false))
  }, [token])

  useEffect(() => {
    refresh()
  }, [refresh])

  const handleRevoke = async (id: number) => {
    if (!token) return
    setRevoking(id)
    try {
      await deleteSilence(token, id)
      toast.success("静默规则已删除")
      refresh()
    } catch (err) {
      toast.error(getErrorMessage(err))
    } finally {
      setRevoking(null)
    }
  }

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-base">静默规则</CardTitle>
        <Button size="sm" onClick={() => setCreateOpen(true)}>
          <Plus className="mr-1 size-4" />
          新建静默规则
        </Button>
      </CardHeader>
      <CardContent>
        {loading ? (
          <p className="text-sm text-muted-foreground">加载中…</p>
        ) : silences.length === 0 ? (
          <p className="text-sm text-muted-foreground">暂无静默规则</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border text-left text-muted-foreground">
                  <th className="pb-2 pr-4 font-medium">名称</th>
                  <th className="pb-2 pr-4 font-medium">匹配</th>
                  <th className="pb-2 pr-4 font-medium">窗口</th>
                  <th className="pb-2 pr-4 font-medium">剩余</th>
                  <th className="pb-2 font-medium">操作</th>
                </tr>
              </thead>
              <tbody>
                {silences.map((s) => (
                  <tr key={s.id} className="border-b border-border/50 last:border-0">
                    <td className="py-2 pr-4 font-medium">{s.name}</td>
                    <td className="py-2 pr-4 text-muted-foreground">{describeMatch(s)}</td>
                    <td className="py-2 pr-4 text-muted-foreground whitespace-nowrap">
                      {formatWindow(s.starts_at, s.ends_at)}
                    </td>
                    <td className="py-2 pr-4 text-muted-foreground whitespace-nowrap">
                      {remaining(s.ends_at)}
                    </td>
                    <td className="py-2">
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={revoking === s.id}
                        onClick={() => void handleRevoke(s.id)}
                        aria-label={`删除静默规则 ${s.name}`}
                      >
                        <Trash2 className="size-4" />
                        {revoking === s.id ? "删除中…" : "立即结束"}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>

      {token && (
        <CreateSilenceDialog
          open={createOpen}
          onOpenChange={setCreateOpen}
          onCreated={refresh}
          token={token}
        />
      )}
    </Card>
  )
}
