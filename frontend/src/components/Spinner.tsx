type Props = {
  label?: string;
};

export function Spinner({ label = "Loading" }: Props) {
  return (
    <span className="spinner" role="status" aria-label={label}>
      <span className="spinner-dot" aria-hidden="true" />
      <span className="spinner-text">{label}…</span>
    </span>
  );
}
