import { useCallback, useEffect, useRef, useState } from "react"
import type { RefObject } from "react"
import { useTranslation } from "react-i18next"
import type { TFunction } from "i18next"
import { Plus, Trash2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { FormDialog } from "@/components/ui/form-dialog"
import { Input } from "@/components/ui/input"
import { Select } from "@/components/ui/select"
import { TagChips } from "@/components/ui/tag-chips"
import { toast } from "@/components/ui/toast-sonner"
import { useAuth } from "@/context/auth-context.hooks"
import { apiClient } from "@/lib/api/client"
import {
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
  { value: "XR-SLO",         i18nKey: "silences.types.slo" },
] as const

const DATETIME_LOCAL_PATTERN = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/

// ---------- helpers ----------

function describeMatch(s: Silence, t: TFunction): string {
  const parts: string[] = []
  if (s.matchNodeId) parts.push(`#${s.matchNodeId}`)
  if (s.matchCategory) {
    const type = ALERT_TYPES.find((a) => a.value === s.matchCategory)
    parts.push(type ? t(type.i18nKey) : s.matchCategory)
  }
  const tags = s.matchTags.length ? s.matchTags : parseSilenceTags(s)
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

function pad2(value: number): string {
  return value.toString().padStart(2, "0")
}

function formatDatetimeLocal(date: Date): string {
  return `${date.getFullYear().toString().padStart(4, "0")}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())}T${pad2(date.getHours())}:${pad2(date.getMinutes())}`
}

function formatSignedOffset(date: Date): string {
  const totalMinutes = -date.getTimezoneOffset()
  const sign = totalMinutes >= 0 ? "+" : "-"
  const absolute = Math.abs(totalMinutes)
  return `${sign}${pad2(Math.floor(absolute / 60))}:${pad2(absolute % 60)}`
}

function floorToMinute(date: Date): Date {
  return new Date(Math.floor(date.getTime() / 60_000) * 60_000)
}

/** Strict YYYY-MM-DDTHH:mm. Round-trip rejects impossible dates and spring-forward gaps. */
function parseDatetimeLocal(value: string): Date | null {
  const match = DATETIME_LOCAL_PATTERN.exec(value)
  if (!match) return null
  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const hour = Number(match[4])
  const minute = Number(match[5])
  if (month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59) return null
  const parsed = new Date(value)
  if (
    parsed.getFullYear() !== year ||
    parsed.getMonth() !== month - 1 ||
    parsed.getDate() !== day ||
    parsed.getHours() !== hour ||
    parsed.getMinutes() !== minute
  ) {
    return null
  }
  return parsed
}

type SilenceWindowEnd = {
  text: string
  /** Preset instant. Cleared on edit so a repeated clock time is not reparsed into the other DST occurrence. */
  instant: Date | null
}

type SilenceWindowState = {
  start: SilenceWindowEnd
  end: SilenceWindowEnd
}

type SilenceFieldErrors = {
  name?: string
  start?: string
  end?: string
}

function windowFromNow(now: Date, hours: number): SilenceWindowState {
  const start = floorToMinute(now)
  const end = new Date(start.getTime() + hours * 3_600_000)
  return {
    start: { text: formatDatetimeLocal(start), instant: start },
    end: { text: formatDatetimeLocal(end), instant: end },
  }
}

function resolveWindowEnd(end: SilenceWindowEnd): Date | null {
  return end.instant ?? parseDatetimeLocal(end.text)
}

function formatWindowSummary(start: Date, end: Date, t: TFunction): string {
  const point = (date: Date) =>
    `${formatDatetimeLocal(date)} ${t("silences.utcOffset", { offset: formatSignedOffset(date) })}`
  return `${point(start)} → ${point(end)}`
}

function SilenceWindowField({
  id,
  label,
  end,
  error,
  inputRef,
  onValueChange,
}: {
  id: string
  label: string
  end: SilenceWindowEnd
  error?: string
  inputRef: RefObject<HTMLInputElement>
  onValueChange: (value: string) => void
}) {
  const { t } = useTranslation()
  const instant = resolveWindowEnd(end)
  const offset = instant ? t("silences.utcOffset", { offset: formatSignedOffset(instant) }) : null
  const offsetId = offset ? `${id}-offset` : undefined
  const errorId = error ? `${id}-error` : undefined
  const describedBy = [offsetId, errorId].filter((item): item is string => Boolean(item)).join(" ")

  return (
    <div className="min-w-0 space-y-1">
      <div className="flex items-baseline justify-between gap-2">
        <label htmlFor={id} className="text-xs text-muted-foreground">
          {label}
        </label>
        {offset ? (
          <span id={offsetId} className="text-xs font-medium tabular-nums text-foreground">
            {offset}
          </span>
        ) : null}
      </div>
      <Input
        ref={inputRef}
        id={id}
        aria-label={label}
        aria-invalid={Boolean(error)}
        aria-describedby={describedBy || undefined}
        type="datetime-local"
        value={end.text}
        onChange={(event) => onValueChange(event.target.value)}
      />
      {error ? (
        <p id={errorId} role="alert" className="text-xs text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  )
}

// ---------- CreateSilenceDialog ----------

type CreateSilenceDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: () => void
  token: string
}

function CreateSilenceDialog({ open, onOpenChange, onCreated, token }: CreateSilenceDialogProps) {
  const { t } = useTranslation()
  const [name, setName] = useState("")
  const [matchNodeId, setMatchNodeId] = useState("")
  const [matchCategory, setMatchCategory] = useState("")
  const [tags, setTags] = useState<string[]>([])
  const [silenceWindow, setSilenceWindow] = useState(() => windowFromNow(new Date(), 1))
  const [note, setNote] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [fieldErrors, setFieldErrors] = useState<SilenceFieldErrors>({})
  const nameRef = useRef<HTMLInputElement>(null)
  const startRef = useRef<HTMLInputElement>(null)
  const endRef = useRef<HTMLInputElement>(null)

  const [nodes, setNodes] = useState<NodeRecord[]>([])

  useEffect(() => {
    let cancelled = false;
    apiClient.getNodes(token).then((data) => {
      if (!cancelled) setNodes(data);
    }).catch(() => { /* silently ignore */ });
    return () => { cancelled = true; };
  }, [token]);

  const applyPreset = (hours: number) => {
    setSilenceWindow(windowFromNow(new Date(), hours))
    setFieldErrors((current) => (
      current.start || current.end ? { ...current, start: undefined, end: undefined } : current
    ))
  }

  const editWindowEnd = (which: "start" | "end", text: string) => {
    if (silenceWindow[which].text === text) return
    setSilenceWindow((current) => ({
      ...current,
      [which]: { text, instant: null },
    }))
    setFieldErrors((current) => (
      current.start || current.end ? { ...current, start: undefined, end: undefined } : current
    ))
  }

  const handleSubmit = async () => {
    const startInstant = resolveWindowEnd(silenceWindow.start)
    const endInstant = resolveWindowEnd(silenceWindow.end)
    const nextErrors: SilenceFieldErrors = {}
    if (!name.trim()) nextErrors.name = t("silences.nameRequired")
    if (!startInstant) nextErrors.start = t("silences.validationDateInvalid")
    if (!endInstant) nextErrors.end = t("silences.validationDateInvalid")
    if (startInstant && endInstant && endInstant.getTime() <= startInstant.getTime()) {
      nextErrors.end = t("silences.validationWindowInvalid")
    }
    if (nextErrors.name || nextErrors.start || nextErrors.end || !startInstant || !endInstant) {
      setFieldErrors(nextErrors)
      if (nextErrors.name) nameRef.current?.focus()
      else if (nextErrors.start) startRef.current?.focus()
      else if (nextErrors.end) endRef.current?.focus()
      return
    }

    const input: SilenceInput = {
      name: name.trim(),
      matchNodeId: matchNodeId ? Number(matchNodeId) : null,
      matchCategory,
      matchTags: tags,
      startsAt: startInstant.toISOString(),
      endsAt: endInstant.toISOString(),
      note: note.trim() || undefined,
    }
    setSubmitting(true)
    try {
      await apiClient.createSilence(token, input)
      toast.success(t("silences.created", { window: formatWindowSummary(startInstant, endInstant, t) }))
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
      description={t("silences.dialogDesc")}
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
          ref={nameRef}
          id="silence-name"
          aria-label={t("silences.name")}
          aria-invalid={Boolean(fieldErrors.name)}
          aria-describedby={fieldErrors.name ? "silence-name-error" : undefined}
          value={name}
          onChange={(e) => {
            setName(e.target.value)
            setFieldErrors((current) => (current.name ? { ...current, name: undefined } : current))
          }}
          placeholder="维护窗口-A"
        />
        {fieldErrors.name ? (
          <p id="silence-name-error" role="alert" className="text-xs text-destructive">
            {fieldErrors.name}
          </p>
        ) : null}
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
        <div className="flex flex-wrap items-center gap-2">
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
        <p className="text-xs text-muted-foreground">{t("silences.presetDurationHint")}</p>
        <div className="mt-2 grid grid-cols-1 gap-3 sm:grid-cols-2">
          <SilenceWindowField
            id="silence-starts"
            label={t("silences.startsAt")}
            end={silenceWindow.start}
            error={fieldErrors.start}
            inputRef={startRef}
            onValueChange={(value) => editWindowEnd("start", value)}
          />
          <SilenceWindowField
            id="silence-ends"
            label={t("silences.endsAt")}
            end={silenceWindow.end}
            error={fieldErrors.end}
            inputRef={endRef}
            onValueChange={(value) => editWindowEnd("end", value)}
          />
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
  const { token } = useAuth();
  return <SilencesPanelContent key={token ?? ""} />;
}

function SilencesPanelContent() {
  const { t } = useTranslation()
  const { token } = useAuth()
  const [silences, setSilences] = useState<Silence[]>([])
  const [loading, setLoading] = useState(Boolean(token))
  const [createOpen, setCreateOpen] = useState(false)
  const [revoking, setRevoking] = useState<number | null>(null)

  const [requestVersion, setRequestVersion] = useState(0);
  const refresh = useCallback(() => {
    if (!token) return;
    setLoading(true);
    setRequestVersion((version) => version + 1);
  }, [token]);

  useEffect(() => {
    if (!token) return;
    const controller = new AbortController();
    apiClient.listSilences(token)
      .then((data) => {
        if (!controller.signal.aborted) setSilences(data);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) toast.error(getErrorMessage(error));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [token, requestVersion]);

  const handleRevoke = async (id: number) => {
    if (!token) return
    setRevoking(id)
    try {
      await apiClient.deleteSilence(token, id)
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
                      {formatWindow(s.startsAt, s.endsAt)}
                    </td>
                    <td className="py-2 pr-4 text-muted-foreground whitespace-nowrap">
                      {remaining(s.endsAt, t)}
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

      {token && createOpen && (
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
