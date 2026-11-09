# Drives testdata/sampleext's "counter" command, which splits a panel
# whose handler renders "count=N" and increments N on <space>. Asserts
# the sandbox can render an installed tui.Handler to a string and that
# the rendered output changes in response to key events.
expect_metadata(id = "xsandbox_sample")
config({"prefix": "demo"})
fs.write("VERSION", "9.9.9\n")

c = expect_command("counter")
invoke_command(c, timeout = "15s")

# The Split install stream is now open; grab a window handle for it.
w = expect_window(method = "browser.WindowManager/Split", timeout = "5s")

# Initial render shows count=0.
out = render(w, width = 40, height = 6)
if "count=0" not in out:
    fail("expected count=0 in initial render, got:\n" + out)

# A <space> key is handled and increments the counter.
r = send_key(w, "<space>")
if not r["handled"]:
    fail("expected <space> to be handled")
if r["quit"]:
    fail("expected <space> not to request exit")

out = render(w, width = 40, height = 6)
if "count=1" not in out:
    fail("expected count=1 after one <space>, got:\n" + out)

# Two more increments land at count=3.
send_key(w, "<space><space>")
out = render(w, width = 40, height = 6)
if "count=3" not in out:
    fail("expected count=3 after three <space>, got:\n" + out)

# A click at a cell of the last render lands in the panel there, at the
# position it has in the panel, and a paste arrives as its keys between a
# paste start and a paste end.
r = send_mouse(w, "<mouse-left><mouse-release>", x = 25, y = 4)
if r["quit"]:
    fail("expected the click not to request exit")
out = render(w, width = 40, height = 6)
if "click 4,1" not in out:
    fail("expected the click in the render, got:\n" + out)
if not send_paste(w, "hi there")["handled"]:
    fail("expected the paste to be handled")
out = render(w, width = 40, height = 6)
if "paste hi there" not in out:
    fail("expected the paste in the render, got:\n" + out)
if not send_event(w, "focus")["handled"]:
    fail("expected focus to be handled")
out = render(w, width = 40, height = 6)
if "focus" not in out:
    fail("expected focus in the render, got:\n" + out)

# The panel has no selection to copy.
if selection(w) != None:
    fail("expected no selection")
