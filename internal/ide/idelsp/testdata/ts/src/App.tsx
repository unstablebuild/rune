import { add } from "./lib.js";

export function App(props: { a: number; b: number }) {
  return <p>{add(props.a, props.b)}</p>;
}
