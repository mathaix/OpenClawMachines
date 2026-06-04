import { useState, useEffect } from "react";
import { listSizes } from "./api";
import type { MachineSize } from "./types";

let cachedSizes: MachineSize[] | null = null;
let fetchPromise: Promise<MachineSize[]> | null = null;

export function useSizes(): MachineSize[] {
  const [sizes, setSizes] = useState<MachineSize[]>(cachedSizes ?? []);

  useEffect(() => {
    if (cachedSizes) return;
    if (!fetchPromise) {
      fetchPromise = listSizes().then((s) => {
        cachedSizes = s;
        return s;
      }).catch((err) => {
        fetchPromise = null; // allow retry on next mount
        throw err;
      });
    }
    fetchPromise.then(setSizes).catch(() => {});
  }, []);

  return sizes;
}
