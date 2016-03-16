package main

import (
    "encoding/gob"
    "strconv"
    "fmt"
    "github.com/cs733-iitb/cluster"
    "math/rand"
    "reflect"
    "time"
)

// Debugging tools
func (rn *RaftNode) prnt(format string, args ...interface{}) {
  fmt.Printf(strconv.Itoa(rn.server_state.currentTerm) + " [NODE\t: " + strconv.Itoa(rn.server_state.server_id) + "] \t" + format + "\n", args...)
}

type NodeNetAddr struct {
    Id   int
    Host string
    Port int
}

// This is an example structure for Config .. change it to your convenience.
type Config struct {
    NodeNetAddrList     []NodeNetAddr     // Information about all servers, including this.
    Id                  int             // this node's id. One of the cluster's entries should match.
    LogDir              string          // Log file directory for this node
    ElectionTimeout     int
    HeartbeatTimeout    int
}

type RaftNode struct { // implements Node interface
    eventCh         chan interface{}
    timeoutCh       chan interface{}
    //config          Config
    LogDir          string          // Log file directory for this node
    server_state    ServerState
    clusterServer   cluster.Server
    timer           *time.Timer
    /*// Node's id
    func Id() int {
        return config.Id
    }*/
    // Id of leader. -1 if unknown
    LeaderId int 
    // A channel for client to listen on. What goes into Append must come out of here at some point.
    CommitChannel chan commitAction
    // Last known committed index in the log.  This could be -1 until the system stabilizes.
    /*func CommittedIndex int {
        return server_state.commitIndex
    }*/
/*
    // Client's message to Raft node
    Append([]byte)
    // Returns the data at a log index, or an error.
    Get(index int) (err, []byte)
    // Signal to shut down all goroutines, stop sockets, flush log and close it, cancel timers.
    Shutdown()*/
}

// Returns a Node object
func NewRaftNode(config Config) *RaftNode {
    var peers []cluster.PeerConfig
    for _,nodeNetAddr := range config.NodeNetAddrList {
        peers = append(peers, cluster.PeerConfig{Id: nodeNetAddr.Id, Address: fmt.Sprintf("%v:%v", nodeNetAddr.Host, nodeNetAddr.Port)})
    }

    config1 := cluster.Config { Peers: peers }
    server1, _ := cluster.New(config.Id, config1)

    var server_state ServerState
    server_state.setupServer ( FOLLOWER, len(config.NodeNetAddrList) )
    server_state.electionTimeout     = config.ElectionTimeout
    server_state.heartbeatTimeout    = config.HeartbeatTimeout
    server_state.server_id           = config.Id

    raft := RaftNode{
                        //config              : config, 
                        server_state        : server_state, 
                        clusterServer       : server1,
                        eventCh             : make(chan interface{}),
                        timeoutCh           : make(chan interface{}),
                        CommitChannel       : make(chan commitAction,200),
                        LogDir              : config.LogDir }
    return &raft
}

// Client's message to Raft node
func (rn *RaftNode) Append(data []byte) {
                //fmt.Println("channel in append ", &rn.eventCh)
    rn.eventCh <- appendEvent{data: data}
                //fmt.Printf("Hello\n")
}

func (rn *RaftNode) processEvents() {
    RegisterEncoding()
    rn.timer = time.AfterFunc(time.Duration(rn.server_state.heartbeatTimeout +rand.Intn(100))*time.Millisecond, func() { rn.timeoutCh <- timeoutEvent{} })

    rn.prnt("Timer started")
    for {
        var ev interface{}
                //fmt.Println("channel in process events ",rn.config.Id, &rn.eventCh)
        select {
        case ev = <- rn.timeoutCh :
            //rn.prnt("Timeout event received")
            actions := rn.server_state.processEvent(ev)
            rn.doActions(actions)
        //ev = timeoutEvent{}
        case ev = <- rn.eventCh :
            rn.prnt("Append request received")
            actions := rn.server_state.processEvent(ev)
            rn.doActions(actions)
        case ev = <- rn.clusterServer.Inbox() :
            ev := ev.(*cluster.Envelope)

            // Debug logging
            switch ev.Msg.(type) {
            case appendRequestEvent:
                rn.prnt("%25v %2v <<-- %-14v %+v", reflect.TypeOf(ev.Msg).Name(), rn.server_state.server_id, ev.Pid, ev.Msg)
            case appendRequestRespEvent:
                rn.prnt("%25v %2v <<-- %-14v %+v", reflect.TypeOf(ev.Msg).Name(), rn.server_state.server_id, ev.Pid, ev.Msg)
            case requestVoteEvent :
                //rn.prnt("%25v %2v <<-- %-14v %+v", reflect.TypeOf(ev.Msg).Name(), rn.server_state.server_id, ev.Pid, ev.Msg)
            case requestVoteRespEvent :
                //rn.prnt("%25v %2v <<-- %-14v %+v", reflect.TypeOf(ev.Msg).Name(), rn.server_state.server_id, ev.Pid, ev.Msg)
            }

            //rn.prnt("InboxEvent  : from %v \"%v\" \t\t%v", ev.Pid, reflect.TypeOf(ev.Msg).Name(), ev)
            //reflect.TypeOf(ev.Msg).Name()
            event := ev.Msg.(interface{})
            actions := rn.server_state.processEvent(event)
            rn.doActions(actions)
        default:
            //fmt.Printf("Hello %v\n", rn.config.Id)
            //rn.eventCh <- timeoutEvent{}
        }
    }
}

func (rn *RaftNode) doActions(actions [] interface{}) {

    //var timer *Timer

    for _,action := range actions {
        switch action.(type) {
        case sendAction :
            action := action.(sendAction)

            // Debug logging
            switch action.event.(type) {
            case appendRequestEvent:
                rn.prnt("%25v %2v -->> %-14v %+v", reflect.TypeOf(action.event).Name(), rn.server_state.server_id, action.toId, action.event)
            case appendRequestRespEvent:
                rn.prnt("%25v %2v -->> %-14v %+v", reflect.TypeOf(action.event).Name(), rn.server_state.server_id, action.toId, action.event)
            case requestVoteEvent :
                rn.prnt("%25v %2v -->> %-14v %+v", reflect.TypeOf(action.event).Name(), rn.server_state.server_id, action.toId, action.event)
            case requestVoteRespEvent :
                rn.prnt("%25v %2v -->> %-14v %+v", reflect.TypeOf(action.event).Name(), rn.server_state.server_id, action.toId, action.event)
            }
            //rn.prnt("OutboxEvent : to   %v \"%v\"\t\t%v", action.toId, reflect.TypeOf(action.event).Name(), action.event)


            if action.toId == -1 {
                rn.clusterServer.Outbox() <- &cluster.Envelope{Pid:cluster.BROADCAST, Msg:action.event}
            } else {
                rn.clusterServer.Outbox() <- &cluster.Envelope{Pid:action.toId, Msg:action.event}
            }
        case commitAction :
            rn.prnt("commitAction received %+v", action)
            action := action.(commitAction)
            rn.CommitChannel <- action
        case logStore :
            rn.prnt("logStore received")
        case alarmAction :
            //rn.prnt("==== %25v", "resetting alarm")
            action := action.(alarmAction)
            rn.timer.Stop()
            //timer =
            rn.timer = time.AfterFunc(time.Duration(action.time)*time.Millisecond, func() { rn.timeoutCh <- timeoutEvent{} })
        default:

        }
    }
}

func RegisterEncoding () {
    gob.Register(appendRequestEvent{})
    gob.Register(appendRequestRespEvent{})
    gob.Register(requestVoteEvent{})
    gob.Register(requestVoteRespEvent{})
    //gob.Register(timeoutEvent{})
    gob.Register(appendEvent{})
    gob.Register(LogEntry{})
}