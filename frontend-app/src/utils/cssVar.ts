/** Read a CSS custom property from the document root (for theme-aware charts). */
export function getCSSVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}
