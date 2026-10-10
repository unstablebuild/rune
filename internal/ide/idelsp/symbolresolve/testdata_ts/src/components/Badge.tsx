import * as geometry from "../geometry";

export function Badge({ label }: { label: string }) {
  const shape: geometry.Shape = new geometry.Shape(1);
  return <span title={label}>{geometry.area(shape)}</span>;
}

export class Panel {
  render() {
    return <div />;
  }
}
