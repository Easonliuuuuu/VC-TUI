# Regenerate from the repository root: vhs docs/readme-demo.tape
Output docs/assets/vsfleet.gif

Require go

Set Shell "bash"
Set FontSize 16
Set FontFamily "DejaVu Sans Mono, monospace"
Set Width 1200
Set Height 760
Set Padding 20
Set Theme { "name": "vsfleet", "black": "#16161e", "red": "#ff7b72", "green": "#7ee787", "yellow": "#e3b341", "blue": "#7aa2f7", "magenta": "#bb9af7", "cyan": "#7dcfff", "white": "#e4e4e4", "brightBlack": "#565f89", "brightRed": "#ff7b72", "brightGreen": "#7ee787", "brightYellow": "#e3b341", "brightBlue": "#7aa2f7", "brightMagenta": "#bb9af7", "brightCyan": "#7dcfff", "brightWhite": "#ffffff", "background": "#16161e", "foreground": "#e4e4e4", "selectionBackground": "#3b4a6b", "cursorAccent": "#16161e" }
Set TypingSpeed 35ms
Set Framerate 24
Set PlaybackSpeed 1

Env TERM "xterm-256color"
Env COLORTERM "truecolor"
Env NO_COLOR ""

Env CI ""
Env VSFLEET_NO_WELCOME ""
Env VSFLEET_NO_UPDATE_NOTIFIER "1"

# Build outside the recording; all visible inventory comes from the offline demo.
Hide
Type "demo_dir=$(mktemp -d /tmp/vsfleet-readme-demo.XXXXXX)"
Enter
Type `go build -o "$demo_dir/vsfleet" ./cmd/vsfleet && echo VSFLEET_''DEMO_READY`
Enter
Wait+Screen@5m /VSFLEET_DEMO_READY/
Type `export PATH="$demo_dir:$PATH"; export PS1='$ '`
Enter
Type "clear"
Enter
Show
Sleep 600ms
Type@100ms "vsfleet demo"
Sleep 600ms
Enter
# No keys until the complete 2.8-second welcome animation has finished.
Wait+Screen /api-01/
Sleep 1800ms

# Preview a new vCenter connection. Demo never tests or saves the form.
Type "c"
Wait+Screen /Contexts/
Sleep 1200ms
Type "n"
Wait+Screen /Add a vCenter/
Sleep 800ms
Type@90ms "lab-vc"
Down
Type@45ms "https://vcsa.example.invalid"
Down
Type@55ms "reader@vsphere.local"
Down
Right
Sleep 2200ms
Escape
Wait+Screen /Contexts/
Sleep 600ms
Escape
Sleep 500ms

# Inspect a VM, its CPU and network charts, then its per-VM timeline.
Type "/"
Type@120ms "wiki-05"
Enter
Sleep 600ms
Enter
Wait+Screen /CPU usage/
Sleep 2500ms
Screenshot docs/assets/vsfleet.png
Sleep 400ms
Type "1"
Wait+Screen /CPU ready/
Sleep 2000ms
Type "4"
Wait+Screen /Transmitted/
Sleep 2000ms
Type "h"
Wait+Screen /stored assessments/
Sleep 2200ms
Type "2"
Wait+Screen /LIVE from prod-vc/
Sleep 2500ms
Type "3"
Wait+Screen /snapshot-created/
Sleep 2500ms
Escape
Sleep 500ms
Escape
Sleep 500ms
Escape
Sleep 500ms

# Inspect the distributed switch, its wiring, and a port group's policies.
Type "6"
Wait+Screen /DVS-Production/
Sleep 1600ms
Enter
Wait+Screen /distributed switch/
Sleep 2400ms
Type "1"
Wait+Screen /PHYSICAL/
Sleep 2800ms
Enter
Wait+Screen /Teaming/
Sleep 2600ms
Escape
Sleep 400ms
Escape
Sleep 400ms
Type "v"
Wait+Screen /VLAN/
Sleep 2400ms
Escape
Sleep 400ms

# Open a vApp with nested members, allocation and startup order.
Type "7"
Type "/"
Type@110ms "api-stack"
Enter
Sleep 500ms
Enter
Wait+Screen /Members/
Sleep 2800ms
Type "s"
Wait+Screen /busiest first/
Sleep 2300ms
Escape
Sleep 400ms
Escape
Sleep 400ms

# Visit the second healthy vCenter, then widen a VM query to the estate.
Type "c"
Wait+Screen /Contexts/
Sleep 1000ms
Down
Enter
Wait+Screen /edge-vc  ·  2 connected/
Type "1"
Wait+Screen /10.42.0.211/
Sleep 1600ms
Type "a"
Wait+Screen /all 3 vCenters/
Sleep 1300ms
Type "/"
Type@140ms "api-01"
Sleep 1600ms
Tab
Wait+Screen /not searched/
Sleep 3000ms
Escape
Sleep 400ms
Escape
Sleep 400ms

# Finish on inventory. Never open the History workspace in this tour.
Sleep 1800ms
Hide
Type "q"
Sleep 500ms
Type `rm -rf -- "$demo_dir"`
Enter
