import { request } from "./core";
import { finiteNumber } from "./number-utils";

type MountVerifyResultRaw = {
  exists?: boolean;
  is_mount_point?: boolean;
  writable?: boolean;
  total_gb?: number;
  free_gb?: number;
  filesystem?: string;
};

export type MountVerifyResult = {
  exists: boolean;
  isMountPoint: boolean;
  writable: boolean;
  totalGb: number;
  freeGb: number;
  filesystem: string;
};

const emptyMountVerifyResult: MountVerifyResult = {
  exists: false,
  isMountPoint: false,
  writable: false,
  totalGb: 0,
  freeGb: 0,
  filesystem: "",
};

function mapMountVerifyResult(raw: MountVerifyResultRaw | null | undefined): MountVerifyResult {
  if (!raw) {
    return emptyMountVerifyResult;
  }
  return {
    exists: Boolean(raw.exists),
    isMountPoint: Boolean(raw.is_mount_point),
    writable: Boolean(raw.writable),
    totalGb: finiteNumber(raw.total_gb),
    freeGb: finiteNumber(raw.free_gb),
    filesystem: String(raw.filesystem ?? ""),
  };
}

export function createStorageGuideApi() {
  return {
    async verifyMount(token: string, path: string): Promise<MountVerifyResult> {
      const raw = await request<MountVerifyResultRaw>("/system/verify-mount", {
        token,
        method: "POST",
        body: { path },
      });
      return mapMountVerifyResult(raw);
    },
  };
}
