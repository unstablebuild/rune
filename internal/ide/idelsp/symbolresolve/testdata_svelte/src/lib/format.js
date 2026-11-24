export function money(amount) {
  return `$${amount.toFixed(2)}`;
}

export function percent(ratio) {
  return `${Math.round(ratio * 100)}%`;
}
