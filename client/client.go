package main

import (
	"os"
	"os/signal"
	"syscall"
	"bufio"
	"io/ioutil"
	"fmt"
	"log"
	"strings"
	"path/filepath"
	"net"
	"mob/proto"
	"mob/client/music"
	"github.com/tcolgate/mp3"
	"github.com/cenkalti/rpc2"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/sdl_mixer"
	"time"
	"sync"
	"unsafe"
)

// SDL music ptr
var m *mix.Music

// TCP and RPC handlers for the tracker
var trackerConn net.Conn
var client *rpc2.Client

// UDP handler for handshake packets
var packetConn net.PacketConn

// UDP handler for mp3 frames
var mp3Conn net.PacketConn

// This client's local IP address
var publicIp string

// Control flags
var connectedToTracker bool // did join a tracker
var isSeeder bool           // tells us if we have access to the mp3 or not
var alreadySeeding bool     // prevent tracker rpc from being over called
var alreadyListeningForMp3 bool
var isSourceSeeder bool

// Seeder's data structures
var peerToSeedees map[string]net.Conn // map of seedees to their udp conn struct
var peerToConn map[string]bool // map of seedees to a boolean if they responded to our request or not
var seedees []string // list of seedees

var currentSong string // the current song playing

var maxSeedees int
var mux sync.Mutex // prevent data races with read/writes to peerToSeedees

// Assume mp3 is no larger than 20MB. We reuse this buffer for each song we play.
// Don't need to worry when it gets GCed since we're using it the whole time
// TODO: figure out by songs < 20MB can still overwite this on only some machines
var songBuf [20 * 1024 * 1024]byte

func main() {
	// Handle kill signal gracefully
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		if connectedToTracker {
			handleLeave()
		}

		defer music.Quit()
		os.Exit(1)
	}()

	// Initialize SDL audio
	music.Init()
	defer music.Quit()

	// Get our local network IP address
	var ipErr error
	publicIp, ipErr = proto.GetLocalIp()
	if ipErr != nil {
		log.Fatal("Error: not connected to the internet.")
		os.Exit(1)
	}

	// Set max number of seedees to our stream to prevent congestion on peers
	maxSeedees = 1

	// Init globals
	m = nil
	seedees = make([]string, 0)
	peerToConn = make(map[string]bool)
	peerToSeedees = make(map[string]net.Conn)
	connectedToTracker = false
	isSeeder = false
	isSourceSeeder = false
	alreadySeeding = false
	alreadyListeningForMp3 = false
	currentSong = ""

	// Start the shell
	fmt.Print(
		`
              ___.
  _____   ____\_ |__
 /     \ /  _ \| __ \
|  Y Y  (  <_> ) \_\ \
|__|_|  /\____/|___  /
      \/           \/
`)

	fmt.Println()
	fmt.Println("internet radio version 0.0.0")
	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Print(">>> ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSuffix(input, "\n")
		input = strings.TrimSpace(input)

		strs := strings.Split(input, " ")
		switch strs[0] {
		case "join": // join 192.168.1.12:1234
			handleJoin(strs[1])
		case "leave": // leave the network
			handleLeave()
		case "list-songs": // list
			handleListSongs()
		case "list-peers":
			handleListPeers()
		case "play": // play blah.mp3
			handlePlay(strs[1])
		case "quit": // quit the program
			if connectedToTracker {
				handleLeave()
			}
			return
		case "help": // help
			handleHelp()
		default:     // error message continue
			fmt.Println("Error: not a valid command")
		}
	}
}

// Join a tracker node and register your rpcs that the tracker can call
func handleJoin(input string) {
	if connectedToTracker {
		handleLeave()
	}

	var err error
	trackerConn, err = net.Dial("tcp", input)
	if err != nil {
		log.Println(err)
	}

	client = rpc2.NewClient(trackerConn)

	// Register the rpc handlers for seedToPeers() so that tracker can notify
	// client when to start seeding
	client.Handle("seed", func(client *rpc2.Client, args *proto.TrackerRes, reply *proto.HandshakePacket) error {
		if alreadySeeding {
			return nil
		}

		// Start seeding
		alreadySeeding = true
		isSeeder = true
		isSourceSeeder = true
		currentSong = args.Res
		go seedToPeers(currentSong)
		return nil
	})

	// Let tracker notify client to start listening for mp3 frames
	client.Handle("listen-for-mp3", func(client *rpc2.Client, args *proto.TrackerRes, reply *proto.HandshakePacket) error {
		if alreadyListeningForMp3 {
			return nil
		}

		alreadyListeningForMp3 = true
		go listenForMp3()
		return nil
	})

	// Let tracker notify client to start playing
	client.Handle("start-playing", func(client *rpc2.Client, args *proto.TimePacket, reply *proto.HandshakePacket) error {
		// Load song from in-memory buffer so that it can be played by SDL
		// sort of a hack by casting pointer to golang array to C void *
		ptrToBuf := sdl.RWFromMem(unsafe.Pointer(&(songBuf)[0]), cap(songBuf))
		m, _ = mix.LoadMUS_RW(ptrToBuf, 0)


		m.Play(1) // Start playing
		for mix.PlayingMusic() {
			time.Sleep(5 * time.Millisecond) // block; cpu friendly
		}

		handleDonePlaying()
		return nil
	})

	go client.Run()

	connectedToTracker = true

	go listenForPeers() // begin handling incoming handshake requests
	go handlePing()     // begin continuous communication with tracker

	_, port, _ := net.SplitHostPort(trackerConn.LocalAddr().String())
	client.Call("join", proto.ClientInfoMsg{net.JoinHostPort(publicIp, port), getSongNames()}, nil)
	fmt.Println("Joining tracker " + input)
}

// Leave the current tracker
func handleLeave() {
	if !connectedToTracker {
		return
	}

	if m != nil {
		mix.HaltMusic()
	}

	client.Call("leave", proto.ClientInfoMsg{trackerConn.LocalAddr().String(), nil}, nil)
	connectedToTracker = false

	fmt.Println("Leaving the tracker in 3 sec ...")
	time.Sleep(3 * time.Second)
	packetConn.Close()
	fmt.Println("done")
}

// Get song list from tracker
func handleListSongs() {
	if !connectedToTracker {
		fmt.Println("Error: not connected to a tracker")
		return
	}

	var res proto.TrackerSlice
	client.Call("list-songs", proto.ClientCmdMsg{""}, &res)
	fmt.Println(res.Res)
}

// Get list of peers from tracker
func handleListPeers() {
	if !connectedToTracker {
		fmt.Println("Error: not connected to a tracker")
		return
	}

	var res proto.TrackerSlice
	client.Call("list-peers", proto.ClientCmdMsg{""}, &res)
	fmt.Println(res.Res)
}

// Notify the tracker to add the given song to its song queue
func handlePlay(input string) {
	if !connectedToTracker {
		fmt.Println("Error: not connected to a tracker")
		return
	}

	client.Call("play", proto.ClientCmdMsg{input}, nil)
	fmt.Println("Enqueued " + input)
}

// Print shell commands
func handleHelp() {
	fmt.Print(
		`commands:
    join  - connect to a tracker
    leave - disconnect from a tracker
    list-songs - list all available songs
    list-peers - list all peers on the network
    play - enqueue a song to be played
    help - show commands
    quit - exit the program
`)
}

// Method of continous communication between clients and tracker
// Client constantly asking the tracker if the next song is ready.
// TODO: use this as a method of timing out clients that have poor connectivity
func handlePing() {
	_, port, _ := net.SplitHostPort(trackerConn.LocalAddr().String())
	for connectedToTracker {
		client.Call("ping", proto.ClientInfoMsg{net.JoinHostPort(publicIp, port), nil}, nil)
		time.Sleep(10 * time.Millisecond)
	}
}

// Notify the client that we finished playing the song
func handleDonePlaying() {
	m.Free()
	m = nil

	// clean up connections
	for _, c := range peerToSeedees {
		c.Close()
	}

	if !isSourceSeeder {
		mp3Conn.Close()
	}

	peerToSeedees = make(map[string]net.Conn)
	peerToConn = make(map[string]bool)
	seedees = make([]string, 0)
	isSeeder = false
	isSourceSeeder = false
	alreadySeeding = false
	alreadyListeningForMp3 = false
	currentSong = ""

	// make rpc call to tracker
	client.Call("done-playing", proto.ClientCmdMsg{""}, nil)
}

// Call this if we're not a source seeder (has song locally) after we set our seedees
func listenForMp3() {
	// listen to incoming udp packets
	var err error
	mp3Conn, err = net.ListenPacket("udp", net.JoinHostPort(publicIp, "6122"))
	if err != nil {
		log.Fatal(err)
	}

	prebufferedFrames := 1
	currIndex := 0

	seeder := ""

	// Continously listen mp3 packets while connected to tracker
	for connectedToTracker { // terminate when we leave a tracker
		if prebufferedFrames == 300 { // pre-buffered 200 frames before playing
			// send rpc to start playing
			go client.Call("ready-to-play", proto.ClientCmdMsg{""}, nil)
		}

		buf := make([]byte, 2048)

		// Read a packet
		n, addr, err := mp3Conn.ReadFrom(buf) // block here
		if err != nil {
			break // this will happen when we close mp3Conn
		}

		seederIp, _, _ := net.SplitHostPort(addr.String())
		if seeder == "" {
			seeder = seederIp
		}

		if seederIp != seeder {
			continue
		}

		for _, c := range peerToSeedees {
			c.Write(buf)
			time.Sleep(300 * time.Microsecond)
		}

		for i := 0; i < n; i++ {
			songBuf[currIndex + i] = buf[i]
		}

		currIndex = currIndex + n
		prebufferedFrames++
	}
}

// Listens for udp request packets from peers in order to build the stream graph
// Handles all possible handshake packets that will be sent to this peer.
// Called in handleJoin when you join a tracker
func listenForPeers() {
	// listen to incoming udp packets
	var err error
	packetConn, err = net.ListenPacket("udp", net.JoinHostPort(publicIp, "6121"))
	if err != nil {
		log.Fatal(err)
	}

	// Continously listen for handshake packets
	// Eventually after a successful round of handshaking, all peers will
	// be seeders and will block on the next ReadFrom() call until the next
	// round
	for connectedToTracker { // terminate when we leave a tracker
		// Read a packet
		buffer := make([]byte, 2048)
		n, addr, e := packetConn.ReadFrom(buffer) // block here
		if e != nil {
			break
		}

		s := string(buffer[:n])
		substrs := strings.Split(s, ":")
		ip, _, _ := net.SplitHostPort(addr.String())
		raddr := net.UDPAddr{IP: net.ParseIP(ip), Port: 6121}

		// Process the packet and handle
		switch substrs[0] {
		case "request": // where this client is a non-seeder
			if currentSong == "" {
				currentSong = substrs[1]
			}

			if isSeeder || hasSongLocally(substrs[1]) {
				packetConn.WriteTo([]byte("reject"), &raddr)
			} else {
				packetConn.WriteTo([]byte("accept"), &raddr)
			}
		case "confirm": // where this client is a non-seeder
			if isSeeder { // if we already confirmed, don't reject a confirm from our origin
				go func() {
					for i := 0; i < 5; i++ { // redundancy
						packetConn.WriteTo([]byte("reject"), &raddr)
						time.Sleep(500 * time.Microsecond)
					}
				}()
			} else {
				//originSeeder = ip
				isSeeder = true
				go seedToPeers(currentSong)
			}
		case "accept": // where this client is a seeder
			if isSeeder && len(seedees) < maxSeedees {
				ip, _, _ := net.SplitHostPort(addr.String())
				seedees = append(seedees, ip)
				go func() {
					for i := 0; i < 5; i++ { // redundancy
						packetConn.WriteTo([]byte("confirm"), &raddr)
						time.Sleep(500 * time.Microsecond)
					}
				}()
				mux.Lock()
				peerToConn[ip] = true
				mux.Unlock()
			} else if isSeeder && len(seedees) >= maxSeedees {
				mux.Lock()
				peerToConn[ip] = true
				mux.Unlock()
			} else {
				// is a non-seeder; shouldn't get here; sanity check
				log.Fatal("non-seeder tried to accept other non-seeder")
			}
		case "reject": // where this client is a seeder
			mux.Lock()
			peerToConn[ip] = true
			mux.Unlock()
		}
	}
}

// If this client has access to mp3 stream, find peers to stream to.
// Broadcasts packets to peers until every peer has responded.
// Called by tracker rpc.
func seedToPeers(songFile string) {
	var wg sync.WaitGroup

	// Get list of peers from tracker
	var peers proto.TrackerSlice
	client.Call("list-peers", proto.ClientCmdMsg{""}, &peers)

	peerToConn = make(map[string]bool)

	// Loop to acquire udp connections to all other peers
	for _, peer := range peers.Res {
		ip, _, _ := net.SplitHostPort(peer)

		if ip != publicIp { // check not this client
			// Connect to an available peer
			pc, _ := net.Dial("udp", net.JoinHostPort(ip, "6121"))
			peerToConn[ip] = false

			wg.Add(1)
			// ARQ requests to the peer until we set its response bool to nil
			go func() {
				defer wg.Done()
				defer pc.Close()
				for {
					mux.Lock()
					if peerToConn[ip] {
						mux.Unlock()
						break
					}
					mux.Unlock()

					pc.Write([]byte("request:" + songFile))
					time.Sleep(500 * time.Microsecond)
				}
			}()
		}
	}

	wg.Wait() // wait until we get a response from every peer


	// Dial seedees mp3 port
	for _, seedee := range seedees {
		c, _ := net.Dial("udp", net.JoinHostPort(seedee, "6122"))
		peerToSeedees[seedee] = c
	}

	if isSourceSeeder {
		r, err := os.Open("../songs/" + songFile)
		if err != nil {
			log.Fatal(err)
			return
		}

		d := mp3.NewDecoder(r)

		skipped := 0
		currIndex := 0
		prebufferedFrames := 0
		var frame mp3.Frame

		for connectedToTracker {
			if prebufferedFrames == 300 { // pre-buffered 200 frames before playing
				// send rpc to start playing
				go client.Call("ready-to-play", proto.ClientCmdMsg{""}, nil)
			}

			if err := d.Decode(&frame, &skipped); err != nil {
				break
			}

			reader := frame.Reader()
			frame_bytes, _ := ioutil.ReadAll(reader)

			for _, c := range peerToSeedees {
				c.Write(frame_bytes)
				time.Sleep(300 * time.Microsecond)
			}

			// Write frame into local songBuf
			for j := 0; j < len(frame_bytes); j++ {
				songBuf[currIndex + j] = frame_bytes[j]
			}
		
			currIndex = currIndex + len(frame_bytes)
			prebufferedFrames++
		}
	}
}

// Returns csv of all song names in the songs folder.
func getSongNames() ([]string) {
	var songs []string
	filepath.Walk("../songs", func (p string, i os.FileInfo, err error) error {
		if err != nil {
			log.Println(err)
			return nil
		}

		s := filepath.Base(p)
		if strings.Compare(s, "songs") != 0 && strings.Contains(s, ".mp3") {
			songs = append(songs, s)
		}

		return nil
	})

	return songs
}

func hasSongLocally(songFile string) bool {
	for _, song := range getSongNames() {
		if song == songFile {
			return true
		}
	}

	return false
}
