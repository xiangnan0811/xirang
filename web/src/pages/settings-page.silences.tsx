import { useCallback, useEffect, useState } from "react"
import { useTranslation } from "react-i18next"
import type { TFunction } from "i18next"
import { Plus, Trash2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { FormDialog } from "@/components/ui/form-dialog"
import { Input } from "@/components/ui/input"
import { Select } from "@/components/ui/select"
import { TagChips } from "@/components/ui/tag-chips"
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

// ---------- alert type catalogue ----------

const ALERT_TYPES = [
  { value: "XR-EXEC",        i18nKey: "silences.types.exec" },
  { value: "XR-VRFY",        i18nKey: "silences.types.vrfy" },
  { value: "XR-NODE",        i18nKey: "silences.types.node" },
  { value: "XR-NODE-EXPIRY", i18nKey: "silences.types.nodeExpiry" },
  { value: "XR-RETN",        i18nKey: "silences.types.retn" },
  { value: "XR-INTG",        i18nKey: "silences.types.intg" },
  { value: "XR-REPORT",      i18nKey: "silences.types.report" },
] as const

// ---------- helpers ----------

function describeMatch(s: Silence, t: TFunction): string {
  const parts: string[] = []
  if (s.match_node_id) parts.push(`#${s.match_node_id}`)
  if (s.match_category) {
    const type = ALERT_TYPES.find((a) => a.value === s.match_category)
    parts.push(type ? t(type.i18nKey) : s.match_category)
  }
  const tags = parseSilenceTags(s)
  if (tags.length) parts.push(tags.join(","))
  return parts.length ? parts.join(" · ") : t("silences.nodeAll")
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

function remaining(endAt: string, t: TFunction): string {
  const end = new Date(endAt)
  if (Number.isNaN(end.getTime())) return "—"
  const diffMs = end.getTime() - Date.now()
  if (diffMs <= 0) return t("silences.remaining.expired")
  const hours = Math.floor(diffMs / 3_600_000)
  if (hours < 1) {
    const minutes = Math.floor(diffMs / 60_000)
    return t("silences.remaining.minutes", { minutes: Math.max(minutes, 1) })
  }
  return t("silences.remaining.hours", { hours })
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
  const { t } = useTranslation()
  const [name, setName] = useState("")
  const [matchNodeId, setMatchNodeId] = useState("")
  const [matchCategory, setMatchCategory] = useState("")
  const [tags, setTags] = useState<string[]>([])
  const [startsAt, setStartsAt] = useState(() => nowPlusHours(0))
  const [endsAt, setEndsAt] = useState(() => nowPlusHours(1))
  const [note, setNote] = useState("")
  const [submitting, setSubmitting] = useState(false)

  const [nodes, setNodes] = useState<NodeRecord[]>([])

  useEffect(() => {
    if (open) {
      setName("")
      setMatchNodeId("")
      setMatchCategory("")
      setTags([])
      setStartsAt(nowPlusHours(0))
      setEndsAt(nowPlusHours(1))
      setNote("")

      // Fetch nodes for dropdown
      apiClient.getNodes(token).then(setNodes).catch(() => { /* silently ignore */ })
    }
  }, [open, token])

  const applyPreset = (hours: number) => {
    setStartsAt(nowPlusHours(0))
    setEndsAt(nowPlusHours(hours))
  }

  const handleSubmit = async () => {
    if (!name.trim()) {
      toast.error(t("silences.name"))
      return
    }
    if (new Date(endsAt) <= new Date(startsAt)) {
      toast.error(t("silences.validationWindowInvalid"))
      return
    }
    const input: SilenceInput = {
      name: name.trim(),
      match_node_id: matchNodeId ? Number(matchNodeId) : null,
      match_category: matchCategory,
      match_tags: tags,
      starts_at: new Date(startsAt).toISOString(),
      ends_at: new Date(endsAt).toISOString(),
      note: note.trim() || undefined,
    }
    setSubmitting(true)
    try {
      await createSilence(token, input)
      toast.success(t("silences.title"))
      onOpenChange(false)
      onCreated()
    } catch (err) {
      toast.error(getErrorMessage(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t("silences.new")}
      size="md"
      saving={submitting}
      onSubmit={handleSubmit}
      submitLabel={t("silences.create")}
      savingLabel={t("silences.creating")}
    >
      {/* 名称 */}
      <div className="space-y-1">
        <label htmlFor="silence-name" className="text-sm font-medium">
          {t("silences.name")}
        </label>
        <Input
          id="silence-name"
          aria-label={t("silences.name")}
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="维护窗口-A"
        />
      </div>

      {/* 节点 dropdown */}
      <div className="space-y-1">
        <label htmlFor="silence-node" className="text-sm font-medium">
          {t("silences.node")}
          <span className="ml-1 text-xs text-muted-foreground">({t("silences.nodeHint")})</span>
        </label>
        <Select
          id="silence-node"
          value={matchNodeId}
          onChange={(e) => setMatchNodeId(e.target.value)}
        >
          <option value="">{t("silences.nodeAll")}</option>
          {nodes.map((n) => (
            <option key={n.id} value={String(n.id)}>
              {n.name}
            </option>
          ))}
        </Select>
      </div>

      {/* 告警类型 Select */}
      <div className="space-y-1">
        <label htmlFor="silence-category" className="text-sm font-medium">
          {t("silences.category")}
          <span className="ml-1 text-xs text-muted-foreground">({t("silences.categoryHint")})</span>
        </label>
        <Select
          id="silence-category"
          value={matchCategory}
          onChange={(e) => setMatchCategory(e.target.value)}
        >
          <option value="">{t("silences.categoryAll")}</option>
          {ALERT_TYPES.map((type) => (
            <option key={type.value} value={type.value}>
              {t(type.i18nKey)}
            </option>
          ))}
        </Select>
      </div>

      {/* 标签 chip picker */}
      <div className="space-y-1">
        <label className="text-sm font-medium">{t("silences.tags")}</label>
        <TagChips
          value={tags}
          onChange={setTags}
          placeholder={t("silences.tagsHint")}
        />
      </div>

      {/* 静默窗口 */}
      <div className="space-y-1">
        <div className="flex items-center gap-2">
          <label className="text-sm font-medium">{t("silences.window")}</label>
          {[
            { label: t("silences.preset1h"), h: 1 },
            { label: t("silences.preset4h"), h: 4 },
            { label: t("silences.preset1d"), h: 24 },
          ].map((p) => (
            <Button key={p.h} size="sm" variant="outline" type="button" onClick={() => applyPreset(p.h)}>
              {p.label}
            </Button>
          ))}
        </div>
        <div className="grid grid-cols-2 gap-3 mt-2">
          <div className="space-y-1">
            <label htmlFor="silence-starts" className="text-xs text-muted-foreground">
              {t("silences.startsAt")}
            </label>
            <Input
              id="silence-starts"
              aria-label={t("silences.startsAt")}
              type="datetime-local"
              value={startsAt}
              onChange={(e) => setStartsAt(e.target.value)}
            />
          </div>
          <div className="space-y-1">
            <label htmlFor="silence-ends" className="text-xs text-muted-foreground">
              {t("silences.endsAt")}
            </label>
            <Input
              id="silence-ends"
              aria-label={t("silences.endsAt")}
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
          {t("silences.note")}
        </label>
        <Input
          id="silence-note"
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder={t("silences.noteHint")}
        />
      </div>
    </FormDialog>
  )
}

// ---------- SilencesPanel ----------

export function SilencesPanel() {
  const { t } = useTranslation()
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
      toast.success(t("silences.revoke"))
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
        <CardTitle className="text-base">{t("silences.title")}</CardTitle>
        <Button size="sm" onClick={() => setCreateOpen(true)}>
          <Plus className="mr-1 size-4" />
          {t("silences.new")}
        </Button>
      </CardHeader>
      <CardContent>
        {loading ? (
          <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
        ) : silences.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("silences.empty")}</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border text-left text-muted-foreground">
                  <th className="pb-2 pr-4 font-medium">{t("silences.columns.name")}</th>
                  <th className="pb-2 pr-4 font-medium">{t("silences.columns.match")}</th>
                  <th className="pb-2 pr-4 font-medium">{t("silences.columns.window")}</th>
                  <th className="pb-2 pr-4 font-medium">{t("silences.columns.remaining")}</th>
                  <th className="pb-2 font-medium">{t("silences.columns.actions")}</th>
                </tr>
              </thead>
              <tbody>
                {silences.map((s) => (
                  <tr key={s.id} className="border-b border-border/50 last:border-0">
                    <td className="py-2 pr-4 font-medium">{s.name}</td>
                    <td className="py-2 pr-4 text-muted-foreground">{describeMatch(s, t)}</td>
                    <td className="py-2 pr-4 text-muted-foreground whitespace-nowrap">
                      {formatWindow(s.starts_at, s.ends_at)}
                    </td>
                    <td className="py-2 pr-4 text-muted-foreground whitespace-nowrap">
                      {remaining(s.ends_at, t)}
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
                        {revoking === s.id ? t("common.loading") : t("silences.revoke")}
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
