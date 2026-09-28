package activity

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestLogKeepsTheNewest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.jsonl")
	l := Open(path)
	for i := 0; i < keep+50; i++ {
		l.Add(Entry{User: "Ava", ServerID: strconv.Itoa(i % 2), Text: strconv.Itoa(i)})
	}
	got := l.List(3, func(e Entry) bool { return e.ServerID == "1" })
	if len(got) != 3 || got[0].Text != strconv.Itoa(keep+49) || got[1].Text != strconv.Itoa(keep+47) {
		t.Errorf("list: %+v", got)
	}
	l2 := Open(path)
	if all := l2.List(keep*2, nil); len(all) != keep || all[0].Text != strconv.Itoa(keep+49) {
		t.Errorf("reopened: %d", len(all))
	}
	// A big file is rewritten with just the newest entries.
	l2.size = maxFile
	l2.Add(Entry{User: "Sam", Text: "last"})
	st, _ := os.Stat(path)
	if st.Size() >= maxFile {
		t.Errorf("not rewritten: %d", st.Size())
	}
	if all := Open(path).List(1, nil); len(all) != 1 || all[0].Text != "last" {
		t.Errorf("after rewrite: %+v", all)
	}
}
