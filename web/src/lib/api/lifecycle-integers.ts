export function finiteInteger(value: unknown, minimum = 0): number | null {
  if (typeof value === "number") {
    return Number.isSafeInteger(value) && value >= minimum ? value : null;
  }
  if (typeof value !== "string" || value.trim() === "") {
    return null;
  }
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= minimum ? parsed : null;
}
