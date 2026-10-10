import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { DialogCloseAutoFocus } from "@/components/ui/form-dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import { InventoryRetryAlert } from "@/components/ui/inventory-retry-alert";
import { LoadingState } from "@/components/ui/loading-state";
import { Pagination } from "@/components/ui/pagination";
import { useAuth } from "@/context/auth-context.hooks";
import { ApiError } from "@/lib/api/core";
import {
  createCredentialsApi,
  type AppCredentialReference,
} from "@/lib/api/credentials";
import { getErrorMessage } from "@/lib/utils";

const PAGE_SIZE = 20;

type ReferencePhase = "loading" | "ready" | "error" | "missing";

export type CredentialReferenceTarget = {
  id: number;
  name: string;
};

type CredentialReferencesDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  credential: CredentialReferenceTarget | null;
  onCloseAutoFocus?: DialogCloseAutoFocus;
};

type AcceptedIdentity = {
  token: string;
  role: "admin";
};

// Survives session remounts for one open cycle. Once identity diverges, the
// cycle stays rejected until `open` is false, including token A → B → A.
type SessionGate = {
  accepted: AcceptedIdentity | null;
  rejected: boolean;
};

type SessionGateRef = {
  readonly current: SessionGate;
};

type ReferenceResult = {
  id: number;
  page: number;
  nonce: number;
  items: AppCredentialReference[];
  total: number;
  phase: Exclude<ReferencePhase, "loading">;
  errorMessage: string | null;
};

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError";
}

function isCredentialNotFound(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false;
  return error.status === 404 || error.httpStatus === 404;
}

function policyReferenceHref(id: number): string {
  const params = new URLSearchParams({ policyId: String(id) });
  return `/app/policies?${params.toString()}`;
}

function acceptedIdentity(
  token: string | null,
  role: string | null,
  authTransitioning: boolean,
): AcceptedIdentity | null {
  if (role !== "admin" || !token || authTransitioning) return null;
  return { token, role: "admin" };
}

function sameIdentity(left: AcceptedIdentity, right: AcceptedIdentity): boolean {
  return left.token === right.token && left.role === right.role;
}

function CredentialReferencesSession({
  credential,
  token,
  gateRef,
  onOpenChange,
}: {
  credential: CredentialReferenceTarget;
  token: string;
  gateRef: SessionGateRef;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useTranslation();
  const credentialId = credential.id;
  const [navigation, setNavigation] = useState({ page: 1, nonce: 0 });
  const [result, setResult] = useState<ReferenceResult | null>(null);
  const [establishedTotal, setEstablishedTotal] = useState<number | null>(null);
  const requestGenerationRef = useRef(0);
  const { page, nonce } = navigation;
  const visible =
    result != null &&
    result.id === credentialId &&
    result.page === page &&
    result.nonce === nonce
      ? result
      : null;
  const phase: ReferencePhase = visible?.phase ?? "loading";

  useEffect(() => {
    const gate = gateRef.current;
    if (gate.rejected || gate.accepted?.token !== token) return;

    const controller = new AbortController();
    const generation = requestGenerationRef.current + 1;
    requestGenerationRef.current = generation;
    let ignore = false;
    const requestPage = page;
    const requestNonce = nonce;

    const stillCurrent = () =>
      !ignore &&
      requestGenerationRef.current === generation &&
      !controller.signal.aborted;

    createCredentialsApi()
      .listReferences(token, credentialId, { page: requestPage, pageSize: PAGE_SIZE }, controller.signal)
      .then((response) => {
        if (!stillCurrent()) return;
        setEstablishedTotal(response.total);
        setResult({
          id: credentialId,
          page: requestPage,
          nonce: requestNonce,
          items: response.items,
          total: response.total,
          phase: "ready",
          errorMessage: null,
        });
      })
      .catch((error: unknown) => {
        if (!stillCurrent() || isAbortError(error)) return;
        if (isCredentialNotFound(error)) {
          setResult({
            id: credentialId,
            page: requestPage,
            nonce: requestNonce,
            items: [],
            total: 0,
            phase: "missing",
            errorMessage: null,
          });
          return;
        }
        setResult({
          id: credentialId,
          page: requestPage,
          nonce: requestNonce,
          items: [],
          total: 0,
          phase: "error",
          errorMessage: getErrorMessage(error),
        });
      });

    return () => {
      ignore = true;
      controller.abort();
    };
  }, [credentialId, gateRef, nonce, page, token]);

  const changePage = (nextPage: number) => {
    setResult(null);
    setNavigation((current) => ({ page: nextPage, nonce: current.nonce + 1 }));
  };
  const refresh = () => {
    setResult(null);
    setNavigation((current) => ({ page: current.page, nonce: current.nonce + 1 }));
  };
  const backToFirst = () => {
    if (page !== 1) {
      changePage(1);
      return;
    }
    refresh();
  };

  const showLinks = visible != null && visible.phase === "ready" && visible.items.length > 0;
  const showEmpty = visible != null && visible.phase === "ready" && visible.total === 0;
  const showShrunk = visible != null && visible.phase === "ready" && visible.total > 0 && visible.items.length === 0;

  return (
    <>
      <DialogBody className="space-y-3">
        {phase === "loading" ? <LoadingState title={t("common.loading")} rows={3} /> : null}
        {visible != null && visible.phase === "error" && visible.errorMessage ? (
          <InventoryRetryAlert error={visible.errorMessage} onRetry={refresh} />
        ) : null}
        {visible != null && visible.phase === "missing" ? (
          <InlineAlert tone="warning" title={t("credentials.referencesNotFound")}>
            <Button type="button" size="sm" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.close")}
            </Button>
          </InlineAlert>
        ) : null}
        {showEmpty ? (
          <p className="text-sm text-muted-foreground">{t("credentials.referencesEmpty")}</p>
        ) : null}
        {showShrunk ? (
          <InlineAlert tone="warning" title={t("credentials.referencesPageChanged")}>
            <Button type="button" size="sm" variant="outline" onClick={backToFirst}>
              {t("credentials.referencesBackToFirst")}
            </Button>
          </InlineAlert>
        ) : null}
        {showLinks && visible ? (
          <ul aria-label={t("credentials.referencesListLabel")} className="min-w-0 divide-y divide-border">
            {visible.items.map((item) => (
              <li key={item.id} className="min-w-0 py-2">
                <Link
                  to={policyReferenceHref(item.id)}
                  aria-label={t("credentials.referencesPolicyLink", { name: item.name, id: item.id })}
                  className="block min-w-0 rounded-sm break-words text-sm font-medium text-primary underline-offset-4 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <span className="break-words">{item.name}</span>
                  <span className="ml-2 whitespace-nowrap text-xs font-normal text-muted-foreground">
                    ID {item.id}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        ) : null}
        {establishedTotal != null && establishedTotal > 0 && phase !== "missing" ? (
          <Pagination
            page={page}
            pageSize={PAGE_SIZE}
            total={establishedTotal}
            onPageChange={changePage}
          />
        ) : null}
      </DialogBody>
      <DialogFooter>
        <Button type="button" size="sm" variant="outline" onClick={refresh} disabled={phase === "loading"}>
          {t("common.refresh")}
        </Button>
        <Button type="button" size="sm" variant="outline" onClick={() => onOpenChange(false)}>
          {t("common.close")}
        </Button>
      </DialogFooter>
    </>
  );
}

export function CredentialReferencesDialog({
  open,
  onOpenChange,
  credential,
  onCloseAutoFocus,
}: CredentialReferencesDialogProps) {
  const { t } = useTranslation();
  const { token, role, authTransitioning } = useAuth();
  const identity = acceptedIdentity(token, role, authTransitioning);
  const gateRef = useRef<SessionGate>({ accepted: null, rejected: false });
  const onOpenChangeRef = useRef(onOpenChange);
  const identityToken = identity?.token ?? null;
  const identityRole = identity?.role ?? null;

  useLayoutEffect(() => {
    onOpenChangeRef.current = onOpenChange;
  }, [onOpenChange]);

  useLayoutEffect(() => {
    const gate = gateRef.current;
    if (!open) {
      gate.accepted = null;
      gate.rejected = false;
      return;
    }
    if (gate.rejected) {
      onOpenChangeRef.current(false);
      return;
    }
    if (identityToken == null || identityRole == null) {
      gate.rejected = true;
      onOpenChangeRef.current(false);
      return;
    }
    const next = { token: identityToken, role: identityRole };
    if (gate.accepted == null) {
      gate.accepted = next;
      return;
    }
    if (!sameIdentity(gate.accepted, next)) {
      gate.rejected = true;
      onOpenChangeRef.current(false);
    }
  }, [identityRole, identityToken, open]);

  const sessionCredential = open && credential && identity ? credential : null;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="md" onCloseAutoFocus={onCloseAutoFocus}>
        <DialogHeader>
          <DialogTitle>{t("credentials.referencesTitle", { name: credential?.name ?? "" })}</DialogTitle>
          <DialogDescription>{t("credentials.referencesDescription")}</DialogDescription>
          <DialogCloseButton />
        </DialogHeader>
        {sessionCredential && identity ? (
          <CredentialReferencesSession
            key={`${sessionCredential.id}:${identity.token}`}
            credential={sessionCredential}
            token={identity.token}
            gateRef={gateRef}
            onOpenChange={onOpenChange}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
