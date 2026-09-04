package editor

import "testing"

func TestDeleteToLineEnd(t *testing.T) {
	cases := []struct {
		name string
		text string
		at   int
		want string
	}{
		{"mid line", "hello world\nnext\n", 5, "hello\nnext\n"},
		{"start of line", "hello world\nnext\n", 0, "\nnext\n"},
		// At the end of a line there is nothing left to take, so it joins the
		// next one; without this, pressing the chord twice does nothing the
		// second time and the line below never arrives.
		{"end of line joins the next", "hello\nnext\n", 5, "hellonext\n"},
		{"end of document", "hello", 5, "hello"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newTestPane(c.text)
			p.Cursors.Set(c.at, c.at)
			p.DeleteToLineEnd()
			if got := p.File.Text(); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// A selection is deleted as a selection, the same as every other editing action.
func TestDeleteToLineEndWithSelection(t *testing.T) {
	p := newTestPane("hello world\n")
	p.Cursors.Set(0, 5)
	p.DeleteToLineEnd()
	if got := p.File.Text(); got != " world\n" {
		t.Errorf("got %q", got)
	}
}

// Repeating it clears the line and then pulls each following line up.
func TestDeleteToLineEndRepeats(t *testing.T) {
	p := newTestPane("a\nb\nc\n")
	p.Cursors.Set(0, 0)
	for i := 0; i < 4; i++ {
		p.DeleteToLineEnd()
	}
	if got := p.File.Text(); got != "c\n" {
		t.Errorf("got %q, want the first two lines gone", got)
	}
}
