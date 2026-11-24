export class Shape {
  constructor(private readonly side: number) {}

  size(): number {
    return this.side;
  }
}

export function area(shape: Shape): number {
  return shape.size() * shape.size();
}

export function describe(shape: Shape): string {
  return `${shape.size()} wide`;
}
