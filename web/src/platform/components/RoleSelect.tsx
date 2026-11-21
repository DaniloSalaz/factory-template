import { cn } from "cn";

/** RoleSelect picks one of roles; a native select, as no form library is used. */
export function RoleSelect(props: {
  id?: string;
  label?: string;
  value: string;
  roles: readonly string[];
  onChange: (role: string) => void;
  disabled?: boolean;
}) {
  return (
    <select
      id={props.id}
      aria-label={props.label}
      value={props.value}
      disabled={props.disabled}
      onChange={(e) => props.onChange(e.target.value)}
      className={cn(
        "h-8 rounded-lg border border-input bg-transparent px-2 text-sm outline-none dark:bg-input/30",
        "focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:opacity-50",
      )}
    >
      {props.roles.map((r) => (
        <option key={r} value={r}>
          {r}
        </option>
      ))}
    </select>
  );
}
