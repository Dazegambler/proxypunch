package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

const relayHost = "delthas.fr:14761"

const defaultPort = 41254

type Peer struct {
	addr       net.UDPAddr
	Found      bool
	Connection bool
}

var Peers map[string]Peer = make(map[string]Peer)

//var mainPeer net.UDPAddr

var _, localIpv4, _ = net.ParseCIDR("127.0.0.0/8")
var _, localIpv6, _ = net.ParseCIDR("fc00::/7")

type Config struct {
	Mode                string `yaml:"mode"`
	LocalPort           int    `yaml:"local_port"`
	Host                string `yaml:"remote_host"`
	RemotePort          int    `yaml:"remote_port"`
	DownloadedAutopunch bool   `yaml:"downloaded_autopunch"`
}

func client(host string, port int) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{
		Port: defaultPort,
	})
	if err != nil {
		c, err = net.ListenUDP("udp4", nil)
		if err != nil {
			log.Fatal(err)
		}
	}
	defer c.Close()

	localPort := c.LocalAddr().(*net.UDPAddr).Port
	fmt.Println("Listening, connect to 127.0.0.1 on port " + strconv.Itoa(localPort))

	relayAddr, err := net.ResolveUDPAddr("udp4", relayHost)
	if err != nil {
		log.Fatal(err)
	}

	remoteAddr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		log.Fatal(err)
	}

	chRelay := make(chan struct{})
	go func() {
		relayPayload := append([]byte{byte(port >> 8), byte(port)}, remoteAddr.IP.To4()...)
		for {
			select {
			case <-chRelay:
				return
			default:
			}
			c.WriteToUDP(relayPayload, relayAddr)
			time.Sleep(500 * time.Millisecond)
		}
	}()
	defer close(chRelay)

	buffer := make([]byte, 4096)

	for {
		n, addr, err := c.ReadFromUDP(buffer)
		if err != nil {
			// err is thrown if the buffer is too small
			continue
		}
		if !addr.IP.Equal(relayAddr.IP) || addr.Port != relayAddr.Port {
			continue
		}
		if n != 2 {
			fmt.Fprintln(os.Stderr, "Error received packet of wrong size from relay. (size:"+strconv.Itoa(n)+")")
			continue
		}
		fmt.Println("Endian:", buffer[:10])
		remoteAddr.Port = int(binary.BigEndian.Uint16(buffer[:2]))
		break
	}

	chPunch := make(chan struct{})
	go func() {
		punchPayload := []byte{0xCD}
		for {
			select {
			case <-chPunch:
				return
			default:
			}
			c.WriteToUDP(punchPayload, remoteAddr)
			for _, h := range Peers {
				c.WriteToUDP(punchPayload, &h.addr)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
	defer close(chPunch)

	foundPeer := false
	var localAddr net.UDPAddr
	for {
		n, addr, err := c.ReadFromUDP(buffer[1:])
		if err != nil {
			// err is thrown if the buffer is too small
			continue
		}
		if n > len(buffer)-1 {
			fmt.Fprintln(os.Stderr, "Error received packet of wrong size from peer. (size:"+strconv.Itoa(n)+")")
			continue
		}
		if addr.IP.Equal(relayAddr.IP) && addr.Port == relayAddr.Port {
			continue
		}

		if addr.IP.Equal(remoteAddr.IP) && addr.Port == remoteAddr.Port {
			if !foundPeer {
				foundPeer = true
				fmt.Println("Connected to peer:", addr)
			}
			if n != 0 && localAddr.Port != 0 && buffer[1] == 0xCC {
				c.WriteToUDP(buffer[2:n+1], &localAddr)
			}
			continue
		}
		if localIpv4.Contains(addr.IP) || localIpv6.Contains(addr.IP) {
			localAddr = *addr
			buffer[0] = 0xCC
			c.WriteToUDP(buffer[:n+1], remoteAddr)
			continue
		}

		//For ref
		// if _, exists := Peers[addr.IP.String()]; exists {
		// 	if n != 0 && buffer[1] == 0xCC {
		// 		c.WriteToUDP(buffer[2:n+1], &localAddr)
		// 	}
		// 	continue
		// }

	}
}

func get_host_ip(c net.UDPConn, relayAddr net.UDPAddr, buffer []byte, port int) {
	receivedIp := false
	for {
		n, addr, err := c.ReadFromUDP(buffer)
		if err != nil {
			// err is thrown if the buffer is too small
			continue
		}
		//If Server failed to start
		if !addr.IP.Equal(relayAddr.IP) || addr.Port != relayAddr.Port {
			continue
		}
		// 4 bytes received means the packet contained the host ip
		if n == 4 {
			if !receivedIp {
				receivedIp = true
				ip := net.IP(buffer[:4])
				fmt.Println("Connected. Ask your peer to connect to " + ip.String() + " on port " + strconv.Itoa(port) + " with proxypunch")
			}
			continue
		}
		if n != 6 {
			fmt.Fprintln(os.Stderr, "Error received packet of wrong size from relay. (size:"+strconv.Itoa(n)+")")
			continue
		}
		addmainpeer(buffer)
		break
		// ip := make([]byte, 4)
		// copy(ip, buffer[2:6])
		// mainPeer = net.UDPAddr{
		// 	IP:   net.IP(ip),
		// 	Port: int(binary.BigEndian.Uint16(buffer[:2])),
		// }
		//6 bytes received means the packet contains the ip and port of peer
		// ip := make([]byte, 4)
		// copy(ip, buffer[2:6])
		// //PEER ADDRESS
		// var peer = net.UDPAddr{
		// 	IP:   net.IP(ip),
		// 	Port: int(binary.BigEndian.Uint16(buffer[:2])),
		// }
		// Peers[peer.String()] = {Addr: peer}
	}
}

func addpeer(buffer []byte) {
	ip := make([]byte, 4)
	copy(ip, buffer[3:7])
	var peer = net.UDPAddr{
		IP:   net.IP(ip),
		Port: int(binary.BigEndian.Uint16(buffer[1:3])),
	}
	var p = Peer{addr: peer, Found: false}
	if _, Exists := Peers[p.addr.IP.String()]; !Exists {
		fmt.Println("sec:", buffer[:10])
		Peers[p.addr.IP.String()] = p
		//fmt.Println(len(Peers))
		fmt.Println("New peer Connected:", p.addr)
	}
}

func addmainpeer(buffer []byte) {
	ip := make([]byte, 4)
	copy(ip, buffer[2:6])
	var peer = net.UDPAddr{
		IP:   net.IP(ip),
		Port: int(binary.BigEndian.Uint16(buffer[:2])),
	}
	var p = Peer{addr: peer, Found: false}
	if _, Exists := Peers[p.addr.IP.String()]; !Exists {
		fmt.Println("pri:", buffer[:10])
		Peers[p.addr.IP.String()] = p
		//fmt.Println(len(Peers))
		fmt.Println("New peer Connected:", p.addr)
	}
}

func packet_handling(relayAddr net.UDPAddr, c net.UDPConn, buffer []byte, port int) {
	localAddr := &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: port,
	}

	//foundmain := false
	for {
		n, addr, err := c.ReadFromUDP(buffer[1:])
		if err != nil {
			// err is thrown if the buffer is too small
			continue
		}

		if n > len(buffer)-1 {
			fmt.Fprintln(os.Stderr, "Error received packet of wrong size from peer. (size:"+strconv.Itoa(n)+")")
			continue
		}

		if addr.IP.Equal(relayAddr.IP) || addr.Port == relayAddr.Port {
			if n == 6 {
				addpeer(buffer)
				continue
			}
		} else if addr.IP.Equal(relayAddr.IP) && addr.Port == relayAddr.Port {
			continue
		}

		if peer, exists := Peers[addr.IP.String()]; exists {
			if n != 0 && buffer[1] == 0xCC {
				// if connection request is received
				if buffer[4] == 87 {
					if !peer.Connection {
						peer.Connection = true
						fmt.Println("Peer connected ingame:", peer.addr.IP)
					}
				}
				c.WriteToUDP(buffer[2:n+1], localAddr)
				// for _, peer := range Peers {
				// 	if !addr.IP.Equal(peer.addr.IP) {
				// 		c.WriteToUDP(buffer, &peer.addr)
				// 	}
				// }
			}
			continue
		}
		if localIpv4.Contains(addr.IP) || localIpv6.Contains(addr.IP) {
			buffer[0] = 0xCC
			//if connection request is sent
			if buffer[1] == 4 {
				for _, peer := range Peers {
					if !peer.Connection {
						c.WriteToUDP(buffer[:n+1], &peer.addr)
					}
				}
				continue
			}
			for _, peer := range Peers {
				if peer.Connection {
					c.WriteToUDP(buffer[:n+1], &peer.addr)
				}
			}
			continue
		}
		//Check for new peers

		// if n == 6 {
		// 	addpeer(buffer)
		// 	continue
		// }

		//PP RECEIVED PACKET FROM PEER
		//forward peer packets to host
		// if addr.IP.Equal(mainPeer.IP) && addr.Port == mainPeer.Port {
		// 	if !foundmain {
		// 		foundmain = true
		// 		fmt.Println("Connected to main peer")
		// 	}
		// 	if n != 0 && buffer[1] == 0xCC {
		// 		c.WriteToUDP(buffer[2:n+1], localAddr)
		// 	}
		// 	// } else if peer, exists := Peers[addr.String()]; exists && addr.Port == peer.addr.Port {
		// 	// 	if !peer.Found {
		// 	// 		peer.Found = true
		// 	// 		fmt.Println("Connected to Peer:", peer.addr.IP)
		// 	// 	}
		// 	// 	if n != 0 && buffer[1] == 0xCC {
		// 	// 		c.WriteToUDP(buffer[2:n+1], localAddr)
		// 	// 	}
		// 	// 	continue
		// } else if (localIpv4.Contains(addr.IP) || localIpv6.Contains(addr.IP)) && addr.Port == port {
		// 	buffer[0] = 0xCC
		// 	c.WriteToUDP(buffer[:n+1], &mainPeer)
		// 	continue
		// }
		//NEW
		// if peer, exists := Peers[addr.String()]; exists {
		// 	if !peer.Found {
		// 		Peers[addr.String()] = Peer{Addr: peer.Addr, Found: true}
		// 		fmt.Println("Connected to peer:", addr)
		// 	}
		// 	if n != 0 && buffer[1] == 0xCC {
		// 		c.WriteToUDP(buffer[2:n+1], localAddr)
		// 	}
		// 	continue
		// }
		// if localIpv4.Contains(addr.IP) || localIpv6.Contains(addr.IP) {
		// 	Peers[addr.String()] = Peer{Addr: *addr, Found: true}
		// 	fmt.Println("New peer connected:", addr)
		// 	if n != 0 && buffer[1] == 0xCC {
		// 		c.WriteToUDP(buffer[2:n+1], localAddr)
		// 	}
		// 	continue
		// }
		//END OF NEW
	}
}

func server(port int) {
	c, err := net.ListenUDP("udp4", nil)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	//Peers := make(map[string]Peer)

	fmt.Println("Listening, start hosting on port " + strconv.Itoa(port))
	fmt.Println("Connecting...")

	// localAddr := &net.UDPAddr{
	// 	IP:   net.IPv4(127, 0, 0, 1),
	// 	Port: port,
	// }

	relayAddr, err := net.ResolveUDPAddr("udp4", relayHost)
	if err != nil {
		log.Fatal(err)
	}

	//i can only assume its keeping the relay updated
	chRelay := make(chan struct{})
	go func() {
		relayPayload := []byte{byte(port >> 8), byte(port)}
		for {
			select {
			case <-chRelay:
				return
			default:
			}
			c.WriteToUDP(relayPayload, relayAddr)
			time.Sleep(500 * time.Millisecond)
		}
	}()
	defer close(chRelay)

	buffer := make([]byte, 4096)

	//FIRST LOOP(till it gets your ip and the peer's ip)
	get_host_ip(*c, *relayAddr, buffer, port)

	//pp sends this every 500 ms
	chPunch := make(chan struct{})
	go func() {
		punchPayload := []byte{0xCD}
		for {
			select {
			case <-chPunch:
				return
			default:
			}
			//fmt.Println("a")
			//NOT BEING CALLED
			for _, h := range Peers {
				c.WriteToUDP(punchPayload, &h.addr)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
	defer close(chPunch)

	//THE MAIN ISSUE IS NEW CONNECTIONS ARE NOT BEING ADDED TO THE MAP OF PEERS

	packet_handling(*relayAddr, *c, buffer, port)

	//LOOP TO MANAGE PACKET HANDLING

}

// func server(port int) {
// 	c, err := net.ListenUDP("udp4", nil)
// 	if err != nil {
// 		log.Fatal(err)
// 	}
// 	defer c.Close()

// 	fmt.Println("Listening, start hosting on port " + strconv.Itoa(port))
// 	fmt.Println("Connecting...")

// 	localAddr := &net.UDPAddr{
// 		IP:   net.IPv4(127, 0, 0, 1),
// 		Port: port,
// 	}

// 	relayAddr, err := net.ResolveUDPAddr("udp4", relayHost)
// 	if err != nil {
// 		log.Fatal(err)
// 	}

// 	chRelay := make(chan struct{})
// 	go func() {
// 		relayPayload := []byte{byte(port >> 8), byte(port)}
// 		for {
// 			select {
// 			case <-chRelay:
// 				return
// 			default:
// 			}
// 			c.WriteToUDP(relayPayload, relayAddr)
// 			time.Sleep(500 * time.Millisecond)
// 		}
// 	}()
// 	defer close(chRelay)

// 	var remoteAddr net.UDPAddr
// 	buffer := make([]byte, 4096)

// 	receivedIp := false
// 	for {
// 		n, addr, err := c.ReadFromUDP(buffer)
// 		if err != nil {
// 			// err is thrown if the buffer is too small
// 			continue
// 		}
// 		//If Server failed to start
// 		if !addr.IP.Equal(relayAddr.IP) || addr.Port != relayAddr.Port {
// 			continue
// 		}
// 		if n == 4 {
// 			if !receivedIp {
// 				receivedIp = true
// 				ip := net.IP(buffer[:4])
// 				fmt.Println("Connected. Ask your peer to connect to " + ip.String() + " on port " + strconv.Itoa(port) + " with proxypunch")
// 			}
// 			continue
// 		}
// 		if n != 6 {
// 			fmt.Fprintln(os.Stderr, "Error received packet of wrong size from relay. (size:"+strconv.Itoa(n)+")")
// 			continue
// 		}
// 		ip := make([]byte, 4)
// 		copy(ip, buffer[2:6])
// 		remoteAddr = net.UDPAddr{
// 			IP:   net.IP(ip),
// 			Port: int(binary.BigEndian.Uint16(buffer[:2])),
// 		}
// 		break
// 	}

// 	chPunch := make(chan struct{})
// 	go func() {
// 		punchPayload := []byte{0xCD}
// 		for {
// 			select {
// 			case <-chPunch:
// 				return
// 			default:
// 			}
// 			c.WriteToUDP(punchPayload, &remoteAddr)
// 			time.Sleep(500 * time.Millisecond)
// 		}
// 	}()
// 	defer close(chPunch)

// 	foundPeer := false
// 	for {
// 		n, addr, err := c.ReadFromUDP(buffer[1:])
// 		if err != nil {
// 			// err is thrown if the buffer is too small
// 			continue
// 		}
// 		if n > len(buffer)-1 {
// 			fmt.Fprintln(os.Stderr, "Error received packet of wrong size from peer. (size:"+strconv.Itoa(n)+")")
// 			continue
// 		}
// 		if addr.IP.Equal(relayAddr.IP) && addr.Port == relayAddr.Port {
// 			continue
// 		}
// 		if addr.IP.Equal(remoteAddr.IP) && addr.Port == remoteAddr.Port {
// 			if !foundPeer {
// 				foundPeer = true
// 				fmt.Println("Connected to peer")
// 			}
// 			if n != 0 && buffer[1] == 0xCC {
// 				c.WriteToUDP(buffer[2:n+1], localAddr)
// 			}
// 		} else if (localIpv4.Contains(addr.IP) || localIpv6.Contains(addr.IP)) && addr.Port == port {
// 			buffer[0] = 0xCC
// 			c.WriteToUDP(buffer[:n+1], &remoteAddr)
// 		}
// 	}
// }

var ProgramVersion string
var ProgramArch string

func main() {
	fmt.Println("proxypunch " + ProgramVersion + " by delthas")
	fmt.Println()

	var mode string
	var host string
	var port int
	var noSave bool
	var noUpdate bool
	var configFile string

	flag.StringVar(&mode, "mode", "", "connect mode: server, client")
	flag.StringVar(&host, "host", "", "remote host for client mode: ipv4 or ipv6 or hostname")
	flag.IntVar(&port, "port", 0, "port for client or server mode")
	flag.BoolVar(&noSave, "nosave", false, "disable saving configuration to file")
	flag.BoolVar(&noUpdate, "noupdate", false, "disable automatic update")
	flag.StringVar(&configFile, "config", "proxypunch.yml", "load configuration from file")
	flag.Parse()

	scanner := bufio.NewScanner(os.Stdin)

	var config Config

	noConfig := (mode == "server" && port != 0) || (mode == "client" && host != "" && port != 0)
	if !noConfig {
		file, err := os.Open(configFile)
		if err != nil {
			if !os.IsNotExist(err) {
				fmt.Fprintln(os.Stderr, "Error opening file "+configFile+": "+err.Error())
			}
		} else {
			decoder := yaml.NewDecoder(file)
			err = decoder.Decode(&config)
			file.Close()
			if err != nil {
				fmt.Fprintln(os.Stderr, "Error decoding config file "+configFile+". ("+err.Error()+")")
			}
			if config.Mode != "server" && config.Mode != "client" {
				config.Mode = ""
			}
			if config.LocalPort <= 0 || config.LocalPort > 65535 {
				config.LocalPort = 0
			}
			if config.RemotePort <= 0 || config.RemotePort > 65535 {
				config.RemotePort = 0
			}
		}
	}

	saveMode := mode == ""
	saveHost := host == ""
	savePort := port == 0

	// mode = "c"
	// host = "181.46.178.236"
	// port = 10800

	for mode != "s" && mode != "server" && mode != "c" && mode != "client" {
		if config.Mode != "" {
			fmt.Println("Mode? s(erver) / c(lient) [" + config.Mode + "]")
		} else {
			fmt.Println("Mode? s(erver) / c(lient) ")
		}
		if !scanner.Scan() {
			return
		}
		mode = strings.ToLower(scanner.Text())
		if mode == "" {
			mode = config.Mode
		}
	}
	if saveMode {
		if mode == "s" {
			mode = "server"
		} else if mode == "c" {
			mode = "client"
		}
		config.Mode = mode
	}

	if mode == "c" || mode == "client" {
		for host == "" {
			if config.Host != "" {
				fmt.Println("Host? [" + config.Host + "]")
			} else {
				fmt.Println("Host? ")
			}
			if !scanner.Scan() {
				return
			}
			h := strings.ToLower(scanner.Text())
			if h == "" {
				host = config.Host
				continue
			}
			i := strings.IndexByte(h, ':')
			if i != -1 {
				var err error
				port, err = strconv.Atoi(h[i+1:])
				if err != nil {
					fmt.Println("Invalid host format, must be <host> or <host>:<port>")
					continue
				}
			} else {
				i = len(h)
			}
			host = h[:i]
		}
		if saveHost {
			config.Host = host
		}
	}

	var configPort int
	if mode == "c" || mode == "client" {
		configPort = config.RemotePort
	} else {
		configPort = config.LocalPort
	}
	for port == 0 {
		if configPort != 0 {
			fmt.Println("Port? [" + strconv.Itoa(configPort) + "]")
		} else {
			fmt.Println("Port? ")
		}
		if !scanner.Scan() {
			return
		}
		p := scanner.Text()
		if p == "" {
			port = configPort
			continue
		}
		port, _ = strconv.Atoi(scanner.Text())
	}
	if savePort {
		if mode == "c" || mode == "client" {
			config.RemotePort = port
		} else {
			config.LocalPort = port
		}
	}

	if !noConfig && !noSave && (saveHost || saveMode || savePort) {
		saveConfig(configFile, config)
	}

	if mode == "c" || mode == "client" {
		client(host, port)
	} else {
		server(port)
	}
}

func saveConfig(configFile string, config Config) {
	file, err := os.Create(configFile)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "Error opening file "+configFile+": "+err.Error())
		}
	} else {
		encoder := yaml.NewEncoder(file)
		err = encoder.Encode(&config)
		file.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error saving config to file "+configFile+". ("+err.Error()+")")
		}
	}
}
