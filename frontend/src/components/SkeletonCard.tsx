export function SkeletonCard() {
  return (
    <div className="bg-card border border-border rounded-[var(--radius)] p-4 md:p-5 shadow-card">
      <div className="flex items-start justify-between mb-3">
        <div>
          <div className="h-5 w-36 rounded animate-shimmer mb-2" />
          <div className="flex gap-3">
            <div className="h-3 w-24 rounded animate-shimmer" />
            <div className="h-3 w-16 rounded animate-shimmer" />
          </div>
        </div>
        <div className="h-5 w-16 rounded-full animate-shimmer" />
      </div>
      <div className="border-t border-border-subtle pt-3 flex gap-2">
        <div className="h-7 w-20 rounded animate-shimmer" />
        <div className="h-7 w-16 rounded animate-shimmer" />
      </div>
    </div>
  );
}
