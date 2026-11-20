package proto

import "testing"

func TestLimitsAreTheValuesOfSectionFour(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"MaxMessage", MaxMessage, 4194304},
		{"FramePiece", FramePiece, 1 << 20},
		{"MaxRow", MaxRow, 128 * 1024},
		{"MaxPaste", MaxPaste, 256 * 1024},
		{"MaxClip", MaxClip, 1 << 20},
		{"MaxText", MaxText, 1 << 20},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if Gen != 1 {
		t.Errorf("Gen = %d", Gen)
	}
}

func TestSpeaksGenerationsOneAndTwo(t *testing.T) {
	for g, want := range map[int]bool{0: false, 1: true, 2: true, 3: false, -1: false} {
		if Speaks(g) != want {
			t.Errorf("Speaks(%d) = %v, want %v", g, !want, want)
		}
	}
}

func TestClampSizeKeepsThePaneAreaInsideTheLimits(t *testing.T) {
	for _, c := range []struct{ cols, rows, wc, wr int }{
		{80, 24, 80, 24},
		{0, 0, 2, 2},
		{-5, 1, 2, 2},
		{1001, 501, 1000, 500},
		{1000, 500, 1000, 500},
		{2, 2, 2, 2},
	} {
		if c2, r2 := ClampSize(c.cols, c.rows); c2 != c.wc || r2 != c.wr {
			t.Errorf("ClampSize(%d, %d) = %d, %d; want %d, %d", c.cols, c.rows, c2, r2, c.wc, c.wr)
		}
	}
}
