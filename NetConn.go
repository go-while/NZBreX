package main

import (
	//"bytes"
	//"bufio"
	"fmt"
	"github.com/go-while/yenc" // fork of chrisfarms with little mods
	"io"
	"log"
	"net"
	"net/textproto"
	//"os"
	//"path/filepath"
	"strings"
	"sync"
	"time"
)

func GoSpeedMeter(byteSize int64, waitWorker *sync.WaitGroup) {
	defer waitWorker.Done()
	if cfg.opt.LogPrintEvery < 0 {
		// don't start speedmeter if LogPrintEvery < 0   (set to -1 to disable)
		return
	}
	LogPrintEvery := cfg.opt.LogPrintEvery
	if LogPrintEvery < 5 {
		LogPrintEvery = 5 // defaults to min 5sec
	}
	cron := time.After(time.Duration(LogPrintEvery) * time.Second)

	var logStr, logStr_RX, logStr_TX string
	var TOTAL_TXbytes, TOTAL_RXbytes uint64
forever:
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
			break forever

		case <-cron:
			tmp_rxb, tmp_txb := Counter.getReset("TMP_RXbytes"), Counter.getReset("TMP_TXbytes")
			logStr, logStr_RX, logStr_TX = "", "", ""
			if tmp_rxb > 0 {
				TOTAL_RXbytes += tmp_rxb
				rx_speed, mbps := ConvertSpeed(int64(tmp_txb), LogPrintEvery)
				dlPerc := int(float64(TOTAL_RXbytes) / float64(byteSize) * 100)
				logStr_RX = fmt.Sprintf(" |  DL  [%3d%%] | %d / %d MiB | SPEED: %5d KiB/s ~%3.1f Mbps", dlPerc, TOTAL_RXbytes/1024/1024, byteSize/1024/1024, rx_speed, mbps)
			}
			if tmp_txb > 0 {
				TOTAL_TXbytes += tmp_txb
				tx_speed, mbps := ConvertSpeed(int64(tmp_txb), LogPrintEvery)
				upPerc := int(float64(TOTAL_TXbytes) / float64(byteSize) * 100)
				logStr_TX = fmt.Sprintf(" |  UL  [%3d%%] | %d / %d MiB | SPEED: %5d KiB/s ~%3.1f Mbps", upPerc, TOTAL_TXbytes/1024/1024, byteSize/1024/1024, tx_speed, mbps)
			}
			cron = time.After(time.Second * time.Duration(LogPrintEvery))
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
		Counter.decr("postProviders")
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
	var article []string
	//var messageIds []string
	var ydec []byte
	var ydat []*string
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
							log.Printf("cleanHeader: seg.ID='%s' ignore key='%s'", item.segment.Id, key)
						}
						// will not append this and ignore any following continued spaced line(s)
						continue readlines
					}
				}
			}
			//head = append(head, line)
			article = append(article, line)
		} // end parseHeader

		// found final dot in line, break here
		if len(line) == 1 && line == "." {
			break
		}

		if !parseHeader {
			i++ // counts body lines
			article = append(article, line)
			if cfg.opt.YencCRC {
				switch cfg.opt.YencTest {
				case 1:
					// case 1 needs double the memory
					ydec = append(ydec, line+CRLF...) // as []byte
				case 2:
					// case 2 should need less memory
					ydat = append(ydat, &line) // as []*string
				}
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
		getCoreLimiter()
		defer returnCoreLimiter()
		var yPart *yenc.Part
		var err error
		switch cfg.opt.YencTest {

		case 1:
			decoder := yenc.NewDecoder(nil, ydec, nil, 1)
			if yPart, err = decoder.Decode(); err != nil { // chrisfarms/yenc
				log.Printf("ERROR yenc.Decode mode=1 seg.Id='%s' @ '%s' ydec=(%d bytes) err='%v'", item.segment.Id, provider.Name, len(ydec), err)
				badcrc = true
			} else {
				if cfg.opt.Debug {
					log.Printf("YencCRC OK mode=1 seg.Id='%s' yPart.Body=%d Number=%d crc32=%x'", item.segment.Id, len(yPart.Body), yPart.Number, yPart.Crc32)
				}
			}
		case 2:
			decoder := yenc.NewDecoder(nil, nil, ydat, 1)
			if yPart, err = decoder.DecodeSlice(); err != nil { // go-while/yenc#testing-branch
				log.Printf("ERROR yenc.Decode mode=2 seg.Id='%s' @ '%s' ydat=(%d lines) err='%v'", item.segment.Id, provider.Name, len(ydat), err)
				badcrc = true
			} else {
				if cfg.opt.Debug {
					log.Printf("YencCRC OK mode=2 seg.Id='%s' yPart.Body=%d Number=%d crc32=%x'", item.segment.Id, len(yPart.Body), yPart.Number, yPart.Crc32)
				}
			}
		} // end switch yencTest

		if badcrc {
			item.mux.Lock()
			item.badcrc++
			item.mux.Unlock()
			errlog := fmt.Sprintf("ERROR CRC32 failed seg.Id='%s' @ '%s'", item.segment.Id, provider.Name)
			log.Print(errlog)
			return fmt.Errorf(errlog)
		}

		if cfg.opt.YencWrite && cacheON && yPart != nil {
			cache.WriteYenc(item, yPart)
		}

	} // end if cfg.opt.YencCRC

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
	setpostProviders := 0
	//log.Printf("Read %d CAPAS from Provider %s", len(lines), provider.Name)
	for _, capability := range lines {
		//log.Printf("CAPAS @ %s: %s", provider.Name, capability)
		switch strings.ToLower(capability) {
		case "check":
			provider.capabilities.check = true
		case "ihave":
			provider.capabilities.ihave = true
			setpostProviders++
		case "post":
			provider.capabilities.post = true
			setpostProviders++
		case "takethis":
			provider.capabilities.stream = true
			setpostProviders++
		case "streaming":
			provider.capabilities.stream = true
			setpostProviders++
		}
	}
	if !msg2srv(connitem.conn, "QUIT") {
		return fmt.Errorf("ERROR checkCapabilities QUIT")
	}
	if setpostProviders > 0 {
		Counter.incr("postProviders")
	}
	// provider.NoUpload will be set to true if none capa is available
	provider.NoUpload = (!provider.capabilities.ihave && !provider.capabilities.post && !provider.capabilities.stream)

	if cfg.opt.Verbose {
		log.Printf("Capabilities: [ IHAVE: %s | POST: %s | CHECK: %s | STREAM: %s ] @ '%s' NoUpload=%t",
			yesno(provider.capabilities.ihave),
			yesno(provider.capabilities.post),
			yesno(provider.capabilities.check),
			yesno(provider.capabilities.stream),
			provider.Name, provider.NoUpload)
	}
	// done
	return nil
} // end func checkCapabilities
