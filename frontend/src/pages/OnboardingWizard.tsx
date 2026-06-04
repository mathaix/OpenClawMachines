import { useReducer } from "react";
import { useAuth } from "../lib/auth";
import { useToast } from "../components/Toast";
import { createAccount, createMachine, putMachineCredential, setMachineIdentity, pushMachineConfig, startMachine } from "../lib/api";
import type { CredentialProvider, MachineKind } from "../lib/types";
import { toSlug } from "../lib/utils";
import { ProgressRail } from "../components/onboarding/ProgressRail";
import { StepAccount } from "../components/onboarding/StepAccount";
import { StepMachine } from "../components/onboarding/StepMachine";
import { StepProvider } from "../components/onboarding/StepProvider";
import { StepChannels } from "../components/onboarding/StepChannels";
import { StepIdentity } from "../components/onboarding/StepIdentity";
import { StepLaunch } from "../components/onboarding/StepLaunch";

// --- State & Actions ---

export interface OnboardingState {
  currentStep: number;
  completedSteps: Set<number>;
  accountName: string;
  accountSlug: string;
  accountSlugTouched: boolean;
  machineName: string;
  machineKind: MachineKind;
  machineSizeId: string;
  selectedProvider: CredentialProvider | null;
  credentialValue: string;
  selectedChannel: CredentialProvider | null;
  channelToken: string;
  assistantName: string;
  launchPhase: "idle" | "provisioning" | "complete" | "error";
  createdAccountId: number | null;
  createdMachineId: string | null;
  submitting: boolean;
  error: string | null;
}

export type OnboardingAction =
  | { type: "SET_FIELD"; field: string; value: unknown }
  | { type: "SET_STEP"; step: number }
  | { type: "MARK_COMPLETED"; step: number }
  | { type: "SET_SUBMITTING"; submitting: boolean }
  | { type: "SET_ERROR"; error: string | null }
  | { type: "SET_CREATED_ACCOUNT"; accountId: number }
  | { type: "SET_CREATED_MACHINE"; machineId: string }
  | { type: "SET_LAUNCH_PHASE"; phase: OnboardingState["launchPhase"] };

function randomMachineName(): string {
  const hex = Math.random().toString(16).slice(2, 6);
  return `machine-${hex}`;
}

const initialState: OnboardingState = {
  currentStep: 0,
  completedSteps: new Set(),
  accountName: "",
  accountSlug: "",
  accountSlugTouched: false,
  machineName: randomMachineName(),
  machineKind: "openclaw",
  machineSizeId: "standard",
  selectedProvider: null,
  credentialValue: "",
  selectedChannel: null,
  channelToken: "",
  assistantName: "",
  launchPhase: "idle",
  createdAccountId: null,
  createdMachineId: null,
  submitting: false,
  error: null,
};

function reducer(state: OnboardingState, action: OnboardingAction): OnboardingState {
  switch (action.type) {
    case "SET_FIELD":
      return { ...state, [action.field]: action.value };
    case "SET_STEP":
      return { ...state, currentStep: action.step, error: null };
    case "MARK_COMPLETED": {
      const next = new Set(state.completedSteps);
      next.add(action.step);
      return { ...state, completedSteps: next };
    }
    case "SET_SUBMITTING":
      return { ...state, submitting: action.submitting };
    case "SET_ERROR":
      return { ...state, error: action.error, submitting: false };
    case "SET_CREATED_ACCOUNT":
      return { ...state, createdAccountId: action.accountId };
    case "SET_CREATED_MACHINE":
      return { ...state, createdMachineId: action.machineId };
    case "SET_LAUNCH_PHASE":
      return { ...state, launchPhase: action.phase };
  }
}

// --- Wizard Page ---

export function OnboardingWizard() {
  const { user, setAccount, refreshAccounts } = useAuth();
  const { toast } = useToast();
  const [state, dispatch] = useReducer(reducer, initialState);

  const advance = (from: number) => {
    dispatch({ type: "MARK_COMPLETED", step: from });
    dispatch({ type: "SET_STEP", step: from + 1 });
  };

  // Step 0: Create account
  const handleAccountContinue = async () => {
    dispatch({ type: "SET_SUBMITTING", submitting: true });
    dispatch({ type: "SET_ERROR", error: null });
    try {
      const slug = state.accountSlugTouched ? state.accountSlug : toSlug(state.accountName);
      const account = await createAccount({ name: state.accountName.trim(), slug });
      setAccount(account);
      await refreshAccounts();
      dispatch({ type: "SET_CREATED_ACCOUNT", accountId: account.id });
      dispatch({ type: "SET_SUBMITTING", submitting: false });
      advance(0);
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to create account";
      dispatch({ type: "SET_ERROR", error: msg });
      toast({ title: "Error", description: msg, variant: "error" });
    }
  };

  // Step 1: Create machine
  const handleMachineContinue = async () => {
    if (!state.createdAccountId) {
      dispatch({ type: "SET_ERROR", error: "Account not created yet — please complete step 1 first" });
      return;
    }
    dispatch({ type: "SET_SUBMITTING", submitting: true });
    dispatch({ type: "SET_ERROR", error: null });
    try {
      const machine = await createMachine(state.createdAccountId, {
        name: state.machineName.trim(),
        kind: state.machineKind,
        size: state.machineSizeId,
        runtime_source: "artifact",
      });
      dispatch({ type: "SET_CREATED_MACHINE", machineId: machine.id });
      dispatch({ type: "SET_SUBMITTING", submitting: false });
      advance(1);
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to create machine";
      dispatch({ type: "SET_ERROR", error: msg });
      toast({ title: "Error", description: msg, variant: "error" });
    }
  };

  // Step 2: Save AI provider credential, link to machine
  const handleProviderContinue = async () => {
    if (!state.createdAccountId || !state.selectedProvider || !state.createdMachineId) {
      dispatch({ type: "SET_ERROR", error: "Please complete the previous steps first" });
      return;
    }
    dispatch({ type: "SET_SUBMITTING", submitting: true });
    dispatch({ type: "SET_ERROR", error: null });
    try {
      await putMachineCredential(state.createdAccountId, state.createdMachineId, state.selectedProvider, {
        value: state.credentialValue,
        label: state.selectedProvider,
      });
      dispatch({ type: "SET_SUBMITTING", submitting: false });
      advance(2);
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to save credential";
      dispatch({ type: "SET_ERROR", error: msg });
      toast({ title: "Error", description: msg, variant: "error" });
    }
  };

  // Step 3: Save channel credential, link to machine
  const handleChannelsContinue = async () => {
    if (!state.createdAccountId || !state.selectedChannel || !state.createdMachineId) {
      dispatch({ type: "SET_ERROR", error: "Please complete the previous steps first" });
      return;
    }
    dispatch({ type: "SET_SUBMITTING", submitting: true });
    dispatch({ type: "SET_ERROR", error: null });
    try {
      await putMachineCredential(state.createdAccountId, state.createdMachineId, state.selectedChannel, {
        value: state.channelToken,
        label: state.selectedChannel,
      });
      dispatch({ type: "SET_SUBMITTING", submitting: false });
      advance(3);
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to save channel";
      dispatch({ type: "SET_ERROR", error: msg });
      toast({ title: "Error", description: msg, variant: "error" });
    }
  };

  // Step 4: Save identity to backend, then advance
  const handleIdentityContinue = async () => {
    if (!state.createdAccountId || !state.createdMachineId) {
      dispatch({ type: "SET_ERROR", error: "Please complete the previous steps first" });
      return;
    }
    if (!state.assistantName.trim()) {
      advance(4);
      return;
    }
    dispatch({ type: "SET_SUBMITTING", submitting: true });
    dispatch({ type: "SET_ERROR", error: null });
    try {
      await setMachineIdentity(state.createdAccountId, state.createdMachineId, {
        name: state.assistantName.trim(),
      });
      dispatch({ type: "SET_SUBMITTING", submitting: false });
      advance(4);
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to save identity";
      dispatch({ type: "SET_ERROR", error: msg });
      toast({ title: "Error", description: msg, variant: "error" });
    }
  };

  // Step 5: Push config (wires up model providers + channels), then start machine
  const handleLaunch = async () => {
    if (!state.createdAccountId || !state.createdMachineId) {
      dispatch({ type: "SET_ERROR", error: "Please complete the previous steps first" });
      return;
    }
    dispatch({ type: "SET_SUBMITTING", submitting: true });
    dispatch({ type: "SET_ERROR", error: null });
    try {
      // Push config first — assembles model provider base URLs, channel config,
      // identity, and default model selection (anthropic > openai > google)
      await pushMachineConfig(state.createdAccountId, state.createdMachineId);
      await startMachine(state.createdAccountId, state.createdMachineId);
      dispatch({ type: "SET_SUBMITTING", submitting: false });
      dispatch({ type: "SET_LAUNCH_PHASE", phase: "provisioning" });
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to start machine";
      dispatch({ type: "SET_ERROR", error: msg });
      dispatch({ type: "SET_LAUNCH_PHASE", phase: "error" });
      toast({ title: "Error", description: msg, variant: "error" });
    }
  };

  return (
    <div className="min-h-screen bg-surface-deep flex items-center justify-center px-4 py-8">
      <div className="w-full max-w-2xl">
        {/* Header */}
        <div className="text-center mb-2">
          <h1 className="text-2xl font-semibold text-gray-100">
            Welcome to <span className="text-brand-500">OpenClaw</span>
          </h1>
          {user?.email && (
            <p className="text-xs text-gray-500 mt-1">Signed in as {user.email}</p>
          )}
        </div>

        {/* Progress Rail */}
        <ProgressRail currentStep={state.currentStep} completedSteps={state.completedSteps} />

        {/* Step Content */}
        <div className="bg-surface-card border border-border rounded-xl p-6 mt-2">
          {state.currentStep === 0 && (
            <StepAccount
              state={state}
              dispatch={dispatch}
              onContinue={handleAccountContinue}
              submitting={state.submitting}
            />
          )}
          {state.currentStep === 1 && (
            <StepMachine
              state={state}
              dispatch={dispatch}
              onContinue={handleMachineContinue}
              submitting={state.submitting}
            />
          )}
          {state.currentStep === 2 && (
            <StepProvider
              state={state}
              dispatch={dispatch}
              onContinue={handleProviderContinue}
              onSkip={() => advance(2)}
              submitting={state.submitting}
            />
          )}
          {state.currentStep === 3 && (
            <StepChannels
              state={state}
              dispatch={dispatch}
              onContinue={handleChannelsContinue}
              onSkip={() => advance(3)}
              submitting={state.submitting}
            />
          )}
          {state.currentStep === 4 && (
            <StepIdentity
              state={state}
              dispatch={dispatch}
              onContinue={handleIdentityContinue}
              onSkip={() => advance(4)}
              submitting={state.submitting}
            />
          )}
          {state.currentStep === 5 && state.createdAccountId && state.createdMachineId && (
            <StepLaunch
              state={state}
              dispatch={dispatch}
              onLaunch={handleLaunch}
              accountId={state.createdAccountId}
              machineId={state.createdMachineId}
            />
          )}
        </div>
      </div>
    </div>
  );
}
