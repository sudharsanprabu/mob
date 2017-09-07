package proto

import (
	"time"
	"net"
)

// Structs for our packet types

type ClientInfoMsg struct {
	Ip string
	List []string
}

type ClientCmdMsg struct {
	Arg string
}

type TrackerRes struct {
	Res string
}

type TrackerSlice struct {
	Res []string
}

type ClientInfoPacket struct {
	ClientIps []string
}

type HandshakePacket struct {
	Type string
}

type TimePacket struct {
	TimeToPlay time.Time
}

// Return our discovered local ip address by pinging google
func GetLocalIp() (string, error) {
	conn, err1 := net.Dial("udp", "www.google.com:80")
	if err1 != nil {
		return "", err1
	}

	defer conn.Close()

	ip, _, err2 := net.SplitHostPort(conn.LocalAddr().String())
	if err2 != nil {
		return "", err2
	}

	return ip, nil
}
