package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/go-while/NZBreX/rapidyenc" // fork of chrisfarms with little mods
	"github.com/go-while/NZBreX/yenc"      // fork of chrisfarms with little mods
)

const debugthis = false // set to true to debug CMD_ARTICLE without a real server connection

var (
	// max read lines, to prevent DoS attacks
	// normal articles have 5000-10000 lines max
	// a yenc line = 128 byte * ~5500 lines = 700KiB article
	// 16384 allows 2 MiB articles, which is enough for most
	// but I dont even know if any provider allowes more than 1 MiB articles
	MaxReadLines = 16384
)

// otherReadLineCommands contains commands that are read line by line
// and not stored in the segmentChanItem, but returned as content.
// These commands are typically used for reading headers or other metadata,
var otherReadLineCommands = map[string]struct{}{
	"LIST":      {},
	"LISTGROUP": {},
	"XHDR":      {},
	"HDR":       {},
	"XOVER":     {},
	"OVER":      {},
	"XPAT":      {},
	"XPATH":     {},
	"NEWNEWS":   {},
	"NEWGROUPS": {},
}

var itemReadLineCommands = map[string]struct{}{
	cmdARTICLE: {},
	cmdHEAD:    {},
	cmdBODY:    {},
}

// CMD_STAT checks if the article exists on the server
// and returns the status code or an error if it fails.
func CMD_STAT(connitem *ConnItem, item *segmentChanItem) (int, error) {
	if connitem == nil || connitem.conn == nil || connitem.srvtp == nil {
		return 0, fmt.Errorf("error CMD_STAT srvtp=nil")
	}
	start := time.Now()
	id, err := connitem.srvtp.Cmd("STAT <%s>", item.segment.Id)
	if err != nil {
		dlog(always, "ERROR checkMessageID @ '%s' srvtp.Cmd err='%v'", connitem.c.provider.Name, err)
		return 0, err
	}
	connitem.srvtp.StartResponse(id)
	code, msg, err := connitem.srvtp.ReadCodeLine(223)
	connitem.srvtp.EndResponse(id)
	switch code {
	case 223:
		// article exists... or should!
		dlog(cfg.opt.DebugSTAT, "CMD_STAT +OK+ seg.Id='%s' @ '%s'", item.segment.Id, connitem.c.provider.Name)
		return code, nil
	case 430:
		// "430 No Such Article"
		dlog(cfg.opt.DebugSTAT, "CMD_STAT -NO- seg.Id='%s' @ '%s'", item.segment.Id, connitem.c.provider.Name)
		return code, nil
	case 451:
		dlog(cfg.opt.DebugSTAT, "CMD_STAT got DMCA code=451 seg.Id='%s' @ '%s' msg='%s'", item.segment.Id, connitem.c.provider.Name, msg)
		return code, nil
	}
	return code, fmt.Errorf("error CMD_STAT returned unknown code=%d msg='%s' @ '%s' reqTook='%v' err='%v'", code, msg, connitem.c.provider.Name, time.Since(start), err)
} // end func CMD_STAT

// CMD_ARTICLE fetches the article from the server.
// It returns the response code, message, size of the article, and any error encountered.
// If the article is successfully fetched, it will be stored in item.article.
func CMD_ARTICLE(connitem *ConnItem, item *segmentChanItem) (int, string, uint64, error) {
	if connitem == nil || connitem.conn == nil || connitem.srvtp == nil {
		return 0, "", 0, fmt.Errorf("error in CMD_ARTICLE srvtp=nil")
	}
	if debugthis {
		return 220, "fake article", 1234, nil
	}

	start := time.Now()
	id, aerr := connitem.srvtp.Cmd("ARTICLE <%s>", item.segment.Id)
	if aerr != nil {
		dlog(always, "ERROR in CMD_ARTICLE srvtp.Cmd @ '%s' err='%v'", connitem.c.provider.Name, aerr)
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
		rcode, rxb, _, err := readDotLines(connitem, item, cmdARTICLE)
		if err != nil {
			dlog(always, "ERROR in CMD_ARTICLE srvtp.ReadDotLines @ '%s' err='%v' code=%d rcode=%d", connitem.c.provider.Name, err, code, rcode)
			return code, "", uint64(rxb), err
		}
		dlog(cfg.opt.DebugARTICLE, "CMD_ARTICLE seg.Id='%s' @ '%s' msg='%s' rxb=%d lines=%d code=%d dlcnt=%d fails=%d", item.segment.Id, connitem.c.provider.Name, msg, item.size, len(item.article), code, item.dlcnt, item.fails)
		if rcode == 99932 {
			code = 99932 // bad crc32
		}
		return code, msg, uint64(rxb), nil

	case 430:
		dlog(cfg.opt.DebugARTICLE, "INFO CMD_ARTICLE:430 seg.Id='%s' @ '%s' msg='%s' err='%v' dlcnt=%d fails=%d", item.segment.Id, connitem.c.provider.Name, msg, err, item.dlcnt, item.fails)
		return code, msg, 0, nil // not an error, just no such article

	case 451:
		dlog((cfg.opt.Verbose || cfg.opt.Print430), "INFO CMD_ARTICLE:451 seg.Id='%s' @ '%s' msg='%s' err='%v' dlcnt=%d fails=%d", item.segment.Id, connitem.c.provider.Name, msg, err, item.dlcnt, item.fails)
		return code, msg, 0, nil // not an error, just DMCA

	default:
		// returns the unknown code with an error!
	}
	return code, msg, 0, fmt.Errorf("error in CMD_ARTICLE got unknown code=%d msg='%s' @ '%s' reqTook='%v' err='%v'", code, msg, connitem.c.provider.Name, time.Since(start), err)
} // end func CMD_ARTICLE

// CMD_IHAVE sends an article to the server using the IHAVE command.
// It returns the response code, message, size of the article sent, and any error encountered.
// The IHAVE command is used to transfer articles to a server that supports it.
func CMD_IHAVE(connitem *ConnItem, item *segmentChanItem) (int, string, uint64, error) {
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
		dlog(always, "ERROR CMD_IHAVE @ '%s' srvtp.Cmd err='%v'", connitem.c.provider.Name, err)
		return 0, "", 0, err
	}

	connitem.srvtp.StartResponse(id)
	code, msg, err := connitem.srvtp.ReadCodeLine(335)
	if err != nil {
		dlog(always, "ERROR CMD_IHAVE srvtp.ReadCodeLine @ '%s' err='%v'", connitem.c.provider.Name, err)
		return code, msg, 0, err
	}
	connitem.srvtp.EndResponse(id)

	switch code {
	case 335:
		// Send article to be transferred
		dlog(cfg.opt.DebugIHAVE, "CMD_IHAVE 335 sendit seg.Id='%s' @ '%s'", item.segment.Id, connitem.c.provider.Name)

	case 435:
		// Article not wanted
		dlog(cfg.opt.DebugIHAVE, "CMD_IHAVE 435 unwanted seg.Id='%s' @ '%s' msg='%s' err='%v'", item.segment.Id, connitem.c.provider.Name, msg, err)
		return code, msg, 0, nil

	case 436:
		// Transfer not possible; try again later
		dlog(cfg.opt.DebugIHAVE, "CMD_IHAVE 436 retry seg.Id='%s' @ '%s' msg='%s' err='%v'", item.segment.Id, connitem.c.provider.Name, msg, err)
		return code, msg, 0, nil

	default:
		dlog(always, "ERROR CMD_IHAVE got unknown code=%d msg='%s' @ '%s' reqTook='%v' err='%v'", code, msg, connitem.c.provider.Name, time.Since(start), err)
		return code, msg, 0, fmt.Errorf("error CMD_IHAVE unknown code=%d msg='%s' @ '%s' reqTook='%v' err='%v'", code, msg, connitem.c.provider.Name, time.Since(start), err)
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
				return 0, msg, txb, fmt.Errorf("error CMD_IHAVE WriteString writer @ '%s' err='%v'", connitem.c.provider.Name, err)
			}
			txb += uint64(n)
		}
		if _, err := io.WriteString(connitem.writer, DOT+CRLF); err != nil {
			return 0, msg, txb, fmt.Errorf("error CMD_IHAVE writer DOT+CRLF @ '%s' err='%v'", connitem.c.provider.Name, err)
		}
		if err := connitem.writer.Flush(); err != nil {
			return 0, msg, txb, fmt.Errorf("error CMD_IHAVE writer.Flush @ '%s' err='%v'", connitem.c.provider.Name, err)
		}
	} // end switch wireformat

	code, msg, err = connitem.srvtp.ReadCodeLine(235)
	if err != nil {
		dlog(always, "ERROR CMD_IHAVE srvtp.ReadCodeLine @ '%s' err='%v'", connitem.c.provider.Name, err)
		return code, msg, txb, err
	}
	switch code {
	case 235:
		//Article transferred OK
		dlog(cfg.opt.DebugIHAVE, "CMD_IHAVE 235 OK seg.Id='%s' @ '%s' msg='%s' txb=%d", item.segment.Id, connitem.c.provider.Name, msg, txb)
		return code, msg, txb, nil

	case 436:
		// Transfer failed; try again later
		dlog(cfg.opt.DebugIHAVE, "CMD_IHAVE 436 transfer failed, retry seg.Id='%s' @ '%s' msg='%s' err='%v'", item.segment.Id, connitem.c.provider.Name, msg, err)
		return code, msg, txb, nil

	case 437:
		//  Transfer rejected; do not retry
		dlog(cfg.opt.DebugIHAVE, "CMD_IHAVE 437 transfer rejected, no retry seg.Id='%s' @ '%s' msg='%s' err='%v'", item.segment.Id, connitem.c.provider.Name, msg, err)
		return code, msg, txb, nil
	}

	return code, msg, txb, fmt.Errorf("error in CMD_IHAVE uncatched return: code=%d msg='%s' err='%v'", code, msg, err)
} // end func CMD_IHAVE

// CMD_POST sends an article to the server using the POST command.
// It returns the response code, message, size of the article sent, and any error encountered.
func CMD_POST(connitem *ConnItem, item *segmentChanItem) (int, string, uint64, error) {
	if connitem == nil || connitem.conn == nil || connitem.srvtp == nil || connitem.writer == nil {
		return 0, "", 0, fmt.Errorf("error CMD_POST srvtp=nil")
	}
	id, err := connitem.srvtp.Cmd("POST")
	if err != nil {
		dlog(always, "ERROR CMD_POST @ '%s' srvtp.Cmd err='%v'", connitem.c.provider.Name, err)
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
		dlog(always, "ERROR code=%d in CMD_POST @ '%s' msg='%s' err='%v'", code, connitem.c.provider.Name, msg, err)
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
			return 0, "", txb, fmt.Errorf("error CMD_POST WriteString writer @ '%s' err='%v'", connitem.c.provider.Name, err)
		}
		txb += uint64(n)
	}
	if _, err := io.WriteString(connitem.writer, DOT+CRLF); err != nil {
		return 0, "", txb, fmt.Errorf("error CMD_POST writer DOT+CRLF @ '%s' err='%v'", connitem.c.provider.Name, err)
	}
	if err := connitem.writer.Flush(); err != nil {
		return 0, "", txb, fmt.Errorf("error CMD_POST writer.Flush @ '%s' err='%v'", connitem.c.provider.Name, err)
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

// readDotLines reads lines from the textproto.Conn until it encounters a single dot (.) line.
// It processes the lines according to the specified command (what) and returns the response code,
// the number of bytes read (rxb), the content as a slice of strings, and any error encountered.
// The function supports parsing headers, handling continued lines, and cleaning headers based on the configuration.
// It also supports yenc decoding if enabled in the configuration.
// WARNING: TODO! Be careful when reading X/OVER or X/HDR on large groups because you may run out of memory!
func readDotLines(connitem *ConnItem, item *segmentChanItem, what string) (code int, rxb int, content []string, err error) {
	if connitem.conn == nil || connitem.srvtp == nil {
		connitem.c.CloseConn(connitem, nil)
		return 0, 0, nil, fmt.Errorf("error readArticleDotLines: conn or srvtp nil @ '%s'", connitem.c.provider.Name)
	}
	if !IsItemCommand(what) && !IsOtherCommand(what) {
		// if not an item command or other command! this is a bug!
		// we just die here because returning an error will f***up the connection as it is already receiving lines from remote
		log.Printf("error readArticleDotLines: invalid command '%s'", what)
		os.Exit(1)
	}
	if IsOtherCommand(what) && item != nil {
		// do not submit an item for other commands! this is a bug!
		// we just die here because returning an error will f***up the connection as it is already receiving lines from remote
		log.Printf("error readArticleDotLines: do not submit an item for command '%s'", what)
		os.Exit(1)
	}
	var decoder *yenc.Decoder
	var parseHeader, ignoreNextContinuedLine, gotYencHeader, gotMultipart, brokenYenc bool // = false, false, false, false
	if what == cmdARTICLE || what == cmdHEAD {
		parseHeader = true
	}
	async, sentLinesToDecoder := false, 0
	var ydec []byte                  // contains yenc encoded body lines as []byte, to pass to the decoder in case 1
	var ydat []*string               // contains yenc encoded body lines as []*string, to pass to the decoder in case 2
	var decodeBodyChan chan *string  // channel for yenc lines to decode asynchronously in case 3
	var counter *Counter_uint64      // counter for async yenc decoding, if enabled in case 3
	var releaseDecoder chan struct{} // channel to release the decoder after processing in case 3
	var rydecoder *rapidyenc.Decoder // pointer to RapidYenc decoder, if enabled in case 4
	var pr *io.PipeReader
	var pw *io.PipeWriter
	var decodedData []byte
	var ryDoneChan chan error // channel to signal when RapidYenc decoding is done

	switch cfg.opt.YencTest {
	case 4:
		// use rapidyenc to decode the body lines
		pr, pw = io.Pipe()
		dlog(cfg.opt.DebugRapidYenc, "readDotLines: rapidyenc AcquireDecoderWithReader for seg.Id='%s' @ '%s'", item.segment.Id, connitem.c.provider.Name)
		rydecoder = rapidyenc.AcquireDecoderWithReader(pr)
		if rydecoder == nil {
			connitem.c.CloseConn(connitem, nil)
			return 0, 0, nil, fmt.Errorf("error readDotLines: failed to acquire rapidyenc decoder")
		}
		defer rapidyenc.ReleaseDecoder(rydecoder) // release the decoder after processing
		dlog(cfg.opt.DebugRapidYenc, "readDotLines: using rapidyenc decoder for seg.Id='%s' @ '%s'", item.segment.Id, connitem.c.provider.Name)
		ryDoneChan = make(chan error, 1)
		// we will read the decoded data from the decoder in a separate goroutine
		// this allows us to decode the data asynchronously while we are reading lines from the textproto.Conn
		go func(segId *string, decoded *[]byte, done chan error) {
			start := time.Now()
			decodedBuf := make([]byte, 64*1024)
			total := 0
			for {
				n, err := rydecoder.Read(decodedBuf)
				if n > 0 {
					*decoded = append(*decoded, decodedBuf[:n]...)
					total += n
				}
				if err == io.EOF {
					done <- nil
					break
				}
				if err != nil {
					done <- err
					break
				}
			}
			dlog(cfg.opt.DebugRapidYenc, "readDotLines: rapidyenc decoder done for seg.Id='%s' @ '%s' decoded=%d bufsize=%d total=%d took=(%d µs)", *segId, connitem.c.provider.Name, len(*decoded), len(decodedBuf), total, time.Since(start).Microseconds())
		}(&item.segment.Id, &decodedData, ryDoneChan)

	case 3:
		// yenc test 3 means we parse yenc lines directly to the decoder as we read them from the textproto.Conn,
		async = true // we will decode yenc lines asynchronously
		decoder = yenc.NewDecoder(nil, nil, ydat, 1)
		decoder.SegId = &item.segment.Id
		counter = NewCounter(2)
		releaseDecoder = make(chan struct{}, 1) // channel to release the decoder after processing

		if async {
			decodeBodyChan = make(chan *string, 6666) // buffered channel for yenc lines
			// launch a new goroutine to parse body lines async
			go func(segId string, provName string, decodeBodyChan chan *string, counter *Counter_uint64, releaseDecoder chan struct{}) {
				defer func() {
					releaseDecoder <- struct{}{}
				}()
				broken := false // for this routine
				// this goroutine will read lines from decodeBodyChan and pass them to the decoder
				// it will run until the channel is closed or nil is sent
				//for line := range decodeBodyChan {
				cOK, cERR := 0, 0
				start := time.Now()
			readChan:
				for {

					line, ok := <-decodeBodyChan
					if line == nil || !ok {
						break readChan // nil means we are done with this line
					}
					if broken {
						cERR++
						continue readChan // if we already got a broken yenc line, skip further processing
					}

					switch cfg.opt.YencTest {
					case 3:
						getAsyncCoreLimiter()
						if err := decoder.ReadBody(*line, true); err != nil {
							broken = true // we got a broken yenc multipart line
							log.Printf("ERROR async decodeBodyChan decoder.ReadBody: seg.Id='%s' @ '%s' err='%v'", segId, provName, err)
							cERR++
							counter.Incr("ERR")
							returnAsyncCoreLimiter()
							break readChan
						}
						returnAsyncCoreLimiter()

						cOK++
						counter.Incr("OK")

					}

					continue readChan
				} // end for range decodeBodyChan
				// we are done with the decodeBodyChan, now we can release the decoder (via defer)
				dlog(always, "END async decoder cOK=%d cERR=%d took=(%d µs) seg.Id='%s' @ '%s'", cOK, cERR, time.Since(start).Microseconds(), segId, provName)
			}(item.segment.Id, connitem.c.provider.Name, decodeBodyChan, counter, releaseDecoder)
		} // end if async
	} // end case 3
	startReadLines := time.Now()
	i := 0
readlines:
	for {
		// read 1 line from the textproto.Conn
		// there will be more until we hit the DOT
		// CRLF is already stripped by textproto!
		// we have pure human readable strings in lines backed in []string
		line, err := connitem.srvtp.ReadLine()
		if err != nil {
			// broken pipe to remote site
			return 0, rxb, nil, err
		}
		// see every line thats coming in
		//dlog( "readArticleDotLines: seg.Id='%s' line='%s'", segment.Id, line)
		rxb += len(line)
		if IsItemCommand(what) && rxb > cfg.opt.MaxArtSize {
			// max article size reached, stop reading
			// this is a DoS protection, so we do not read more than maxartsize
			err = fmt.Errorf("error readArticleDotLines: maxartsize=%d > rxb=%d seg.Id='%s' what='%s'", cfg.opt.MaxArtSize, rxb, item.segment.Id, what)
			log.Print(err)
			connitem.c.CloseConn(connitem, nil)
			return 0, rxb, nil, err
		}

		// found final dot in line, break here
		if len(line) == 1 && line == "." {
			break
		}

		if parseHeader && len(line) == 0 {
			// reading header ends here
			parseHeader = false

			// add new headers for ignored ones
			now := time.Now().Format(time.RFC1123Z)
			datestr := fmt.Sprintf("Date: %s", now)
			content = append(content, datestr)
		}

		if parseHeader {
			// parse header lines
			// check for continued lines, which are indented with a space or tab
			isSpacedLine := (len(line) > 0 && (line[0] == ' ' || line[0] == '\t'))
			if !isSpacedLine && ignoreNextContinuedLine {
				ignoreNextContinuedLine = false
			}
			if isSpacedLine && ignoreNextContinuedLine {
				continue readlines
			}

			if cfg.opt.CleanHeaders {
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
			content = append(content, line)
		} // end parseHeader

		if !parseHeader && what != cmdHEAD {
			i++ // counts body lines
			if what == cmdARTICLE || what == cmdBODY {
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
			}

			if what == cmdARTICLE || what == cmdBODY || IsOtherCommand(what) {
				// if we are in ARTICLE or BODY or any other multiline command, we store the line
				content = append(content, line)
			}

			if cfg.opt.YencCRC && !brokenYenc && (what == cmdARTICLE || what == cmdBODY) {
				switch cfg.opt.YencTest {
				case 1:
					// case 1 needs double the memory
					ydec = append(ydec, line...) // as []byte (with(out) crlf?) via an io.reader
				case 2:
					// case 2
					ydat = append(ydat, &line) // as []*string line per line needs less mem allocs
				case 4:
					// rapidyenc test 4
					//ydec = append(ydec, line+CRLF...) // append the line to the byte buffer
					dlog(cfg.opt.BUG, "readArticleDotLines: rapidyenc pw.Write bodyline=%d size=(%d bytes) @ '%s'", i, len(line), connitem.c.provider.Name)
					if _, err := pw.Write([]byte(line + CRLF)); err != nil {
						pw.CloseWithError(err)
						connitem.c.CloseConn(connitem, nil)
						dlog(always, "ERROR readArticleDotLines: rapidyenc pw.Write failed @ '%s' err='%v'", connitem.c.provider.Name, err)
						return 0, rxb, nil, fmt.Errorf("error readArticleDotLines: rapidyenc pw.Write failed @ '%s' err='%v'", connitem.c.provider.Name, err)
					}
					dlog(cfg.opt.BUG, "readArticleDotLines: rapidyenc pw.Write done line=%d seg.Id='%s' @ '%s'", i, item.segment.Id, connitem.c.provider.Name)
				case 3:
					// case 3
					// parse yenc lines directly and asynchronously
					// to the decoder as we read them from the textproto.Conn
					switch gotYencHeader {
					case true:
						if brokenYenc {
							// if we got a broken yenc, we do not parse yenc lines anymore
							// but we still read the lines to get the rest of the article
							// because we cant stop reading lines until we hit the final dot (.)
							continue readlines
						}
						// we got a yenc header, check if its multipart or not
						if decoder.Multipart && !gotMultipart {
							if err := decoder.ReadPartHeader(line); err != nil {
								brokenYenc = true // we got a broken yenc multipart line
								log.Printf("ERROR brokenYenc readArticleDotLines: seg.Id='%s' @ '%s' decoder.ReadPartHeader err='%v'", item.segment.Id, connitem.c.provider.Name, err)
								// continue reading lines, but we will not decode yenc anymore
								continue readlines
							}
							gotMultipart = true // we got a multipart yenc header
							continue readlines
						}
						// we got the yenc header

						switch async {
						case true:
							// if we are in async mode check first if we got an error
							if counter.GetValue("ERR") > 0 {
								// if we got an error, we do not decode yenc anymore
								brokenYenc = true // we got a broken yenc body line
								dlog(always, "ERROR async brokenYenc readArticleDotLines: seg.Id='%s' @ '%s' yenc.ReadBody counter.GetValue('ERR') > 0", item.segment.Id, connitem.c.provider.Name)
								continue readlines // continue reading lines, but we will not decode yenc anymore
							}
							// send line to the decodeBodyChan for async processing
							// will block if channel is full until decoder processed more lines
							decodeBodyChan <- &line
							// sent line to the channel for async processing
							sentLinesToDecoder++ // count sent lines to decoder
							dlog(cfg.opt.BUG, "async readArticleDotLines: seg.Id='%s' @ '%s' sentLinesToDecoder=%d line='%x'", item.segment.Id, connitem.c.provider.Name, sentLinesToDecoder, line)

						case false:
							// now decode the incoming lines line by line for the yenc body
							//if err := decoder.ReadBody(strings.TrimSpace(line), true); err != nil {
							if err := decoder.ReadBody(line, true); err != nil {
								brokenYenc = true // we got a broken yenc body line
								dlog(always, "ERROR brokenYenc readArticleDotLines: seg.Id='%s' @ '%s' yenc.ReadBody err='%v'", item.segment.Id, connitem.c.provider.Name, err)
								// continue reading lines, but we will not decode yenc anymore
								continue readlines
							}
						} // end switch async
						continue readlines

					case false:
						//if !decoder.Multipart && !gotYencHeader && !gotMultipart {
						if len(line) < 7 || !strings.HasPrefix(line, "=ybegin ") {
							continue readlines // skip this line, we are not in a yenc header
						}
						// this is a yenc line, so we can parse it directly to the decoder
						if err := decoder.ReadHeader(&line, false); err != nil {
							brokenYenc = true // we got a broken yenc header
							dlog(always, "ERROR brokenYenc readArticleDotLines: seg.Id='%s' @ '%s' yenc.ReadPartHeader err='%v'", item.segment.Id, connitem.c.provider.Name, err)
							// continue reading lines, but we will not decode yenc anymore
							continue readlines
						}
						if decoder.Part.Name == "" {
							brokenYenc = true // we got a broken yenc header
							dlog(always, "ERROR brokenYenc readArticleDotLines: seg.Id='%s' @ '%s' empty Name field in yenc.Decoder", item.segment.Id, connitem.c.provider.Name)
							// continue reading lines, but we will not decode yenc anymore
							continue readlines
						}
						//}
						// finally got a yenc header
						gotYencHeader = true // good to read the next body lines now
						continue readlines   // continue reading body lines
					} // end select gotYencHeader
				} // end case 3
			} // end if cfg.opt.YencCRC && !brokenYenc
		} // end if !parseHeader && what != cmdHEAD

		// spammy! following dlog is for debugging purposes only and prints
		//  the line number, length of the line, rxb
		//   and accumulated content length while still reading lines!
		dlog((cfg.opt.BUG && cfg.opt.DebugARTICLE), "readArticleDotLines: seg.Id='%s' lineNum=%d len(line)=%d rxb=%d content=(%d lines)", item.segment.Id, i, len(line), rxb, len(content))

	} // end for readlines

	// case 4: we are done reading lines, close the pipe writer
	if pw != nil {
		pw.Write([]byte(DOT + CRLF)) // write the final dot to the pipe
		pw.Close()                   // <-- THIS IS CRUCIAL!
		err = <-ryDoneChan           // wait for decoder to finish
		if err != nil {
			log.Printf("ERROR rapidyenc.Read: err='%v'", err)
			brokenYenc = true
		}
	}
	if async && decodeBodyChan != nil {
		close(decodeBodyChan) // close the channel to signal we are done with reading lines
	}
	dlog(cfg.opt.Debug, "readArticleDotLines: seg.Id='%s' rxb=%d content=(%d lines) took=(%d µs) what='%s'", item.segment.Id, rxb, len(content), time.Since(startReadLines).Microseconds(), what)

	if cfg.opt.YencCRC && !brokenYenc && (what == cmdARTICLE || what == cmdBODY) {
		yencstart := time.Now()
		var startReadSignals time.Time
		var isBadCrc bool

		getCoreLimiter()
		defer returnCoreLimiter()

		switch cfg.opt.YencTest {

		case 1:
			decoder := yenc.NewDecoder(nil, ydec, nil, 1)
			decoder.SegId = &item.segment.Id
			if yPart, badcrc, err := decoder.Decode(); err != nil { // chrisfarms/yenc
				dlog(always, "ERROR yenc.Decode mode=%d seg.Id='%s' @ '%s' ydec=(%d bytes) err='%v'", cfg.opt.YencTest, item.segment.Id, connitem.c.provider.Name, len(ydec), err)
				isBadCrc = badcrc
			} else {
				dlog(always, "YencCRC OK mode=%d seg.Id='%s' yPart.Body=%d Number=%d crc32=%x'", cfg.opt.YencTest, item.segment.Id, len(yPart.Body), yPart.Number, yPart.Crc32)
				isBadCrc = badcrc
			}
		case 2:
			decoder := yenc.NewDecoder(nil, nil, ydat, 1)
			decoder.SegId = &item.segment.Id
			if yPart, badcrc, err := decoder.DecodeSlice(); err != nil { // go-while/yenc#testing-branch
				dlog(always, "ERROR yenc.Decode mode=%d seg.Id='%s' @ '%s' ydat=(%d lines) err='%v' badcrc=%t", cfg.opt.YencTest, item.segment.Id, connitem.c.provider.Name, len(ydat), err, badcrc)
			} else {
				if yPart != nil {
					dlog(always, "YencCRC OK yenctest=%d simd=%d seg.Id='%s' yPart.Body=%d Number=%d crc32=%x'", cfg.opt.YencTest, yenc.SimdMode, item.segment.Id, len(yPart.Body), yPart.Number, yPart.Crc32)
				} else {
					dlog(always, "YencCRC OK yenctest=%d simd=%d seg.Id='%s' yPart is nil", cfg.opt.YencTest, yenc.SimdMode, item.segment.Id)
					// we got a nil yPart, so we do not write it to the cache
					break
				}
				if cfg.opt.YencWrite && cacheON {
					cache.WriteYenc(item, yPart)
				} else {
					decoder.Part.Body = nil
					decoder.Part = nil
					decoder = nil
				}
			}
		case 3:
			if decoder == nil {
				dlog(always, "error readArticleDotLines: decoder is nil for seg.Id='%s' @ '%s'", item.segment.Id, connitem.c.provider.Name)
				break
			}
			startReadSignals = time.Now()
			if async {
				var countOK uint64
				// wait for all lines to be processed by the decoder
				// we will wait for the yencReturnsOK channel to be closed or nil sent
				// this will block until all lines are processed
				//timeout := time.Now().Add(100 * time.Millisecond)
				//target := uint64(sentLinesToDecoder)
				// possible deadlock here if we do not wait for the decoder to finish processing
				start := time.Now()
				if releaseDecoder != nil {
					<-releaseDecoder // wait for the decoder to finish processing
				}
				dlog(always, "readArticleDotLines: seg.Id='%s' @ '%s' waited=(%d µs) for async decoder sentLinesToDecoder=%d", item.segment.Id, connitem.c.provider.Name, time.Since(start).Microseconds(), sentLinesToDecoder)
				/*
					waitYenc:
						for {
							if counter.GetValue("ERR") > 0 {
								// if we got an error, we do not decode yenc anymore
								dlog(always, "ERROR async brokenYenc readArticleDotLines: seg.Id='%s' @ '%s' yenc.ReadBody counter.GetValue('ERR') > 0", item.segment.Id, connitem.c.provider.Name)
								break waitYenc
							}
							value := counter.GetValue("OK")
							if value > countOK {
								timeout = time.Now().Add(100 * time.Millisecond)
							}
							countOK = value
							if value == target {
								// we got all lines processed
								break waitYenc
							}
							if time.Now().After(timeout) {
								// if we waited more than 100 milliseconds, we stop waiting
								dlog(always, "ERROR async brokenYenc readArticleDotLines: seg.Id='%s' @ '%s' timeout reached after 100 milliseconds", item.segment.Id, connitem.c.provider.Name)
								break waitYenc
							}
							time.Sleep(time.Millisecond)
						}
				*/
				if counter.GetValue("OK") != uint64(sentLinesToDecoder) {
					// we did not process all lines, so we have a problem
					brokenYenc = true // we got a broken yenc body line
					dlog(always, "ERROR async brokenYenc countOK=%d sentLinesToDecoder=%d readArticleDotLines: seg.Id='%s' @ '%s'", countOK, sentLinesToDecoder, item.segment.Id, connitem.c.provider.Name)
				} else {
					dlog(cfg.opt.BUG, "readArticleDotLines: async yencReturnsOK countOK=%d sentLinesToDecoder=%d", countOK, sentLinesToDecoder)
				}
			}
			if brokenYenc || decoder.Part == nil {
				break // the case 3
			}
			// validate the yenc part
			dlog(cfg.opt.BUG, "readArticleDotLines:try Validate yenctest=%d YencCRC seg.Id='%s' @ '%s' decoder.Part.Body=%d", cfg.opt.YencTest, item.segment.Id, connitem.c.provider.Name, len(decoder.Part.Body))
			if err := decoder.Part.Validate(&item.segment.Id); err != nil || decoder.Part.BadCRC {
				isBadCrc = decoder.Part.BadCRC
				/*
				* d.Part='&yenc.Part{
				* 		Number:18,
				* 		HeaderSize:209715695,
				* 		Size:640000,
				* 		Begin:10880001,
				* 		End:11520000,
				* 		Name:"debian-11.1.0-i386-DVD-1.part01.rev",
				* 		cols:128,
				* 		Crc32:0xfd8ec0f3,
				* 		crcHash:(*crc32.digest)(0xc00092d230), Body: :[]uint8{0x93, 0x9, 0x2d, 0xeb, 0x7d, 0x1d, 0x55, 0xbf, 0xfe, 0xde, 0x89, 0x68, 0x56, 0x84, 0x82, 0xf9, 0xed, ....
				 */
				if decoder.Part != nil {
					log.Printf("Error readDotLines: yenc.Part.Validate: error validate decoder.seg.Id='%s' @Number=%d err='%v' isBadCrc=%t", *decoder.SegId, decoder.Part.Number, err, isBadCrc)
				} else {
					log.Printf("Error readDotLines: yenc.Part.Validate: decoder.seg.Id='%s' err='%v' decoder.Part is nil", *decoder.SegId, err)
				}
				break // the case 3
			}
			if cfg.opt.YencWrite && cacheON && decoder.Part != nil {
				cache.WriteYenc(item, decoder.Part)
			}

		case 4:
			if rydecoder == nil {
				dlog(always, "error readArticleDotLines: rydecoder is nil for seg.Id='%s' @ '%s'", item.segment.Id, connitem.c.provider.Name)
				break
			}

			// rapidyenc test 4
			/*
				getAsyncCoreLimiter()

				returnAsyncCoreLimiter()
			*/
			dlog(cfg.opt.DebugRapidYenc, "readArticleDotLines: rapidyenc.Read seg.Id='%s' @ '%s' decodedData=(%d bytes)", item.segment.Id, connitem.c.provider.Name, len(decodedData))
			// decodedData now contains the decoded yEnc body
			// TODO check crc again vs old yenc.crc ?
			meta := rydecoder.Meta()
			part := &yenc.Part{
				Number:     item.segment.Number, // or use correct part number if available
				HeaderSize: 0,                   // set if you have this info
				Size:       meta.Size,
				Begin:      meta.Begin,
				End:        meta.End,
				Name:       meta.Name,
				Crc32:      meta.Hash,
				Body:       decodedData,
				BadCRC:     false, // set to true if you detected a CRC error
			}
			// Now write to cache
			cache.WriteYenc(item, part)
		} // end switch yencTest
		dlog(cfg.opt.DebugWorker, "readArticleDotLines: YencCRC yenctest=%d seg.Id='%s' @ '%s' rxb=%d content=(%d lines) Part.Validate:took=(%d µs) readDotLines:took=(%d µs) startReadSignals:took=(%d µs) cfg.opt.YencWrite=%t err='%v'", cfg.opt.YencTest, item.segment.Id, connitem.c.provider.Name, rxb, len(content), time.Since(yencstart).Microseconds(), time.Since(yencstart).Microseconds(), time.Since(startReadSignals).Microseconds(), cfg.opt.YencWrite, err)
		if rydecoder != nil {
			rapidyenc.ReleaseDecoder(rydecoder)
		}
		if isBadCrc {
			item.mux.Lock()
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
			dlog(always, "ERROR readArticleDotLines crc32 failed seg.Id='%s' @ '%s'", item.segment.Id, connitem.c.provider.Name)
			return 99932, rxb, nil, nil // 99932 is a custom code for bad crc32
		}
	} // end if cfg.opt.YencCRC

	item.mux.Lock()

	switch what {
	case cmdARTICLE:
		item.article = content
		item.size = rxb
		item.dlcnt++
	case cmdBODY:
		item.body = content
		item.bodysize = rxb
	case cmdHEAD:
		item.head = content
		item.headsize = rxb
	default:
		// handle multi-line dot-terminated command
		if !IsOtherCommand(what) {
			// error unknown command
			return 0, rxb, nil, fmt.Errorf("error readArticleDotLines parsed unknown command '%s' @ '%s'", what, connitem.c.provider.Name)
		}
		// these commands are not stored in item, but will be returned as content
		// pass
	}

	// reaching here means we have read the article or body or head
	// or any other command that is not stored in the item
	if !IsOtherCommand(what) {
		// clears content on head, body or article
		content = nil // clear content if not an other command
	} // else pass
	item.mux.Unlock()
	return 1, rxb, content, nil
} // end func readArticleDotLines

// isItemCommand checks if the command is in the itemReadLineCommands map.
func IsItemCommand(what string) bool {
	// these commands store content in item, and will NOT return the content!
	_, ok := itemReadLineCommands[strings.ToUpper(what)]
	if ok {
		return true
	}
	// if not found in the map, return false
	return false
}

// isOtherCommand checks if the command is in the otherReadLineCommands map.
func IsOtherCommand(what string) bool {
	// these commands are NOT stored in item, but will be returned as content!
	_, ok := otherReadLineCommands[strings.ToUpper(what)]
	if ok {
		return true
	}
	// if not found in the map, return false
	return false
}

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
