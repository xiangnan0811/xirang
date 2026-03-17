import { request } from "./core";

export type DockerVolume = {
  name: string;
  driver: string;
  mountpoint: string;
};

type DockerVolumesResponse = {
  data: DockerVolume[];
  warning?: string;
};

export function createDockerApi() {
  return {
    async listDockerVolumes(
      token: string,
      nodeId: number
    ): Promise<{ volumes: DockerVolume[]; warning?: string }> {
      const payload = await request<DockerVolumesResponse>(
        `/nodes/${nodeId}/docker-volumes`,
        { token }
      );
      return {
        volumes: payload.data ?? [],
        warning: payload.warning,
      };
    },
  };
}
