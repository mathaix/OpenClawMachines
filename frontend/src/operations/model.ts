import { setMachineModel, pushMachineConfig } from "../lib/api";
import type { MachineOperationResult } from "./machine";

export async function changeModel(
  accountId: number,
  machineId: string,
  model: string,
): Promise<MachineOperationResult> {
  try {
    await setMachineModel(accountId, machineId, model);
    await pushMachineConfig(accountId, machineId);
    return { success: true };
  } catch (err) {
    return {
      success: false,
      error: err instanceof Error ? err : new Error(String(err)),
    };
  }
}
