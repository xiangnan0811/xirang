import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import type { TFunction } from "i18next"
import { Plus, Trash2 } from "lucide-react"
import { CreateSilenceDialog } from "@/components/create-silence-dialog"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { toast } from "@/components/ui/toast-sonner"
import { useAuth } from "@/context/auth-context.hooks"
import { apiClient } from "@/lib/api/client"
import {
  getAuthIdentitySnapshot,
  getAuthSessionGeneration,
  isAuthTransitionActive,
  subscribeAuthTransition,
} from "@/lib/api/core"
import {
  parseSilenceTags,
  type Silence,
} from "@/lib/api/silences"
import { SILENCE_CATEGORIES } from "@/lib/silence-categories"
import { getErrorMessage } from "@/lib/utils"

function describeMatch(s: Silence, t: TFunction): string {
  const parts: string[] = []
  if (s.matchNodeId) parts.push(`#${s.matchNodeId}`)
  if (s.matchCategory) {
    const type = SILENCE_CATEGORIES.find((item) => item.value === s.matchCategory)
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

function isAbortError(error: unknown): boolean {
  if (error instanceof DOMException && error.name === "AbortError") return true
  return error instanceof Error && error.name === "AbortError"
}

type SilenceListOwner = {
  localGeneration: number
  authGeneration: number
  token: string
}

export function SilencesPanel() {
  const { token, role } = useAuth()
  return <SilencesPanelContent key={`${token ?? ""}\0${role ?? ""}`} />
}

function SilencesPanelContent() {
  const { t } = useTranslation()
  const { token, role } = useAuth()
  const [silences, setSilences] = useState<Silence[]>([])
  const [loaded, setLoaded] = useState(false)
  const [loading, setLoading] = useState(role === "admin" && Boolean(token))
  const [listFailed, setListFailed] = useState(false)
  const [listErrorDetail, setListErrorDetail] = useState<string | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [revoking, setRevoking] = useState<number | null>(null)
  const [listNonce, setListNonce] = useState(0)
  const mountedRef = useRef(false)
  const localGenerationRef = useRef(0)
  const listAttemptRef = useRef(0)
  const listAbortRef = useRef<AbortController | null>(null)
  const revokeAttemptRef = useRef(0)
  // Released only by the owned finally or layout/transition cleanup.
  const revokeLockRef = useRef(false)
  const loadedRef = useRef(false)
  const identityRef = useRef({ token, role })

  const canManage = role === "admin" && typeof token === "string" && token.length > 0

  /**
   * listSilences receives a signal, but a settled response is applied only when
   * the attempt, this mount generation, and the admin identity still match the
   * values captured before the request. A late response can ignore cancellation.
   */
  const captureOwner = (): SilenceListOwner | null => {
    if (!mountedRef.current) return null
    if (isAuthTransitionActive()) return null
    const currentToken = identityRef.current.token
    const currentRole = identityRef.current.role
    if (currentRole !== "admin" || !currentToken) return null
    const snapshot = getAuthIdentitySnapshot()
    if (snapshot.token !== currentToken || snapshot.role !== "admin") return null
    return {
      localGeneration: localGenerationRef.current,
      authGeneration: getAuthSessionGeneration(),
      token: currentToken,
    }
  }

  const ownerCurrent = (owner: SilenceListOwner): boolean => {
    if (!mountedRef.current) return false
    if (localGenerationRef.current !== owner.localGeneration) return false
    if (isAuthTransitionActive()) return false
    if (getAuthSessionGeneration() !== owner.authGeneration) return false
    if (identityRef.current.token !== owner.token || identityRef.current.role !== "admin") return false
    const snapshot = getAuthIdentitySnapshot()
    return snapshot.token === owner.token && snapshot.role === "admin"
  }


  const requestList = useCallback(() => {
    if (!mountedRef.current || isAuthTransitionActive()) return
    const currentToken = identityRef.current.token
    const currentRole = identityRef.current.role
    if (currentRole !== "admin" || !currentToken) return
    const snapshot = getAuthIdentitySnapshot()
    if (snapshot.token !== currentToken || snapshot.role !== "admin") return
    setLoading(true)
    setListNonce((version) => version + 1)
  }, [])

  useLayoutEffect(() => {
    mountedRef.current = true
    identityRef.current = { token, role }
    localGenerationRef.current += 1
    return () => {
      mountedRef.current = false
      localGenerationRef.current += 1
      listAttemptRef.current += 1
      revokeAttemptRef.current += 1
      revokeLockRef.current = false
      listAbortRef.current?.abort()
      listAbortRef.current = null
    }
  }, [token, role])


  useLayoutEffect(() => {
    const retire = () => {
      localGenerationRef.current += 1
      listAttemptRef.current += 1
      revokeAttemptRef.current += 1
      revokeLockRef.current = false
      listAbortRef.current?.abort()
      listAbortRef.current = null
      setCreateOpen(false)
      setRevoking(null)
      setLoading(false)
      if (!loadedRef.current) setListFailed(true)
    }
    if (isAuthTransitionActive()) retire()
    return subscribeAuthTransition(() => {
      if (isAuthTransitionActive()) {
        retire()
        return
      }
      requestList()
    })
  }, [requestList])

  useEffect(() => {
    if (role !== "admin" || !token) return
    const owner = captureOwner()
    if (!owner) return
    const attempt = ++listAttemptRef.current
    const controller = new AbortController()
    listAbortRef.current = controller
    void apiClient.listSilences(owner.token, false, { signal: controller.signal })
      .then((data) => {
        if (listAttemptRef.current !== attempt || !ownerCurrent(owner)) return
        loadedRef.current = true
        setSilences(data)
        setLoaded(true)
        setListFailed(false)
        setListErrorDetail(null)
      })
      .catch((error: unknown) => {
        if (listAttemptRef.current !== attempt || !ownerCurrent(owner)) return
        if (isAbortError(error)) return
        const message = getErrorMessage(error)
        if (listAttemptRef.current !== attempt || !ownerCurrent(owner)) return
        setListFailed(true)
        setListErrorDetail(message)
        if (listAttemptRef.current !== attempt || !ownerCurrent(owner)) return
        toast.error(message)
      })
      .finally(() => {
        if (listAttemptRef.current !== attempt) return
        if (!ownerCurrent(owner)) return
        setLoading(false)
      })
    return () => {
      controller.abort()
      if (listAbortRef.current === controller) listAbortRef.current = null
    }
    // Owner capture reads refs that the layout effects above maintain.
  }, [token, role, listNonce])

  const handleRevoke = async (id: number) => {
    if (revokeLockRef.current) return
    const owner = captureOwner()
    if (!owner) return
    revokeLockRef.current = true
    const attempt = ++revokeAttemptRef.current
    setRevoking(id)
    try {
      if (revokeAttemptRef.current !== attempt || !ownerCurrent(owner)) return
      await apiClient.deleteSilence(owner.token, id)
      if (revokeAttemptRef.current !== attempt || !ownerCurrent(owner)) return
      toast.success(t("silences.revoke"))
      if (revokeAttemptRef.current !== attempt || !ownerCurrent(owner)) return
      requestList()
    } catch (error: unknown) {
      if (revokeAttemptRef.current !== attempt || !ownerCurrent(owner)) return
      toast.error(getErrorMessage(error))
    } finally {
      if (revokeAttemptRef.current === attempt && ownerCurrent(owner)) {
        revokeLockRef.current = false
        setRevoking((current) => (current === id ? null : current))
      }
    }
  }

  const showInitialLoading = canManage && !loaded && (loading || !listFailed)

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-base">{t("silences.title")}</CardTitle>
        {canManage ? (
          <Button
            size="sm"
            onClick={() => {
              if (!captureOwner()) return
              setCreateOpen(true)
            }}
          >
            <Plus className="mr-1 size-4" />
            {t("silences.new")}
          </Button>
        ) : null}
      </CardHeader>
      <CardContent>
        {canManage ? (
          showInitialLoading ? (
            <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
          ) : (
            <div className="space-y-3">
              {listFailed ? (
                <div
                  role="alert"
                  className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
                >
                  <div className="min-w-0 space-y-1">
                    <p>{t("silences.loadFailed")}</p>
                    {listErrorDetail ? <p className="break-words text-xs">{listErrorDetail}</p> : null}
                  </div>
                  <Button type="button" size="sm" variant="outline" onClick={requestList}>
                    {t("silences.loadRetry")}
                  </Button>
                </div>
              ) : null}
              {loading && loaded ? (
                <p className="text-xs text-muted-foreground">{t("common.loading")}</p>
              ) : null}
              {loaded && silences.length === 0 ? (
                <p className="text-sm text-muted-foreground">{t("silences.empty")}</p>
              ) : silences.length > 0 ? (
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
                              disabled={revoking !== null}
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
              ) : null}
            </div>
          )
        ) : null}
      </CardContent>

      {canManage && token && createOpen ? (
        <CreateSilenceDialog
          open={createOpen}
          onOpenChange={setCreateOpen}
          onCreated={requestList}
          token={token}
        />
      ) : null}
    </Card>
  )
}
