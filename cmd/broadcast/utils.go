package main

import (
	"bufio"
	"encoding/hex"
	ida "github.com/harmony-one/libunison/internal/ida/coopcast"
	"io"
	"log"
	"math/rand"
	"os"
	"strconv"
	"strings"
)

type Role int

const (
	Self       Role = 0
	Neighbor   Role = 1
	All        Role = 2
	pubKeySize int  = 20
)

// PeerConfig is a single config of a node.
type PeerConfig struct {
	Sid     string // SimpleID, might be replaced later for more generic ID like byte array
	IP      string
	TCPPort string
	UDPPort string
	PubKey  string
	Role    string
}

// Config is a struct containing network topolgy, i.e. multiple PeerConfig of all nodes.
type Config struct {
	config []PeerConfig
}

// NewConfig returns a pointer to a Config.
func NewConfig() *Config {
	config := Config{}
	return &config
}

// GetPeerInfo returns the selfPeer, peerList, allPeers from config instance, which used to create node instance
func (config *Config) GetPeerInfo() (selfPeer ida.Peer, peerList []ida.Peer, allPeers []ida.Peer) {
	for _, entry := range config.config {
		sid, err := strconv.Atoi(entry.Sid)
		if err != nil {
			log.Printf("cannot convert sid")
		}
		peer := ida.Peer{IP: entry.IP, TCPPort: entry.TCPPort, UDPPort: entry.UDPPort, PubKey: entry.PubKey, Sid: sid}
		if entry.Role == "self" {
			selfPeer = peer
		} else if entry.Role == "neighbor" {
			peerList = append(peerList, peer)
			allPeers = append(allPeers, peer)
		} else {
			allPeers = append(allPeers, peer)
		}
	}
	return selfPeer, peerList, allPeers
}

// ReadConfigFile parses the config file and return an error
func (config *Config) ReadConfigFile(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		log.Fatal("Failed to read config file ", filename)
		return err
	}
	defer file.Close()
	fscanner := bufio.NewScanner(file)

	result := []PeerConfig{}
	for fscanner.Scan() {
		p := strings.Split(fscanner.Text(), " ")
		if len(p) != 6 {
			log.Printf("incorrect format, need 6 columns, but actually have %v columns", len(p))
			return nil
		}
		entry := PeerConfig{p[0], p[1], p[2], p[3], p[4], p[5]}
		result = append(result, entry)
	}
	config.config = result
	return nil
}

// GenerateConfigFromGraph generate config files from graph config file using adjacent map definition of a graph
func GenerateConfigFromGraph(graphfile string) {
	file, err := os.Open(graphfile)
	if err != nil {
		log.Fatal("Failed to read config file ", graphfile)
		return
	}

	defer file.Close()
	fscanner := bufio.NewScanner(file)
	var n int
	fscanner.Scan()
	n, err = strconv.Atoi(fscanner.Text())
	if err != nil {
		log.Printf("not able to convert to number of nodes")
	}
	pubkeys, tcps, udps := initConfig(n)

	for fscanner.Scan() {
		p := strings.Split(fscanner.Text(), " ")
		writeGraphRelationToConfig(p, n, pubkeys, tcps, udps)
	}
}

func initConfig(n int) (map[int][]byte, []int, []int) {
	filename := "configs/config_allpeers.txt"
	f, err := os.Create(filename)
	if err != nil {
		log.Printf("cannot create file: %v", filename)
	}
	defer f.Close()

	udpport := 10000
	tcpport := 20000
	udps := make([]int, n)
	tcps := make([]int, n)
	pubkeys := make(map[int][]byte)

	for i := 0; i < n; i++ {
		sid := strconv.Itoa(i)
		ts := strconv.Itoa(tcpport)
		us := strconv.Itoa(udpport)
		line := sid + " 127.0.0.1 " + ts + " " + us + " "
		buf := make([]byte, pubKeySize)
		_, err := rand.Read(buf)
		if err != nil {
			log.Printf("unable to create random number")
		}
		pubkey := hex.EncodeToString(buf)
		line = line + pubkey + " all\n"
		tcps[i] = tcpport
		udps[i] = udpport
		pubkeys[i] = buf
		udpport++
		tcpport++
		io.WriteString(f, line)
	}
	return pubkeys, tcps, udps
}

func writeGraphRelationToConfig(p []string, n int, pubkeys map[int][]byte, tcps []int, udps []int) {
	idx, err := strconv.Atoi(p[0])
	if err != nil {
		log.Printf("cannot convert index %v", p[0])
		return
	}
	filename := "configs/config_" + p[0] + ".txt"
	f, err := os.Create(filename)
	if err != nil {
		log.Printf("cannot create file %v", filename)
		return
	}
	defer f.Close()
	ts := strconv.Itoa(tcps[idx])
	us := strconv.Itoa(udps[idx])
	sid := strconv.Itoa(idx)
	line := sid + " 127.0.0.1 " + ts + " " + us + " " + hex.EncodeToString(pubkeys[idx]) + " self\n"
	io.WriteString(f, line)
	for _, v := range p[1:] {
		idx, err = strconv.Atoi(v)
		if err != nil {
			log.Printf("cannot convert index %v", v)
		}
		ts := strconv.Itoa(tcps[idx])
		us := strconv.Itoa(udps[idx])
		sid := strconv.Itoa(idx)
		line := sid + " 127.0.0.1 " + ts + " " + us + " " + hex.EncodeToString(pubkeys[idx]) + " neighbor\n"
		io.WriteString(f, line)
	}
}
