import { request } from "./core";

export type MountVerifyResult = {
  exists: boolean;
  is_mount_point: boolean;
  writable: boolean;
  total_gb: number;
  free_gb: number;
  filesystem: string;
};

export function createStorageGuideApi() {
  return {
    async verifyMount(token: string, path: string): Promise<MountVerifyResult> {
      return (await request<MountVerifyResult>("/system/verify-mount", {
        token,
        method: "POST",
        body: { path },
      })) ?? {
        exists: false,
        is_mount_point: false,
        writable: false,
        total_gb: 0,
        free_gb: 0,
        filesystem: "",
      };
    },
  };
}
