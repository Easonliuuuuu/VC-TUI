package tui

import (
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// WelcomeKind says why the welcome animation is playing.
type WelcomeKind int

const (
	// WelcomeNone opens the interface straight away.
	WelcomeNone WelcomeKind = iota
	// WelcomeFirstRun greets someone who has never run vsfleet here.
	WelcomeFirstRun
	// WelcomeUpdated marks the first run after an upgrade.
	WelcomeUpdated
)

// Welcome is the once-per-version greeting played before the interface. The
// zero value plays nothing. Deciding whether it plays at all — the
// remembered version, VSFLEET_NO_WELCOME, CI — is the caller's job; the
// interface itself only declines to draw it in a terminal too small for the
// scene, and says the same thing in its message line instead.
type Welcome struct {
	Kind WelcomeKind
	// Version is the running release, such as "0.10.0" or "dev".
	Version string
	// Previous is the release last welcomed. It is empty for a first run and
	// for an upgrade from a release that predates the welcome.
	Previous string
}

// The scene is drawn on a fixed grid centred in the terminal. At 50ms a
// frame it runs 2.8 seconds, and the load the interface starts in Init keeps
// going underneath the whole time, so the animation costs no startup time.
const (
	welcomeCols     = 64
	welcomeRows     = 18
	welcomeFrames   = 56
	welcomeInterval = 50 * time.Millisecond
	welcomeRepo     = "github.com/Easonliuuuuu/vsfleet"
)

type welcomeTickMsg struct{}

func welcomeTick() tea.Cmd {
	return tea.Tick(welcomeInterval, func(time.Time) tea.Msg { return welcomeTickMsg{} })
}

// welcomeModel plays the welcome over the interface and then gets out of the
// way: it hands Bubble Tea the next model — the interface itself, or the
// upgrade prompt in front of it — so nothing after the last frame passes
// through it. Until then every message other than a key or its own tick goes
// on to that model, so the loads, credential prompts and spinner ticks the
// interface started in Init carry on underneath.
type welcomeModel struct {
	inner  *Model
	next   tea.Model
	w      Welcome
	frame  int
	width  int
	height int
	sized  bool
}

func newWelcomeModel(inner *Model, w Welcome) *welcomeModel {
	return &welcomeModel{inner: inner, next: inner, w: w}
}

func (w *welcomeModel) base() *Model { return w.inner }

func (w *welcomeModel) Init() tea.Cmd {
	return tea.Batch(w.inner.Init(), welcomeTick())
}

func (w *welcomeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width, w.height, w.sized = msg.Width, msg.Height, true
		cmd := w.forward(msg)
		if msg.Width < welcomeCols || msg.Height < welcomeRows {
			w.inner.setMessage(w.note(), false)
			return w.next, cmd
		}
		return w, cmd
	case welcomeTickMsg:
		w.frame++
		if w.frame >= welcomeFrames {
			return w.next, nil
		}
		return w, welcomeTick()
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return w.next.Update(msg)
		}
		// Any other key skips the rest, and is not passed on: it was pressed
		// to stop the animation, not to act on a table nobody has seen yet.
		return w.next, nil
	case tea.MouseMsg:
		return w, nil
	}
	return w, w.forward(msg)
}

func (w *welcomeModel) forward(msg tea.Msg) tea.Cmd {
	next, cmd := w.next.Update(msg)
	w.next = next
	return cmd
}

func (w *welcomeModel) View() string {
	if !w.sized {
		return ""
	}
	return lipgloss.Place(w.width, w.height, lipgloss.Center, lipgloss.Center, w.scene(w.frame))
}

// welcomeSpan is one differently styled piece of a line of the scene.
type welcomeSpan struct {
	text  string
	style lipgloss.Style
}

func displayVersion(v string) string {
	if v != "" && v[0] >= '0' && v[0] <= '9' {
		return "v" + v
	}
	return v
}

func (w *welcomeModel) released() bool { return w.w.Version != "" && w.w.Version != "dev" }

func (w *welcomeModel) headline() []welcomeSpan {
	th := w.inner.theme
	bright := th.text.Bold(true)
	switch {
	case w.w.Kind == WelcomeFirstRun:
		return []welcomeSpan{{"Welcome aboard.", bright}}
	case !w.released():
		return []welcomeSpan{{"Running a development build", bright}}
	case w.w.Previous == "" || w.w.Previous == "dev":
		return []welcomeSpan{{"Updated to ", bright}, {displayVersion(w.w.Version), th.ok}}
	default:
		return []welcomeSpan{
			{"Updated  ", bright}, {displayVersion(w.w.Previous), th.dim},
			{" → ", th.dim}, {displayVersion(w.w.Version), th.ok},
		}
	}
}

func (w *welcomeModel) subline() string {
	switch {
	case w.w.Kind == WelcomeFirstRun:
		return "Let's commission your first vCenter."
	case w.released():
		return w.releaseURL()
	default:
		return ""
	}
}

func (w *welcomeModel) releaseURL() string {
	return welcomeRepo + "/releases/tag/" + displayVersion(w.w.Version)
}

// note is the welcome as a single line, for the message line of a terminal
// too small to draw the scene in.
func (w *welcomeModel) note() string {
	if w.w.Kind == WelcomeFirstRun {
		return "Welcome aboard. Let's commission your first vCenter."
	}
	var b strings.Builder
	for _, s := range w.headline() {
		b.WriteString(s.text)
	}
	head := strings.Join(strings.Fields(b.String()), " ")
	if !w.released() {
		return head
	}
	return head + " · what's new: " + w.releaseURL()
}

// shipNames is one label per ship: every configured context up to four,
// always including the one the interface opens on. A first run gets a single
// ship for the vCenter about to be added, labelled "" here.
func (w *welcomeModel) shipNames() []string {
	if w.w.Kind == WelcomeFirstRun || len(w.inner.states) == 0 {
		return []string{""}
	}
	names := make([]string, 0, 4)
	for _, st := range w.inner.states {
		if len(names) == 4 {
			break
		}
		names = append(names, st.cc.Name)
	}
	if w.inner.selected >= len(names) && w.inner.selected < len(w.inner.states) {
		names[len(names)-1] = w.inner.states[w.inner.selected].cc.Name
	}
	return names
}

func (w *welcomeModel) selectedName() string {
	if w.inner.selected < len(w.inner.states) {
		return w.inner.states[w.inner.selected].cc.Name
	}
	return ""
}

var (
	welcomeLogo  = buildWelcomeLogo("VSFLEET")
	welcomeNoise = []rune(`░▒▓<>/\|+*#%=`)
	welcomeWave  = []rune("~~^~--")
	welcomeShip  = []string{`  |\  `, `  |_\ `, `\____/`}
)

func buildWelcomeLogo(word string) [][]rune {
	font := map[rune][5]string{
		'V': {"█   █", "█   █", "█   █", " █ █ ", "  █  "},
		'S': {" ████", "█    ", " ███ ", "    █", "████ "},
		'F': {"█████", "█    ", "████ ", "█    ", "█    "},
		'L': {"█    ", "█    ", "█    ", "█    ", "█████"},
		'E': {"█████", "█    ", "████ ", "█    ", "█████"},
		'T': {"█████", "  █  ", "  █  ", "  █  ", "  █  "},
	}
	rows := make([][]rune, 5)
	for r := range rows {
		parts := make([]string, 0, len(word))
		for _, l := range word {
			parts = append(parts, font[l][r])
		}
		rows[r] = []rune(strings.Join(parts, "  "))
	}
	return rows
}

// welcomeHash is a fixed integer hash, so the noise the wordmark resolves out
// of is the same every run and every test.
func welcomeHash(a, b, c int) uint32 {
	x := uint32(a)*374761393 + uint32(b)*668265263 + uint32(c)*1274126177
	x = (x ^ (x >> 13)) * 1274126177
	return x ^ (x >> 16)
}

type welcomeCell struct {
	r     rune
	style *lipgloss.Style
}

type welcomeCanvas [welcomeRows][welcomeCols]welcomeCell

func (c *welcomeCanvas) set(x, y int, r rune, st *lipgloss.Style) {
	if x < 0 || y < 0 || x >= welcomeCols || y >= welcomeRows {
		return
	}
	c[y][x] = welcomeCell{r, st}
}

// put writes s from column x. A transparent put leaves the cells under its
// spaces alone, which is how a ship sits on the sea.
func (c *welcomeCanvas) put(x, y int, s string, st *lipgloss.Style, transparent bool) {
	for i, r := range []rune(s) {
		if transparent && r == ' ' {
			continue
		}
		c.set(x+i, y, r, st)
	}
}

func (c *welcomeCanvas) centre(y int, s string, st *lipgloss.Style) {
	c.put((welcomeCols-len([]rune(s)))/2, y, s, st, false)
}

func (c *welcomeCanvas) render() string {
	lines := make([]string, welcomeRows)
	for y := range c {
		var b, run strings.Builder
		var cur *lipgloss.Style
		flush := func() {
			if run.Len() == 0 {
				return
			}
			if cur == nil {
				b.WriteString(run.String())
			} else {
				b.WriteString(cur.Render(run.String()))
			}
			run.Reset()
		}
		for _, cell := range c[y] {
			if cell.style != cur {
				flush()
				cur = cell.style
			}
			if cell.r == 0 {
				run.WriteRune(' ')
			} else {
				run.WriteRune(cell.r)
			}
		}
		flush()
		lines[y] = b.String()
	}
	return strings.Join(lines, "\n")
}

// scene draws frame t. The wordmark resolves left to right out of noise
// (frames 0-31) and a glint crosses it (from 32); the headline types itself
// out from 28 and the release link appears at 40; meanwhile one ship per
// vCenter sails in from the left, a few frames apart, and anchors above its
// name.
func (w *welcomeModel) scene(t int) string {
	th := w.inner.theme
	bright := th.text.Bold(true)
	var c welcomeCanvas

	const logoX, logoY = 8, 1
	for r, row := range welcomeLogo {
		for col, ch := range row {
			if ch != '█' {
				continue
			}
			resolve := 6 + col*22/len(row) + int(welcomeHash(r, col, 1)%4)
			appear := col * 10 / len(row)
			switch {
			case t >= resolve:
				st := &th.accent
				if t >= 32 && absInt((t-32)*4-col-r*2) < 2 {
					st = &bright
				}
				c.set(logoX+col, logoY+r, '█', st)
			case t >= appear:
				noise := welcomeNoise[welcomeHash(t, r*97, col)%uint32(len(welcomeNoise))]
				c.set(logoX+col, logoY+r, noise, &th.faint)
			}
		}
	}

	head := w.headline()
	total := 0
	for _, s := range head {
		total += len([]rune(s.text))
	}
	shown := min(max((t-28)*2, 0), total)
	x := (welcomeCols - total) / 2
	for i := range head {
		for _, r := range head[i].text {
			if shown == 0 {
				break
			}
			c.set(x, 7, r, &head[i].style)
			x++
			shown--
		}
	}
	if sub := w.subline(); sub != "" && t >= 40 {
		c.centre(8, sub, &th.dim)
	}

	names := w.shipNames()
	selected := w.selectedName()
	start := (welcomeCols - (len(names)*13 - 7)) / 2
	for i, name := range names {
		// The ship with the farthest berth sets off first, so none overtakes
		// another on the way in.
		target := start + i*13
		launch := 3 + (len(names)-1-i)*4
		p := math.Min(math.Max(float64(t-launch)/20, 0), 1)
		eased := 1 - math.Pow(1-p, 3)
		sx := -8 + int(math.Round(float64(target+8)*eased))
		for r, row := range welcomeShip {
			st := &bright
			if r == len(welcomeShip)-1 {
				st = &th.text
			}
			c.put(sx, 10+r, row, st, true)
		}
		if p < 1 {
			continue
		}
		label, st := name, &th.dim
		if label == "" {
			label = "your first vCenter"
		} else if label == selected {
			st = &th.accent
		}
		if l := []rune(label); len(l) > 12 {
			label = string(l[:11]) + "…"
		}
		c.put(target+3-len([]rune(label))/2, 14, label, st, false)
	}
	for x := 0; x < welcomeCols; x++ {
		if c[13][x].r != 0 {
			continue
		}
		n := len(welcomeWave)
		r := welcomeWave[((x-t/2)%n+n)%n]
		st := &th.faint
		if r == '^' {
			st = &th.accent
		}
		c.set(x, 13, r, st)
	}

	hint := &th.faint
	if t%20 >= 14 {
		hint = &th.rule
	}
	c.centre(welcomeRows-1, "any key to skip", hint)
	return c.render()
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
