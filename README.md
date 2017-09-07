```
              ___.
  _____   ____\_ |__
 /     \ /  _ \| __ \
|  Y Y  (  <_> ) \_\ \
|__|_|  /\____/|___  /
      \/           \/
```
# Internet Radio

## Description

A semi peer-to-peer, somewhat real-time internet radio application written in golang.
Clients can connect to a tracker and enqueue songs
to be played to all clients. When clients join a tracker, their
songs in their respective `songs` folder are visible to all other clients.
If a song is enqueued and a client does not have it locally, one of
its peers will stream it to them.


#### Handshake Protocol

In order for clients who have songs locally to stream to clients that do not
currently have the song, we initiate a handshake between nodes to reach a
state where all clients have the resource.

When the tracker's song queue is not empty and there are no songs currently playing,
it notifies the clients who have the next song locally to begin seeding to peers as
well as notifies clients who don't have the song to begin listening for MP3 frames.

Each client that can seed the song will initiate a global handshake (all UDP packets):

* The seeder pulls the list of peers that the tracker knows of via rpc, and then
broadcasts UDP packets to each of these peers with a "request" string payload.
It repeatedly sends these request packets in an ARQ fashion until it has gotten
a response back from all peers.

* When a client receives a request packet, it will:
    * Respond with an "accept" string if it is a non-seeder without access to the MP3.
    * Respond with a "reject" string if it is already a seeder or is already being streamed to by another seeder.

* When a seeder receives an "accept" string from a peer, it will remove it from its request ARQ list and :
    * Add this peer to its list of customers (seedees) and send it a few "confirm" packets if it hasn't reached its maximum number of seedees (default in the code is 1).
    * See that it can no longer accept seedees as it has reached its maximum number of seedees, and ignores the packet.

* When a seedee receives a "confirm" string from a peer, it will:
    * Set itself to be a valid seeder and initiate the handshake process as a seeder.
    * Respond with a "reject" if another seeder already confirmed it.

* When a seeder receives a "reject" string from a peer, it will remove it from its request ARQ list.

Since non-seeders initiate the handshake when they gain access to the resource, each client in the
network should undergo this handshake as they should all eventually gain access, leading to a fully connected
stream graph. i.e. this handshake protocol propagates on the peer network in a somewhat BFS fashion.

Note: restricting a seeder to a maximum number of customers is meant to reduce load on the seeder, and
hopefully distribute load to other available seeders. Also, the idea is that we only want to stream to
those peers who have the best connectivity to us (i.e. the peers who respond to our requests first).

Note: it is possible that a customer of a stream could have multiple seeders sending it packets, we chose
to handle this by checking to see if received MP3 frames came from the peer it expects. If not, ignore them.

#### MP3 Streaming

Once a seeder has its list of seedees, it either opens the song file and decodes each MP3 frame (if it has it locally)
or reads from its in-memory buffer for storing the MP3 frames.

The seeder will first send its received frames to its peers sequentially as UDP packets before writing the frames to its own buffer,
with the idea that its faster to buffer locally than sending to peers, so we want to try to equalize frame buffering time by delaying the seeder writing to their own song buffer.

When a client has buffered N frames (in our case 300 frames; 4-5 seconds of music; each frame is about 650 bytes),
we make an rpc to the tracker saying that we're ready to play. The goal is to start playing as MP3 frames are still
being received. The tracker then makes an rpc on the client to invoke the SDL play function to start playing
the audio.

When a client is done playing audio, it makes an rpc to the tracker to say that its done playing.
Once that tracker sees that all clients have reported that they're done playing, it will move onto the
next song in the queue and restart the process of propagating the handshakes and streaming MP3.

#### Interface

After you run the client the commands are:

```
join <ip:port> - connect to a tracker with the given ip and port // i.e. join 192.168.0.106:1234
leave - disconnect from a tracker
list-songs - list all available songs
list-peers - list all peers on the network
play <song-file> - enqueue a song to be played // i.e. play The-entertainer-piano.mp3
help - show commands
quit - exit the program
```

#### Limitations

* Attempts at synchronization via timestamp/RTTs actually increased audio delay between clients.
* Only had 3 machines to test with. Unsure if this application can support more than 3 clients.
* Can only run one instance of the client on a machine.
* Only mp3 is supported.
* Only works over local NAT for now.
* Some clients crash when the song is greater than about 6 MB while others do not.
    * Test with The-entertainer-piano.mp3 for expected results

## Usage

#### Setup your Go environment

If you already have a go environment setup, move on to cloning the repo.

Else, install golang 1.8 onto your system. Version 1.8 automatically sets your
go environment to be in your home folder:

On linux/mac that is `$HOME/go`.
In `$HOME/go`, create a `bin`, `pkg`, and `src` folder if they do not exist.

Now clone this repo into the `src` folder:

```
git clone https://github.com/wongnat/mob.git
```

#### Setup SDL2 development libraries

This project requires that your installation of the SDL2 dev libraries have
been compiled to support MP3.

###### Mac OSX

Simply copy and paste this command into your terminal:

```
brew install sdl2_mixer --with-flac --with-fluid-synth --with-libmikmod \
--with-libmodplug --with-libvorbis --with-smpeg2
```

###### Linux

Consult your distribution for the SDL2 dev library packages.

On Ubuntu 16.04 its:

```
sudo apt-get install libsdl2{,-mixer,-image,-ttf}-dev
```

###### Windows

Getting the SDL2 dev libraries on Windows is a more involved process.

Follow the [go-sdl2 bindings setup instructions](https://github.com/veandco/go-sdl2) for windows

#### Go get golang libraries

In the main `mob` directory, it should be sufficient to run:

```
go get -v ...
```

to install all packages.

If you are missing any packages, you will be prompted when you try build/run the client or tracker.

This full list of package commands is:

```
go get -v github.com/veandco/go-sdl2/sdl
go get -v github.com/veandco/go-sdl2/sdl_mixer
go get -v github.com/tcolgate/mp3
go get -v github.com/cenkalti/rpc2
```

#### Build
If you have make installed, just run the following in the `mob` directory:

```
make build
```

#### Run the client

Note that you must be in a subdirectory when you run the client as it uses
the relative path `../songs` to expose its song folder to the tracker.

```
cd bin
./client
```

Alternatively,

```
cd client
go run client.go
```

#### Run the tracker
```
cd bin
./tracker <port>
```

Alternatively,

```
cd tracker
go run tracker.go <port>
```

## Dependencies

* [go-SDL2](https://github.com/veandco/go-sdl2) - golang SDL2 bindings to play mp3s
* [mp3](https://github.com/tcolgate/mp3) - golang mp3 library to parse mp3 frames
* [rpc2](https://github.com/cenkalti/rpc2) - golang rpc library for communication between clients and trackers

## Platforms

Linux, Mac OSX, Windows

## TODO

* Write tests
* Fix relative paths to resources
* Allow clients to join and play audio mid-stream
* Improve synchronization of audio among clients
* Remove tracker and make fully peer-to-peer
* Support other audio file types
* Allow connectivity over the internet and not just local networks
* Write a GUI

## License

MIT License Copyright (c) 2017 Nathan Wong, Sudharsan Prabu, Tariq Amireh
