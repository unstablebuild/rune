<script context="module">
  export function load() {
    return { theme: "dark" };
  }
</script>

<script>
  export let theme = "light";

  function toggle() {
    theme = theme === "dark" ? "light" : "dark";
  }
</script>

<button on:click={toggle}>{theme}</button>
