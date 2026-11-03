/** Realm's mark: chambers nested inside their parents. */
export const RealmMark = ({ className }: { className?: string }) => (
  <svg viewBox="0 0 24 24" aria-hidden="true" className={className}>
    <rect x="1" y="1" width="22" height="22" rx="6" fill="currentColor" opacity="0.22" />
    <rect x="5.5" y="5.5" width="16" height="16" rx="4.5" fill="currentColor" opacity="0.5" />
    <rect x="10.5" y="10.5" width="10" height="10" rx="3" fill="currentColor" />
  </svg>
);
