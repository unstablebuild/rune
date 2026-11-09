# Mirrors exactly what testdata/sampleext does, so the sandbox run
# passes: config read, VERSION read + notify, command registrations,
# command invocations, and editor open-event subscription + notify.
expect_metadata(
    id = "xsandbox_sample",
    permissions = [
        "permcmd", "permnoti", "permfs", "permcfg", "permed",
        "permexec", "permpty", "permstore", "permwm", "permopen",
        "permint", "permsyntax", "permlsp", "permdap", "permllm",
    ],
)
config({"prefix": "demo"})
fs.write("VERSION", "9.9.9\n")

expect_rpc("config.Config/Get")
expect_rpc("workspace.Files/Read")
expect_rpc("text.Editor/SubscribeCommand")
expect_rpc("text.Editor/SubscribeEvent")
expect_rpc(
    "browser.Notifications/Notify",
    where = {"msg": "demo: version 9.9.9"},
    timeout = "5s",
)

g = expect_command("greet")
invoke_command(g, args = ["ada"])
expect_rpc(
    "browser.Notifications/Notify",
    where = {"msg": "demo: hi ada", "level": present()},
    timeout = "5s",
)

c = expect_command("count")
invoke_command(c, args = ["a", "b", "c"])
expect_rpc(
    "browser.Notifications/Notify",
    where = {"msg": regex("^demo: n=3$")},
    timeout = "5s",
)

# Completion and help, as Rune asks for them on Tab and for a console.
if complete_command(c, args = ["a", "b"]) != ["beta"]:
    fail("unexpected count completions")
if complete_command(c) != ["alpha", "beta"]:
    fail("unexpected count completions without args")
s = expect_repl_command("status")
if complete_repl_command(s, args = ["v"]) != ["verbose"]:
    fail("unexpected status completions")
if repl_help(s) != "usage: status [verbose]":
    fail("unexpected status help")

publish_event("open", uri = "file:///tmp/foo.go")
expect_rpc(
    "browser.Notifications/Notify",
    where = {"msg": "demo: opened foo.go"},
    timeout = "5s",
)

wait_idle("200ms")
