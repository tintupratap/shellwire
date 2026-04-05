package main

import (
	"strings"
	"sync"
)

// cellAttr holds display attributes for a single cell.
type cellAttr uint8

const (
	attrBold    cellAttr = 1 << 0
	attrReverse cellAttr = 1 << 1 // inverse video (selection highlight in htop etc.)
	attrUnder   cellAttr = 1 << 2
)

type cell struct {
	ch   rune
	attr cellAttr
}

// screenLine is one row of cells.
type screenLine struct {
	cells []cell
}

func newScreenLine(cols int) screenLine {
	c := make([]cell, cols)
	for i := range c {
		c[i].ch = ' '
	}
	return screenLine{cells: c}
}

// termState is a VT100/ANSI terminal emulator.
type termState struct {
	mu      sync.Mutex
	cols    int
	rows    int
	lines   []screenLine
	curX    int
	curY    int
	savedX  int
	savedY  int
	altBuf  bool
	mainBuf []screenLine

	// current SGR attributes
	curAttr cellAttr

	// parser state: 0=normal 1=ESC 2=CSI 3=OSC 4=ESC-intermediate
	parseState int
	csiParams  string
	oscBuf     string

	appKeypad bool
	cursorVis bool
	Title     string
}

func newTermState(cols, rows int) *termState {
	t := &termState{cols: cols, rows: rows, cursorVis: true}
	t.lines = makeGrid(rows, cols)
	return t
}

func makeGrid(rows, cols int) []screenLine {
	g := make([]screenLine, rows)
	for i := range g {
		g[i] = newScreenLine(cols)
	}
	return g
}

func (t *termState) resize(cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	newLines := makeGrid(rows, cols)
	for y := 0; y < rows && y < len(t.lines); y++ {
		for x := 0; x < cols && x < len(t.lines[y].cells); x++ {
			newLines[y].cells[x] = t.lines[y].cells[x]
		}
	}
	t.lines = newLines
	t.cols = cols
	t.rows = rows
	if t.curX >= cols {
		t.curX = cols - 1
	}
	if t.curY >= rows {
		t.curY = rows - 1
	}
	// Also resize alt buffer if active
	if t.mainBuf != nil {
		newMain := makeGrid(rows, cols)
		for y := 0; y < rows && y < len(t.mainBuf); y++ {
			for x := 0; x < cols && x < len(t.mainBuf[y].cells); x++ {
				newMain[y].cells[x] = t.mainBuf[y].cells[x]
			}
		}
		t.mainBuf = newMain
	}
}

func (t *termState) write(data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range string(data) {
		if r < 0x80 {
			t.processByte(byte(r))
		} else if t.parseState == 0 {
			t.putChar(r)
		}
	}
}

func (t *termState) processByte(b byte) {
	switch t.parseState {
	case 0:
		switch b {
		case 0x1b:
			t.parseState = 1
		case '\r':
			t.curX = 0
		case '\n':
			t.curY++
			if t.curY >= t.rows {
				t.scrollUp(1)
				t.curY = t.rows - 1
			}
		case '\b':
			if t.curX > 0 {
				t.curX--
			}
		case '\t':
			t.curX = (t.curX/8 + 1) * 8
			if t.curX >= t.cols {
				t.curX = t.cols - 1
			}
		case 7, 0x0e, 0x0f:
		default:
			if b >= 0x20 {
				t.putChar(rune(b))
			}
		}
	case 1: // after ESC
		switch b {
		case '[':
			t.parseState = 2
			t.csiParams = ""
		case ']':
			t.parseState = 3
			t.oscBuf = ""
		case '\\':
			t.parseState = 0
		case 'M':
			if t.curY > 0 {
				t.curY--
			} else {
				t.scrollDown(1)
			}
			t.parseState = 0
		case '7':
			t.savedX, t.savedY = t.curX, t.curY
			t.parseState = 0
		case '8':
			t.curX, t.curY = t.savedX, t.savedY
			t.parseState = 0
		case '=':
			t.appKeypad = true
			t.parseState = 0
		case '>':
			t.appKeypad = false
			t.parseState = 0
		case 'c':
			t.fullReset()
			t.parseState = 0
		case '(', ')', '*', '+', '%', '#':
			t.parseState = 4
		default:
			t.parseState = 0
		}
	case 4: // ESC intermediate — consume final byte
		t.parseState = 0
	case 2: // CSI
		if (b >= '0' && b <= '9') || b == ';' || b == '?' || b == '>' || b == '<' || b == '=' {
			t.csiParams += string(b)
		} else if b >= 0x20 && b <= 0x2F {
			t.csiParams += string(b)
		} else {
			t.handleCSI(b)
			t.parseState = 0
		}
	case 3: // OSC
		if b == 7 {
			t.handleOSC(t.oscBuf)
			t.parseState = 0
		} else if b == 0x1b {
			t.handleOSC(t.oscBuf)
			t.parseState = 1
		} else {
			t.oscBuf += string(b)
		}
	}
}

func (t *termState) putChar(r rune) {
	if t.curY < 0 || t.curY >= t.rows {
		return
	}
	if t.curX >= t.cols {
		t.curX = 0
		t.curY++
		if t.curY >= t.rows {
			t.scrollUp(1)
			t.curY = t.rows - 1
		}
	}
	// Ensure lines slice is large enough (can be sparse before first full draw)
	for len(t.lines) <= t.curY {
		t.lines = append(t.lines, newScreenLine(t.cols))
	}
	// Ensure the target line has enough cells (e.g. after resize with old shorter lines)
	line := &t.lines[t.curY]
	for len(line.cells) <= t.curX {
		line.cells = append(line.cells, cell{ch: ' '})
	}
	line.cells[t.curX] = cell{ch: r, attr: t.curAttr}
	t.curX++
}

func (t *termState) blankLine() screenLine {
	l := screenLine{cells: make([]cell, t.cols)}
	for i := range l.cells {
		l.cells[i] = cell{ch: ' ', attr: t.curAttr}
	}
	return l
}

func (t *termState) scrollUp(n int) {
	for i := 0; i < n; i++ {
		t.lines = append(t.lines[1:], newScreenLine(t.cols))
	}
}

func (t *termState) scrollDown(n int) {
	for i := 0; i < n; i++ {
		t.lines = append([]screenLine{newScreenLine(t.cols)}, t.lines[:t.rows-1]...)
	}
}

func (t *termState) fullReset() {
	t.lines = makeGrid(t.rows, t.cols)
	t.curX, t.curY = 0, 0
	t.savedX, t.savedY = 0, 0
	t.curAttr = 0
	t.appKeypad = false
	t.cursorVis = true
	t.Title = ""
}

func (t *termState) handleCSI(cmd byte) {
	params := t.csiParams
	private := strings.HasPrefix(params, "?")
	if private {
		params = params[1:]
	}

	getParam := func(idx, def int) int {
		parts := strings.Split(params, ";")
		if idx >= len(parts) || parts[idx] == "" {
			return def
		}
		n := 0
		for _, c := range parts[idx] {
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
			}
		}
		return n
	}

	switch cmd {
	case 'A':
		t.curY -= getParam(0, 1)
		if t.curY < 0 {
			t.curY = 0
		}
	case 'B':
		t.curY += getParam(0, 1)
		if t.curY >= t.rows {
			t.curY = t.rows - 1
		}
	case 'C':
		t.curX += getParam(0, 1)
		if t.curX >= t.cols {
			t.curX = t.cols - 1
		}
	case 'D':
		t.curX -= getParam(0, 1)
		if t.curX < 0 {
			t.curX = 0
		}
	case 'E':
		t.curY += getParam(0, 1)
		t.curX = 0
		if t.curY >= t.rows {
			t.curY = t.rows - 1
		}
	case 'F':
		t.curY -= getParam(0, 1)
		t.curX = 0
		if t.curY < 0 {
			t.curY = 0
		}
	case 'G':
		t.curX = getParam(0, 1) - 1
		if t.curX < 0 {
			t.curX = 0
		}
	case 'H', 'f':
		row := getParam(0, 1) - 1
		col := getParam(1, 1) - 1
		if row < 0 {
			row = 0
		}
		if col < 0 {
			col = 0
		}
		if row >= t.rows {
			row = t.rows - 1
		}
		if col >= t.cols {
			col = t.cols - 1
		}
		t.curY, t.curX = row, col
	case 'J':
		switch getParam(0, 0) {
		case 0:
			for x := t.curX; x < t.cols; x++ {
				t.lines[t.curY].cells[x] = cell{ch: ' ', attr: t.curAttr}
			}
			for y := t.curY + 1; y < t.rows; y++ {
				t.lines[y] = t.blankLine()
			}
		case 1:
			for y := 0; y < t.curY; y++ {
				t.lines[y] = t.blankLine()
			}
			for x := 0; x <= t.curX && x < t.cols; x++ {
				t.lines[t.curY].cells[x] = cell{ch: ' ', attr: t.curAttr}
			}
		case 2, 3:
			t.lines = makeGrid(t.rows, t.cols)
		}
	case 'K':
		switch getParam(0, 0) {
		case 0:
			for x := t.curX; x < t.cols; x++ {
				t.lines[t.curY].cells[x] = cell{ch: ' ', attr: t.curAttr}
			}
		case 1:
			for x := 0; x <= t.curX && x < t.cols; x++ {
				t.lines[t.curY].cells[x] = cell{ch: ' ', attr: t.curAttr}
			}
		case 2:
			t.lines[t.curY] = t.blankLine()
		}
	case 'L':
		n := getParam(0, 1)
		for i := 0; i < n; i++ {
			t.lines = append(t.lines[:t.curY], append([]screenLine{newScreenLine(t.cols)}, t.lines[t.curY:t.rows-1]...)...)
		}
	case 'M':
		n := getParam(0, 1)
		for i := 0; i < n && t.curY < t.rows; i++ {
			t.lines = append(t.lines[:t.curY], append(t.lines[t.curY+1:], newScreenLine(t.cols))...)
		}
	case 'P':
		n := getParam(0, 1)
		line := t.lines[t.curY].cells
		copy(line[t.curX:], line[t.curX+n:])
		for x := t.cols - n; x < t.cols; x++ {
			line[x] = cell{ch: ' '}
		}
	case 'S':
		t.scrollUp(getParam(0, 1))
	case 'T':
		t.scrollDown(getParam(0, 1))
	case 'd':
		row := getParam(0, 1) - 1
		if row < 0 {
			row = 0
		}
		if row >= t.rows {
			row = t.rows - 1
		}
		t.curY = row
	case 'h':
		if private {
			switch getParam(0, 0) {
			case 1:
				t.appKeypad = true
			case 25:
				t.cursorVis = true
			case 47, 1047, 1049:
				if !t.altBuf {
					t.mainBuf = t.lines
					t.lines = makeGrid(t.rows, t.cols)
					t.altBuf = true
				}
			}
		}
	case 'l':
		if private {
			switch getParam(0, 0) {
			case 1:
				t.appKeypad = false
			case 25:
				t.cursorVis = false
			case 47, 1047, 1049:
				if t.altBuf {
					t.lines = t.mainBuf
					t.mainBuf = nil
					t.altBuf = false
				}
			}
		}
	case 'm': // SGR
		t.handleSGR(params)
	case 'r': // scroll region — ignore
	case 's':
		t.savedX, t.savedY = t.curX, t.curY
	case 'u':
		t.curX, t.curY = t.savedX, t.savedY
	}
}

func (t *termState) handleSGR(params string) {
	if params == "" {
		t.curAttr = 0
		return
	}
	parts := strings.Split(params, ";")
	for _, p := range parts {
		n := 0
		for _, c := range p {
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
			}
		}
		switch n {
		case 0:
			t.curAttr = 0
		case 1:
			t.curAttr |= attrBold
		case 4:
			t.curAttr |= attrUnder
		case 7:
			t.curAttr |= attrReverse
		case 22:
			t.curAttr &^= attrBold
		case 24:
			t.curAttr &^= attrUnder
		case 27:
			t.curAttr &^= attrReverse
		// color codes (30-37, 40-47, 90-97, 100-107, 38, 48) — ignore color, keep attrs
		}
	}
}

func (t *termState) handleOSC(s string) {
	if strings.HasPrefix(s, "0;") || strings.HasPrefix(s, "2;") {
		t.Title = s[2:]
	}
}

// snapshot returns a copy of the screen state for rendering (avoids holding lock during render).
type screenSnapshot struct {
	lines     []screenLine
	curX, curY int
	cursorVis  bool
	cols, rows int
}

func (t *termState) snapshot() screenSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := make([]screenLine, len(t.lines))
	for i, l := range t.lines {
		cells := make([]cell, len(l.cells))
		copy(cells, l.cells)
		lines[i] = screenLine{cells: cells}
	}
	return screenSnapshot{
		lines:     lines,
		curX:      t.curX,
		curY:      t.curY,
		cursorVis: t.cursorVis,
		cols:      t.cols,
		rows:      t.rows,
	}
}
