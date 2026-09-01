package scrape

import "testing"

// 実ページ（tomcat.2ch.sc/livejupiter/kako/ 2026-08-21 取得）の断片。
const scIndexFixture = `<TABLE BORDER=2>
<TR><TD>倉庫番号</TD><TD>格納数</TD><TD>速さ</TD><TD align='center'>subject.txt</TD></TR>
<tr><td><a target="_blank" href="o1787/">#livejupiter/1787</a></td><td align="right">214</td><td align="right">18.49</td><td align="right"><a href="o1787/subject.txt">subject.txt</a></td></tr>
<tr><td><a target="_blank" href="o1786/">#livejupiter/1786</a></td><td align="right">1284</td><td align="right">110.94</td><td align="right"><a href="o1786/subject.txt">subject.txt</a></td></tr>
<tr><td><a target="_blank" href="o1785/">#livejupiter/1785</a></td><td align="right">1407</td><td align="right">121.56</td><td align="right"><a href="o1785/subject.txt">subject.txt</a></td></tr>
</TABLE>`

func TestParseKakoIndex(t *testing.T) {
	got := ParseKakoIndex(scIndexFixture)
	want := []KakoDir{{1787, 214}, {1786, 1284}, {1785, 1407}}
	if len(got) != len(want) {
		t.Fatalf("倉庫数 = %d, want %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// 格納数の列が読めない板でも、subject.txt のリンクだけは拾って取得対象にする。
// Count = -1 は「格納数が分からない＝毎回取得する」の意。
func TestParseKakoIndexFallbackWithoutCount(t *testing.T) {
	got := ParseKakoIndex(`<ul><li><a href="o1234/subject.txt">subject.txt</a></li>
	<li><a href="o1233/subject.txt">subject.txt</a></li></ul>`)
	want := []KakoDir{{1234, -1}, {1233, -1}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseKakoIndexEmpty(t *testing.T) {
	if got := ParseKakoIndex(`<html><body>過去ログはありません</body></html>`); len(got) != 0 {
		t.Fatalf("got %+v, want 空", got)
	}
}

func TestParseSubject(t *testing.T) {
	// 実データ（o1787/subject.txt）から。数値文字参照・全角チルダ・末尾の二重スペースを含む
	in := "1787155791.dat<>安倍晋三「ペニス工房」←こいつ (2)\n" +
		"1787154018.dat<>&#129317;       &#9995;&#129402;あっ、ゆめちゃんじゃんー (4)\n" +
		"1787004202.dat<>誰もいない？  (2)\n" +
		"1786000001.dat<>イラスト描ける人～ (1001)\n" +
		"1786000002.dat<>【悲報】ワイ(31)、無職 (7)\n" +
		"1786000003.dat<>レス数が無い行\n" +
		"ゴミ行\n" +
		"\n"
	got := ParseSubject(in)
	want := []Entry{
		{1787155791, "安倍晋三「ペニス工房」←こいつ", 2},
		{1787154018, "🤥       ✋🥺あっ、ゆめちゃんじゃんー", 4},
		{1787004202, "誰もいない？", 2},
		{1786000001, "イラスト描ける人〜", 1001},  // ～(U+FF5E) → 〜(U+301C) へ寄せる
		{1786000002, "【悲報】ワイ(31)、無職", 7}, // 途中の括弧数字は残す
		{1786000003, "レス数が無い行", 0},
	}
	if len(got) != len(want) {
		t.Fatalf("件数 = %d, want %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v\n want %+v", i, got[i], want[i])
		}
	}
}

// タイトル自身が括弧数字で終わる場合、末尾のレス数と区別できない。
// sc は必ずレス数を付けるため「末尾の1つはレス数」と決め打つ。この挙動を固定する。
func TestParseSubjectTitleEndingWithParen(t *testing.T) {
	got := ParseSubject("1786000004.dat<>ドラゴンボール(改) (5)\n")
	if len(got) != 1 || got[0].Title != "ドラゴンボール(改)" || got[0].ResCount != 5 {
		t.Fatalf("got %+v", got)
	}
}

// cp932 と JIS X 0208 でコードポイントが食い違う6文字。5ch 側の表記へ寄せる。
// NFKC では吸収されない差なので、放置すると検索で一致しないタイトルが混ざる。
func TestFixSCNotation(t *testing.T) {
	cases := []struct{ in, want string }{
		{"チーズ～ケーキ", "チーズ〜ケーキ"},
		{"∥－￠￡￢", "‖−¢£¬"},
		{"チーズ〜ケーキ", "チーズ〜ケーキ"}, // 既に5ch表記なら変えない
		{"普通のタイトル", "普通のタイトル"},
		{"", ""},
	}
	for _, c := range cases {
		if got := FixSCNotation(c.in); got != c.want {
			t.Errorf("FixSCNotation(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSCURLs(t *testing.T) {
	base := "https://tomcat.2ch.sc/livejupiter/kako"
	if got := SCIndexURL(base); got != "https://tomcat.2ch.sc/livejupiter/kako/" {
		t.Errorf("SCIndexURL = %q", got)
	}
	if got := SCSubjectURL(base+"/", 1787); got != "https://tomcat.2ch.sc/livejupiter/kako/o1787/subject.txt" {
		t.Errorf("SCSubjectURL = %q", got)
	}
}

func TestKakoNoOf(t *testing.T) {
	if got := KakoNoOf(1787155791); got != 1787 {
		t.Errorf("KakoNoOf = %d, want 1787", got)
	}
	if got := KakoNoOf(999999999); got != 999 {
		t.Errorf("KakoNoOf = %d, want 999", got)
	}
}

// 実ページ（www.2ch.sc/bbsmenu.html 2026-08-21 取得）の断片。
// 属性値が引用符無し・プロトコル相対であることに注意。
const bbsMenuFixture = `<BR><BR><B>実況</B><BR>
<A HREF=//tomcat.2ch.sc/livejupiter/>なんでも実況(ジュピター)</A><br>
<A HREF=//viper.2ch.sc/news4vip/ TARGET="_top">ニュー速VIP</A><br>
<A HREF=//ai.2ch.sc/newsplus/>ニュース速報+</A><br>
<A HREF=//info.2ch.sc/guide/>2ch総合案内</A><br>
<A HREF=//tomcat.2ch.sc/livejupiter/>なんでも実況(重複)</A>`

func TestParseBBSMenu(t *testing.T) {
	got := ParseBBSMenu(bbsMenuFixture)
	want := map[string]string{
		"livejupiter": "tomcat.2ch.sc",
		"news4vip":    "viper.2ch.sc",
		"newsplus":    "ai.2ch.sc",
		"guide":       "info.2ch.sc", // 板以外も混ざる。突き合わせは呼び出し側の責務
	}
	if len(got) != len(want) {
		t.Fatalf("件数 = %d, want %d (%v)", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}
