package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/textproto"
	"strings"
	"time"

	"github.com/go-while/yenc" // fork of chrisfarms with little mods
)

var (
	// max read lines, to prevent DoS attacks
	// normal articles have 5000-10000 lines max
	// a yenc line = 128 byte * ~5500 lines = 700KiB article
	// 16384 allows 2 MiB articles, which is enough for most
	// but I dont even know if any provider allowes more than 1 MiB articles
	MaxReadLines = 16384
)

func CMD_STAT(provider *Provider, connitem *ConnItem, item *segmentChanItem) (int, error) {
	if connitem == nil || connitem.conn == nil || connitem.srvtp == nil {
		return 0, fmt.Errorf("error CMD_STAT srvtp=nil")
	}
	start := time.Now()
	id, err := connitem.srvtp.Cmd("STAT <%s>", item.segment.Id)
	if err != nil {
		dlog(always, "ERROR checkMessageID @ '%s' srvtp.Cmd err='%v'", provider.Name, err)
		return 0, err
	}
	connitem.srvtp.StartResponse(id)
	code, msg, err := connitem.srvtp.ReadCodeLine(223)
	connitem.srvtp.EndResponse(id)
	switch code {
	case 223:
		// article exists... or should!
		dlog(cfg.opt.DebugSTAT, "CMD_STAT +OK+ seg.Id='%s' @ '%s'", item.segment.Id, provider.Name)
		return code, nil
	case 430:
		// "430 No Such Article"
		dlog(cfg.opt.DebugSTAT, "CMD_STAT -NO- seg.Id='%s' @ '%s'", item.segment.Id, provider.Name)
		return code, nil
	case 451:
		dlog(cfg.opt.DebugSTAT, "CMD_STAT got DMCA code=451 seg.Id='%s' @ '%s' msg='%s'", item.segment.Id, provider.Name, msg)
		return code, nil
	}
	return code, fmt.Errorf("error CMD_STAT returned unknown code=%d msg='%s' @ '%s' reqTook='%v' err='%v'", code, msg, provider.Name, time.Since(start), err)
} // end func CMD_STAT

const debugthis = false // set to true to debug CMD_ARTICLE without a real server connection

func CMD_ARTICLE(provider *Provider, connitem *ConnItem, item *segmentChanItem) (int, string, uint64, error) {
	if connitem == nil || connitem.conn == nil || connitem.srvtp == nil {
		return 0, "", 0, fmt.Errorf("error in CMD_ARTICLE srvtp=nil")
	}
	if debugthis {
		return 220, "fake article", 1234, nil
	}

	start := time.Now()
	id, aerr := connitem.srvtp.Cmd("ARTICLE <%s>", item.segment.Id)
	if aerr != nil {
		dlog(always, "ERROR in CMD_ARTICLE srvtp.Cmd @ '%s' err='%v'", provider.Name, aerr)
		return 0, "", 0, aerr
	}
	connitem.srvtp.StartResponse(id)
	code, msg, err := connitem.srvtp.ReadCodeLine(220)
	connitem.srvtp.EndResponse(id)
	switch code {
	case 220:
		// article is coming
		// old textproto.ReadDotLines replaced with new function: readArticleDotLines
		// to clean up headers directly while fetching from network
		// and decoding yenc on the fly
		rcode, rxb, err := readDotLines(provider, item, connitem.srvtp, "ARTICLE")
		if err != nil {
			dlog(always, "ERROR in CMD_ARTICLE srvtp.ReadDotLines @ '%s' err='%v' code=%d rcode=%d", provider.Name, err, code, rcode)
			return code, "", uint64(rxb), err
		}
		dlog(cfg.opt.DebugARTICLE, "CMD_ARTICLE seg.Id='%s' @ '%s' msg='%s' rxb=%d lines=%d code=%d dlcnt=%d fails=%d", item.segment.Id, provider.Name, msg, item.size, len(item.article), code, item.dlcnt, item.fails)
		if rcode == 99932 {
			code = 99932 // bad crc32
		}
		return code, msg, uint64(rxb), nil

	case 430:
		dlog(cfg.opt.DebugARTICLE, "INFO CMD_ARTICLE:430 seg.Id='%s' @ '%s' msg='%s' err='%v' dlcnt=%d fails=%d", item.segment.Id, provider.Name, msg, err, item.dlcnt, item.fails)
		return code, msg, 0, nil // not an error, just no such article

	case 451:
		dlog((cfg.opt.Verbose || cfg.opt.Print430), "INFO CMD_ARTICLE:451 seg.Id='%s' @ '%s' msg='%s' err='%v' dlcnt=%d fails=%d", item.segment.Id, provider.Name, msg, err, item.dlcnt, item.fails)
		return code, msg, 0, nil // not an error, just DMCA

	default:
		// returns the unknown code with an error!
	}
	return code, msg, 0, fmt.Errorf("error in CMD_ARTICLE got unknown code=%d msg='%s' @ '%s' reqTook='%v' err='%v'", code, msg, provider.Name, time.Since(start), err)
} // end func CMD_ARTICLE

func CMD_IHAVE(provider *Provider, connitem *ConnItem, item *segmentChanItem) (int, string, uint64, error) {
	if connitem == nil || connitem.conn == nil || connitem.srvtp == nil || connitem.writer == nil {
		return 0, "", 0, fmt.Errorf("error CMD_IHAVE connitem nil")
	}
	if debugthis {
		memlim.MemReturn("fake ihave", item)
		return 235, "fake ihave", uint64(item.segment.Bytes), nil
	}
	start := time.Now()
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
		dlog(always, "ERROR CMD_IHAVE @ '%s' srvtp.Cmd err='%v'", provider.Name, err)
		return 0, "", 0, err
	}

	connitem.srvtp.StartResponse(id)
	code, msg, err := connitem.srvtp.ReadCodeLine(335)
	if err != nil {
		dlog(always, "ERROR CMD_IHAVE srvtp.ReadCodeLine @ '%s' err='%v'", provider.Name, err)
		return code, msg, 0, err
	}
	connitem.srvtp.EndResponse(id)

	switch code {
	case 335:
		// Send article to be transferred
		dlog(cfg.opt.DebugIHAVE, "CMD_IHAVE 335 sendit seg.Id='%s' @ '%s'", item.segment.Id, provider.Name)

	case 435:
		// Article not wanted
		dlog(cfg.opt.DebugIHAVE, "CMD_IHAVE 435 unwanted seg.Id='%s' @ '%s' msg='%s' err='%v'", item.segment.Id, provider.Name, msg, err)
		return code, msg, 0, nil

	case 436:
		// Transfer not possible; try again later
		dlog(cfg.opt.DebugIHAVE, "CMD_IHAVE 436 retry seg.Id='%s' @ '%s' msg='%s' err='%v'", item.segment.Id, provider.Name, msg, err)
		return code, msg, 0, nil

	default:
		dlog(always, "ERROR CMD_IHAVE got unknown code=%d msg='%s' @ '%s' reqTook='%v' err='%v'", code, msg, provider.Name, time.Since(start), err)
		return code, msg, 0, fmt.Errorf("error CMD_IHAVE unknown code=%d msg='%s' @ '%s' reqTook='%v' err='%v'", code, msg, provider.Name, time.Since(start), err)
	}

	var txb uint64
	// Send article
	switch wireformat {
	case true:
		// Sending article in wireformat mode is not implemented.
		// To add wireformat support, update the cache logic as follows:
		// 1. Optimize cache reads to work directly with []byte data, so you don't need to split by '\n' into lines.
		// 2. Ensure the cache reading code correctly handles CRLF line endings when retrieving articles.
		// 3. Test the cache with both wireformat and standard (non-wireformat) articles to ensure compatibility.
		// Note: textproto removes CRLF and provides lines as []string, so this change mainly affects how data is formatted and stored.
		// Implementing this is optional and only impacts data formatting, not core functionality.
	case false:
		if len(item.article) == 0 {
			return 0, msg, txb, fmt.Errorf("error CMD_IHAVE: item.article is empty")
		}
		for _, line := range item.article {
			// dot-stuffing !
			// When sending articles (IHAVE/POST/TAKETHIS), lines starting with a dot ('.') must be "dot-stuffed":
			// prepend an extra dot to any such line. This prevents confusion with the protocol's end-of-article marker,
			// which is a line containing only a single dot.
			if strings.HasPrefix(line, ".") {
				line = "." + line
			}
			n, err := io.WriteString(connitem.writer, line+CRLF)
			if err != nil {
				return 0, msg, txb, fmt.Errorf("error CMD_IHAVE WriteString writer @ '%s' err='%v'", provider.Name, err)
			}
			txb += uint64(n)
		}
		if _, err := io.WriteString(connitem.writer, DOT+CRLF); err != nil {
			return 0, msg, txb, fmt.Errorf("error CMD_IHAVE writer DOT+CRLF @ '%s' err='%v'", provider.Name, err)
		}
		if err := connitem.writer.Flush(); err != nil {
			return 0, msg, txb, fmt.Errorf("error CMD_IHAVE writer.Flush @ '%s' err='%v'", provider.Name, err)
		}
	} // end switch wireformat

	code, msg, err = connitem.srvtp.ReadCodeLine(235)
	if err != nil {
		dlog(always, "ERROR CMD_IHAVE srvtp.ReadCodeLine @ '%s' err='%v'", provider.Name, err)
		return code, msg, txb, err
	}
	switch code {
	case 235:
		//Article transferred OK
		dlog(cfg.opt.DebugIHAVE, "CMD_IHAVE 235 OK seg.Id='%s' @ '%s' msg='%s' txb=%d", item.segment.Id, provider.Name, msg, txb)
		return code, msg, txb, nil

	case 436:
		// Transfer failed; try again later
		dlog(cfg.opt.DebugIHAVE, "CMD_IHAVE 436 transfer failed, retry seg.Id='%s' @ '%s' msg='%s' err='%v'", item.segment.Id, provider.Name, msg, err)
		return code, msg, txb, nil

	case 437:
		//  Transfer rejected; do not retry
		dlog(cfg.opt.DebugIHAVE, "CMD_IHAVE 437 transfer rejected, no retry seg.Id='%s' @ '%s' msg='%s' err='%v'", item.segment.Id, provider.Name, msg, err)
		return code, msg, txb, nil
	}

	return code, msg, txb, fmt.Errorf("error in CMD_IHAVE uncatched return: code=%d msg='%s' err='%v'", code, msg, err)
} // end func CMD_IHAVE

func CMD_POST(provider *Provider, connitem *ConnItem, item *segmentChanItem) (int, string, uint64, error) {
	if connitem == nil || connitem.conn == nil || connitem.srvtp == nil || connitem.writer == nil {
		return 0, "", 0, fmt.Errorf("error CMD_POST srvtp=nil")
	}
	id, err := connitem.srvtp.Cmd("POST")
	if err != nil {
		dlog(always, "ERROR CMD_POST @ '%s' srvtp.Cmd err='%v'", provider.Name, err)
		return 0, "", 0, err
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
		/*
				provider.mux.Lock()
				provider.capabilities.post = false
				provider.mux.Unlock()

			GCounter.Decr("postProviders")
		*/
		dlog(always, "ERROR code=%d in CMD_POST @ '%s' msg='%s' err='%v'", code, provider.Name, msg, err)
		return code, "", 0, nil
	default:
		// uncatched return code
		return code, "", 0, err
	}

	var txb uint64
	for _, line := range item.article {
		// dot-stuffing !
		// When sending articles (IHAVE/POST/TAKETHIS), lines starting with a dot ('.') must be "dot-stuffed":
		// prepend an extra dot to any such line. This prevents confusion with the protocol's end-of-article marker,
		// which is a line containing only a single dot.
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		n, err := io.WriteString(connitem.writer, line+CRLF)
		if err != nil {
			return 0, "", txb, fmt.Errorf("error CMD_POST WriteString writer @ '%s' err='%v'", provider.Name, err)
		}
		txb += uint64(n)
	}
	if _, err := io.WriteString(connitem.writer, DOT+CRLF); err != nil {
		return 0, "", txb, fmt.Errorf("error CMD_POST writer DOT+CRLF @ '%s' err='%v'", provider.Name, err)
	}
	if err := connitem.writer.Flush(); err != nil {
		return 0, "", txb, fmt.Errorf("error CMD_POST writer.Flush @ '%s' err='%v'", provider.Name, err)
	}
	code, msg, err = connitem.srvtp.ReadCodeLine(240)
	switch code {
	case 240:
		// Article received OK (posted)
		return code, msg, txb, nil
	case 441:
		// Posting failed
		return code, msg, txb, nil
	}

	return code, msg, txb, fmt.Errorf("error in CMD_POST uncatched return: code=%d msg='%s' err='%v'", code, msg, err)
} // end func CMD_POST

func readDotLines(provider *Provider, item *segmentChanItem, srvtp *textproto.Conn, what string) (int, int, error) {
	if srvtp == nil {
		return 0, 0, fmt.Errorf("error readArticleDotLines conn or srvtp nil @ '%s'", provider.Name)
	}

	var parseHeader bool = true // initial
	var ignoreNextContinuedLine bool
	var article []string
	var head []string
	var body []string
	//var messageIds []string
	var ydec []byte
	var ydat []*string
	var rxb int
	start := time.Now()
	i := 0
readlines:
	for {
		// read 1 line from the textproto.Conn
		// there will be more until we hit the DOT
		// CRLF is already stripped by textproto!
		// we have pure human readable strings in lines backed in []string
		line, err := srvtp.ReadLine()
		if err != nil {
			// broken pipe to remote site
			return 0, rxb, err
		}
		// see every line thats coming in
		//dlog( "readArticleDotLines: seg.Id='%s' line='%s'", segment.Id, line)
		rxb += len(line)
		if rxb > cfg.opt.MaxArtSize {
			// max article size reached, stop reading
			// this is a DoS protection, so we do not read more than maxartsize
			err = fmt.Errorf("error readArticleDotLines > maxartsize=%d seg.Id='%s'", cfg.opt.MaxArtSize, item.segment.Id)
			log.Print(err)
			return 0, rxb, err
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
					dlog( "WARN readArticleDotLines '%s' cleanHdr appends 'Message-Id: <%s>'", provider.Name, item.segment.Id)
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
							dlog( "WARN readArticleDotLines cleanHdr getMsgId seg.Id='%s' msgId='%s' p='%s'", item.segment.Id, msgid, provider.Name)
						}
						continue readlines
					}
				*/

				// ignore headers from cleanHeader slice
				for _, key := range cleanHeader {
					if strings.HasPrefix(line, key) {
						ignoreNextContinuedLine = true
						dlog(cfg.opt.DebugARTICLE, "cleanHeader: seg.ID='%s' ignore key='%s'", item.segment.Id, key)
						// will not append this and ignore any following continued spaced line(s)
						continue readlines
					}
				}
			}
			article = append(article, line)
		} // end parseHeader

		// found final dot in line, break here
		if len(line) == 1 && line == "." {
			break
		}

		if !parseHeader {
			i++ // counts body lines
			// dot-stuffing on received lines
			/*
				Receiver Side: How to Handle Dot-Stuffing

				When receiving the article (reading lines), you must:

					Stop reading at a line with only a single dot (.).
					This marks the end of the article.

					Un-dot-stuff:
						For any line that begins with two dots (..), remove the first dot.
						For all other lines, leave as-is.


			*/
			if strings.HasPrefix(line, "..") {
				line = line[1:] // Remove one leading dot
			}
			switch what {
			case "ARTICLE":
				article = append(article, line)
			case "BODY":
				body = append(body, line)
			case "HEAD":
				head = append(head, line)
			}

			if cfg.opt.YencCRC && (what == "ARTICLE" || what == "BODY") {
				switch cfg.opt.YencTest {
				case 1:
					// case 1 needs double the memory
					ydec = append(ydec, line...) // as []byte (with(out) crlf?) via an io.reader
				case 2:
					// case 2
					ydat = append(ydat, &line) // as []*string line per line needs less mem allocs
				}
			}
		}
		dlog((cfg.opt.BUG && cfg.opt.DebugARTICLE), "readArticleDotLines: seg.Id='%s' lineNum=%d len(line)=%d rxb=%d article_lines=%d", item.segment.Id, i, len(line), rxb, len(article))
	} // end for

	dlog(cfg.opt.Debug, "readArticleDotLines: seg.Id='%s' rxb=%d article_lines=%d took=(%d ms)", item.segment.Id, rxb, len(article), time.Since(start).Milliseconds())

	if cfg.opt.YencCRC && (what == "ARTICLE" || what == "BODY") {
		yencstart := time.Now()
		getCoreLimiter()
		defer returnCoreLimiter()
		var isBadCrc bool
		switch cfg.opt.YencTest {

		case 1:
			decoder := yenc.NewDecoder(nil, ydec, nil, 1)
			decoder.SegId = &item.segment.Id
			if yPart, badcrc, err := decoder.Decode(); err != nil { // chrisfarms/yenc
				dlog(always, "ERROR yenc.Decode mode=1 seg.Id='%s' @ '%s' ydec=(%d bytes) err='%v'", item.segment.Id, provider.Name, len(ydec), err)
				isBadCrc = badcrc
			} else {
				dlog(always, "YencCRC OK mode=1 seg.Id='%s' yPart.Body=%d Number=%d crc32=%x'", item.segment.Id, len(yPart.Body), yPart.Number, yPart.Crc32)
				isBadCrc = badcrc
			}
		case 2:
			decoder := yenc.NewDecoder(nil, nil, ydat, 1)
			decoder.SegId = &item.segment.Id
			if yPart, badcrc, err := decoder.DecodeSlice(); err != nil { // go-while/yenc#testing-branch
				dlog(always, "ERROR yenc.Decode mode=2 seg.Id='%s' @ '%s' ydat=(%d lines) err='%v' badcrc=%t", item.segment.Id, provider.Name, len(ydat), err, badcrc)
			} else {
				dlog(always, "YencCRC OK mode=2 seg.Id='%s' yPart.Body=%d Number=%d crc32=%x'", item.segment.Id, len(yPart.Body), yPart.Number, yPart.Crc32)
				if cfg.opt.YencWrite && cacheON && yPart != nil {
					cache.WriteYenc(item, yPart)
				}
			}
		} // end switch yencTest
		dlog(always, "readArticleDotLines: YencCRC seg.Id='%s' @ '%s' rxb=%d lines=%d took=(%d ms)", item.segment.Id, provider.Name, rxb, len(article), time.Since(yencstart).Milliseconds())

		if isBadCrc {
			item.mux.Lock()
			//item.flaginDL = false // FIXME REVIEW: moved to routines where all other flags are set
			//item.flagisDL = false // FIXME REVIEW: moved to routines where all other flags are set
			item.badcrc++
			/*
				for pid, prov := range providerList {
					if prov.Group != provider.Group {
						continue
					}
					item.missingOn[pid] = true
					item.errorOn[pid] = true
					delete(item.availableOn, pid)
				}
			*/
			item.mux.Unlock()
			// DecreaseDLQueueCnt() // FIXME NEEDS REVIEW // DISABLED
			dlog(always, "ERROR readArticleDotLines crc32 failed seg.Id='%s' @ '%s'", item.segment.Id, provider.Name)
			return 99932, rxb, nil
		}

	} // end if cfg.opt.YencCRC

	item.mux.Lock()
	item.size = rxb

	switch what {
	case "ARTICLE":
		item.article = article
	case "BODY":
		item.body = body
	case "HEAD":
		item.head = head
	}

	item.dlcnt++
	item.mux.Unlock()
	return 1, rxb, nil
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
	if cfg.opt.CheckOnly {
		dlog(cfg.opt.Debug, "checkCapabilities: flag checkonly is set, skipping capabilities check")
		// if checkonly is set, we don't need to check capabilities
		return nil
	}
	rcode := 101 // expected

	provider.mux.Lock()         // mutex #9b71 checkCapabilities
	defer provider.mux.Unlock() // mutex #9b71 checkCapabilities
	defer provider.ConnPool.CloseConn(connitem, nil)

	if !msg2srv(connitem.conn, "CAPABILITIES") {
		return fmt.Errorf("error '%s' checkCapabilities", provider.Name)
	}
	code, rmsg, err := connitem.srvtp.ReadCodeLine(rcode)
	if code != rcode {
		return fmt.Errorf("error '%s' checkCapabilities ReadCodeLine code=%d != 101 rmsg='%s' err='%v'", provider.Name, code, rmsg, err)
	}
	lines, err := connitem.srvtp.ReadDotLines()
	if err != nil {
		return fmt.Errorf("error '%s' checkCapabilities ReadDotLines err='%v'", provider.Name, err)
	}
	setpostProviders := 0
	//dlog( "Read %d CAPAS from Provider %s", len(lines), provider.Name)
	for _, capability := range lines {
		//dlog( "CAPAS @ %s: %s", provider.Name, capability)
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
		return fmt.Errorf("error checkCapabilities QUIT")
	}
	if setpostProviders > 0 {
		GCounter.Incr("postProviders")
	}
	// provider.NoUpload will be set to true if none capa is available
	provider.NoUpload = (!provider.capabilities.ihave && !provider.capabilities.post && !provider.capabilities.stream)
	// done
	return nil
} // end func checkCapabilities
