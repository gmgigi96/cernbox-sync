package migrate

import (
	"slices"
	"testing"
)

func TestSettings_GroupsAndPlainValues(t *testing.T) {
	s := parseSettings("[General]\nclientVersion=5.3.2.15486\n\n[Accounts]\n0\\Folders\\abc\\localPath=/home/u/cernbox/\n0\\Folders\\abc\\paused=false\n0\\display-name=gdelmont\n")
	if got := s.str("General/clientVersion"); got != "5.3.2.15486" {
		t.Errorf("clientVersion = %q", got)
	}
	if got := s.str("Accounts/0/Folders/abc/localPath"); got != "/home/u/cernbox/" {
		t.Errorf("localPath = %q", got)
	}
	if s.boolean("Accounts/0/Folders/abc/paused", true) {
		t.Error("paused should be false")
	}
	if !s.boolean("Accounts/0/Folders/abc/missing", true) {
		t.Error("an unset key must yield the default")
	}
	if got := s.str("Accounts/0/display-name"); got != "gdelmont" {
		t.Errorf("display-name = %q", got)
	}
}

func TestSettings_PercentEscapedKeys(t *testing.T) {
	s := parseSettings("[Credentials]\nfoo%3Abar%U00E9\\http\\user=x\n")
	if _, ok := s["Credentials/foo:baré/http/user"]; !ok {
		t.Errorf("unexpected keys: %v", s)
	}
}

func TestSettings_QUrlVariant(t *testing.T) {
	// Verbatim from a CERNBox desktop client 5.3 configuration.
	s := parseSettings(`[Accounts]
0\Folders\x\davUrl=@Variant(\0\0\0\x11\0\0\0\x46https://cernbox.cern.ch/cernbox/desktop/remote.php/dav/files/gdelmont/)
`)
	if got := s["Accounts/0/Folders/x/davUrl"].url(); got != "https://cernbox.cern.ch/cernbox/desktop/remote.php/dav/files/gdelmont/" {
		t.Errorf("davUrl = %q", got)
	}
}

func TestSettings_HexEscapesMapToBytes(t *testing.T) {
	// Qt escapes a hex digit that follows a \x escape, so `\x44\x37` are
	// separate characters; `\xe` is a single-digit escape.
	s := parseSettings(`[A]
uuid=@Variant(\0\0\0\x7f\x1f\xafS\xeL\x44\x37\"\rl)
`)
	v := s["A/uuid"]
	want := []byte{0, 0, 0, 0x7f, 0x1f, 0xaf, 'S', 0x0e, 'L', 'D', '7', '"', '\r', 'l'}
	if v.kind != kindVariant || !slices.Equal(v.bytes, want) {
		t.Errorf("uuid = %v %v", v.kind, v.bytes)
	}
	if v.url() != "" {
		t.Error("a QUuid is not a URL")
	}
}

func TestSettings_QuotesAndLists(t *testing.T) {
	s := parseSettings("[A]\npath=\"/home/u/Projects, old \"\nplain = trimmed  \n[General]\nfilter=FatalError, NormalError,  Conflict\n")
	if got := s.str("A/path"); got != "/home/u/Projects, old " {
		t.Errorf("path = %q", got)
	}
	if got := s.str("A/plain"); got != "trimmed" {
		t.Errorf("plain = %q", got)
	}
	if v := s["General/filter"]; v.kind != kindStringList || !slices.Equal(v.list, []string{"FatalError", "NormalError", "Conflict"}) {
		t.Errorf("filter = %v %q", v.kind, v.list)
	}
}

func TestSettings_UTF16EscapesAndRawUTF8(t *testing.T) {
	// Qt 5 escapes non-ASCII as UTF-16 code units; Qt 6 writes UTF-8.
	s := parseSettings("[A]\nqt5=/home/u/R\\xe9sum\\xe9s\nqt6=/home/u/Résumés\nemoji=\\xd83d\\xde00\n")
	for key, want := range map[string]string{"A/qt5": "/home/u/Résumés", "A/qt6": "/home/u/Résumés", "A/emoji": "😀"} {
		if got := s.str(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestSettings_SpecialValuesEscapesAndComments(t *testing.T) {
	s := parseSettings("\ufeff; comment\n[A]\nat=@@literal\ninv=@Invalid()\nbytes=@ByteArray(\\x1\\xd9)\nesc=a\\\\b\\tc\\\"d\nempty=\nstr=@String(x)\ncut=value ; trailing comment\nkept=\"a;b\"\n#hash=not a comment\n")
	for key, want := range map[string]string{
		"A/at": "@literal", "A/esc": "a\\b\tc\"d", "A/empty": "", "A/str": "x",
		"A/cut": "value", "A/kept": "a;b", "A/#hash": "not a comment",
	} {
		if got := s.str(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if s["A/inv"].kind != kindInvalid {
		t.Error("inv should be invalid")
	}
	if v := s["A/bytes"]; v.kind != kindByteArray || !slices.Equal(v.bytes, []byte{0x01, 0xd9}) {
		t.Errorf("bytes = %v", v.bytes)
	}
}

func TestSettings_ContinuationLinesAndCRLF(t *testing.T) {
	s := parseSettings("[A]\r\nlong=abc\\\r\ndef\r\nend=x\\\\\r\n")
	if got := s.str("A/long"); got != "abcdef" {
		t.Errorf("long = %q", got)
	}
	if got := s.str("A/end"); got != `x\` {
		t.Errorf("end = %q", got)
	}
}
