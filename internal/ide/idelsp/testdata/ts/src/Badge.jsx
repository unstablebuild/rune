import { double } from "./util.js";

/** @param {{ count: number }} props */
export function Badge({ count }) {
  return <span>{double(count)}</span>;
}
