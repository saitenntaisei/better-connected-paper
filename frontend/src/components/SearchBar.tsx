import { useEffect, useState } from "react";

type Props = {
  onSubmit: (query: string) => void;
  initial?: string;
  busy?: boolean;
  placeholder?: string;
};

export function SearchBar({ onSubmit, initial = "", busy = false, placeholder }: Props) {
  const [value, setValue] = useState(initial);
  useEffect(() => {
    setValue(initial);
  }, [initial]);
  const trimmed = value.trim();

  return (
    <form
      className="search-bar"
      role="search"
      onSubmit={(e) => {
        e.preventDefault();
        if (trimmed) onSubmit(trimmed);
      }}
    >
      <input
        type="search"
        aria-label="Paper search"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder={placeholder ?? "e.g. Attention Is All You Need"}
        autoFocus
      />
      <button type="submit" disabled={!trimmed || busy}>
        {busy ? "Searching…" : "Search"}
      </button>
    </form>
  );
}
