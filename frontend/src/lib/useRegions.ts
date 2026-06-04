import { useEffect, useState } from "react";
import { listRegions } from "./api";
import type { RegionOption } from "./types";

const CACHE_TTL_MS = 5 * 60 * 1000;
let cachedRegions: RegionOption[] | null = null;
let cachedAt = 0;
let fetchRegionsPromise: Promise<RegionOption[]> | null = null;

export function selectDefaultRegion(regions: RegionOption[]): string {
  if (regions.length === 0) return "";
  const available = regions.filter((r) => r.available);
  const pool = available.length > 0 ? available : regions;
  const usWest = pool.find((r) => r.code === "us-west1" || r.code === "us-west");
  if (usWest) return usWest.code;
  return pool[0].code;
}

export function useRegions(): RegionOption[] {
  const [regions, setRegions] = useState<RegionOption[]>(cachedRegions ?? []);

  useEffect(() => {
    if (cachedRegions && Date.now() - cachedAt < CACHE_TTL_MS) return;
    cachedRegions = null;
    if (!fetchRegionsPromise) {
      fetchRegionsPromise = listRegions()
        .then((r) => {
          cachedRegions = r;
          cachedAt = Date.now();
          return r;
        })
        .catch((err) => {
          fetchRegionsPromise = null;
          throw err;
        });
    }
    fetchRegionsPromise.then(setRegions).catch(() => {});
  }, []);

  return regions;
}
