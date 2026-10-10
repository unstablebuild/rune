export interface Sized {
  size(): number;
}

export type Point = { x: number; y: number };

export enum Unit {
  Px,
  Em,
}

export class Shape implements Sized {
  constructor(private readonly side: number) {}

  size(): number {
    return this.side;
  }

  scale(by: number): Shape {
    return new Shape(this.side * by);
  }
}

export abstract class Polygon {
  abstract sides(): number;

  describe(): string {
    return `${this.sides()} sides`;
  }
}

export function area(shape: Shape): number {
  return shape.size() * shape.size();
}

export const perimeter = (shape: Shape): number => shape.size() * 4;

class Internal {}

function makeInternal(): Internal {
  return new Internal();
}
