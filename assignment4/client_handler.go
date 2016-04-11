package main

import (
    "bufio"
    "fmt"
    "net"
    "os"
    "strconv"
    "cs733/assignment4/raft_node"
    "cs733/assignment4/filesystem/fs"
    "sync"
    rsm "cs733/assignment4/raft_node/raft_state_machine"
    "cs733/assignment4/raft_config"
    "cs733/assignment4/logging"
    "time"
    "encoding/gob"
)

const CONNECTION_TIMEOUT = 1000000 // in seconds

func (rn *ClientHandler) log_error(skip int, format string, args ...interface{}) {
    format = fmt.Sprintf("[CH:%v] ", strconv.Itoa(rn.ClientPort)) + format
    logging.Error(skip, format, args...)
}
func (rn *ClientHandler) log_info(skip int, format string, args ...interface{}) {
    format = fmt.Sprintf("[CH:%v] ", strconv.Itoa(rn.ClientPort)) + format
    logging.Info(skip, format, args...)
}
func (rn *ClientHandler) log_warning(skip int, format string, args ...interface{}) {
    format = fmt.Sprintf("[CH:%v] ", strconv.Itoa(rn.ClientPort)) + format
    logging.Warning(skip, format, args...)
}

var crlf = []byte{'\r', '\n'}

/****
 *      Variables used for replicating
 */

/*
 *  Request is replicated into raft nodes
 */
type Request struct {
    ServerId int // Id of raft node on which the request has arrived
    ReqId    int // Connection id, mapped into activeConn, from which request has arrived
    msg      fs.Msg
}

type ClientHandler struct {
    Raft            *raft_node.RaftNode
    ActiveReq       map[int]chan rsm.CommitAction
    ActiveReqLock   sync.RWMutex
    NextConnId      int
    ClientPort      int     //Port on which the client handler will listen
}

func New(config *raft_config.Config, restore bool) (ch *ClientHandler) {
    ch = &ClientHandler{
        ActiveReq   : make(map[int]chan rsm.CommitAction),
        NextConnId  : 0,
        ClientPort  : config.ClientPort }

    if restore {
        ch.Raft = raft_node.RestoreServerState(config)
    } else {
        ch.Raft = raft_node.NewRaftNode(config)
    }

    return ch
}

func (ch *ClientHandler) serverMain() {

    gob.Register(Request{})

    ch.Raft.Start()

    tcpaddr, err := net.ResolveTCPAddr("tcp", "localhost:"+strconv.Itoa(ch.ClientPort))
    ch.check(err)
    tcp_acceptor, err := net.ListenTCP("tcp", tcpaddr)
    ch.check(err)

    ch.log_info(3, "Starting handle commits thread")
    go ch.handleCommits()

    ch.log_info(3, "Starting loop to handle tcp connections")
    go func () {
        for {
            tcp_conn, err := tcp_acceptor.AcceptTCP()
            ch.check(err)
            go ch.serve(tcp_conn)
        }
    }()
}


// Add a connection to active request queue and returns request id
func (ch *ClientHandler) AddConnection (conn chan rsm.CommitAction) int {
    ch.ActiveReqLock.Lock()
    ch.NextConnId++
    connId := ch.NextConnId
    ch.ActiveReq [ connId ] = conn
    ch.ActiveReqLock.Unlock()
    return connId
}
// Remove request from active requests
func (ch *ClientHandler) RemoveConn (connId int) {
    ch.ActiveReqLock.Lock()
    delete(ch.ActiveReq, connId)
    ch.ActiveReqLock.Unlock()
}
func (ch *ClientHandler) GetConn (connId int) chan rsm.CommitAction {
    ch.ActiveReqLock.RLock()
    conn := ch.ActiveReq[connId]
    ch.log_info(4, "Connection extracted from map : id:%v conn:%v", connId, conn)
    ch.ActiveReqLock.RUnlock()
    return conn
}


func (ch *ClientHandler) check(obj interface{}) {
    if obj != nil {
        ch.log_error(3, "Error occurred : %v", obj)
        fmt.Println(obj)
        os.Exit(1)
    }
}

func (ch *ClientHandler) reply(conn *net.TCPConn, msg *fs.Msg) bool {
    var err error
    write := func(data []byte) {
        if err != nil {
            return
        }
        _, err = conn.Write(data)
    }
    var resp string
    switch msg.Kind {
    case 'C': // read response
        resp = fmt.Sprintf("CONTENTS %d %d %d", msg.Version, msg.Numbytes, msg.Exptime)
    case 'O':
        resp = "OK "
        if msg.Version > 0 {
            resp += strconv.Itoa(msg.Version)
        }
    case 'F':
        resp = "ERR_FILE_NOT_FOUND"
    case 'V':
        resp = "ERR_VERSION " + strconv.Itoa(msg.Version)
    case 'M':
        resp = "ERR_CMD_ERR"
    case 'I':
        resp = "ERR_INTERNAL"
    case 'R': // redirect addr of leader
        resp = fmt.Sprintf("ERR_REDIRECT %v", msg.RedirectAddr)
    default:
        ch.log_error(3, "Unknown response kind '%c', of msg : %+v", msg.Kind, msg)
        return false
    }
    resp += "\r\n"
    write([]byte(resp))
    if msg.Kind == 'C' {
        write(msg.Contents)
        write(crlf)
    }
    return err == nil
}

func (ch *ClientHandler) serve(conn *net.TCPConn) {
    reader := bufio.NewReader(conn)
    for {
        msg, msgerr, fatalerr := fs.GetMsg(reader)
        if fatalerr != nil || msgerr != nil {
            ch.reply(conn, &fs.Msg{Kind: 'M'})
            if msgerr!=nil {
                ch.log_error(3, "Error occured while getting a msg from client : %v, %v", msgerr, fatalerr)
            }
            conn.Close()
            break
        }

        if msgerr != nil {
            if (!ch.reply(conn, &fs.Msg{Kind: 'M'})) {
                ch.log_error(3, "Reply to client was not sucessful")
                conn.Close()
                break
            }
        }

        ch.log_info(3, "Request received from client : %+v", *msg)
        /***
         *      Replicate msg and after receiving at commitChannel, ProcessMsg(msg)
         */

        waitChan := make(chan rsm.CommitAction)
        reqId := ch.AddConnection(waitChan)

        request := Request{ServerId:ch.Raft.GetId(), ReqId:reqId, msg:*msg}
        ch.Raft.Append(request)

        select {
        case commitAction := <-waitChan:
            var response *fs.Msg

            request := commitAction.Log.Data.(Request)

            if commitAction.Err == nil {
                response = fs.ProcessMsg(&request.msg)
            } else {
                switch commitAction.Err.(type) {
                case rsm.Error_Commit:                  // unable to commit, internal error
                    response = &fs.Msg{Kind:'I'}
                case rsm.Error_NotLeader:               // not a leader, redirect error
                    errorNotLeader := commitAction.Err.(rsm.Error_NotLeader)
                    response = &fs.Msg{Kind:'R', RedirectAddr:errorNotLeader.LeaderAddr + ":" + strconv.Itoa(errorNotLeader.LeaderPort)}
                default:
                    ch.log_error(3, "Unknown error type : %v", commitAction.Err)
                }
            }

            if !ch.reply(conn, response) {
                ch.log_error(3, "Reply to client was not sucessful")
                conn.Close()
            }
        case  <- time.After(CONNECTION_TIMEOUT*time.Second) :
            // Connection timed out
            ch.log_error(3, "Connection timed out, closing the connection")
            conn.Close()
        }


        // Remove request from active requests
        ch.log_info(3, "Removing request from queue with reqId %v", reqId)
        ch.RemoveConn(reqId)
    }
}

func (ch *ClientHandler) handleCommits() {
    for {
        commitAction, ok := <- ch.Raft.CommitChannel
        if ok {
            request := commitAction.Log.Data.(Request)

            // Reply only if the client has requested this server
            if request.ServerId == ch.Raft.GetId() {
                // TODO:: check if the connection was timed out and the request has been removed from the queue, i.e. map
                conn := ch.GetConn(request.ReqId)
                conn <- commitAction
            }

            // update last applied
            ch.Raft.UpdateLastApplied(commitAction.Log.Index)
        } else {
            // Raft node closed
            ch.log_info(3, "Raft node shutdown")
            return
        }
    }
}


func (ch *ClientHandler) Shutdown() {
    ch.log_info(3, "Client handler shuting down")
    ch.Raft.Shutdown()
}