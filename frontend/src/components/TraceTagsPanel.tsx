import { type KeyboardEvent, type MouseEvent, useEffect, useMemo, useState } from "react";
import { Plus, Save, Tag, X } from "lucide-react";
import { updateTraceTags } from "../lib/api";
import type { OpikTraceListItem } from "../lib/types";

const QUICK_TAGS = ["needs-review", "bug", "slow", "high-cost", "regression", "eval-fail"];
const LOW_SIGNAL_TAGS = new Set(["ocm", "openclawmachines"]);
const TAG_RE = /^[a-z0-9._:/-]+$/;

function normalizeTag(raw: string): string {
  return raw.trim().toLowerCase().replace(/\s+/g, "-");
}

function uniqueTags(tags: string[]): string[] {
  const seen = new Set<string>();
  const next: string[] = [];
  for (const raw of tags) {
    const tag = normalizeTag(raw);
    if (!tag || seen.has(tag)) continue;
    seen.add(tag);
    next.push(tag);
  }
  return next;
}

function isLowSignalTag(tag: string): boolean {
  return LOW_SIGNAL_TAGS.has(normalizeTag(tag));
}

export function displayTraceTags(tags?: string[]): string[] {
  return uniqueTags(tags ?? []).filter((tag) => !isLowSignalTag(tag));
}

function sourceTags(tags?: string[]): string[] {
  return uniqueTags(tags ?? []).filter(isLowSignalTag);
}

function validateTag(tag: string): string | null {
  if (tag.length > 64) return "Tags must be 64 characters or fewer.";
  if (!TAG_RE.test(tag)) return "Use letters, numbers, spaces, dashes, underscores, dots, colons, or slashes.";
  if (isLowSignalTag(tag)) return "That source tag is hidden from triage tags.";
  return null;
}

export function TraceTagsPanel({
  accountId,
  trace,
  onSaved,
}: {
  accountId: number;
  trace: OpikTraceListItem;
  onSaved: (traceId: string, tags: string[]) => Promise<void> | void;
}) {
  const traceTagKey = (trace.tags ?? []).join("\n");
  const hiddenSourceTags = useMemo(() => sourceTags(trace.tags), [traceTagKey]);
  const initialTags = useMemo(() => displayTraceTags(trace.tags), [traceTagKey]);
  const [draftTags, setDraftTags] = useState<string[]>(initialTags);
  const [input, setInput] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setDraftTags(initialTags);
    setInput("");
    setError(null);
  }, [trace.id, initialTags.join("\n")]);

  const dirty = draftTags.join("\n") !== initialTags.join("\n");

  const containEvent = (event: MouseEvent<HTMLElement> | KeyboardEvent<HTMLInputElement>) => {
    event.preventDefault();
    event.stopPropagation();
  };

  const addTag = (raw: string) => {
    const tag = normalizeTag(raw);
    if (!tag) return;
    const message = validateTag(tag);
    if (message) {
      setError(message);
      return;
    }
    setDraftTags((current) => uniqueTags([...current, tag]).slice(0, 20));
    setInput("");
    setError(null);
  };

  const removeTag = (tag: string) => {
    setDraftTags((current) => current.filter((item) => item !== tag));
    setError(null);
  };

  const saveTags = async () => {
    if (draftTags.length > 20) {
      setError("A trace can have at most 20 tags.");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      const savedTags = uniqueTags([...draftTags, ...hiddenSourceTags]);
      await updateTraceTags(accountId, trace.id, savedTags);
      await onSaved(trace.id, savedTags);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save tags");
    } finally {
      setSaving(false);
    }
  };

  return (
    <section
      onClick={(event) => event.stopPropagation()}
      onMouseDown={(event) => event.stopPropagation()}
    >
      <div className="flex items-center justify-between gap-3 mb-2">
        <div className="flex items-center gap-2">
          <Tag className="w-4 h-4 text-text-tertiary" />
          <h3 className="text-sm font-semibold text-text-primary">Tags</h3>
        </div>
        {dirty && <span className="text-xs text-text-tertiary">Unsaved</span>}
      </div>

      <div className="border border-border rounded-[var(--radius-md)] p-3 space-y-3">
        <div className="flex flex-wrap gap-2 min-h-8">
          {draftTags.length === 0 ? (
            <span className="text-sm text-text-tertiary">No triage tags.</span>
          ) : (
            draftTags.map((tag) => (
              <span
                key={tag}
                className="inline-flex items-center gap-1.5 min-h-7 px-2 rounded-[var(--radius-sm)] bg-[rgba(255,255,255,0.06)] text-sm text-text-secondary"
              >
                {tag}
                <button
                  type="button"
                  onClick={(event) => {
                    containEvent(event);
                    removeTag(tag);
                  }}
                  title={`Remove ${tag}`}
                  className="inline-flex items-center justify-center w-4 h-4 text-text-muted hover:text-text-primary transition-colors"
                >
                  <X className="w-3 h-3" />
                </button>
              </span>
            ))
          )}
        </div>

        <div className="flex flex-wrap gap-2">
          {QUICK_TAGS.map((tag) => (
            <button
              key={tag}
              type="button"
              onClick={(event) => {
                containEvent(event);
                addTag(tag);
              }}
              disabled={draftTags.includes(tag)}
              className="h-7 px-2 rounded-[var(--radius-sm)] border border-border text-xs font-medium text-text-secondary hover:text-text-primary hover:bg-[rgba(255,255,255,0.04)] disabled:opacity-40 disabled:hover:bg-transparent transition-colors"
            >
              {tag}
            </button>
          ))}
        </div>

        <div className="flex flex-col sm:flex-row gap-2">
          <input
            value={input}
            onChange={(event) => setInput(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                containEvent(event);
                addTag(input);
              }
            }}
            placeholder="Add tag"
            className="h-9 flex-1 border border-border rounded-[var(--radius-sm)] bg-[rgba(0,0,0,0.12)] px-3 text-sm text-text-primary placeholder:text-text-muted outline-none"
          />
          <div className="flex gap-2">
            <button
              type="button"
              onClick={(event) => {
                containEvent(event);
                addTag(input);
              }}
              title="Add tag"
              className="inline-flex items-center justify-center w-9 h-9 border border-border rounded-[var(--radius-sm)] text-text-secondary hover:text-text-primary hover:bg-[rgba(255,255,255,0.04)] transition-colors"
            >
              <Plus className="w-4 h-4" />
            </button>
            <button
              type="button"
              onClick={(event) => {
                containEvent(event);
                void saveTags();
              }}
              disabled={!dirty || saving}
              className="inline-flex items-center gap-2 h-9 px-3 rounded-[var(--radius-sm)] bg-brand-600 text-white text-sm font-medium hover:bg-brand-700 disabled:opacity-50 transition-colors"
            >
              <Save className="w-4 h-4" />
              {saving ? "Saving..." : "Save"}
            </button>
          </div>
        </div>

        {hiddenSourceTags.length > 0 && (
          <p className="text-xs text-text-muted">Hidden source tag: {hiddenSourceTags.join(", ")}</p>
        )}

        {error && (
          <p className="text-sm border border-red-400/50 bg-red-950/60 text-red-50 rounded-[var(--radius-md)] p-3">
            {error}
          </p>
        )}
      </div>
    </section>
  );
}
