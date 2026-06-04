import { startMachine, stopMachine, deleteMachine } from "../lib/api";

export interface MachineOperationResult {
  success: boolean;
  error?: Error;
}

export async function transitionMachine(
  accountId: number,
  machineId: string,
  action: "start" | "stop" | "delete",
): Promise<MachineOperationResult> {
  try {
    switch (action) {
      case "start":
        await startMachine(accountId, machineId);
        break;
      case "stop":
        await stopMachine(accountId, machineId);
        break;
      case "delete":
        await deleteMachine(accountId, machineId);
        break;
    }
    return { success: true };
  } catch (err) {
    return {
      success: false,
      error: err instanceof Error ? err : new Error(String(err)),
    };
  }
}
