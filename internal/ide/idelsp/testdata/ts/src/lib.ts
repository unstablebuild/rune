export interface Shape {
  area(): number;
}

export class Square implements Shape {
  constructor(private readonly side: number) {}

  area(): number {
    return this.side * this.side;
  }
}

export class Greeter {
  constructor(private readonly name: string) {}

  greet(): string {
    return `Hello, ${this.name}!`;
  }
}

export function add(a: number, b: number): number {
  return a + b;
}
