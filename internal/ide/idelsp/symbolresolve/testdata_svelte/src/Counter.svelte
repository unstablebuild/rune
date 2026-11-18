<script lang="ts">
  import * as geometry from "./lib/geometry";
  import * as motion from "svelte/motion";
  import Badge from "./Badge.svelte";

  export let start = 0;

  export class Store {
    constructor(private value: number) {}

    reset(): void {
      this.value = 0;
    }
  }

  export type Step = { by: number };

  const store = new Store(start);
  const shape: geometry.Shape = new geometry.Shape(start);
  const progress = motion.tweened(0);

  export function increment(step: Step): number {
    start += step.by;
    store.reset();
    return geometry.area(shape);
  }
</script>

<Badge label={geometry.describe(shape)} />
<button on:click={() => increment({ by: 1 })}>{start} {$progress}</button>

<style>
  button {
    color: red;
  }
</style>
