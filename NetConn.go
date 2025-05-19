package main

import (
	"bytes"
	"fmt"
	"gopkg.in/yenc.v0"
	"io"
	"log"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"time"
)

var (
	needHeaders = []string{
		"Date:",
		"From:",
		"Subject:",
		"Newsgroups:",
		"Message-Id:",
	}
	cleanHeader = []string{
		"X-",
		"Date:",
		"Nntp-",
		"Path:",
		"Xref:",
		"Cancel-",
		"Injection-",
		"User-Agent:",
		"Organization:",
	}
)

func GoSpeedMeter(byteSize int64, waitWorker *sync.WaitGroup) {
	defer waitWorker.Done()
	if cfg.opt.LogPrintEvery < 5 {
		cfg.opt.LogPrintEvery = 5
	}
	cron := time.After(time.Duration(cfg.opt.LogPrintEvery) * time.Second)

	var logStr, logStr_RX, logStr_TX string
	var TOTAL_TXbytes, TOTAL_RXbytes uint64

	for {
		select {
		case quit := <-stop_chan:
			if cfg.opt.Debug {
				log.Print("GoSpeedMeter stop_chan")
			}
			stop_chan <- quit
			if cfg.opt.Debug {
				log.Print("GoSpeedMeter returns")
			}
			return

		case <-cron:
			tmp_rxb, tmp_txb := Counter.getReset("TMP_RXbytes"), Counter.getReset("TMP_TXbytes")
			logStr, logStr_RX, logStr_TX = "", "", ""
			if tmp_rxb > 0 {
				rx_speed := int64(tmp_rxb) / cfg.opt.LogPrintEvery / 1024
				TOTAL_RXbytes += tmp_rxb
				dlPerc := int(float64(TOTAL_RXbytes) / float64(byteSize) * 100)
				logStr_RX = fmt.Sprintf(" |  DL  [%3d%%]   SPEED: %5d KiB/s | (Total: %.2f MB)", dlPerc, rx_speed, float64(Counter.get("TOTAL_RXbytes")/1024/1024))
			}
			if tmp_txb > 0 {
				tx_speed := int64(tmp_txb) / cfg.opt.LogPrintEvery / 1024
				TOTAL_TXbytes += tmp_txb
				upPerc := int(float64(TOTAL_TXbytes) / float64(byteSize) * 100)
				logStr_TX = fmt.Sprintf(" |  UL  [%3d%%]   SPEED: %5d KiB/s | (Total: %.2f MB)", upPerc, tx_speed, float64(Counter.get("TOTAL_TXbytes")/1024/1024))
			}
			cron = time.After(time.Second * time.Duration(cfg.opt.LogPrintEvery))
			if logStr_RX != "" && cfg.opt.Verbose {
				//logStr = logStr + logStr_RX
				log.Print(logStr_RX)
			}
			if logStr_TX != "" && cfg.opt.Verbose {
				//logStr = logStr + logStr_TX
				log.Print(logStr_TX)
			}
			if logStr != "" && cfg.opt.Verbose {
				log.Print(logStr)
			}
		}
	} // end for
	if cfg.opt.Debug {
		log.Print("GoSpeedMeter quit")
	}
} // end func GoSpeedMeter


func CMD_STAT(provider *Provider, connitem *ConnItem, item *segmentChanItem) (int, error) {
	if connitem.srvtp == nil {
		return 0, fmt.Errorf("ERROR CMD_STAT srvtp=nil")
	}
	id, err := connitem.srvtp.Cmd("STAT <%s>", item.segment.Id)
	if err != nil {
		log.Printf("Error checkMessageID @ '%s' srvtp.Cmd err='%v'", provider.Name, err)
		return 0, err
	}
	connitem.srvtp.StartResponse(id)
	code, msg, err := connitem.srvtp.ReadCodeLine(223)
	connitem.srvtp.EndResponse(id)
	switch code {
	case 223:
		// article exists... or should!
		//log.Printf("CMD_STAT +OK+ seg.Id='%s' @ '%s'", item.segment.Id, provider.Name)
		return code, nil
	case 430:
		// "430 No Such Article"
		//log.Printf("CMD_STAT -NO- seg.Id='%s' @ '%s'", item.segment.Id, provider.Name)
		return code, nil
	case 451:
		//log.Printf("CMD_STAT got DMCA code=451 seg.Id='%s' @ '%s' msg='%s'", item.segment.Id, provider.Name, msg)
		return code, nil
	}
	return code, fmt.Errorf("Error CMD_STAT returned unknown code=%d msg='%s' @ '%s'", code, msg, provider.Name)
} // end func CMD_STAT

func CMD_ARTICLE(provider *Provider, connitem *ConnItem, item *segmentChanItem) (int, error) {
	if connitem.srvtp == nil {
		return 0, fmt.Errorf("ERROR CMD_ARTICLE srvtp=nil")
	}
	id, err := connitem.srvtp.Cmd("ARTICLE <%s>", item.segment.Id)
	if err != nil {
		log.Printf("ERROR CMD_ARTICLE @ '%s' srvtp.Cmd err='%v'", provider.Name, err)
		return 0, err
	}
	connitem.srvtp.StartResponse(id)
	code, msg, _ := connitem.srvtp.ReadCodeLine(220)
	connitem.srvtp.EndResponse(id)
	switch code {
	case 220:
		// article is coming
		//lines, err := srvtp.ReadDotLines() // original go
		// new function to clean up headers directly while fetching from network
		err := readArticleDotLines(provider, item, connitem.srvtp)
		if err != nil {
			log.Printf("ERROR CMD_ARTICLE @ '%s' srvtp.ReadDotLines err='%v'", provider.Name, err)
			return code, err
		}
		return code, nil
	case 430:
		log.Printf("WARN CMD_ARTICLE seg.Id='%s' @ '%s' code=%d msg='%s' err='%v' dlcnt=%d fails=%d", item.segment.Id, provider.Name, code, msg, err, item.dlcnt, item.fails)
		return code, nil
	case 451:
		return code, nil
	default:
	}
	return code, fmt.Errorf("Error CMD_ARTICLE returned unknown code=%d @ '%s'", code, provider.Name)
} // end func CMD_ARTICLE

func CMD_IHAVE(provider *Provider, connitem *ConnItem, item *segmentChanItem) (int, uint64, error) {
	if connitem.srvtp == nil {
		return 0, 0, fmt.Errorf("ERROR CMD_IHAVE srvtp=nil")
	}
	/*
	 * IHAVE
	 *   Initial responses
	 *    335    Send article to be transferred
	 *    435    Article not wanted
	 *    436    Transfer not possible; try again later
	 *
	 *
	 *  Subsequent responses
	 *    235    Article transferred OK
	 *    436    Transfer failed; try again later
	 *    437    Transfer rejected; do not retry
	 */
	wireformat := false // not implemented. read below in: case true
	id, err := connitem.srvtp.Cmd("IHAVE <%s>", item.segment.Id)
	if err != nil {
		log.Printf("Error CMD_IHAVE @ '%s' srvtp.Cmd err='%v'", provider.Name, err)
		return 0, 0, err
	}
	connitem.srvtp.StartResponse(id)
	code, msg, err := connitem.srvtp.ReadCodeLine(335)
	connitem.srvtp.EndResponse(id)
	switch code {
	case 335:
		// Send article to be transferred
		// pass
	case 435:
		// Article not wanted
		return code, 0, nil
	case 436:
		// Transfer not possible; try again later
		return code, 0, nil
	case 502:
		/* FIXME TODO #b8bd287b do we need/want dynamic capabilities while running? */
		//provider.mux.Lock()
		//provider.capabilities.ihave = false
		//provider.mux.Unlock()
		log.Printf("ERROR code=%d in CMD_IHAVE @ '%s' msg='%s' err='%v'", code, provider.Name, msg, err)
		return code, 0, nil
	default:
		return code, 0, fmt.Errorf("ERROR Unknown code=%d in CMD_IHAVE @ '%s' msg='%s' err='%v'", code, provider.Name, msg, err)
	}
	var txb uint64

	// Send article
	switch wireformat {
	case true:
		// not implemented.
		// sends article as []byte which must include CRLF in articles (not stripped out)
		// needs change in cache to write articles with \r\n instead of \n
		// if change is done: reading from cache is a []byte and no need to split by \n into lines
		// actually CRLF gets removed by textproto and we work with lines in []string
		// but this is only cosmetics... no real need for.
	case false:
		for _, line := range item.lines {
			n, err := io.WriteString(connitem.writer, line+CRLF)
			if err != nil {
				return 0, txb, fmt.Errorf("ERROR CMD_IHAVE WriteString writer @ '%s' err='%v'", provider.Name, err)
			}
			txb += uint64(n)
		}
		if _, err := io.WriteString(connitem.writer, DOT+CRLF); err != nil {
			return 0, txb, fmt.Errorf("ERROR CMD_IHAVE writer DOT+CRLF @ '%s' err='%v'", provider.Name, err)
		}
		if err := connitem.writer.Flush(); err != nil {
			return 0, txb, fmt.Errorf("ERROR CMD_IHAVE writer.Flush @ '%s' err='%v'", provider.Name, err)
		}
	} // end switch wireformat

	code, msg, err = connitem.srvtp.ReadCodeLine(235)
	switch code {
	case 235:
		//Article transferred OK
		return code, txb, nil
	case 436:
		// Transfer failed; try again later
		return code, txb, nil
	case 437:
		//  Transfer rejected; do not retry
		return code, txb, nil
	}

	return code, txb, fmt.Errorf("Uncatched ERROR in CMD_IHAVE code=%d msg='%s' err='%v'", code, msg, err)
} // end func CMD_IHAVE

func CMD_POST(provider *Provider, connitem *ConnItem, item *segmentChanItem) (int, uint64, error) {
	if connitem.srvtp == nil {
		return 0, 0, fmt.Errorf("ERROR CMD_POST srvtp=nil")
	}
	id, err := connitem.srvtp.Cmd("POST")
	if err != nil {
		log.Printf("Error CMD_POST @ '%s' srvtp.Cmd err='%v'", provider.Name, err)
		return 0, 0, err
	}
	connitem.srvtp.StartResponse(id)
	code, msg, err := connitem.srvtp.ReadCodeLine(340)
	defer connitem.srvtp.EndResponse(id)
	switch code {
	case 340:
		// pass
	case 440:
		// posting not allowed
		//
		/* FIXME TODO #b8bd287b: do we need/want dynamic capabilities while running?
		 * beeing non dynamic spares some locks in WorkDividers ...
		 */
		provider.mux.Lock()
		provider.capabilities.post = false
		provider.mux.Unlock()
		globalmux.Lock()
		postProviders-- /* FIXME TODO #b8bd287b postProviders */
		globalmux.Unlock()
		log.Printf("ERROR code=%d in CMD_POST @ '%s' msg='%s' err='%v'", code, provider.Name, msg, err)
		return code, 0, nil
	default:
		return code, 0, err
	}

	var txb uint64
	for _, line := range item.lines {
		n, err := io.WriteString(connitem.writer, line+CRLF)
		if err != nil {
			return 0, txb, fmt.Errorf("ERROR CMD_POST WriteString writer @ '%s' err='%v'", provider.Name, err)
		}
		txb += uint64(n)
	}
	if _, err := io.WriteString(connitem.writer, DOT+CRLF); err != nil {
		return 0, txb, fmt.Errorf("ERROR CMD_POST writer DOT+CRLF @ '%s' err='%v'", provider.Name, err)
	}
	if err := connitem.writer.Flush(); err != nil {
		return 0, txb, fmt.Errorf("ERROR CMD_POST writer.Flush @ '%s' err='%v'", provider.Name, err)
	}
	code, msg, err = connitem.srvtp.ReadCodeLine(240)
	switch code {
	case 240:
		// Article received OK (posted)
		return code, txb, nil
	case 441:
		// Posting failed
		return code, txb, nil
	}

	return code, txb, fmt.Errorf("Uncatched ERROR in CMD_POST code=%d msg='%s' err='%v'", code, msg, err)
} // end func CMD_POST

func readArticleDotLines(provider *Provider, item *segmentChanItem, srvtp *textproto.Conn) error {
	rxb, i := 0, 0
	var parseHeader bool = true // initial
	var ignoreNextContinuedLine bool
	var head []string
	var article []string
	//var messageIds []string
	var ydec []byte
	var badcrc bool

readlines:
	for {
		if srvtp == nil {
			return fmt.Errorf("ERROR readArticleDotLines srvtp nil @ '%s'", provider.Name)
		}
		line, err := srvtp.ReadLine()
		if err != nil {
			// broken pipe to remote site
			return err
		}
		// see every line thats coming in
		//log.Printf("readArticleDotLines: seg.Id='%s' line='%s'", segment.Id, line)

		rxb += len(line)
		if rxb > cfg.opt.MaxArtSize {
			err := fmt.Errorf("ERROR readArticleDotLines > maxartsize=%d seg.Id='%s'", cfg.opt.MaxArtSize, item.segment.Id)
			log.Print(err)
			return err
		}

		if parseHeader && len(line) == 0 {
			// reading header ends here
			parseHeader = false

			// add new headers for ignored ones
			now := time.Now().Format(time.RFC1123Z)
			datestr := fmt.Sprintf("Date: %s", now)
			article = append(article, datestr)
			// add read headers to item
			for _, line := range head {
				article = append(article, line)
			}
			/*
			article = append(article, "Message-Id: "+"<"+item.segment.Id+">")
			if len(messageIds) == 0 {
				log.Printf("WARN readArticleDotLines '%s' cleanHdr appends 'Message-Id: <%s>'", provider.Name, item.segment.Id)
				article = append(article, "Message-Id: "+"<"+item.segment.Id+">")
			}
			*/
			article = append(article, "Path: not-for-mail")
		}

		if parseHeader {

			isSpacedLine := (len(line) > 0 && (line[0] == ' ' || line[0] == '\t'))

			if !isSpacedLine && ignoreNextContinuedLine {
				ignoreNextContinuedLine = false
			}
			if isSpacedLine && ignoreNextContinuedLine {
				continue readlines
			}

			if cfg.opt.CleanHeaders {
				/*
				if strings.HasPrefix(line, "Message-Id: ") {
					if len(messageIds) > 0 {
						ignoreNextContinuedLine = true
						continue readlines
					}

					msgidSlice := strings.Split(line, " ")[1:]
					msgid := strings.Join(msgidSlice, "")
					messageIds = append(messageIds, msgid)
					if msgid != "<"+item.segment.Id+">" {
						ignoreNextContinuedLine = true
						log.Printf("WARN readArticleDotLines cleanHdr getMsgId seg.Id='%s' msgId='%s' p='%s'", item.segment.Id, msgid, provider.Name)
					}
					continue readlines
				}
				*/

				// ignore headers from cleanHeader slice
				for _, key := range cleanHeader {
					if strings.HasPrefix(line, key) {
						ignoreNextContinuedLine = true
						if cfg.opt.Debug {
							log.Printf("cleanHeader: seg.ID='%s' ignore key='%s' ", item.segment.Id, key)
						}
						// will not append this and ignore any following continued spaced line(s)
						continue readlines
					}
				}
			}
			head = append(head, line)
		} // end parseHeader

		// found final dot in line, break here and not append it to cache
		if len(line) == 1 && line == "." {
			break
		}
		if !parseHeader {
			i++ // counts body lines
			article = append(article, line)
			if cfg.opt.YencCRC {
				ydec = append(ydec, line+CRLF...)
			}

		}
		if cfg.opt.BUG {
			log.Printf("readArticleDotLines: line=%d rxb=%d lines=%d", i, rxb, len(item.lines))
		}
	} // end for
	if cfg.opt.Debug {
		log.Printf("readArticleDotLines: seg.Id='%s' rxb=%d lines=%d", item.segment.Id, rxb, len(item.lines))
	}

	if cfg.opt.YencCRC {
		r := bytes.NewReader(ydec)
		decoder, err := yenc.Decode(r, yenc.DecodeWithPrefixData())
		if err != nil {
			log.Printf("ERROR yenc.Decode seg.Id='%s' @ '%s' input=%d err='%v'", item.segment.Id, provider.Name, len(ydec), err)
			badcrc = true
		} else {
			if cfg.opt.Debug {
				log.Printf("seg.Id='%s' yenc.Decoder='%#v", item.segment.Id, decoder)
			}
		}
	}
	if badcrc {
		item.mux.Lock()
		item.badcrc++
		item.mux.Unlock()
		return fmt.Errorf("ERROR CRC32 failed seg.Id='%s' @ '%s'", item.segment.Id, provider.Name)
	}
	item.mux.Lock()
	item.size = rxb
	item.lines = article

	item.dlcnt++
	item.mux.Unlock()
	return nil
} // end func readArticleDotLines

func msg2srv(conn net.Conn, message string) bool {
	_, err := io.WriteString(conn, message+CRLF)
	if err != nil {
		// broken pipe
		return false
	}
	if len(message) == 4 && strings.ToLower(message) == "quit" {
		if conn != nil {
			conn.Close()
			conn = nil
		}
	}
	return true
} // end func msg2srv

func checkCapabilities(provider *Provider, connitem *ConnItem) error {
	rcode := 101 // expected

	provider.mux.Lock()         // mutex #9b71 checkCapabilities
	defer provider.mux.Unlock() // mutex #9b71 checkCapabilities
	defer provider.Conns.CloseConn(provider, connitem)

	if !msg2srv(connitem.conn, "CAPABILITIES") {
		return fmt.Errorf("ERROR '%s' checkCapabilities", provider.Name)
	}
	code, rmsg, err := connitem.srvtp.ReadCodeLine(rcode)
	if code != rcode {
		return fmt.Errorf("ERROR '%s' checkCapabilities ReadCodeLine code=%d != 101 rmsg='%s' err='%v'", provider.Name, code, rmsg, err)
	}
	lines, err := connitem.srvtp.ReadDotLines()
	if err != nil {
		return fmt.Errorf("ERROR '%s' checkCapabilities ReadDotLines err='%v'", provider.Name, err)
	}

	//log.Printf("Read %d CAPAS from Provider %s", len(lines), provider.Name)
	for _, capability := range lines {
		//log.Printf("CAPAS @ %s: %s", provider.Name, capability)
		switch strings.ToLower(capability) {
		case "check":
			provider.capabilities.check = true
		case "ihave":
			provider.capabilities.ihave = true
			postProviders++
		case "post":
			provider.capabilities.post = true
			postProviders++
		case "takethis":
			provider.capabilities.stream = true
			postProviders++
		case "streaming":
			provider.capabilities.stream = true
			postProviders++
		}
	}
	if !msg2srv(connitem.conn, "QUIT") {
		return fmt.Errorf("ERROR checkCapabilities QUIT")
	}

	// provider.NoUpload will be set to true if none capa is available
	provider.NoUpload = (!provider.capabilities.ihave && !provider.capabilities.post && !provider.capabilities.stream)

	if cfg.opt.Debug || (!provider.capabilities.ihave && provider.capabilities.post /*&& !provider.capabilities.check && !provider.capabilities.stream*/) {
		log.Printf("Capabilities: [ IHAVE: %s | POST: %s | CHECK: %s | STREAM: %s ] @ '%s'",
			yesno(provider.capabilities.ihave),
			yesno(provider.capabilities.post),
			yesno(provider.capabilities.check),
			yesno(provider.capabilities.stream),
			provider.Name)
	}
	// done
	return nil
} // end func checkCapabilities
