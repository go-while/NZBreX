package main

import (
	"bufio"
	"fmt"
	"github.com/go-while/yenc" // fork of chrisfarms with little mods
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Cache struct {
	mux               sync.RWMutex
	cachedir          string
	checkOnly         bool
	crw               int
	cache_reader_chan chan *segmentChanItem
	cache_writer_chan chan *segmentChanItem
	yenc_writer_chan  chan *yenc_item
	cache_check_chan  chan *segmentChanItem
	maxartsize        int
	yenc_write        bool
	debug             bool
}

type yenc_item struct {
	item  *segmentChanItem
	yPart *yenc.Part
}

func NewCache(cachedir string, crw int, checkOnly bool, maxartsize int, yenc_write bool, debug bool) (c *Cache) {
	if cachedir == "" {
		log.Printf("Error NewCache: cachedir is empty!")
		return nil
	}
	c = &Cache{
		cachedir:   cachedir,
		checkOnly:  checkOnly,
		crw:        crw,
		maxartsize: maxartsize,
		yenc_write: yenc_write,
		debug:      debug,
	}
	if !Mkdir(c.cachedir) {
		log.Printf("ERROR creating Cachedir: '%s/'", c.cachedir)
		return nil
	}
	c.cache_check_chan = make(chan *segmentChanItem, c.crw)
	c.cache_reader_chan = make(chan *segmentChanItem, c.crw)
	c.cache_writer_chan = make(chan *segmentChanItem, c.crw)
	c.yenc_writer_chan = make(chan *yenc_item, c.crw)

	for i := 1; i <= c.crw; i++ {
		go c.GoCacheReader(i)
		go c.GoCacheChecker(i)
		if !c.checkOnly {
			go c.GoCacheWriter(i)
			if yenc_write {
				go c.GoYencWriter(i)
			}
		}
	}
	return
} // end func NewCache

func (c *Cache) Add2Cache(item *segmentChanItem) {
	c.cache_writer_chan <- item
} // end func c.Add2Cache

func (c *Cache) MkSubDir(nzbhashname string) (exists bool) {
	subdir := filepath.Join(c.cachedir, nzbhashname)
	if DirExists(subdir) {
		if c.debug {
			log.Printf("Cache SubDir exists: '%s'", subdir)
		}
		return true
	}
	if !Mkdir(subdir) {
		log.Printf("ERROR cache.MkSubDir: '%s/%s'", c.cachedir, nzbhashname)
		return false
	}
	if c.debug {
		log.Printf("Cache SubDir created: '%s'", subdir)
	}
	return true
} // func c.MkSubDir

func (c *Cache) CheckCache(item *segmentChanItem) (exists bool) {
	c.cache_check_chan <- item // request
	exists = <-item.checkChan  // infinite wait for request
	return
} // end func c.CheckCache

func (c *Cache) ReadCache(item *segmentChanItem) (n int) {
	item.mux.RLock()
	if len(item.lines) > 0 {
		item.mux.RUnlock()
		return item.size
	}
	item.mux.RUnlock()

	c.cache_reader_chan <- item // request
	// FIXME TODO ReadCache add select with timeout
	n = <-item.readChan // infinite wait for notify that file has been read from cache

	if n > 0 {
		item.mux.Lock()
		item.cached = true
		if item.size == 0 {
			item.size = n
		}
		if item.flaginDL {
			item.flaginDL = false
		}
		if item.flaginDLMEM {
			item.flaginDLMEM = false
		}
		item.mux.Unlock()
	}
	return
} // end func c.ReadCache

func (c *Cache) GoCacheChecker(cid int) {
	for {
		select {
		case item := <-c.cache_check_chan:
			if item == nil {
				c.cache_check_chan <- nil
				return
			}
			filename := filepath.Join(c.cachedir, *item.nzbhashname, item.hashedId+".art")
			exists := FileExists(filename)
			if c.debug {
				log.Printf("GoCacheChecker exists=%t seg.Id='%s' hashedId='%s'", exists, item.segment.Id, item.hashedId)
			}
			if exists {
				item.mux.Lock()
				item.cached = true
				item.mux.Unlock()
			}
			item.checkChan <- exists // notify
		}
	}
} // end func c.GoCacheChecker

func (c *Cache) GoCacheReader(cid int) {
	//var read_bytes uint64
	for {
		select {
		case item := <-c.cache_reader_chan:
			if item == nil {
				c.cache_reader_chan <- nil
				return
			}
			size := c.CacheReader(item)
			if c.debug {
				log.Printf("GoCacheReader size=%d id='%s'", size, item.hashedId)
			}
			//read_bytes += uint64(size)
			item.readChan <- size // notify
		}
	}
} // end func c.GoCacheReader

func (c *Cache) GoCacheWriter(cid int) {
	var wrote_bytes uint64
	for {
		select {
		case item := <-c.cache_writer_chan:
			if item == nil {
				c.cache_writer_chan <- nil
				return
			}
			n := c.CacheWriter(item)
			wrote_bytes += uint64(n)
		} // end select
	}
} // end func c.GoCacheWriter

func (c *Cache) GoYencWriter(cid int) {
	var wrote_bytes uint64
	for {
		select {
		case yitem := <-c.yenc_writer_chan:
			if yitem == nil {
				c.yenc_writer_chan <- nil
				return
			}
			n := c.YencWriter(yitem)
			wrote_bytes += uint64(n)
		} // end select
	}
} // end func c.GoCacheWriter

func (c *Cache) CacheReader(item *segmentChanItem) (read_bytes int) {
	item.mux.Lock()
	if item.hashedId == "" {
		item.hashedId = SHA256str("<" + item.segment.Id + ">")
	}
	item.mux.Unlock()
	filename := filepath.Join(c.cachedir, *item.nzbhashname, item.hashedId+".art")
	if fileobj, err := ioutil.ReadFile(filename); err != nil {
		// read from diskcache failed
		if c.debug {
			log.Printf("CacheReader 404 ioutil.ReadFile err='%v'", err)
		}
	} else {
		read_bytes = len(fileobj)
		// TODO: store as wireformat \r\n needs changes as sendDotLines won't work
		if read_bytes > 0 {
			// parses the byte object from file to []string
			item.mux.Lock()
			item.lines = strings.Split(string(fileobj)[:len(fileobj)-1], "\n")
			item.cached = true
			if item.flaginDL {
				item.flaginDL = false
			}
			item.mux.Unlock()
		}
	} // end ioutil.ReadFile
	return
} // end func c.CacheReader

func (c *Cache) CacheWriter(item *segmentChanItem) (wrote_bytes int) {

	if len(item.lines) == 0 {
		return 0
	}

	item.mux.Lock()
	if item.hashedId == "" {
		item.hashedId = SHA256str("<" + item.segment.Id + ">")
	}
	item.mux.Unlock()

	filename := filepath.Join(c.cachedir, *item.nzbhashname, item.hashedId+".art")

	if FileExists(filename) {
		item.mux.Lock()
		item.cached = true
		item.mux.Unlock()
		return
	}

	filename_tmp := filename + ".tmp"
	if file, err := os.OpenFile(filename_tmp, os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		defer file.Close()
		datawriter := bufio.NewWriterSize(file, c.maxartsize)
		for _, line := range item.lines {
			if n, err := datawriter.WriteString(line + LF); err != nil {
				log.Printf("ERROR GoCacheWriter datawriter.Write err='%v'", err)
				return 0
			} else {
				wrote_bytes += n
			}
		}

		if err := datawriter.Flush(); err != nil {
			log.Printf("ERROR CacheWriter datawriter.Flush err='%v'", err)
			return 0
		}

		file.Close()
		if err := os.Rename(filename_tmp, filename); err != nil {
			log.Printf("ERROR GoCacheWriter move .tmp failed err='%v'", err)
			return 0
		}
	} // end OpenFile

	item.mux.Lock()
	item.cached = true
	item.flaginDLMEM = false
	item.flaginDL = false
	if Counter.get("postProviders") == 0 || cfg.opt.UploadLater {
		item.lines = []string{} // free memory
	}
	item.mux.Unlock()

	if c.debug {
		log.Printf("CacheWrote seg.Id='%s' h_Id='%s' => f='%s' (b=%d)", item.segment.Id, item.hashedId, filename, wrote_bytes)
	}
	return
} // end func c.CacheWriter

func (c *Cache) GetYenc(item *segmentChanItem) (filename string, filename_tmp string, yencdir string, fp string, fp_tmp string) {
	//filename = fmt.Sprintf("%s.part.%d.yenc", filepath.Base(item.file.Filename), item.segment.Number)
	filename = fmt.Sprintf("%s.%0"+D+"d", filepath.Base(item.file.Filename), item.segment.Number)
	filename_tmp = filename + ".tmp"
	yencdir = filepath.Join(c.cachedir, *item.nzbhashname, "yenc")
	fp = filepath.Join(yencdir, filename)
	fp_tmp = filepath.Join(yencdir, filename_tmp)
	return
} // end func GetYenc

func (c *Cache) WriteYenc(item *segmentChanItem, yPart *yenc.Part) {
	if c.yenc_writer_chan == nil {
		log.Printf("ERROR cache.WriteYenc yenc_writer_chan is nil")
		return
	}
	if len(yPart.Body) == 0 {
		log.Printf("Error WriteYenc: empty Body seg.Id='%s'", item.segment.Id)
		return
	}
	Counter.incr("yencQueueCnt")
	Counter.incr("TOTAL_yencQueueCnt")
	item.mux.Lock()
	item.flaginYenc = true
	item.mux.Unlock()
	c.yenc_writer_chan <- &yenc_item{
		item:  item,
		yPart: yPart,
	}
} // emd func WriteYenc

func (c *Cache) YencWriter(yitem *yenc_item) (wrote_bytes int) {
	defer Counter.decr("yencQueueCnt")

	yitem.item.mux.Lock()
	if yitem.item.hashedId == "" {
		yitem.item.hashedId = SHA256str("<" + yitem.item.segment.Id + ">")
	}
	yitem.item.mux.Unlock()

	_, _, yencdir, fp, fp_tmp := c.GetYenc(yitem.item)

	if FileExists(fp) {
		c.resetYencFlagsOnErr(yitem.item)
		return 0
	}

	if !Mkdir(yencdir) {
		c.resetYencFlagsOnErr(yitem.item)
		return 0
	}

	if c.debug {
		log.Printf("Writing yenc part: '%s'", fp_tmp)
	}
	//doMemReturn := true
	if file, err := os.OpenFile(fp_tmp, os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		defer file.Close()
		datawriter := bufio.NewWriterSize(file, len(yitem.yPart.Body))
		if n, err := datawriter.Write(yitem.yPart.Body); err != nil {
			log.Printf("ERROR YencWriter datawriter.Write err='%v'", err)
			c.resetYencFlagsOnErr(yitem.item)
			return 0
		} else {
			wrote_bytes += n
		}

		if err := datawriter.Flush(); err != nil {
			log.Printf("ERROR YencWriter datawriter.Flush err='%v'", err)
			c.resetYencFlagsOnErr(yitem.item)
			return 0
		}

		file.Close()

		if err := os.Rename(fp_tmp, fp); err != nil {
			log.Printf("ERROR YencWriter move .tmp failed err='%v'", err)
			c.resetYencFlagsOnErr(yitem.item)
			return 0
		}

		yitem.item.mux.Lock()
		yitem.item.flaginYenc = false
		yitem.item.flagisYenc = true
		/* // watch out for broken wings #99ffff!
		if yitem.item.flaginUP {
			doMemReturn = false
		}
		*/
		yitem.item.mux.Unlock()
	} // end OpenFile
	/* // watch out for broken wings #99ffff!
	if doMemReturn {
		memlim.MemReturn("yenc:cache", yitem.item)
	}
	*/
	yitem.yPart.Body = nil
	yitem.yPart = nil
	return
} // end func c.YencWriter

func (c *Cache) resetYencFlagsOnErr(item *segmentChanItem) {
	//doMemReturn := true
	item.mux.Lock()
	item.flaginYenc = false
	item.flagisYenc = false
	/* // watch out for broken wings #99ffff!
	if item.flaginUP {
		doMemReturn = false
	}
	*/
	item.mux.Unlock()
	/*
		if doMemReturn {
			memlim.MemReturn("resetYencFlagsOnErr", item)
		}
	*/
} // end func resetYencFlagsOnErr
