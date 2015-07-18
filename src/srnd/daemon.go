//
// daemon.go
//
package srnd
import (
  "log"
  "net"
  "strconv"
  "strings"
  "net/textproto"
  "time"
)

type NNTPDaemon struct {
  instance_name string
  bind_addr string
  conf *SRNdConfig
  store ArticleStore
  database Database
  mod Moderation
  expire ExpirationCore
  listener net.Listener
  debug bool
  sync_on_start bool
  running bool
  // http frontend
  frontend Frontend

  // nntp feeds map, feed, isoutbound
  feeds map[NNTPConnection]bool
  infeed chan NNTPMessage
  // channel to load messages to infeed given their message id
  infeed_load chan string
  // channel for broadcasting a message to all feeds given their message id
  send_all_feeds chan string
}

func (self *NNTPDaemon) End() {
  self.listener.Close()
}


// register a new connection
// can be either inbound or outbound
func (self *NNTPDaemon) newConnection(conn net.Conn, inbound bool, policy *FeedPolicy) NNTPConnection {
  feed := NNTPConnection{conn, textproto.NewConn(conn), inbound, self.debug, new(ConnectionInfo), policy,  make(chan string, 512), false, self.store, self.store}
  self.feeds[feed] = ! inbound
  return feed
}

func (self *NNTPDaemon) persistFeed(conf FeedConfig) {
  for {
    if self.running {
      
      var conn net.Conn
      var err error
      proxy_type := strings.ToLower(conf.proxy_type)
      
      if proxy_type ==  "" || proxy_type == "none" {
        // connect out without proxy 
        log.Println("dial out to ", conf.addr)
        conn, err = net.Dial("tcp", conf.addr)
        if err != nil {
          log.Println("cannot connect to outfeed", conf.addr, err)
					time.Sleep(time.Second)
          continue
        }
      } else if proxy_type == "socks4a" {
        // connect via socks4a
        log.Println("dial out via proxy", conf.proxy_addr)
        conn, err = net.Dial("tcp", conf.proxy_addr)
        if err != nil {
          log.Println("cannot connect to proxy", conf.proxy_addr)
					time.Sleep(time.Second)
          continue
        }
        // generate request
        idx := strings.LastIndex(conf.addr, ":")
        if idx == -1 {
          log.Fatal("invalid outfeed address")
        }
        var port uint64
        addr := conf.addr[:idx]
        port, err = strconv.ParseUint(conf.addr[idx+1:], 10, 16)
        if port >= 25536 {
          log.Fatal("bad proxy port" , port)
        }
        var proxy_port uint16
        proxy_port = uint16(port)
        proxy_ident := "srndv2"
        req_len := len(addr) + 1 + len(proxy_ident) + 1 + 8

        req := make([]byte, req_len)
        // pack request
        req[0] = '\x04'
        req[1] = '\x01'
        req[2] = byte(proxy_port & 0xff00 >> 8)
        req[3] = byte(proxy_port & 0x00ff)
        req[7] = '\x01'
        idx = 8
        
        proxy_ident_b := []byte(proxy_ident)
        addr_b := []byte(addr)
        
        var bi int
        for bi = range proxy_ident_b {
          req[idx] = proxy_ident_b[bi]
          idx += 1
        }
        idx += 1
        for bi = range addr_b {
          req[idx] = addr_b[bi]
          idx += 1
        }
  
        // send request
        conn.Write(req)
        resp := make([]byte, 8)
        
        // receive response
        conn.Read(resp)
        if resp[1] == '\x5a' {
          // success
          log.Println("connected to", conf.addr)
        } else {
          log.Println("failed to connect to", conf.addr)
					time.Sleep(5)
          continue
        }
      }
      policy := &conf.policy
      nntp := self.newConnection(conn, false, policy)
      // start syncing in background
      go func() {
        if self.sync_on_start {
          log.Println("sync on start")
          // get every article
          articles := self.database.GetAllArticles()
          // wait 5 seconds for feed to handshake
          time.Sleep(5 * time.Second)
          log.Println("outfeed begin sync")
          for _, result := range articles {
            msgid := result[0]
            group := result[1]
            if policy.AllowsNewsgroup(group) {
              //XXX: will this crash if interrupted?
              nntp.sync <- msgid
            }
          }
          log.Println("outfeed end sync")
        }
      }()
      nntp.HandleOutbound(self)
      log.Println("remove outfeed")
      delete(self.feeds, nntp)
    }
  }
  time.Sleep(1 * time.Second)
}

// run daemon
func (self *NNTPDaemon) Run() {	
  defer self.listener.Close()
  // run expiration mainloop
  go self.expire.Mainloop()
  // we are now running
  self.running = true
  
  // persist outfeeds
  for idx := range self.conf.feeds {
    go self.persistFeed(self.conf.feeds[idx])
  }

  // start accepting incoming connections
  go self.acceptloop()

  go func () {
    // if we have no initial posts create one
    if self.database.ArticleCount() == 0 {
      nntp := newPlaintextArticle("welcome to nntpchan, this post was inserted on startup automatically", "system@"+self.instance_name, "Welcome to NNTPChan", "system", self.instance_name, "overchan.test")
      file := self.store.CreateTempFile(nntp.MessageID())
      if file != nil {
        err := self.store.WriteMessage(nntp, file)
        file.Close()
        if err == nil {
          self.infeed <- nntp
        } else {
          log.Println("failed to create startup messge?", err)
        }
      }
    }
  }()
  // if we have no frontend this does nothing
  if self.frontend != nil {
    go self.pollfrontend()
  }
  self.pollfeeds()

}


func (self *NNTPDaemon) pollfrontend() {
  chnl := self.frontend.NewPostsChan()
  for {
    select {
    case nntp := <- chnl:
      // new post from frontend
      log.Println("frontend post", nntp.MessageID)
      self.infeed <- nntp
    }
  }
}

func (self *NNTPDaemon) pollfeeds() {
  chnl := self.frontend.PostsChan()
  for {
    select {
    case msgid := <- self.send_all_feeds:
      // send all feeds
      nntp := self.store.GetMessage(msgid)
      if nntp == nil {
        log.Printf("failed to load %s for federation", msgid)
      } else {
        for feed , use := range self.feeds {
          if use && feed.policy != nil {
            if feed.policy.AllowsNewsgroup(nntp.Newsgroup()) {
              feed.sync <- nntp.MessageID()
              log.Println("told feed")
            } else {
              log.Println("not syncing", msgid)
            }
          }
        }
      }
    case msgid := <- self.infeed_load:
      log.Println("load from infeed", msgid)
      msg := self.store.ReadTempMessage(msgid)
      if msg != nil {
        self.infeed <- msg
      }
    case nntp := <- self.infeed:
      // ammend path
      nntp = nntp.AppendPath(self.instance_name)
      // check for validity
      msgid := nntp.MessageID()
      log.Println("daemon got", msgid)
      nntp, err := self.store.VerifyMessage(nntp)
      if err == nil {
        // register article
        self.database.RegisterArticle(nntp)
        // store article
        // this generates thumbs and stores attachemnts
        err = self.store.StorePost(nntp)
        if err == nil {
          // queue to all outfeeds
          self.send_all_feeds <- msgid
          // roll over old content
          // TODO: hard coded expiration threshold
          self.expire.ExpireGroup(nntp.Newsgroup(), 100)
          // tell frontend
          chnl <- nntp
        } else {
          log.Printf("%s failed to store: %s", msgid, err)
        }
      } else {
        log.Printf("%s has invalid signature: %s", msgid, err)
      }
    }
  }
}

func (self *NNTPDaemon) acceptloop() {	
  for {
    // accept
    conn, err := self.listener.Accept()
    if err != nil {
      log.Fatal(err)
    }
    // make a new inbound nntp connection handler 
    nntp := self.newConnection(conn, true, nil)
    go self.RunInbound(nntp)
  }
}

func (self *NNTPDaemon) RunInbound(nntp NNTPConnection) {
  nntp.HandleInbound(self)
  delete(self.feeds, nntp)
}


func (self *NNTPDaemon) Setup() {
  log.Println("checking for configs...")
  // check that are configs exist
  CheckConfig()
  log.Println("loading config...")
  // read the config
  self.conf = ReadConfig()
  if self.conf == nil {
    log.Fatal("failed to load config")
  }
  // validate the config
  log.Println("validating configs...")
  self.conf.Validate()
  log.Println("configs are valid")
}

// bind to address
func (self *NNTPDaemon) Bind() error {
  listener , err := net.Listen("tcp", self.bind_addr)
  if err != nil {
    log.Println("failed to bind to", self.bind_addr, err)
    return err
  }
  self.listener = listener
  log.Printf("SRNd NNTPD bound at %s", listener.Addr())
  return nil
}

// load configuration
// bind to interface
func (self *NNTPDaemon) Init() bool {

  // set up daemon configs
  self.Setup()
  
  self.infeed = make(chan NNTPMessage, 64)
  self.infeed_load = make(chan string, 64)
  self.send_all_feeds = make(chan string, 64)
  self.feeds = make(map[NNTPConnection]bool)

  self.bind_addr = self.conf.daemon["bind"]
  
  err := self.Bind()
  if err != nil {
    log.Println("failed to bind:", err)
    return false
  }

  db_host := self.conf.database["host"]
  db_port := self.conf.database["port"]
  db_user := self.conf.database["user"]
  db_passwd := self.conf.database["password"]

  self.database = NewDatabase(self.conf.database["type"], self.conf.database["schema"], db_host, db_port, db_user, db_passwd)
  self.database.CreateTables()
  
  self.store = createArticleStore(self.conf.store, self.database)
  
  self.expire = createExpirationCore(self.database, self.store)
  self.sync_on_start = self.conf.daemon["sync_on_start"] == "1"
  self.debug = self.conf.daemon["log"] == "debug"
  self.instance_name = self.conf.daemon["instance_name"]
  if self.debug {
    log.Println("debug mode activated")
  }

  // initialize moderation engine
  self.mod.Init(self)
  
  // do we enable the frontend?
  if self.conf.frontend["enable"] == "1" {
    log.Printf("frontend %s enabled", self.conf.frontend["name"]) 
    self.frontend = NewHTTPFrontend(self, self.conf.frontend) 
    go self.frontend.Mainloop()
  }
  
  return true
}
