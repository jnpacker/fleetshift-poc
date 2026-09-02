import { memo } from "react";

// Inline server-rack SVG (no external asset) — matches the KindIcon pattern
// of a memoized, fixed 16x16 icon used for search results.
const AssistedIcon = memo(() => {
  return (
    <svg
      width={16}
      height={16}
      viewBox="0 0 16 16"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      role="img"
      aria-hidden="true"
    >
      <rect x="1" y="1" width="14" height="3.2" rx="0.6" fill="currentColor" />
      <rect
        x="1"
        y="6.4"
        width="14"
        height="3.2"
        rx="0.6"
        fill="currentColor"
      />
      <rect
        x="1"
        y="11.8"
        width="14"
        height="3.2"
        rx="0.6"
        fill="currentColor"
      />
      <circle cx="3.2" cy="2.6" r="0.6" fill="white" />
      <circle cx="3.2" cy="8" r="0.6" fill="white" />
      <circle cx="3.2" cy="13.4" r="0.6" fill="white" />
    </svg>
  );
});

AssistedIcon.displayName = "AssistedIcon";

export default AssistedIcon;
