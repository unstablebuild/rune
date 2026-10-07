export function slugify(value: string): string {
  return privateHelper(value).toLowerCase().replace(/\s+/g, "-");
}

function privateHelper(value: string): string {
  return value.trim();
}
