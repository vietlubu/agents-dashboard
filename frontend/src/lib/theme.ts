// Theme handling. "system" follows the OS; the resolved theme is written to a data attribute
// on <html> so the CSS variables switch in one place.
export type ThemeChoice = "system" | "light" | "dark";

const MEDIA = "(prefers-color-scheme: light)";

let listener: ((event: MediaQueryListEvent) => void) | null = null;
let query: MediaQueryList | null = null;

export function resolveTheme(choice: ThemeChoice): "light" | "dark" {
  if (choice === "light" || choice === "dark") return choice;
  if (typeof window === "undefined" || !window.matchMedia) return "dark";
  return window.matchMedia(MEDIA).matches ? "light" : "dark";
}

export function applyTheme(choice: ThemeChoice): void {
  const resolved = resolveTheme(choice);
  document.documentElement.setAttribute("data-theme", resolved);

  // Keep following the OS only while the choice is "system".
  if (query && listener) {
    query.removeEventListener("change", listener);
    query = null;
    listener = null;
  }
  if (choice === "system" && window.matchMedia) {
    query = window.matchMedia(MEDIA);
    listener = () => applyTheme("system");
    query.addEventListener("change", listener);
  }
}