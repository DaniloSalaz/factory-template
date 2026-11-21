// The app's logo. Changing the brand is replacing this file and src/app/theme.css.
export function Logo(props: { className?: string }) {
  return (
    <svg
      viewBox="0 0 32 32"
      role="img"
      aria-hidden="true"
      className={props.className}
      fill="currentColor"
    >
      <rect x="2" y="2" width="12" height="12" rx="3" />
      <rect x="18" y="2" width="12" height="12" rx="3" opacity="0.6" />
      <rect x="2" y="18" width="12" height="12" rx="3" opacity="0.6" />
      <rect x="18" y="18" width="12" height="12" rx="3" opacity="0.3" />
    </svg>
  );
}
