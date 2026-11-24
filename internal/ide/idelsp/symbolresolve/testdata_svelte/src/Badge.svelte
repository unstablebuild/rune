<script>
  import * as format from "./lib/format";

  export let label = "";
  export let ratio = 0;

  export class Palette {
    constructor(name) {
      this.name = name;
    }

    swatch() {
      return `swatch-${this.name}`;
    }
  }

  export function emphasize(text) {
    return text.toUpperCase();
  }

  const palette = new Palette("warm");
  const price = format.money(ratio);
</script>

<span class={palette.swatch()}>{emphasize(label)} {format.percent(ratio)} {price}</span>
