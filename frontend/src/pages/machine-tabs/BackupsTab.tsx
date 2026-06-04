import { BackupsTab as BackupsTabInner } from "../../components/BackupsTab";
import type { Machine } from "../../lib/types";

interface BackupsTabProps {
  machine: Machine;
  accountId: number;
  onRefresh: () => void;
}

export function BackupsTab({ machine, accountId, onRefresh }: BackupsTabProps) {
  return (
    <BackupsTabInner
      machine={machine}
      accountId={accountId}
      onRefresh={onRefresh}
    />
  );
}
