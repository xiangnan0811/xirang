import { useCallback, useEffect, useState } from "react"
import { Plus, Trash2 } from "lucide-react"
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
import { toast } from "@/components/ui/toast"
import { useAuth } from "@/context/auth-context"
import {
  createSilence,
  deleteSilence,
  listSilences,
  parseSilenceTags,
  type Silence,
  type SilenceInput,
} from "@/lib/api/silences"
import { getErrorMessage } from "@/lib/utils"

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
  const [matchTags, setMatchTags] = useState("")
  const [startsAt, setStartsAt] = useState(() => nowPlusHours(0))
  const [endsAt, setEndsAt] = useState(() => nowPlusHours(1))
  const [note, setNote] = useState("")
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    if (open) {
      setName("")
      setMatchNodeId("")
      setMatchCategory("")
      setMatchTags("")
      setStartsAt(nowPlusHours(0))
      setEndsAt(nowPlusHours(1))
      setNote("")
    }
  }, [open])

  const applyPreset = (hours: number) => {
    setStartsAt(nowPlusHours(0))
    setEndsAt(nowPlusHours(hours))
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
      match_node_id: matchNodeId.trim() ? Number(matchNodeId.trim()) : null,
      match_category: matchCategory.trim(),
      match_tags: matchTags.trim() ? matchTags.split(",").map((t) => t.trim()).filter(Boolean) : [],
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

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="md">
        <DialogHeader>
          <DialogTitle>新建静默规则</DialogTitle>
        </DialogHeader>
        <div className="space-y-3 py-2">
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
          <div className="space-y-1">
            <label htmlFor="silence-node" className="text-sm font-medium">
              节点 ID（留空表示全部节点）
            </label>
            <Input
              id="silence-node"
              value={matchNodeId}
              onChange={(e) => setMatchNodeId(e.target.value)}
              placeholder="1"
              type="number"
            />
          </div>
          <div className="space-y-1">
            <label htmlFor="silence-category" className="text-sm font-medium">
              告警类别（留空匹配全部）
            </label>
            <Input
              id="silence-category"
              value={matchCategory}
              onChange={(e) => setMatchCategory(e.target.value)}
              placeholder="backup_failed"
            />
          </div>
          <div className="space-y-1">
            <label htmlFor="silence-tags" className="text-sm font-medium">
              标签（逗号分隔）
            </label>
            <Input
              id="silence-tags"
              value={matchTags}
              onChange={(e) => setMatchTags(e.target.value)}
              placeholder="prod,web"
            />
          </div>
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
                  type="datetime-local"
                  value={startsAt}
                  onChange={(e) => setStartsAt(e.target.value)}
                />
              </div>
              <div className="flex-1 space-y-1">
                <label htmlFor="silence-ends" className="text-xs text-muted-foreground">结束</label>
                <Input
                  id="silence-ends"
                  type="datetime-local"
                  value={endsAt}
                  onChange={(e) => setEndsAt(e.target.value)}
                />
              </div>
            </div>
          </div>
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
