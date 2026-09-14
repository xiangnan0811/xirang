import { request } from "./core";

export type DockerVolume = {
  name: string;
  driver: string;
  mountpoint: string;
};

type DockerVolumesResponse = {
  data: DockerVolume[];
  partial: boolean;
  warning?: string;
};

export function createDockerApi() {
  return {
    async listDockerVolumes(
      token: string,
      nodeId: number,
      signal?: AbortSignal
    ): Promise<{ volumes: DockerVolume[]; partial: boolean; warning?: string }> {
      const payload = await request<DockerVolumesResponse>(
        `/nodes/${nodeId}/docker-volumes`,
        { token, signal }
      );
      return {
        volumes: payload.data ?? [],
        partial: payload.partial,
        warning: payload.warning,
      };
    },
  };
}
