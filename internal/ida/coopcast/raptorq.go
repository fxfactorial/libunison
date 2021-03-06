package coopcast

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	raptorfactory "github.com/harmony-one/go-raptorq/pkg/defaults"
	libraptorq "github.com/harmony-one/go-raptorq/pkg/raptorq"
	"io"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net"
	"strconv"
	"time"
)

// ListeningOnBroadCast listens and handle message received
func (node *Node) ListeningOnBroadCast(pc net.PacketConn) {
	go node.Gossip(pc)
	go node.clearCache()

	addr := net.JoinHostPort("", node.SelfPeer.TCPPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("cannot listening to the port %s", node.SelfPeer.TCPPort)
		return
	}
	log.Printf("server start listening on tcp port %s", node.SelfPeer.TCPPort)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("cannot accept connection")
			return
		}
		clientinfo := conn.RemoteAddr().String()
		log.Printf("accept connection from %s", clientinfo)
		go node.handleResponse(conn)
	}
}

// BroadCast broadcast a message to peer nodes in the network
func (node *Node) BroadCast(msg []byte, pc net.PacketConn) (map[int]interface{}, *RaptorQImpl) {
	raptorq := RaptorQImpl{}
	raptorq.threshold = int(threshold * float32(len(node.AllPeers)))
	log.Printf("threshold value is %v", raptorq.threshold)
	raptorq.senderID = node.SelfPeer.Sid
	raptorq.rootHash = getRootHash(msg)
	raptorq.Encoder = make(map[int]libraptorq.Encoder)
	raptorq.stats = make(map[int]float64)
	raptorq.chunkSize = normalChunkSize
	raptorq.initTime = time.Now().UnixNano()

	hashkey := convertToFixedSize(raptorq.rootHash)
	node.SenderCache[hashkey] = true

	F := len(msg)
	B := raptorq.chunkSize
	if F <= B {
		raptorq.numChunks = 1
	} else if F%B == 0 {
		raptorq.numChunks = F / B
	} else {
		raptorq.numChunks = F/B + 1
	}

	cancels := make(map[int]interface{})
	for z := 0; z < raptorq.numChunks; z++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[z] = cancel
		go node.broadCastEncodedSymbol(ctx, msg, &raptorq, pc, z)
	}
	return cancels, &raptorq
}

// StopBroadCast controls when to stop sender from continuing broadcast
func (node *Node) StopBroadCast(cancels map[int]interface{}, raptorq *RaptorQImpl) {
	//stop := make(chan bool)
	//go node.ReportUnfinishedBlocks(raptorq, stop)

	hashkey := convertToFixedSize(raptorq.rootHash)
	canceled := make(map[int]bool)
	for start := time.Now(); time.Since(start) < stopBroadCastTime*time.Second; {
		for z := 0; z < raptorq.numChunks; z++ {
			if canceled[z] {
				continue
			}
			if node.PeerDecodedCounter[hashkey][z] >= raptorq.threshold {
				delta := float64(time.Now().UnixNano()-raptorq.initTime) / 1000000
				raptorq.mux.Lock()
				raptorq.stats[z] = delta
				raptorq.mux.Unlock()
				cancels[z].(context.CancelFunc)()
				canceled[z] = true
				log.Printf("***** chunkID %v canceled", z)
			}
		}
		if len(canceled) >= raptorq.numChunks {
			//stop <- true
			log.Printf("t0/t1/base/t2/hop: %v ms, %v ms, %v, %v ms, %v", node.InitialDelayTime, node.MaxDelayTime, node.ExpBase, node.RelayTime, node.Hop)
			for z, delta := range raptorq.stats {
				log.Printf("block %v broadcast finished with time elapse = %v ms", z, delta)
			}
			log.Printf("total broadcast time: %v ms", float64(time.Now().UnixNano()-raptorq.initTime)/1000000)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (node *Node) clearCache() {
	OneSec := int64(1000000000)
	node.mux.Lock()
	locked := true
	defer func() {
		if locked {
			node.mux.Unlock()
		}
	}()
	for {
		locked = false
		node.mux.Unlock()
		time.Sleep(cacheClearInterval * time.Second)
		locked = true
		node.mux.Lock()
		currentTime := time.Now().UnixNano()
		for k, v := range node.Cache {
			if v.successTime > 0 && currentTime-v.successTime > int64(cacheClearInterval)*OneSec {
				delete(node.Cache, k)
				log.Printf("file hash %v cache deleted", k)
			} else if currentTime-v.initTime > enforceClearInterval*OneSec {
				delete(node.Cache, k)
				log.Printf("file hash %v cache eventually deleted", k)
			}
		}
	}
}

//return 20 byte of the sha1 sum of message
func getRootHash(msg []byte) []byte {
	x := sha1.Sum(msg)
	return x[:]
}

func convertToFixedSize(buf []byte) [hashSize]byte {
	var arr [hashSize]byte
	copy(arr[:], buf[:hashSize])
	return arr
}

func symDebug(prefix string, z int, esi uint32, symbol []byte) {
	symhash := sha1.Sum(symbol)
	symhh := make([]byte, hex.EncodedLen(len(symhash)))
	hex.Encode(symhh, symhash[:])
	log.Printf("%s: z=%+v esi=%+v len=%v symhh=%s", prefix, z, esi, len(symbol), symhh)
}

func (raptorq *RaptorQImpl) constructSymbolPacket(msg []byte, chunkID int, symbolID uint32, hop int) ([]byte, error) {
	// |hashSize(20)|hop(1)|senderID(2)|numChunks(4)|chunkID(4)|chunkSize(4)|symbolID(4)|symbol(1200)|
	T := raptorq.Encoder[chunkID].SymbolSize()
	symbol := make([]byte, int(T))
	_, err := raptorq.Encoder[chunkID].Encode(0, symbolID, symbol)
	if err != nil {
		return nil, err
	}
	symDebug("encoded", chunkID, symbolID, symbol)
	packet := make([]byte, 0)
	packet = append(packet, raptorq.rootHash...)

	packet = append(packet, byte(hop))

	senderIDBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(senderIDBytes, uint16(raptorq.senderID))
	packet = append(packet, senderIDBytes...)

	numChunksBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(numChunksBytes, uint32(raptorq.numChunks))
	packet = append(packet, numChunksBytes...)

	chunkIDBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(chunkIDBytes, uint32(chunkID))
	packet = append(packet, chunkIDBytes...)

	chunkSize := raptorq.getChunkSize(msg, chunkID)
	chunkSizeBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(chunkSizeBytes, uint32(chunkSize))
	packet = append(packet, chunkSizeBytes...)

	symbolIDBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(symbolIDBytes, symbolID)
	packet = append(packet, symbolIDBytes...)
	packet = append(packet, symbol...)

	return packet, nil
}

// Specification of RaptorQ FEC is defined in RFC6330
// return the TransferLength/ChunkSize
func (raptorq *RaptorQImpl) setEncoderIfNotExist(msg []byte, chunkID int) error {
	if _, ok := raptorq.Encoder[chunkID]; ok {
		return nil
	}

	encf := raptorfactory.DefaultEncoderFactory()
	// each source block, the size is limit to a 40 bit integer 946270874880 = 881.28 GB
	//there are some hidden restrictions: WS/T >=10
	// Al: symbol alignment parameter
	var Al uint8 = 4
	// T: symbol size, can take it to be maximum payload size, multiple of Al
	T := uint16(symbolSize)
	// WS: working memory, maxSubBlockSize
	WS := 2 * uint32(normalChunkSize)
	// minimum sub-symbol size is SS, must be a multiple of Al
	minSubSymbolSize := T // then N=1

	t0 := time.Now().UnixNano()
	a := chunkID * normalChunkSize
	b := a + raptorq.getChunkSize(msg, chunkID)
	piece := msg[a:b]
	encoder, err := encf.New(piece, T, minSubSymbolSize, WS, Al)
	log.Printf("encoder for chunkID=%v is created with size %v", chunkID, b-a)
	log.Printf("DEBUG:****** encoder for common: %v, specific: %v", encoder.CommonOTI(), encoder.SchemeSpecificOTI())
	log.Printf("****: N: %v", encoder.NumSubBlocks())
	log.Printf("****: Al: %v", encoder.SymbolAlignmentParameter())

	if err == nil {
		raptorq.Encoder[chunkID] = encoder
	} else {
		return err
	}
	log.Printf("numChunks=%v, chunkID=%v, numMinSymbols=%v", raptorq.numChunks, chunkID, raptorq.Encoder[chunkID].MinSymbols(0))
	log.Printf("encoder for chunkID %v creation time is %v ms", chunkID, (time.Now().UnixNano()-t0)/1000000)
	return nil
}

func (raptorq *RaptorQImpl) getChunkSize(msg []byte, chunkID int) int {
	a := chunkID * normalChunkSize
	b := (chunkID + 1) * normalChunkSize
	if chunkID == raptorq.numChunks-1 {
		b = len(msg)
	}
	return b - a
}

func (raptorq *RaptorQImpl) constructCommonOTI(transferLength uint64) uint64 {
	// CommonOTI = |Transfer Length (5)|Reserved(1)|Symbol Size(2)| 8 bytes
	commonOTI := make([]byte, 0)

	transferLengthBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(transferLengthBytes, transferLength)
	commonOTI = append(commonOTI, transferLengthBytes[3:8]...)
	commonOTI = append(commonOTI, byte(0))

	symbolSizeBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(symbolSizeBytes, uint16(symbolSize))
	commonOTI = append(commonOTI, symbolSizeBytes...)

	return binary.BigEndian.Uint64(commonOTI)
}

func (raptorq *RaptorQImpl) constructSpecificOTI() uint32 {
	// SpecificOTI = |Z(1)|N(2)|Al(1)| 4 bytes
	specificOTI := make([]byte, 0)
	specificOTI = append(specificOTI, byte(1))
	specificOTI = append(specificOTI, 0x00, 0x01)
	specificOTI = append(specificOTI, 0x01)
	return binary.BigEndian.Uint32(specificOTI)
}

func (raptorq *RaptorQImpl) setDecoderIfNotExist(chunkID int, chunkSize uint64, node *Node) error {
	raptorq.mux.Lock()
	defer raptorq.mux.Unlock()
	if _, ok := raptorq.Decoder[chunkID]; ok {
		return nil
	}
	decf := raptorfactory.DefaultDecoderFactory()
	commonOTI := raptorq.constructCommonOTI(chunkSize)
	specificOTI := raptorq.constructSpecificOTI()
	log.Printf("DEBUG******: commonOTI: %v, specific: %v", commonOTI, specificOTI)

	decoder, err := decf.New(commonOTI, specificOTI)
	if err == nil {
		raptorq.Decoder[chunkID] = decoder
	} else {
		return err
	}
	ready := make(chan uint8)
	raptorq.Decoder[chunkID].AddReadyBlockChan(ready)
	go node.handleDecodeSuccess(raptorq.rootHash, chunkID, ready)
	return nil
}

func expBackoffDelay(initialDelayTime float64, maxDelayTime float64, expBase float64) func(int, int) time.Duration {
	// delay time unit is milliseconds
	maxK := math.Log2(maxDelayTime/initialDelayTime) / math.Log2(expBase) //result cap by maxDelayTime
	return func(k int, k0 int) time.Duration {
		delta := float64(k - k0)
		power := math.Max(delta, 0)
		power = math.Min(power, maxK)
		return time.Duration(1000000 * initialDelayTime * math.Pow(expBase, power))
	}
}

func (node *Node) broadCastEncodedSymbol(ctx context.Context, msg []byte, raptorq *RaptorQImpl, pc net.PacketConn, chunkID int) {
	var symbolID uint32
	peerList := node.PeerList
	var bytesSent int
	backoff := expBackoffDelay(node.InitialDelayTime, node.MaxDelayTime, node.ExpBase)
	err := raptorq.setEncoderIfNotExist(msg, chunkID)
	if err != nil {
		log.Printf("unable to create encoder for chunkID=%v", chunkID)
	}
	k0 := int(raptorq.Encoder[chunkID].MinSymbols(0))
	for {
		select {
		case <-ctx.Done():
			log.Printf("chunkID=%v broadcast stopped", chunkID)
			return
		default:
			k := int(symbolID)
			time.Sleep(backoff(k, k0))

			packet, err := raptorq.constructSymbolPacket(msg, chunkID, symbolID, node.Hop)
			if err != nil {
				log.Printf("raptorq encoding error: %s", err)
				return //chao: return or continue
			}
			idx := int(symbolID) % len(peerList)
			remoteAddr := net.JoinHostPort(peerList[idx].IP, peerList[idx].UDPPort)
			addr, err := net.ResolveUDPAddr("udp", remoteAddr)
			if err != nil {
				log.Printf("cannot resolve udp address %v", remoteAddr)
			}
			bytesSent, err = pc.WriteTo(packet, addr)
			if err != nil {
				log.Printf("broadcast encoded symbol written error %v with %v symbol written", err, bytesSent)
			}
			if err == nil && bytesSent < len(packet) {
				log.Printf("udp write with only %v bytes, with original %v bytes", bytesSent, len(packet))
			}
			if symbolID%100 == 0 {
				log.Printf("chunkID=%v,  symbolID=%v sent to %v", chunkID, symbolID, remoteAddr)
			}
		}
		symbolID++
	}
}

func (node *Node) relayEncodedSymbol(pc net.PacketConn, packet []byte) {
	hop := packet[hashSize]
	if hop == 0 {
		return
	}
	packet[hashSize] = packet[hashSize] - 1

	idx0 := rand.Intn(len(node.PeerList))
	for i := range node.PeerList {
		idx := (i + idx0) % len(node.PeerList)
		peer := node.PeerList[idx]
		remoteAddr := net.JoinHostPort(peer.IP, peer.UDPPort)
		addr, err := net.ResolveUDPAddr("udp", remoteAddr)
		if err != nil {
			log.Printf("cannot resolve udp address %v", remoteAddr)
		}
		time.Sleep(time.Duration(node.RelayTime * 1000000))
		n, err := pc.WriteTo(packet, addr)
		if err != nil {
			log.Printf("relay symbol failed at %v with %v bytes written", addr, n)
		}
		if err == nil && n < len(packet) {
			log.Printf("relay symbol write only %v bytes, need write %v bytes", n, len(packet))
		}
	}
}

// Gossip responsible to receive, decode and relay message
func (node *Node) Gossip(pc net.PacketConn) {
	buffer := make([]byte, udpCacheSize)
	for {
		n, addr, err := pc.ReadFrom(buffer)
		if err != nil {
			log.Printf("gossip receive response from peer %v with error %s", addr, err)
			continue
		}
		if n < hashSize+19+symbolSize {
			log.Printf("gossip received only %v symbols, need %v symbols", n, hashSize+19+symbolSize)
		}
		copybuffer := make([]byte, n)
		copy(copybuffer, buffer[:n])

		hash := copybuffer[0:hashSize]
		hashkey := convertToFixedSize(hash)
		// not gossip its own message
		if node.SenderCache[hashkey] {
			continue
		}
		raptorq := node.initRaptorQIfNotExist(hash)
		raptorq.senderID = int(binary.BigEndian.Uint16(copybuffer[hashSize+1 : hashSize+3]))
		raptorq.numChunks = int(binary.BigEndian.Uint32(copybuffer[hashSize+3 : hashSize+7]))

		chunkID := int(binary.BigEndian.Uint32(copybuffer[hashSize+7 : hashSize+11]))
		chunkSizeBytes := append(make([]byte, 4), copybuffer[hashSize+11:hashSize+15]...)
		chunkSize := binary.BigEndian.Uint64(chunkSizeBytes)
		symbolID := binary.BigEndian.Uint32(copybuffer[hashSize+15 : hashSize+19])
		symbol := copybuffer[hashSize+19 : n]
		symDebug("received", chunkID, symbolID, symbol)
		err = raptorq.setDecoderIfNotExist(chunkID, chunkSize, node)
		if err != nil {
			log.Printf("unable to set decoder for chunkID=%v, with chunkSize=%v", chunkID, chunkSize)
			continue
		}

		if _, ok := raptorq.receivedSymbols[chunkID]; !ok {
			raptorq.receivedSymbols[chunkID] = make(map[uint32]bool)
		}

		// just relay once
		if raptorq.receivedSymbols[chunkID][symbolID] {
			continue
		}
		raptorq.receivedSymbols[chunkID][symbolID] = true

		if !raptorq.Decoder[chunkID].IsSourceObjectReady() {
			raptorq.Decoder[chunkID].Decode(0, symbolID, symbol)
			log.Printf("decode symbol %v", symbolID)
		}
		go node.relayEncodedSymbol(pc, copybuffer[:n])
	}
}

func (node *Node) handleDecodeSuccess(hash []byte, chunkID int, ch chan uint8) {
	sbn, ok := <-ch
	log.Printf("ready channel returned sbn=%+v ok=%+v", sbn, ok)
	hashkey := convertToFixedSize(hash)
	node.mux.Lock()
	defer node.mux.Unlock()
	raptorq := node.Cache[hashkey]
	raptorq.mux.Lock()
	defer raptorq.mux.Unlock()
	raptorq.numDecoded++
	numDecoded := raptorq.numDecoded
	go node.responseSuccess(hash, chunkID)
	log.Printf("source object is ready for block %v", chunkID)
	F := raptorq.Decoder[chunkID].TransferLength()
	buf := make([]byte, F)
	raptorq.Decoder[chunkID].SourceObject(buf)
	log.Printf("sha1 hash for block %v is %v", chunkID, getRootHash(buf))
	if numDecoded >= raptorq.numChunks {
		raptorq.successTime = time.Now().UnixNano()
		writeReceivedMessage(raptorq)
		//	delete(node.Cache, hashkey) // release resources after receive the file
	}
}

func (node *Node) initRaptorQIfNotExist(hash []byte) *RaptorQImpl {
	hashkey := convertToFixedSize(hash)
	node.mux.Lock()
	defer node.mux.Unlock()
	if node.Cache[hashkey] == nil {
		log.Printf("raptorq initialized with hash %v", hashkey)
		raptorq := RaptorQImpl{}
		raptorq.threshold = int(threshold * float32(len(node.AllPeers)))
		raptorq.rootHash = hash
		raptorq.chunkSize = normalChunkSize
		raptorq.receivedSymbols = make(map[int]map[uint32]bool)
		raptorq.initTime = time.Now().UnixNano()
		raptorq.Decoder = make(map[int]libraptorq.Decoder)
		node.Cache[hashkey] = &raptorq
	}
	return node.Cache[hashkey]
}

func (node *Node) handleResponse(conn net.Conn) {
	defer conn.Close()
	c := bufio.NewReader(conn)
	hash := make([]byte, hashSize)
	n, err := io.ReadFull(c, hash)
	if err != nil {
		log.Printf("response received %v size message with err %v", n, err)
		return
	}
	hashkey := convertToFixedSize(hash)
	//message is not sent by the node
	if _, ok := node.SenderCache[hashkey]; !ok {
		return
	}
	mtype, _ := c.ReadByte()
	switch mtype {
	case metaReceived:
		chunkIDBytes := make([]byte, 4)
		_, err := io.ReadFull(c, chunkIDBytes)
		chunkID := int(binary.BigEndian.Uint32(chunkIDBytes))
		node.mux.Lock()
		if _, ok := node.PeerDecodedCounter[hashkey]; !ok {
			node.PeerDecodedCounter[hashkey] = make(map[int]int)
		}
		node.PeerDecodedCounter[hashkey][chunkID] = node.PeerDecodedCounter[hashkey][chunkID] + 1
		node.mux.Unlock()
		sid := make([]byte, 4)
		_, err = io.ReadFull(c, sid)
		if err != nil {
			log.Printf("node sid read error")
		}
		log.Printf("chunkID=%v decoded confirmation received from %v", chunkID, binary.BigEndian.Uint32(sid))
		return
	default:
		log.Printf("tcp received unknown data type")
	}
}

// this is used for stop sender, will be replaced by consensus algorithm later
func (node *Node) responseSuccess(hash []byte, chunkID int) {
	// |hash(20)|Received(1)|chunkID(4)|peerId(4)|
	okmsg := make([]byte, 0)
	okmsg = append(okmsg, hash...)
	okmsg = append(okmsg, metaReceived)
	chunkIDBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(chunkIDBytes, uint32(chunkID))
	okmsg = append(okmsg, chunkIDBytes...)
	sid := make([]byte, 4)
	binary.BigEndian.PutUint32(sid, uint32(node.SelfPeer.Sid))
	okmsg = append(okmsg, sid...)
	hashkey := convertToFixedSize(hash)
	node.mux.Lock()
	raptorq := node.Cache[hashkey]
	node.mux.Unlock()
	for _, peer := range node.AllPeers {
		if peer.Sid != raptorq.senderID {
			continue
		}
		tcpaddr := net.JoinHostPort(peer.IP, peer.TCPPort)
		conn, err := net.Dial("tcp", tcpaddr)
		if err != nil {
			log.Printf("dial to tcp addr %v failed with %v", tcpaddr, err)
			backoff := expBackoffDelay(1000, 15000, 1.35)
			for i := 0; i < 10; i++ {
				time.Sleep(backoff(i, 0))
				conn, err = net.Dial("tcp", tcpaddr)
				if err == nil {
					break
				}
				log.Printf("dial to tcp addr %v failed with %v (retry %v)", tcpaddr, err, i)
			}
			log.Printf("retry exhausted")
		}
		if err == nil && conn != nil {
			_, err = conn.Write(okmsg)
			log.Printf("node %v send okay message for chunkID=%v to sender %v", node.SelfPeer.Sid, chunkID, tcpaddr)
			if err != nil {
				log.Printf("send received message to sender %v failed with %v", tcpaddr, err)
			}
		}
		return
	}
}

func writeReceivedMessage(raptorq *RaptorQImpl) {
	if raptorq.numDecoded < raptorq.numChunks {
		log.Printf("source object is not ready")
		return
	}
	var F int
	for i := 0; i < raptorq.numChunks; i++ {
		F += int(raptorq.Decoder[i].TransferLength())
	}
	log.Printf("writing decoded source file with %v bytes......", F)
	buf := make([]byte, F)
	var offset int
	for i := 0; i < raptorq.numChunks; i++ {
		size := int(raptorq.Decoder[i].TransferLength())
		_, err := raptorq.Decoder[i].SourceObject(buf[offset : offset+size])
		if err != nil {
			log.Printf("decode object failed at chunkID=%v with chunkSize=%v", i, size)
			return
		}
		offset += size
	}
	fileloc := "received/" + strconv.Itoa(raptorq.senderID) + "_" + strconv.FormatUint(uint64(raptorq.successTime), 10)
	err := ioutil.WriteFile(fileloc, buf, 0644)
	if err != nil {
		log.Printf("unable to write file %v to disk", fileloc)
	}
}
