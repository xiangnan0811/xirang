/**
 * Categories the settings form may choose explicitly.
 * This is not the quick-silence allowlist: a blank value remains the wildcard,
 * and quick mode must not treat this list as permission to invent a match.
 */
export const SILENCE_CATEGORIES = [
  { value: "XR-EXEC", i18nKey: "silences.types.exec" },
  { value: "XR-VRFY", i18nKey: "silences.types.vrfy" },
  { value: "XR-NODE", i18nKey: "silences.types.node" },
  { value: "XR-NODE-EXPIRY", i18nKey: "silences.types.nodeExpiry" },
  { value: "XR-RETN", i18nKey: "silences.types.retn" },
  { value: "XR-INTG", i18nKey: "silences.types.intg" },
  { value: "XR-REPORT", i18nKey: "silences.types.report" },
  { value: "XR-SLO", i18nKey: "silences.types.slo" },
] as const;
