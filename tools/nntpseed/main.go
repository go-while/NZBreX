// Command nntpseed generates deterministic yEnc test articles, feeds them to a
// local NNTP server with IHAVE, and writes a matching .nzb file.
//
// It exists so NZBreX can be tested against a local go-while/nntp-server
// instead of live Usenet providers. The same -seed always produces the same
// articles, so several servers can be filled from one data set, each missing
// a different subset (-omit-every).
//
// Example:
//
//	nntpseed -addr 127.0.0.1:11119 -files 2 -parts 20 -broken-crc 1 -broken-yenc 1 -nzb test.nzb
//	nntpseed -addr 127.0.0.1:11120 -files 2 -parts 20 -omit-every 3 -nzb /dev/null
package main

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"hash/crc32"
	"log"
	"math/rand"
	"net"
	"net/textproto"
	"os"
	"time"
)

const lineLen = 128

type article struct {
	MsgID  string `json:"msgid"` // without angle brackets
	File   int    `json:"file"`
	Part   int    `json:"part"`
	Bytes  int    `json:"bytes"`
	Kind   string `json:"kind"` // ok | badcrc | truncated
	Sent   bool   `json:"sent"`
	Result string `json:"result"`
	body   []byte
	name   string
	total  int
	begin  int
	size   int
	fcrc   uint32
}

func main() {
	addr := flag.String("addr", "127.0.0.1:11119", "NNTP server address (must accept IHAVE from this host)")
	group := flag.String("group", "alt.binaries.nzbrex.test", "newsgroup for the articles")
	seed := flag.Int64("seed", 1, "random seed; same seed = same articles")
	files := flag.Int("files", 1, "number of files")
	parts := flag.Int("parts", 10, "parts per file")
	partSize := flag.Int("partsize", 64*1024, "decoded bytes per part")
	brokenCRC := flag.Int("broken-crc", 0, "number of parts (from the end of the last file) with a wrong pcrc32")
	brokenYenc := flag.Int("broken-yenc", 0, "number of parts (before the bad-crc ones) with the =yend line cut off")
	omitEvery := flag.Int("omit-every", 0, "if > 0: do not send every Nth article (1-based), to leave gaps on this server")
	nzbOut := flag.String("nzb", "", "write the .nzb file here (empty: don't write)")
	manifest := flag.String("manifest", "", "write a JSON list of all articles and what happened to them")
	dry := flag.Bool("dry", false, "generate only, don't connect")
	flag.Parse()

	rng := rand.New(rand.NewSource(*seed))
	var arts []*article
	for f := 1; f <= *files; f++ {
		name := fmt.Sprintf("nzbrex-test-s%d-f%d.bin", *seed, f)
		data := make([]byte, *parts**partSize)
		rng.Read(data)
		fcrc := crc32.ChecksumIEEE(data)
		for p := 1; p <= *parts; p++ {
			begin := (p - 1) * *partSize
			arts = append(arts, &article{
				MsgID: fmt.Sprintf("s%d.f%d.p%d.%s@nzbrex.test", *seed, f, p, randTag(rng)),
				File:  f, Part: p, Kind: "ok",
				body: data[begin : begin+*partSize], name: name, total: *parts,
				begin: begin + 1, size: len(data), fcrc: fcrc,
			})
		}
	}
	// Mark broken articles at the end of the list so the first files stay clean.
	n := len(arts)
	for i := 0; i < *brokenCRC && i < n; i++ {
		arts[n-1-i].Kind = "badcrc"
	}
	for i := 0; i < *brokenYenc && *brokenCRC+i < n; i++ {
		arts[n-1-*brokenCRC-i].Kind = "truncated"
	}

	if !*dry {
		if err := feed(*addr, *group, arts, *omitEvery); err != nil {
			log.Fatalf("feed: %v", err)
		}
	}
	if *nzbOut != "" {
		if err := writeNZB(*nzbOut, *group, arts); err != nil {
			log.Fatalf("nzb: %v", err)
		}
	}
	if *manifest != "" {
		b, _ := json.MarshalIndent(arts, "", " ")
		if err := os.WriteFile(*manifest, b, 0o644); err != nil {
			log.Fatalf("manifest: %v", err)
		}
	}
	counts := map[string]int{}
	for _, a := range arts {
		counts[a.Kind]++
		if a.Sent {
			counts["sent:"+a.Result]++
		}
	}
	log.Printf("articles=%d %v", len(arts), counts)
}

func randTag(r *rand.Rand) string {
	const abc = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = abc[r.Intn(len(abc))]
	}
	return string(b)
}

// encodeArticle returns the article lines (headers, blank line, yEnc body),
// without dot-stuffing; textproto.DotWriter does that.
func encodeArticle(a *article, group string) []string {
	date := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(a.File*1000+a.Part) * time.Second)
	lines := []string{
		"From: nzbrex-test <test@nzbrex.test>",
		"Newsgroups: " + group,
		fmt.Sprintf("Subject: [%d] \"%s\" yEnc (%d/%d)", a.File, a.name, a.Part, a.total),
		"Message-ID: <" + a.MsgID + ">",
		"Date: " + date.Format(time.RFC1123Z),
		"Path: nzbrex.test!not-for-mail",
		"",
		fmt.Sprintf("=ybegin part=%d total=%d line=%d size=%d name=%s", a.Part, a.total, lineLen, a.size, a.name),
		fmt.Sprintf("=ypart begin=%d end=%d", a.begin, a.begin+len(a.body)-1),
	}
	body := yencLines(a.body)
	lines = append(lines, body...)
	pcrc := crc32.ChecksumIEEE(a.body)
	if a.Kind == "badcrc" {
		pcrc ^= 0xdeadbeef
	}
	yend := fmt.Sprintf("=yend size=%d part=%d pcrc32=%08x", len(a.body), a.Part, pcrc)
	if a.Part == a.total {
		yend += fmt.Sprintf(" crc32=%08x", a.fcrc)
	}
	switch a.Kind {
	case "truncated":
		// cut the body in half and drop =yend
		lines = lines[:len(lines)-len(body)/2]
	default:
		lines = append(lines, yend)
	}
	return lines
}

// yencLines encodes data as yEnc lines of at most lineLen output characters.
func yencLines(data []byte) []string {
	var out []string
	var line []byte
	for i, c := range data {
		e := byte(c + 42)
		esc := e == 0 || e == '\n' || e == '\r' || e == '='
		if len(line) == 0 && (e == '\t' || e == ' ' || e == '.') {
			esc = true
		}
		if (len(line) >= lineLen-1 || i == len(data)-1) && (e == '\t' || e == ' ') {
			esc = true // no trailing whitespace
		}
		if esc {
			line = append(line, '=', e+64)
		} else {
			line = append(line, e)
		}
		if len(line) >= lineLen {
			out = append(out, string(line))
			line = line[:0]
		}
	}
	if len(line) > 0 {
		out = append(out, string(line))
	}
	return out
}

func feed(addr, group string, arts []*article, omitEvery int) error {
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	tp := textproto.NewConn(conn)
	if _, _, err := tp.ReadCodeLine(200); err != nil {
		return fmt.Errorf("welcome: %w", err)
	}
	for i, a := range arts {
		if omitEvery > 0 && (i+1)%omitEvery == 0 {
			continue
		}
		code, msg, err := ihave(tp, a, group)
		if err != nil {
			return fmt.Errorf("IHAVE <%s>: %w", a.MsgID, err)
		}
		a.Sent = true
		a.Result = fmt.Sprintf("%d", code)
		if code != 235 && code != 435 {
			log.Printf("IHAVE <%s> kind=%s: %d %s", a.MsgID, a.Kind, code, msg)
		}
	}
	tp.PrintfLine("QUIT")
	return nil
}

func ihave(tp *textproto.Conn, a *article, group string) (int, string, error) {
	if err := tp.PrintfLine("IHAVE <%s>", a.MsgID); err != nil {
		return 0, "", err
	}
	code, msg, err := tp.ReadCodeLine(335)
	if err != nil {
		return code, msg, nil // 435/436/437: server doesn't want it
	}
	dw := tp.DotWriter()
	bw := bufio.NewWriter(dw)
	for _, l := range encodeArticle(a, group) {
		bw.WriteString(l)
		bw.WriteString("\n")
	}
	if err := bw.Flush(); err != nil {
		return 0, "", err
	}
	if err := dw.Close(); err != nil {
		return 0, "", err
	}
	code, msg, err = tp.ReadCodeLine(0)
	if err != nil && code == 0 {
		return 0, "", err
	}
	return code, msg, nil
}

type nzbXML struct {
	XMLName xml.Name  `xml:"nzb"`
	Xmlns   string    `xml:"xmlns,attr"`
	Files   []nzbFile `xml:"file"`
}

type nzbFile struct {
	Poster   string   `xml:"poster,attr"`
	Date     int64    `xml:"date,attr"`
	Subject  string   `xml:"subject,attr"`
	Groups   []string `xml:"groups>group"`
	Segments []nzbSeg `xml:"segments>segment"`
}

type nzbSeg struct {
	Bytes  int    `xml:"bytes,attr"`
	Number int    `xml:"number,attr"`
	ID     string `xml:",chardata"`
}

func writeNZB(path, group string, arts []*article) error {
	n := nzbXML{Xmlns: "http://www.newzbin.com/DTD/2003/nzb"}
	byFile := map[int]int{} // file number -> index in n.Files
	for _, a := range arts {
		i, ok := byFile[a.File]
		if !ok {
			n.Files = append(n.Files, nzbFile{
				Poster:  "nzbrex-test <test@nzbrex.test>",
				Date:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
				Subject: fmt.Sprintf("[%d] \"%s\" yEnc (1/%d)", a.File, a.name, a.total),
				Groups:  []string{group},
			})
			i = len(n.Files) - 1
			byFile[a.File] = i
		}
		raw := 0
		for _, l := range encodeArticle(a, group) {
			raw += len(l) + 2
		}
		a.Bytes = raw
		n.Files[i].Segments = append(n.Files[i].Segments, nzbSeg{Bytes: raw, Number: a.Part, ID: a.MsgID})
	}
	b, err := xml.MarshalIndent(n, "", " ")
	if err != nil {
		return err
	}
	out := []byte(xml.Header + "<!DOCTYPE nzb PUBLIC \"-//newzBin//DTD NZB 1.1//EN\" \"http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd\">\n")
	out = append(out, b...)
	out = append(out, '\n')
	return os.WriteFile(path, out, 0o644)
}
